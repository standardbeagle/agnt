---
sidebar_position: 4
---

# proxy

Manage reverse proxies with traffic logging and frontend instrumentation.

## Synopsis

```json
proxy {action: "<action>", ...params}
```

## Actions

| Action | Description |
|--------|-------------|
| `start` | Create and start a reverse proxy |
| `stop` | Stop a running proxy |
| `status` | Get proxy status and statistics |
| `list` | List all running proxies |
| `exec` | Execute JavaScript in connected browsers |

## start

Create and start a reverse proxy.

```json
proxy {
  action: "start",
  id: "app",
  target_url: "http://localhost:3000",
  port: 8080
}
```

Parameters:
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `id` | string | Yes | - | Unique proxy identifier |
| `target_url` | string | Yes | - | Backend server URL |
| `port` | integer | No | 8080 | Listen port (0 for auto-assign) |
| `max_log_size` | integer | No | 1000 | Maximum log entries |

Response:
```json
{
  "id": "app",
  "status": "running",
  "target_url": "http://localhost:3000",
  "listen_addr": ":8080",
  "message": "Proxy started"
}
```

### Port Auto-Assignment

If port is busy:
```json
proxy {action: "start", id: "app", target_url: "http://localhost:3000", port: 8080}
→ {
    "listen_addr": ":45123",
    "message": "Port 8080 was busy, using 45123"
  }
```

Request any available port:
```json
proxy {action: "start", id: "app", target_url: "http://localhost:3000", port: 0}
→ {listen_addr: ":49152"}
```

## stop

Stop a running proxy.

```json
proxy {action: "stop", id: "app"}
```

Response:
```json
{
  "id": "app",
  "message": "Proxy stopped"
}
```

## status

Get proxy status and statistics.

```json
proxy {action: "status", id: "app"}
```

Response:
```json
{
  "id": "app",
  "status": "running",
  "target_url": "http://localhost:3000",
  "listen_addr": ":8080",
  "uptime": "15m32s",
  "total_requests": 1542,
  "log_stats": {
    "http_entries": 1000,
    "error_entries": 3,
    "performance_entries": 45,
    "dropped": 542
  },
  "restart_count": 0,
  "last_error": null
}
```

## list

List all running proxies.

```json
proxy {action: "list"}
```

Response:
```json
{
  "proxies": [
    {
      "id": "frontend",
      "status": "running",
      "target_url": "http://localhost:3000",
      "listen_addr": ":8080"
    },
    {
      "id": "api",
      "status": "running",
      "target_url": "http://localhost:4000",
      "listen_addr": ":8081"
    }
  ],
  "active_count": 2
}
```

## exec

Execute JavaScript in connected browser clients.

```json
proxy {action: "exec", id: "app", code: "document.title"}
```

Parameters:
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | Proxy ID |
| `code` | string | Yes | JavaScript code to execute |

Response:
```json
{
  "result": "My App - Dashboard",
  "clients": 1
}
```

### Examples

```json
// Get page title
proxy {action: "exec", id: "app", code: "document.title"}

// Get current URL
proxy {action: "exec", id: "app", code: "window.location.href"}

// Access __devtool API
proxy {action: "exec", id: "app", code: "window.__devtool.inspect('#header')"}

// Take screenshot
proxy {action: "exec", id: "app", code: "window.__devtool.screenshot('debug')"}

// Highlight element
proxy {action: "exec", id: "app", code: "window.__devtool.highlight('.button')"}
```

### Timeout

Execution has a 30-second timeout. For async operations:

```json
// Interactive element selection (waits for user click)
proxy {action: "exec", id: "app", code: "window.__devtool.selectElement()"}

// Ask user a question
proxy {action: "exec", id: "app", code: "window.__devtool.ask('OK?', ['Yes', 'No'])"}
```

## Features

### What the Proxy Does

1. **Forwards HTTP Traffic** - Transparent to the application
2. **Logs Requests/Responses** - Captured in circular buffer
3. **Injects JavaScript** - Adds `window.__devtool` to HTML pages
4. **Captures Errors** - Frontend JavaScript errors with stack traces
5. **Collects Metrics** - Page load timing, paint metrics
6. **Tracks Pages** - Groups requests by page session

### WebSocket Support

WebSocket connections (e.g., HMR) are proxied transparently:

```
Browser ←→ Proxy (:8080) ←→ Dev Server (:3000)
              │
              └── /__devtool_metrics (reserved for metrics WebSocket)
```

### Auto-Restart

Proxies auto-restart on crash (max 5 restarts per minute):

```json
proxy {action: "status", id: "app"}
→ {
    "restart_count": 2,
    "last_error": "bind: address already in use"
  }
```

## Error Responses

### Proxy Not Found

```json
{
  "error": "proxy not found",
  "id": "nonexistent"
}
```

### ID Already Exists

```json
{
  "error": "proxy already exists",
  "id": "app"
}
```

### Invalid Target URL

```json
{
  "error": "invalid target URL",
  "target_url": "not-a-url"
}
```

### No Connected Clients

```json
{
  "error": "no connected clients",
  "id": "app"
}
```

## Real-World Patterns

### Development Setup

```json
// Start dev server
run {script_name: "dev"}

// Wait for it to be ready
proc {action: "output", process_id: "dev", grep: "ready", tail: 5}

// Start proxy
proxy {action: "start", id: "app", target_url: "http://localhost:3000", port: 8080}

// Browse to http://localhost:8080
```

### Multiple Environments

```json
// Local dev
proxy {action: "start", id: "local", target_url: "http://localhost:3000", port: 8080}

// Staging
proxy {action: "start", id: "staging", target_url: "https://staging.example.com", port: 8081}

// Compare traffic
proxylog {proxy_id: "local", types: ["http"], url_pattern: "/api"}
proxylog {proxy_id: "staging", types: ["http"], url_pattern: "/api"}
```

### Debugging Session

```json
// Start proxy
proxy {action: "start", id: "debug", target_url: "http://localhost:3000"}

// User navigates to problem page...

// Check for errors
proxylog {proxy_id: "debug", types: ["error"]}

// Inspect problem element
proxy {action: "exec", id: "debug", code: "window.__devtool.inspect('.broken-component')"}

// Take screenshot
proxy {action: "exec", id: "debug", code: "window.__devtool.screenshot('bug')"}
```

## See Also

- [proxylog](/api/proxylog) - Query proxy traffic logs
- [currentpage](/api/currentpage) - View page sessions
- [Reverse Proxy Feature](/features/reverse-proxy)
- [Frontend Diagnostics](/features/frontend-diagnostics)
