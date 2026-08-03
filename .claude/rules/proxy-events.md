---
paths:
  - "internal/daemon/proxy_events.go"
  - "internal/autostart/autostart.go"
  - "internal/daemon/urltracker.go"
---

# Proxy Event System

## Event Types

Four event types flow through `proxyEvents` channel:

| Event | Trigger | Handler | Result |
|-------|---------|---------|--------|
| `URLDetected` | URLTracker finds URL in process output | `handleURLDetected` | Create proxy targeting detected URL |
| `ExplicitStart` | `autostartProxy` for non-script-linked proxies | `handleExplicitStart` | Create proxy with explicit target |
| `ScriptStopped` | Process exits or cleaned up | `handleScriptStopped` | Stop all proxies linked to that script |
| `FallbackPortCheck` | `scheduleFallbackPortChecks` 30s after autostart | `handleFallbackPortCheck` | Create proxy targeting `localhost:<fallback-port>` if URL detection never fired |

## Event Channel Contract

`proxyEvents` channel buffered (10 events). Channel full → events **dropped with warning log**. Known failure mode:
- High-volume URL detection (many scripts start at once) fills buffer
- Dropped events = proxies silently not created
- Health check reconciliation must detect missing proxies as safety net

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

Process IDs have format `{project-hash}:{scriptName}`. Handler splits on `:` to extract script name, then matches against `proxyConfig.Script`. Coupling means:
- Process IDs MUST keep `prefix:name` format
- Script name in process ID MUST match key in `.agnt.kdl` `scripts {}` block

## Fallback Flow (FallbackPortCheck)

URL detection fails for script-linked proxy that declares `fallback-port`:

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

Fallback handler uses same proxy-id scheme as explicit-start path (`makeProcessID(projectPath, proxyName)`) so idempotency checks align, and so late URL-detection event (uses `makeProxyIDFromURL`) can still create own distinct entry.

Both success and failure entries flow through `startupErrorStore` — outcome surfaces in `get_incidents` and overlay, never silent.

30s timer constant must not shorten for production. Tests exercise `handleFallbackPortCheck` directly or deliver `FallbackPortCheck` events to channel, bypassing timer.

## Silent Failure Points (Known)

Places where current code silently fails — each must be addressed:

1. **`autostartProxy` line 1511**: Script-linked proxies skipped with `debug.Log` only — no session log entry
2. **`handleURLDetected`**: Config reload fails → warning to debug log only
3. **Event channel full**: Warning to debug log, event dropped, proxy never created
4. **URL matcher mismatch**: No feedback pattern matched no output — proxy just never appears

## Proxy ID Formats

Two formats depending on creation path:
- Event-driven: `{project-hash}:{proxyName}-{host}-{port}` (via `makeProxyIDFromURL`)
- Explicit: `{project-hash}:{proxyName}` (via `makeProcessID`)

Inconsistency can break lookups if code assumes one format. Beware when querying proxies by ID.