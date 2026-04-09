---
name: daemon-health-check
description: Design and implement health check, doctor command, and reconciliation logic for the daemon
---

# Daemon Health Check & Doctor

Use this skill when implementing or modifying health check, reconciliation, or doctor functionality.

## Reconciliation Algorithm

The daemon's in-memory state is a cache. Reconciliation verifies it against OS truth and fixes mismatches.

### Step 1: Gather Expected State
- Parse `.agnt.kdl` for all projects with running scripts
- Build expected set: `{script → process, proxy → target, port → owner}`

### Step 2: Gather Actual State (OS Truth)

**Cross-platform requirement** — every probe must go through `internal/platform/`:

| Probe | Linux/macOS | Windows | WSL |
|-------|------------|---------|-----|
| PID alive | `kill(pid, 0)` or `/proc/<pid>` | `OpenProcess` | `kill(pid, 0)`, `cmd.exe` for Windows-spawned |
| Port owner | `ss -tlnp` / `lsof -i` | `netstat -ano` | `ss` for Linux, `netstat.exe` for Windows |
| Proxy health | TCP connect / HTTP GET | Same | Same |

### Step 3: Diff Expected vs Actual

| Finding | State update | Event |
|---------|-------------|-------|
| Process in daemon but PID dead | Mark Dead | Emit ScriptStopped |
| Port in use but not by our process | Flag rogue | Record PID in warning |
| Proxy in daemon but not responding | Mark unhealthy | Emit warning |
| Config expects proxy but none exists | Attempt fallback-port | Emit warning if no fallback |
| Process running, no URL detected, has fallback-port | Create proxy via fallback | Log proxy creation |
| Process producing error output | Mark Running With Errors | Surface to AI agent |

### Step 4: Reconcile
- Update daemon cache to match reality
- Emit events for each state change
- Return diff report (for doctor command)

## Stall Detection

**CRITICAL**: Never kill a process that is producing output, even error output.

A process is stalled ONLY when ALL are true:
- No stdout/stderr output for stall threshold (default 120s)
- AlertScanner has no recent classifications
- Process is not in Starting state

Error output IS activity:
- AlertScanner classifies: compile errors, panics, exceptions
- Recent alerts → "Running With Errors", not stalled
- "Running With Errors" → surface to AI agent for code fixes, NEVER kill

Stall detection is disabled during shutdown.

## Doctor Command

MCP tool `doctor` (or action in overlay panel):

### Input
```json
{"action": "doctor", "project_path": "/optional/filter"}
```

### Output Format (compact)
```
=== Doctor Report ===

HEALTHY (2)
  backend (pid 12345): running, port 6111 ✓, proxy api ✓
  frontend (pid 12346): running, port 6112 ✓, proxy web ✓

WARNINGS (1)
  api proxy: created via fallback-port (URL detection failed)

ERRORS (1)
  deck:dev (pid 0): dead, port 5173 held by rogue process (pid 34954)
    → action: kill rogue and restart, or skip

ACTIONS AVAILABLE
  [1] Kill rogue process on port 5173 and restart deck:dev
  [2] Create missing proxy for backend using fallback-port 6111
```

### Output Format (raw JSON)
Full structured report with all fields for programmatic consumption.

## Three Reconciliation Triggers

1. **On session connect**: Full check before first query response. AI agent must receive verified state.
2. **Periodic (30s)**: Background check, emits events only. No developer output.
3. **Doctor command**: Developer-initiated, returns full report with offered actions.

## Implementation Checklist

When implementing health check:
- [ ] Create platform-specific PID probe in `internal/platform/`
- [ ] Create platform-specific port ownership probe in `internal/platform/`
- [ ] Create proxy health probe (TCP connect with timeout)
- [ ] Implement reconciliation loop in `internal/daemon/`
- [ ] Wire periodic check into daemon startup (configurable interval)
- [ ] Wire on-connect check into session registration
- [ ] Implement doctor MCP tool in `internal/tools/`
- [ ] Integrate AlertScanner classifications into stall detection
- [ ] Implement fallback-port proxy creation in reconciliation
- [ ] Add tests for each platform path
- [ ] Add tests for stall vs error distinction
- [ ] Add e2e test: script running with errors is NOT killed
