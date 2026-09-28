---
paths:
  - "internal/daemon/**"
  - "vendor/github.com/standardbeagle/go-cli-server/process/**"
---

# Process & Proxy Lifecycle

## Process States

| State | Detected by | Action |
|-------|-------------|--------|
| Starting | no URL/ready signal yet; build output | never kill; wait for ready, error, or stall |
| Running Healthy | URL detected, normal output | none |
| Running With Errors | AlertScanner classifies compile errors/panics; process alive | surface to agent; **never** restart or kill |
| Stalled | no output AND no recent alerts AND not Starting, for the stall threshold | mark, surface, offer restart |
| Dead | PID gone | clean state, emit `ScriptStopped`, clean proxies |

**Never kill an active process.** Output is activity, even error output — `dotnet watch` printing compile errors is working; the code is broken. Error output resets the stall timer. Stall detection is off during shutdown. Stall threshold target is 120s default, per-script configurable (not yet an `.agnt.kdl` key).

Transitions use `CompareAndSwapState()`; on CAS failure re-read and decide again, never force.

## Proxy Creation

1. **Event-driven** (script-linked): `URLDetected` → `handleURLDetected`.
2. **Explicit**: `autostartProxy` → `ExplicitStart`.

**fallback-port contract**: with both `script` and `fallback-port`, wait for URL detection; if it never fires, create the proxy on `fallback-port`; a later detection updates the target, never duplicates. Details in `proxy-events.md`.

## Shutdown

Stop registrations (`atomic.Bool`) → SIGTERM process groups (Windows: `CTRL_BREAK_EVENT`) → wait graceful timeout (5s) → SIGKILL.

## Session Resource Ownership

Session → scripts → processes; proxies by project path. Each script has an owner session plus observers.

**Non-last session leaves**: drop it as observer; transfer ownership to the first remaining observer; stop only zero-observer processes; remove only their registry entries.

**Last session for a project leaves** (`CleanupSessionResources`): stop all processes, stop all project proxies, empty the script registry and `scriptConfigs` for the project, unregister auto-restarters, unregister the session.

The registry is a cache rebuilt from `.agnt.kdl`. Stale entries show up as phantom overlay indicators: indicator count must equal the script count in current config (`SCRIPT LIST`).
