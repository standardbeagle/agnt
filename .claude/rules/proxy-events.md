---
paths:
  - "internal/daemon/proxy_events*.go"
  - "internal/daemon/daemon_autostart.go"
  - "internal/daemon/urltracker*.go"
---

# Proxy Event System

`d.proxyEvents` (buffer 10) is drained by `handleProxyEvents` (`proxy_events.go`):

| Event | Trigger | Handler | Result |
|-------|---------|---------|--------|
| `URLDetected` | URLTracker finds a URL in output | `handleURLDetected` | proxy to detected URL |
| `ExplicitStart` | `autostartProxy`, non-script proxy | `handleExplicitStart` | proxy to explicit target |
| `ScriptStopped` | process removed (URL tracker), exits on its own with no restart to follow (`watchProcessExit`), auto-restart gives up or fails, `restartProcess` fails, reconcile finds it dead | `handleScriptStopped` | stop the script's proxies, unless a live replacement already runs under the id |
| `FallbackPortCheck` | 30s after autostart (`scheduleFallbackPortChecks`) | `handleFallbackPortCheck` | proxy to `localhost:<fallback-port>` if nothing exists yet |

A restart keeps the proxies: `watchProcessExit` skips the event while `restartPending` (PROC RESTART / RESTART-ALL) is set or auto-restart is registered, and the auto-restart monitor emits it itself when it stops restarting.

Full channel → event dropped and recorded as `proxy_event_dropped` in the startup log. Reconciliation must still catch missing proxies.

## URL Detection

`URLTracker.scanProcess`: strip ANSI → apply `url-matchers` (else whole line) → `devServerURLRegex` → dedupe `seenURLs` → `URLDetected` → `handleURLDetected` reloads `.agnt.kdl` and creates proxies whose `script` matches.

Process IDs are `{project-hash}:{scriptName}`; the handler splits on `:`. Keep that format, and keep the name equal to the `scripts {}` key.

## Fallback

One goroutine per script-linked proxy with `fallback-port > 0` waits 30s, then emits `FallbackPortCheck`. If a proxy exists (by `makeProcessID` id, or any detection-created proxy for the script) → `startup_proxy_fallback_skipped_already_running`; else create → `startup_proxy_fallback_used` / `…_failed`. All outcomes go through `startupErrorStore`. Do not shorten the 30s in production; tests call the handler or send the event directly.

A detection that arrives after the fallback proxy exists replaces it, never duplicates it (`replaceFallbackProxy`). The fallback is found by exact `makeProcessID` id; if it already targets the detected URL (scheme, host, port, path up to a trailing slash) nothing changes. Otherwise it is stopped with full cleanup (config snapshot, script link, incident owner, admin entry, persisted state) and the detected-URL proxy binds the **same listen port**, strictly, retried for 2s, carrying auto-restart and the live public URL; the overlay binding comes from the owning session as on any creation. The proxy id changes (`makeProcessID` → `makeProxyIDFromURL`), so open browser connections drop and reconnect to the same address. Logged as `startup_proxy_fallback_replaced` (old target → new target). If the port cannot be reclaimed, `startup_proxy_fallback_replace_failed` (error) is logged and the proxy is created on its configured port instead.

## Proxy ID Formats

- Event-driven: `{hash}:{proxyName}-{host}-{port}` (`makeProxyIDFromURL`)
- Explicit / fallback: `{hash}:{proxyName}` (`makeProcessID`)

Do not assume one format when looking proxies up.

## Remaining Silent Failure

A `url-matchers` pattern that never matches gives no feedback — the proxy just never appears (until fallback, if configured).
