---
paths:
  - "internal/daemon/**"
  - "internal/process/**"
---

# Process & Proxy Lifecycle

## Process State Machine

Five states, with clear transitions and detection criteria:

```
Starting → Running Healthy → Stalled → Dead
              ↓                          ↑
         Running With Errors ────────────┘
              ↓
            Dead
```

| State | Detection criteria | Action |
|-------|-------------------|--------|
| **Starting** | No URL/ready signal yet, may be compiling. Output is build/compile logs. | NEVER kill. Wait indefinitely until ready signal, error output, or stall threshold. |
| **Running Healthy** | Producing normal output, serving requests, URL detected. | Normal operation. |
| **Running With Errors** | AlertScanner classifies output as compile errors, panics, exceptions. Process is still alive and responding to file changes. | Surface to AI agent as actionable. NEVER restart or kill — the process is working correctly, the code is broken. |
| **Stalled** | No output of any kind (stdout, stderr) for stall threshold period AND no CPU/IO activity. | Mark as stalled, surface to developer/agent, offer restart. |
| **Dead** | PID no longer exists in OS. | Clean up daemon state, emit ScriptStopped event, trigger proxy cleanup. |

## Critical Rule: Never Kill Active Processes

**A process producing output is active, even if that output is errors.**

A `dotnet watch` printing compilation errors every save is a process working correctly — the code is broken, not the process. A Go compiler spending 30 seconds on a build is active, not stalled.

### Stall Detection

A process is stalled ONLY when ALL of these are true:
- No stdout/stderr output for the stall threshold
- AlertScanner has no recent classifications for this process
- Process is not in Starting state

**Stall threshold**: configurable per-script in `.agnt.kdl` (default: 120s). Compilation-heavy scripts should set higher thresholds.

### Error vs Stall Distinction

The AlertScanner already classifies output patterns (compile errors, panics, exceptions, warnings). The health check must consume these classifications:

- Recent alerts exist → process is "Running With Errors", NOT stalled
- Error output IS activity — it resets any stall timer
- "Running With Errors" → surface to AI agent for code fixes, never restart/kill

## Proxy Lifecycle

Proxies are created through two paths:

1. **Event-driven** (script-linked): URLDetected event → `handleURLDetected` → create proxy
2. **Explicit** (no script link): `autostartProxy` → ExplicitStart event → create proxy

### Fallback-Port Contract

When a proxy config declares both `script` and `fallback-port`:
1. First, wait for URL detection from the linked script
2. If the script reaches "Running Healthy" or "Running With Errors" state without a URL being detected, create the proxy using `fallback-port` as the target
3. If URL detection later succeeds, update the proxy target (don't create a duplicate)

`fallback-port` must never be dead code. If config declares it, the system must use it when the primary path (URL detection) fails.

## Shutdown Safety

Graceful shutdown sequence:
1. Stop accepting new registrations (`atomic.Bool`)
2. Send SIGTERM to process groups (Linux/macOS) or CTRL_BREAK_EVENT (Windows)
3. Wait graceful timeout (5s default)
4. SIGKILL remaining processes

**Stall detection must be disabled during shutdown** — processes are expected to be cleaning up, not producing output.

## Session Lifecycle & Resource Ownership

Sessions are the unit of resource ownership. When a session disconnects, `CleanupSessionResources` must fully clean up:

### Ownership Model

```
Session → owns scripts → owns processes
                       → owns proxies (via project path)
```

- Each script entry has an **owner session** (the session that started it) and **observer sessions** (other sessions sharing it).
- When the owner disconnects, ownership transfers to the first remaining observer.
- When the **last session** for a project disconnects, ALL resources are torn down.

### CleanupSessionResources Contract

When the last session for a project disconnects:

1. Stop all processes (orphaned scripts + remaining project processes)
2. Stop all proxies for the project
3. **Remove all script entries from the registry** — the registry must be empty for this project
4. **Remove all scriptConfigs entries** — no stale config data
5. Unregister auto-restarter entries
6. Unregister the session

**Critical**: The script registry is a cache, not persistent state. The next session rebuilds it from `.agnt.kdl`. Leaving stale entries causes the status bar to show indicators for scripts that no longer exist in config.

### Multi-Session Ownership Transfer

When a non-last session disconnects:
1. Remove session as observer from all scripts
2. If session was owner, transfer to first remaining observer
3. Only stop processes that have zero remaining observers
4. Only remove registry entries for zero-observer scripts

### Status Bar Indicator Count

The overlay renders one indicator per script in `status.Scripts` (from `SCRIPT LIST`). The indicator count MUST equal the number of scripts in the current `.agnt.kdl` config. Stale registry entries directly cause phantom indicators.

## State Transition Atomicity

All state transitions use `CompareAndSwapState()`. If a CAS fails, re-read state and decide again. Never force a state transition — if the process moved to a different state between your read and write, something else handled it.
