---
paths:
  - "internal/daemon/proxy_events.go"
  - "internal/autostart/autostart.go"
  - "internal/daemon/urltracker.go"
---

# Proxy Event System

## Event Types

Four event types flow through the `proxyEvents` channel:

| Event | Trigger | Handler | Result |
|-------|---------|---------|--------|
| `URLDetected` | URLTracker finds a URL in process output | `handleURLDetected` | Create proxy targeting detected URL |
| `ExplicitStart` | `autostartProxy` for non-script-linked proxies | `handleExplicitStart` | Create proxy with explicit target |
| `ScriptStopped` | Process exits or is cleaned up | `handleScriptStopped` | Stop all proxies linked to that script |
| `FallbackPortCheck` | `scheduleFallbackPortChecks` 30s after autostart | `handleFallbackPortCheck` | Create proxy targeting `localhost:<fallback-port>` if URL detection never fired |

## Event Channel Contract

The `proxyEvents` channel is buffered (10 events). If the channel is full, events are **dropped with a warning log**. This is a known failure mode:
- High-volume URL detection (multiple scripts starting simultaneously) can fill the buffer
- Dropped events mean proxies silently don't get created
- The health check reconciliation must detect missing proxies as a safety net

## URL Detection Flow

```
Process output → URLTracker.scanProcess()
  → Strip ANSI codes
  → Apply url-matchers (or scan entire line if none)
  → Extract URLs via devServerURLRegex
  → Deduplicate against seenURLs
  → Callback: onURLDetected(processID, url)
    → Emit URLDetected event to proxyEvents channel
      → handleURLDetected loads .agnt.kdl
      → Finds proxy configs with matching script name
      → Creates proxy targeting the detected URL
```

### Script Name Extraction

Process IDs have format `{project-hash}:{scriptName}`. The event handler splits on `:` to extract the script name, then matches against `proxyConfig.Script`. This coupling means:
- Process IDs MUST maintain the `prefix:name` format
- The script name in the process ID MUST match the key in `.agnt.kdl` `scripts {}` block

## Fallback Flow (FallbackPortCheck)

When URL detection fails for a script-linked proxy that declares `fallback-port`:

```
autostart → scheduleFallbackPortChecks spawns one goroutine per
             script-linked proxy with fallback-port > 0
  → goroutine waits 30s (or ctx cancel)
  → emits FallbackPortCheck event onto d.proxyEvents
    → handleProxyEvents dispatches to handleFallbackPortCheck
      → if proxy already exists under makeProcessID id
          or URL detection already created any proxy for the script
        → log startup_proxy_fallback_skipped_already_running (info)
      → else create proxy targeting http://localhost:<fallback-port>
        → on success: startup_proxy_fallback_used (info)
        → on failure: startup_proxy_fallback_failed (warning)
```

The fallback handler uses the same proxy-id scheme as the explicit-start
path (`makeProcessID(projectPath, proxyName)`) so idempotency checks align
and so a subsequent URL-detection event (which uses `makeProxyIDFromURL`) can
still create its own distinct entry if it fires late.

Both success and failure entries flow through `startupErrorStore`, so the
outcome surfaces in `get_errors` and the overlay — never silent.

The 30s timer constant must not be shortened for production; tests exercise
`handleFallbackPortCheck` directly or deliver `FallbackPortCheck` events to
the channel, bypassing the timer.

## Silent Failure Points (Known)

These are places where the current code silently fails — each must be addressed:

1. **`autostartProxy` line 1511**: Script-linked proxies skipped with `debug.Log` only — no session log entry
2. **`handleURLDetected`**: If config reload fails, warning goes to debug log only
3. **Event channel full**: Warning to debug log, event dropped, proxy never created
4. **URL matcher mismatch**: No feedback that the pattern didn't match any output — proxy just never appears

## Proxy ID Formats

Two formats depending on creation path:
- Event-driven: `{project-hash}:{proxyName}-{host}-{port}` (via `makeProxyIDFromURL`)
- Explicit: `{project-hash}:{proxyName}` (via `makeProcessID`)

This inconsistency can cause lookups to fail if code assumes one format. Be aware when querying proxies by ID.
