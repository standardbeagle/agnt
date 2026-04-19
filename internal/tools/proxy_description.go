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
  restart: Stop then start with same config
  toast:   Send notification to connected browsers
  chaos:   Inject controlled errors for resilience testing

Port selection:
  - Default: stable port derived from target URL hash (range 10000-60000)
  - Specify 'port' only when a fixed port is required
  - Assigned port returned in 'listen_addr' response field

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
