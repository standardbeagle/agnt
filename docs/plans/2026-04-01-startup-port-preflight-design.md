# Startup Port Pre-flight & User Stories

**Status:** Implemented (2026-04-01)

## Problem

When a previous agnt session exits uncleanly (crash, `kill -9`, daemon restart), child processes can survive as orphans holding ports. The next autostart silently fails: the new process can't bind, exits immediately, dependents time out, and the user sees a confusing cascade of failures instead of the actual cause.

## Design: Port Pre-flight Check

Add a pre-flight step to `RunAutostart` between config load (step 2) and script start (step 4). Before launching any scripts:

1. Collect all declared `ports` from autostart scripts
2. For each port, call `FindPIDsByPort` to detect listeners
3. Filter out PIDs already managed by the daemon's ProcessManager
4. For unmanaged blockers, apply the configured resolution policy

### Port Discovery

Ports are collected from autostart scripts' `ports` fields — no separate declaration needed. Scripts without `ports` are skipped during pre-flight (no port to check).

### Resolution Policies

Configured at the `project` level in `.agnt.kdl`:

```kdl
project {
    name "bifrostui"
    port-conflict "prompt"   // default: prompt | auto-kill | skip | fail
}
```

| Policy | Behavior |
|--------|----------|
| `prompt` | Report conflicts to client, wait for user confirmation before killing. Default. |
| `auto-kill` | Kill blocking processes automatically, log what was killed. |
| `skip` | Log a warning and start the script anyway (current behavior — bind will fail). |
| `fail` | Abort autostart entirely if any port conflict exists. |

### Kill Strategy

