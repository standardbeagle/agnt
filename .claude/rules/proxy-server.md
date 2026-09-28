---
paths:
  - "internal/proxy/**"
  - "internal/daemon/hub_stream.go"
  - "internal/daemon/hub_proxy*.go"
---

# Reverse proxy

`ProxyServer` (`internal/proxy/server.go`) wraps `httputil.ReverseProxy`. It forwards and logs traffic, injects JS into `text/html` responses only, serves the frontend WebSocket at `/__devtool_metrics`, and runs `proxy exec` browser control.

- The default port is hash-derived from the project, so it is stable (10000–60000). Check `listen_addr` in the response for the port actually bound.
- `/__devtool_metrics` is reserved and shadows any backend route with the same path.
- Auto-restart is capped at 5 per minute.
- JS injection fails silently on malformed HTML.
- `Create()` on an existing id returns `ErrProxyExists`.
- `TrafficLogger` (`logger.go`) is a 1000-entry circular buffer with 16 log types. It caps request and response bodies at 10KB, reports overflow in the stats `dropped` count, and feeds the StreamEvents hub through `onLogEntry`.
  - Its `ErrorsOnly` filter also drops diagnostic warnings; use an explicit `Types` filter instead.
- The StreamEvents hub (`internal/daemon/hub_stream.go`) registers `StreamSink`s with type/proxy/process/severity/grep filters and a 30s keepalive.
- Browser toasts are not the agent queue. A `LogCustom` warn never reaches the agent (use `LogDiagnostic`), and toasts are never auto-mirrored to the agent, which would loop `channel_reply` and hook echoes.
- Director header rewrites (`Origin`, `Referer`, `Host`) apply only to the value the proxy itself introduced; see `publish-security-review-lessons.md` §15.
