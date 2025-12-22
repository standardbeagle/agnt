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
| `chaos` | Configure chaos engineering (network failures, latency) |
| `toast` | Display toast notifications in the browser |

## start

Create and start a reverse proxy.

```json
proxy {
  action: "start",
  id: "app",
  target_url: "http://localhost:3000"
}
```

Parameters:
| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `id` | string | Yes | - | Unique proxy identifier |
| `target_url` | string | Yes | - | Backend server URL |
| `port` | integer | No | hash-based | Listen port. Only specify if you need a specific port. |
| `max_log_size` | integer | No | 1000 | Maximum log entries |
| `bind_address` | string | No | `127.0.0.1` | Bind address: `127.0.0.1` (localhost only) or `0.0.0.0` (all interfaces for tunnel/mobile testing) |
| `public_url` | string | No | - | Public URL for tunnel services (e.g., `https://abc123.trycloudflare.com`). Used for URL rewriting. |

Response:
```json
{
  "id": "app",
  "status": "running",
  "target_url": "http://localhost:3000",
  "listen_addr": "127.0.0.1:45849",
  "message": "Proxy started"
}
```

### Port Selection

By default, the proxy assigns a **stable port based on a hash of the target URL**. This ensures:
- The same target URL always gets the same port (consistent across restarts)
- Different URLs get different ports (avoids conflicts)
- Ports are in the range 10000-60000 (avoids well-known and ephemeral ports)

**Recommended**: Let the proxy choose the port automatically. Only specify `port` if you need a specific port number.

Request a specific port:
```json
proxy {action: "start", id: "app", target_url: "http://localhost:3000", port: 9000}
→ {listen_addr: ":9000"}
```

If the requested port is busy, the proxy finds an available one:
```json
proxy {action: "start", id: "app", target_url: "http://localhost:3000", port: 9000}
→ {
    "listen_addr": ":45123",
    "message": "Port 9000 was busy, using 45123"
  }
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

## chaos

Configure chaos engineering to simulate network failures, latency, and API errors.

```json
proxy {action: "chaos", id: "app", preset: "flaky-api"}
```

Parameters:
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | Proxy ID |
| `preset` | string | No | Built-in preset name |
| `rules` | array | No | Custom chaos rules |
| `enabled` | boolean | No | Enable/disable chaos |
| `clear` | boolean | No | Clear all rules |
| `status` | boolean | No | Get chaos status |
| `enable_rule` | string | No | Enable specific rule by ID |
| `disable_rule` | string | No | Disable specific rule by ID |

### Built-in Presets

| Preset | Description |
|--------|-------------|
| `mobile-3g` | 200-2000ms latency, 2% packet loss |
| `mobile-4g` | 50-500ms latency, 0.5% packet loss |
| `flaky-api` | Random 500s, timeouts, variable latency |
| `race-condition` | Out-of-order responses, high variance delays |
| `stale-tab` | 3-hour delays (test token expiry) |
| `slow-connection` | 5KB/s bandwidth throttling |
| `connection-drops` | 10% mid-response disconnects |
| `rate-limited` | 20% 429 errors |

### Examples

```json
// Apply preset
proxy {action: "chaos", id: "app", preset: "mobile-3g"}

// Custom rules
proxy {
  action: "chaos",
  id: "app",
  rules: [
    {
      "id": "api-latency",
      "type": "latency",
      "enabled": true,
      "url_pattern": "/api/.*",
      "min_latency_ms": 500,
      "max_latency_ms": 2000,
      "probability": 0.3
    }
  ]
}

// Check status
proxy {action: "chaos", id: "app", status: true}
→ {enabled: true, preset: "flaky-api", stats: {affected_count: 38, errors_injected: 7}}

// Disable chaos
proxy {action: "chaos", id: "app", enabled: false}

// Clear all rules
proxy {action: "chaos", id: "app", clear: true}
```

See [Chaos Engineering](/features/chaos-engineering) for complete documentation.

## toast

Display toast notifications in connected browsers.

```json
proxy {action: "toast", id: "app", message: "Build complete!"}
```

Parameters:
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | string | Yes | Proxy ID |
| `message` | string | Yes | Notification message |
| `toast_type` | string | No | `success`, `error`, `warning`, `info` (default: `info`) |
| `toast_title` | string | No | Optional title |
| `toast_duration` | integer | No | Duration in milliseconds (default: 5000) |

### Examples

```json
// Simple notification
proxy {action: "toast", id: "app", message: "Saved!"}

// Success with title
proxy {action: "toast", id: "app", message: "All tests passed", toast_type: "success", toast_title: "Tests"}

// Error that stays longer
proxy {action: "toast", id: "app", message: "Build failed", toast_type: "error", toast_duration: 10000}

// Warning
proxy {action: "toast", id: "app", message: "Slow response detected", toast_type: "warning"}
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

// Start proxy (port auto-assigned based on target URL)
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}

// Check the listen_addr in the response, then browse to that address
```

### Multiple Environments

```json
// Local dev (each gets a unique port based on URL hash)
proxy {action: "start", id: "local", target_url: "http://localhost:3000"}

// Staging
proxy {action: "start", id: "staging", target_url: "https://staging.example.com"}

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

## Mobile Testing with Tunnels

For testing on real mobile devices, you need to expose your proxy publicly. agnt supports this via the `bind_address` and `public_url` options, combined with the [tunnel](/api/tunnel) tool.

### Quick Setup

```json
// 1. Start proxy on all interfaces
proxy {
  action: "start",
  id: "app",
  target_url: "http://localhost:3000",
  bind_address: "0.0.0.0"
}

// 2. Start a Cloudflare tunnel pointing to the proxy
tunnel {
  action: "start",
  id: "app",
  provider: "cloudflare",
  local_port: 45849,
  proxy_id: "app"
}
```

The tunnel automatically configures the proxy's `public_url`, enabling proper URL rewriting for HTTPS.

### Manual Configuration

If you're using an external tunnel service:

```json
proxy {
  action: "start",
  id: "app",
  target_url: "http://localhost:3000",
  bind_address: "0.0.0.0",
  public_url: "https://your-tunnel-url.trycloudflare.com"
}
```

### Security Note

By default, proxies bind to `127.0.0.1` (localhost only) for security. Only use `0.0.0.0` when you need external access (tunnels, mobile testing).

## See Also

- [Chaos Engineering](/features/chaos-engineering) - Complete chaos testing documentation
- [tunnel](/api/tunnel) - Manage Cloudflare/ngrok tunnels
- [proxylog](/api/proxylog) - Query proxy traffic logs
- [currentpage](/api/currentpage) - View page sessions
- [Mobile Testing Guide](/use-cases/mobile-testing) - Complete mobile testing workflow
- [Reverse Proxy Feature](/features/reverse-proxy)
- [Frontend Diagnostics](/features/frontend-diagnostics)