Killing a blocking PID uses process-group kill (same as agnt's shutdown path):

1. Resolve the full descendant tree via `/proc/[pid]/task/[pid]/children` (recursive)
2. Send SIGTERM to the entire process group (`-pid`)
3. Wait up to 3s for exit
4. SIGKILL the process group if still alive
5. Verify port is free after kill (retry port check)

This aggressive approach is intentional — dev/test toolchains (dotnet watch, vite, webpack, jest) spawn deep process trees where killing just the parent leaves children holding ports and file locks.

### Communication: Daemon → Client

The autostart response already carries `AutostartResult` back to the client. Extend it:

```go
type PortConflict struct {
    ScriptName string `json:"script_name"`
    Port       int    `json:"port"`
    PIDs       []int  `json:"pids"`
    ProcessName string `json:"process_name,omitempty"` // from /proc/[pid]/comm
}

type AutostartResult struct {
    Scripts       []string       `json:"scripts,omitempty"`
    Proxies       []string       `json:"proxies,omitempty"`
    Errors        []string       `json:"errors,omitempty"`
    PortConflicts []PortConflict `json:"port_conflicts,omitempty"` // NEW
    PortsCleared  []PortConflict `json:"ports_cleared,omitempty"`  // NEW: auto-killed
}
```

For `prompt` mode, the flow is:
1. Daemon detects conflicts, populates `PortConflicts`, returns result *without* starting scripts
2. Client (`displayAutostartResults`) shows conflicts and asks user
3. User confirms → client sends `AUTOSTART CLEAR_PORTS` command → daemon kills PIDs → daemon proceeds with script start
4. User declines → client sends `AUTOSTART CONTINUE` → daemon starts scripts anyway (they'll fail on bind)

For `auto-kill` mode: daemon kills, populates `PortsCleared`, proceeds. No round-trip.

### Startup Log Events

New event types for `StartupLogStore`:

| EventType | Level | When |
|-----------|-------|------|
| `port_conflict_detected` | warning | Unmanaged process found on declared port |
| `port_conflict_killed` | info | Blocking process killed (auto-kill or user-confirmed) |
| `port_conflict_skipped` | warning | User declined to kill, proceeding anyway |
| `port_conflict_failed` | error | Failed to kill blocking process (permission denied, etc.) |

---

## User Stories: Startup Lifecycle

### S1: Clean Start (happy path)

**Given** no processes hold any declared ports  
**When** agnt autostart runs  
**Then** all scripts start in dependency order, proxies attach, overlay shows "auto-started: dev-lib, dev-backend, dev-frontend"

### S2: Port Blocked — Prompt Mode (default)

**Given** port 5000 is held by orphaned PID 1655344 (`bifrostui`)  
**And** `port-conflict` is `prompt` (or unset)  
**When** agnt autostart runs  
**Then** the overlay displays:

```
⚠ Port 5000 (dev-backend) blocked by bifrostui (PID 1655344)
  Kill blocking process? [Y/n]
```

**When** user confirms  
**Then** PID 1655344 is killed (SIGTERM → SIGKILL after 3s), autostart proceeds, startup log records `port_conflict_killed`

### S3: Port Blocked — User Declines Kill

**Given** port 5000 is blocked, prompt mode  
**When** user declines the kill  
**Then** autostart proceeds anyway, dev-backend fails on bind, startup log records `port_conflict_skipped`, error surfaces via `get_errors`

### S4: Port Blocked — Auto-Kill Mode

**Given** port 5000 is held by orphaned PID  
**And** `port-conflict "auto-kill"` in config  
**When** agnt autostart runs  
**Then** PID is killed automatically, overlay shows "cleared port 5000 (was: bifrostui PID 1655344)", autostart proceeds

### S5: Port Blocked — Kill Fails (permission denied)

**Given** port 5000 is held by a root-owned process  
**When** agnt tries to kill it  
**Then** kill fails, startup log records `port_conflict_failed` with the error, autostart proceeds (script will fail on bind), error surfaces clearly: "port 5000 blocked by PID 1234 (owned by root) — cannot kill"

### S6: Multiple Port Conflicts

**Given** ports 5000 and 5173 are both blocked by different orphans  
**When** prompt mode  
**Then** a single prompt lists all conflicts:

```
⚠ Port conflicts detected:
  5000 (dev-backend) ← bifrostui (PID 1655344)
  5173 (dev-frontend) ← node (PID 1655400)
  Kill all blocking processes? [Y/n]
```

Not per-port prompts. One decision for all.

### S7: Port Held by Managed Process

**Given** port 5000 is held by a process the daemon is already managing  
**When** autostart runs  
**Then** no conflict reported — the managed process is already "ours". The `already_running` skip logic handles this.

### S8: Idempotent Second Session Join

**Given** session A already started scripts for this project  
**When** session B registers for the same project  
**Then** session B joins as observer, no autostart, no port check. Existing scripts are reported as already running.

### S9: Dependency Timeout After Port Clear

**Given** port 5000 was cleared (auto-killed orphan)  
**And** dev-frontend depends on dev-backend with 120s timeout  
**When** dev-backend starts and takes 90s to listen  
**Then** port probe detects readiness at ~90s, dev-frontend starts normally. No timeout.

### S10: Config Parse Error

**Given** `.agnt.kdl` has a syntax error  
**When** autostart runs  
**Then** startup aborts with a clear config parse error in the startup log. No port checks attempted (no config to check against).

### S11: No Ports Declared

**Given** a script has `autostart true` but no `ports` field  
**When** autostart runs  
**Then** no port check for that script. Pre-flight only checks scripts with declared ports.

### S12: Port Freed Between Check and Start

**Given** port 5000 was blocked during pre-flight  
**And** the blocking process exits on its own before kill is sent  
**When** daemon tries to kill  
**Then** kill returns "no such process" — treated as success (port is free). Autostart proceeds.

### S13: Fail Mode

**Given** `port-conflict "fail"` in config  
**And** port 5000 is blocked  
**When** autostart runs  
**Then** autostart aborts entirely, no scripts start, error: "port conflict on 5000 — aborting (port-conflict: fail)"

### S14: Skip Mode

**Given** `port-conflict "skip"` in config  
**And** port 5000 is blocked  
**When** autostart runs  
**Then** warning logged, scripts start anyway (current behavior). The script that needs port 5000 will fail on bind, EADDRINUSE recovery may kick in.

### S15: Daemon Restart Recovery

**Given** daemon was killed (`kill -9`)  
**And** child processes survived (orphans)  
**When** daemon restarts and a new session registers  
**Then** pre-flight detects orphans via port scan, resolution policy applies. PID tracker files from the old daemon help identify which PIDs are former children.

---

## Implementation Order

1. **`PortConflict` type + `detectPortConflicts()` in daemon** — core detection logic, reuses `FindPIDsByPort` and `ProcessManager.IsManagedPID`, adds `ProcessNameByPID()` via `/proc/[pid]/comm`
2. **Process-group kill** — `killProcessTree(pid)`: descendant scan → SIGTERM process group → 3s wait → SIGKILL → verify port free
3. **Config parsing** — `port-conflict` property on `project` node, `PortConflictPolicy` string field
4. **`RunAutostart` integration** — call `detectPortConflicts` between step 2 and step 4, branch on policy
5. **Auto-kill path** — kill all blockers, populate `PortsCleared`, proceed to script start
6. **Two-phase prompt path**:
   - Extend `AutostartResult` with `PortConflicts` / `PortsCleared`
   - Daemon returns conflicts and pauses (does not start scripts yet)
   - New IPC verbs: `AUTOSTART CLEAR_PORTS <project_path>` (kill + continue) and `AUTOSTART CONTINUE <project_path>` (skip + continue)
   - Client prompt in `displayAutostartResults` — single batched prompt for all conflicts
   - Daemon resumes script start after receiving either verb
7. **Startup log events** — `port_conflict_detected`, `port_conflict_killed`, `port_conflict_skipped`, `port_conflict_failed`
8. **Tests** — unit tests for detection/kill logic, e2e test with port-blocked fixture, prompt round-trip test

### Proxy→Agent Message Wiring

Separate from port pre-flight but related to startup reliability: ensure browser messages (panel messages, sketch, design mode) reach the AI agent regardless of how the proxy was started. The daemon already binds the overlay endpoint during `hubHandleProxyStart`, but there are race conditions where the proxy is created before the session registers its overlay path. Fix: `rebindProxyOverlays()` runs during session registration and reconnection to catch any proxies that were created before the overlay was available.

## Key Files

| File | Change |
|------|--------|
| `internal/daemon/daemon.go` | `detectPortConflicts()`, `killProcessTree()`, integrate into `RunAutostart` |
| `internal/config/agnt.go` | `PortConflictPolicy` field on `AgntProjectMeta` |
| `internal/config/portdetect_unix.go` | `ProcessNameByPID()` helper |
| `internal/config/portdetect_windows.go` | `ProcessNameByPID()` Windows equivalent |
| `internal/daemon/startup_errors.go` | New event types |
| `internal/daemon/hub_handlers.go` | `AUTOSTART CLEAR_PORTS` / `AUTOSTART CONTINUE` verbs, two-phase session registration |
| `cmd/agnt/pty_common.go` | Prompt display + user input handling in `displayAutostartResults` |
| `internal/daemon/e2e_autostart_test.go` | Port-blocked fixture tests |
