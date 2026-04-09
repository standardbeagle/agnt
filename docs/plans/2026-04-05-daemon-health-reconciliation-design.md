# Daemon Health Check & Reconciliation Design

Date: 2026-04-05

## Problem

The daemon's autostart/proxy/process flow has several failure modes that silently fail:

1. **Fallback-port is dead code for proxy creation.** Config parses `fallback-port`, port preflight reads it, but proxy creation never uses it when URL detection fails.

2. **Silent proxy creation failures.** Script-linked proxies are skipped with a debug log if URL detection doesn't fire. No session log entry, no warning to the AI agent.

3. **No health check exists.** No periodic or on-connect reconciliation between daemon state and OS truth. Stale state persists until something queries it.

4. **Error output misidentified as stall.** Compilation errors on a `dotnet watch` were treated as stall evidence, causing processes to be killed mid-compilation.

## Design

### Personas

1. **Developer** — configures, expects things to work, needs doctor command
2. **AI Agent** — queries state, acts on it, needs verified truth
3. **Daemon** — orchestrates lifecycles, state is a cache not authority
4. **Managed processes/proxies** — emit errors, need restarts, can go rogue

### Data Ownership

Daemon in-memory state is a cache. Source of truth:

| State | Authority |
|-------|-----------|
| Process alive/dead | OS (PID check) |
| Port ownership | OS (socket table) |
| Proxy alive/responsive | Proxy instance (health probe) |
| Expected state | `.agnt.kdl` on disk |

### Process States

```
Starting → Running Healthy → Stalled → Dead
              ↓
         Running With Errors → Dead
```

- **Starting**: compiling, no ready signal yet. NEVER kill.
- **Running Healthy**: serving, URL detected.
- **Running With Errors**: AlertScanner finds errors. Process is working, code is broken. Surface to agent, NEVER kill.
- **Stalled**: no output AND no AlertScanner activity for threshold. Offer restart.
- **Dead**: PID gone.

### Reconciliation Algorithm

1. Gather expected state from `.agnt.kdl`
2. Probe OS truth (PID, ports, proxy health) — cross-platform via `internal/platform/`
3. Diff: find mismatches between daemon cache and reality
4. Update cache, emit events, return report

### Fallback-Port Flow

When health check finds config expects proxy for a running script but no proxy exists:
1. Check if proxy config has `fallback-port`
2. If yes: create proxy targeting `localhost:fallback-port`
3. If no: emit warning to session log

### Doctor Command

MCP tool returning structured report: healthy components, warnings, errors, and offered actions (kill rogue, create missing proxy, restart failed script).

### Three Triggers

1. On session connect (full check before first response)
2. Periodic 30s (background, events only)
3. Doctor command (developer-initiated, full report)

## Implementation Plan

### Phase 1: Fallback-port proxy creation
- Wire `fallback-port` into proxy creation when URL detection fails
- Detect "script running, no proxy, has fallback-port" in existing health check path
- Add session log entries for proxy creation failures

### Phase 2: Platform probes
- PID alive check in `internal/platform/`
- Port ownership check in `internal/platform/`
- Proxy health probe

### Phase 3: Reconciliation loop
- Implement reconciliation algorithm in `internal/daemon/health.go`
- Wire periodic check into daemon startup
- Wire on-connect check into session registration

### Phase 4: Process state refinement
- Add "Running With Errors" state
- Integrate AlertScanner into stall detection
- Error output resets stall timer

### Phase 5: Doctor command
- MCP tool in `internal/tools/doctor.go`
- Compact and raw output formats
- Offered actions with confirmation

## Files Affected

- `internal/daemon/health.go` (new) — reconciliation algorithm
- `internal/daemon/proxy_events.go` — fallback-port flow
- `internal/daemon/daemon.go` — periodic check, on-connect check
- `internal/daemon/hub_handlers.go` — doctor IPC verb
- `internal/platform/health.go` (new) — cross-platform probes
- `internal/tools/doctor.go` (new) — MCP tool
- `internal/process/managed.go` — Running With Errors state
