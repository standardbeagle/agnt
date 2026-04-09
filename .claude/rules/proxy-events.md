---
paths:
  - "internal/daemon/proxy_events.go"
  - "internal/autostart/autostart.go"
  - "internal/daemon/urltracker.go"
---

# Proxy Event System

## Event Types

Three event types flow through the `proxyEvents` channel:

| Event | Trigger | Handler | Result |
|-------|---------|---------|--------|
| `URLDetected` | URLTracker finds a URL in process output | `handleURLDetected` | Create proxy targeting detected URL |
| `ExplicitStart` | `autostartProxy` for non-script-linked proxies | `handleExplicitStart` | Create proxy with explicit target |
| `ScriptStopped` | Process exits or is cleaned up | `handleScriptStopped` | Stop all proxies linked to that script |

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

## Fallback Flow (Required — Currently Missing)

When URL detection fails for a script-linked proxy:

```
Script reaches Running state
  → Health check runs (periodic or on-connect)
  → Finds: config expects proxy for script, no proxy exists, script is running
  → Checks: does proxy config have fallback-port?
    → Yes: create proxy targeting localhost:fallback-port
    → No: emit warning event — "proxy X expected but no URL detected and no fallback-port configured"
```

This flow does not exist today and is the primary bug to fix.

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
