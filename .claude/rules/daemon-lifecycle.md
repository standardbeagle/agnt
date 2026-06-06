---
paths:
  - "internal/daemon/**"
  - "internal/process/**"
---

# Process & Proxy Lifecycle

## Process State Machine

Five states. Clear transitions + detection criteria:

```
Starting → Running Healthy → Stalled → Dead
              ↓                          ↑
         Running With Errors ────────────┘
              ↓
            Dead
```

| State | Detection criteria | Action |
|-------|-------------------|--------|
| **Starting** | No URL/ready signal yet, maybe compiling. Output = build/compile logs. | NEVER kill. Wait forever till ready signal, error output, or stall threshold. |
| **Running Healthy** | Normal output, serving requests, URL detected. | Normal operation. |
| **Running With Errors** | AlertScanner classifies output as compile errors, panics, exceptions. Process still alive, responds to file changes. | Surface to AI agent as actionable. NEVER restart or kill — process work fine, code broke. |
| **Stalled** | No output (stdout, stderr) for stall threshold AND no CPU/IO activity. | Mark stalled, surface to developer/agent, offer restart. |
| **Dead** | PID gone from OS. | Clean up daemon state, emit ScriptStopped event, trigger proxy cleanup. |

## Critical Rule: Never Kill Active Processes

**Process making output = active, even if output = errors.**

`dotnet watch` printing compile errors every save = process working right — code broke, not process. Go compiler spending 30s on build = active, not stalled.

### Stall Detection

Process stalled ONLY when ALL true:
- No stdout/stderr output for stall threshold
- AlertScanner has no recent classifications for this process
- Process not in Starting state

**Stall threshold**: configurable per-script in `.agnt.kdl` (default: 120s). Compile-heavy scripts set higher.

### Error vs Stall Distinction

AlertScanner already classifies output patterns (compile errors, panics, exceptions, warnings). Health check must consume them:

- Recent alerts exist → process "Running With Errors", NOT stalled
- Error output IS activity — resets stall timer
- "Running With Errors" → surface to AI agent for code fixes, never restart/kill

## Proxy Lifecycle

Proxies created two ways:

1. **Event-driven** (script-linked): URLDetected event → `handleURLDetected` → create proxy
2. **Explicit** (no script link): `autostartProxy` → ExplicitStart event → create proxy

### Fallback-Port Contract

When proxy config declares both `script` and `fallback-port`:
1. First wait for URL detection from linked script
2. If script hits "Running Healthy" or "Running With Errors" without URL detected, create proxy using `fallback-port` as target
3. If URL detection later succeeds, update proxy target (no duplicate)

`fallback-port` never dead code. Config declares it → system must use it when primary path (URL detection) fails.

## Shutdown Safety

Graceful shutdown sequence:
1. Stop accepting new registrations (`atomic.Bool`)
2. Send SIGTERM to process groups (Linux/macOS) or CTRL_BREAK_EVENT (Windows)
3. Wait graceful timeout (5s default)
4. SIGKILL remaining processes

**Stall detection must be disabled during shutdown** — processes expected to clean up, not make output.

## Session Lifecycle & Resource Ownership

Sessions = unit of resource ownership. Session disconnects → `CleanupSessionResources` must fully clean up:

### Ownership Model

```
Session → owns scripts → owns processes
                       → owns proxies (via project path)
```

- Each script entry has **owner session** (started it) + **observer sessions** (others sharing it).
- Owner disconnects → ownership transfers to first remaining observer.
- **Last session** for project disconnects → ALL resources torn down.

### CleanupSessionResources Contract

Last session for project disconnects:

1. Stop all processes (orphaned scripts + remaining project processes)
2. Stop all proxies for project
3. **Remove all script entries from registry** — registry must be empty for this project
4. **Remove all scriptConfigs entries** — no stale config data
5. Unregister auto-restarter entries
6. Unregister session

**Critical**: script registry = cache, not persistent state. Next session rebuilds it from `.agnt.kdl`. Stale entries → status bar shows indicators for scripts gone from config.

### Multi-Session Ownership Transfer

Non-last session disconnects:
1. Remove session as observer from all scripts
2. If session was owner, transfer to first remaining observer
3. Only stop processes with zero remaining observers
4. Only remove registry entries for zero-observer scripts

### Status Bar Indicator Count

Overlay renders one indicator per script in `status.Scripts` (from `SCRIPT LIST`). Indicator count MUST equal script count in current `.agnt.kdl` config. Stale registry entries directly cause phantom indicators.

## State Transition Atomicity

All transitions use `CompareAndSwapState()`. CAS fails → re-read state, decide again. Never force a transition — if process moved to different state between your read and write, something else handled it.