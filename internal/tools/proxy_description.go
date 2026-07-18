package tools

// ProxyToolDescription is the shared description for the proxy MCP tool.
// Both daemon_tools.go and proxy_tools.go reference this constant so the
// description stays in sync across both registration paths.
const ProxyToolDescription = `Manage reverse proxy servers with traffic logging and frontend instrumentation.

Discovery (do this FIRST when using exec):
  proxy {action: "exec", search: "X"}          -- find helpers by keyword
  proxy {action: "exec", describe: "name"}     -- get full signature + example
  proxy {action: "exec", code: "..."}          -- run JS (use helpers, not raw DOM)

Proxy lifecycle:
  proxy {action: "start", id: "dev", target_url: "http://localhost:3000"}
  proxy {action: "status", id: "dev"}
  proxy {action: "stop", id: "dev"}
  proxy {action: "list"}

Additional actions:
  restart:  Stop then start with same config
  navigate: Drive the page — proxy {action:"navigate", id:"dev", direction:"back|forward|reload"} or {direction:"goto", target_url:"..."}
  resize:   Resize the page viewport from outside (no reload) — proxy {action:"resize", id:"dev", width:375} (width:0 resets)
  toast:    Send notification to connected browsers
  chaos:    Inject controlled errors for resilience testing

Always-wrap frame model (the page runs inside a content iframe hosted by a chrome shell):
  - exec defaults to the INNER page content frame (the frame the developer is interacting with).
  - target:"outer" runs the REPL against the OUTER chrome shell (the proxy UI runtime / host) instead:
      proxy {action:"exec", id:"dev", target:"outer", code:"location.href"}
  - target:"inner" (default) scripts the page content frame.
  - resize/navigate observe and drive the page from the shell: resize the viewport, then run
    api_audit / loading_audit / responsive_audit (they target the inner frame) to measure the
    page at that size — all without a page reload.

Port selection:
  - Default: stable port derived from target URL hash (range 10000-60000)
  - Specify 'port' only when a fixed port is required
  - Assigned port returned in 'listen_addr' response field

Public URLs:
  - This tool serves a local listener; to expose it publicly use the 'tunnel' tool
    (cloudflare/ngrok), which mints a public URL and reports it back to the proxy
    (surfaced as 'public_url' in start/status).
  - The 'public_url' start field only sets URL-rewriting when you already front the
    proxy with your own tunnel; it does not create a tunnel.

Toast notifications:
  proxy {action: "toast", id: "dev", toast_message: "Task complete"}
  proxy {action: "toast", id: "dev", toast_type: "error", toast_title: "Build Failed", toast_message: "See console"}
  Toast types: success, error, warning, info (default)

The proxy automatically:
  - Logs all HTTP traffic (requests/responses)
  - Injects JavaScript to capture frontend errors
  - Captures performance metrics (page load, resources)
  - Provides WebSocket endpoint for metrics
  - Injects __devtool API with 50+ diagnostic functions

Each proxy has separate log storage and WebSocket connections.`
