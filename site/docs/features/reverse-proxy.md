---
sidebar_position: 3
---

# Reverse Proxy

The reverse proxy is agnt's most powerful feature, enabling comprehensive frontend debugging through transparent HTTP interception and JavaScript instrumentation.

## Overview

When you start a proxy:

1. **HTTP Proxy** - All traffic is forwarded to your dev server
2. **Traffic Logging** - Requests and responses are captured
3. **JS Injection** - Diagnostic JavaScript is added to HTML pages
4. **WebSocket Server** - Receives metrics from instrumented frontend
5. **Page Sessions** - Groups requests by page view

## Quick Start

```json
// Start your dev server
run {script_name: "dev"}

// Create proxy in front of it
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}
```

The proxy auto-assigns a stable port based on the target URL (check `listen_addr` in response). Everything works normally, but now you have:

- Complete HTTP traffic logs
- JavaScript error capture
- Performance metrics
- 50+ diagnostic primitives via `window.__devtool`

## Proxy Management

### Start a Proxy

```json
proxy {action: "start", id: "myapp", target_url: "http://localhost:3000"}
→ {
    "id": "myapp",
    "status": "running",
    "target_url": "http://localhost:3000",
    "listen_addr": ":45849"
  }
```

### Port Selection

By default, the proxy assigns a **stable port based on a hash of the target URL**:
- Same URL always gets the same port (consistent across restarts)
- Different URLs get different ports (avoids conflicts)
- Ports are in range 10000-60000 (avoids well-known and ephemeral ports)

**Recommended**: Let the proxy choose the port automatically. Only specify `port` if you need a specific port.

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

### Check Status

```json
proxy {action: "status", id: "myapp"}
→ {
    "id": "myapp",
    "status": "running",
    "uptime": "15m32s",
    "total_requests": 1542,
    "log_stats": {
      "http_entries": 1000,
      "error_entries": 3,
      "performance_entries": 45
    }
  }
```

### List All Proxies

```json
proxy {action: "list"}
→ {
    "proxies": [
      {"id": "frontend", "status": "running", "listen_addr": ":8080"},
      {"id": "api", "status": "running", "listen_addr": ":8081"}
    ],
    "active_count": 2
  }
```

### Stop a Proxy

```json
proxy {action: "stop", id: "myapp"}
→ {message: "Proxy stopped"}
```

## Traffic Logging

### Query HTTP Traffic

```json
proxylog {proxy_id: "app", types: ["http"], limit: 20}
→ {
    "entries": [
      {
        "type": "http",
        "method": "GET",
        "url": "/api/users",
        "status": 200,
        "duration_ms": 45,
        "request_headers": {...},
        "response_headers": {...},
        "request_body": null,
        "response_body": "[{\"id\": 1, ...}]"
      }
    ]
  }
```

### Filter by Method

```json
proxylog {proxy_id: "app", types: ["http"], methods: ["POST", "PUT"]}
→ Only POST and PUT requests
```

### Filter by Status Code

```json
proxylog {proxy_id: "app", types: ["http"], status_codes: [500, 502, 503]}
→ Only server errors
```

### Filter by URL Pattern

```json
proxylog {proxy_id: "app", types: ["http"], url_pattern: "/api"}
→ Only requests containing "/api"
```

### Time-Based Queries

```json
proxylog {proxy_id: "app", types: ["http"], since: "5m"}
→ Last 5 minutes

proxylog {proxy_id: "app", since: "2024-01-15T10:00:00Z", until: "2024-01-15T10:30:00Z"}
→ Specific time range
```

## Error Tracking

The proxy automatically captures frontend JavaScript errors:

```json
proxylog {proxy_id: "app", types: ["error"]}
→ {
    "entries": [
      {
        "type": "error",
        "message": "Cannot read property 'map' of undefined",
        "source": "http://localhost:8080/static/js/main.js",
        "line": 142,
        "column": 23,
        "stack": "TypeError: Cannot read property 'map' of undefined\n    at UserList...",
        "url": "http://localhost:8080/users",
        "timestamp": "2024-01-15T10:32:15Z"
      }
    ]
  }
```

Captures:
- Uncaught exceptions
- Unhandled promise rejections
- Error source and line/column
- Full stack trace
- Page URL where error occurred

## Performance Metrics

Automatically collected on every page load:

```json
proxylog {proxy_id: "app", types: ["performance"]}
→ {
    "entries": [
      {
        "type": "performance",
        "url": "http://localhost:8080/dashboard",
        "navigation": {
          "dom_content_loaded": 245,
          "load_event": 892
        },
        "paint": {
          "first_paint": 156,
          "first_contentful_paint": 234
        },
        "resources": [
          {"name": "main.js", "duration": 123, "size": 45678},
          {"name": "styles.css", "duration": 45, "size": 12345}
        ]
      }
    ]
  }
```

Metrics include:
- DOM content loaded time
- Page load event time
- First paint / First contentful paint
- Resource timing (up to 50 resources)

## Page Sessions

Group requests by page view for easier debugging:

```json
currentpage {proxy_id: "app"}
→ {
    "sessions": [
      {
        "id": "page-1",
        "url": "http://localhost:8080/dashboard",
        "started_at": "2024-01-15T10:30:00Z",
        "resource_count": 24,
        "error_count": 0
      },
      {
        "id": "page-2",
        "url": "http://localhost:8080/users",
        "started_at": "2024-01-15T10:31:15Z",
        "resource_count": 18,
        "error_count": 2
      }
    ]
  }
```

Get details for a specific page:

```json
currentpage {proxy_id: "app", action: "get", session_id: "page-2"}
→ {
    "session": {
      "id": "page-2",
      "url": "http://localhost:8080/users",
      "document": {...},
      "resources": [
        {"url": "/static/js/main.js", "status": 200, "duration": 123},
        {"url": "/api/users", "status": 200, "duration": 456}
      ],
      "errors": [
        {"message": "Cannot read property 'map'...", "line": 142}
      ],
      "performance": {...}
    }
  }
```

## Executing Browser Code

Run JavaScript in connected browsers:

```json
proxy {action: "exec", id: "app", code: "document.title"}
→ {result: "My App - Dashboard"}

proxy {action: "exec", id: "app", code: "window.location.href"}
→ {result: "http://localhost:8080/dashboard"}
```

This is how you access the [Frontend Diagnostics API](/features/frontend-diagnostics).

## Floating Indicator

Every proxied page includes a floating indicator bug in the corner that provides quick access to devtool features:

### Features

- **Message Panel** - Type messages to send to your AI agent
- **Screenshot** - Click and drag to select an area for screenshot
- **Element Selection** - Click to select any element and capture its details
- **Sketch Mode** - Draw wireframes directly on the page
- **Audit Dropdown** - Quick access to quality audits

### Audit Actions

The Audit dropdown provides one-click access to diagnostic functions, organized by category:

**Quality Audits**
| Action | Description |
|--------|-------------|
| Full Page Audit | Comprehensive audit with A-F grade and recommendations |
| Accessibility | WCAG issues (missing alt, labels, contrast) |
| Security | Mixed content, XSS risks, missing noopener |
| SEO / Meta | Meta tags, headings, document structure |

**Layout & Visual**
| Action | Description |
|--------|-------------|
| Layout Issues | Overflows, z-index contexts, offscreen elements |
| Text Fragility | Truncation, overflow, font issues (WCAG 1.4.10) |
| Responsive Risk | Elements that may break at different viewport sizes |

**Debug Context**
| Action | Description |
|--------|-------------|
| Last Click Context | What the user just clicked + mouse trail |
| Recent DOM Changes | Elements added/removed/modified in last 30 seconds |

**State & Network**
| Action | Description |
|--------|-------------|
| Browser State | localStorage, sessionStorage, cookies |
| Network/Resources | Resource timing and loading data |

**Technical**
| Action | Description |
|--------|-------------|
| DOM Complexity | Node count, depth, performance impact |
| CSS Quality | Inline styles, !important usage |

When you run an audit, the results are:
1. **Executed immediately** in the browser
2. **Summarized** as a chip in the message area
3. **Logged** to the proxy for the AI agent to query via `proxylog`

This lets the LLM receive structured diagnostic data instead of you describing problems in natural language.

### Programmatic Control

```javascript
// Toggle indicator visibility
window.__devtool.indicator.show()
window.__devtool.indicator.hide()
window.__devtool.indicator.toggle()

// Toggle the message panel
window.__devtool.indicator.togglePanel()
```

### Log Types

Messages and attachments from the indicator are logged and queryable:

```json
// User messages from the panel
proxylog {proxy_id: "app", types: ["panel_message"]}

// Screenshot captures
proxylog {proxy_id: "app", types: ["screenshot"]}

// Sketch captures
proxylog {proxy_id: "app", types: ["sketch"]}
```

## WebSocket Support

The proxy transparently handles WebSocket connections:

- Hot Module Replacement (HMR) works normally
- WebSocket upgrades are proxied to the target
- No additional configuration needed

```
Browser ←→ Proxy (8080) ←→ Dev Server (3000)
              │
              └── WebSocket for HMR
```

## Log Statistics

```json
proxylog {proxy_id: "app", action: "stats"}
→ {
    "total_entries": 1542,
    "by_type": {
      "http": 1489,
      "error": 8,
      "performance": 45
    },
    "dropped": 0,
    "max_entries": 1000
  }
```

Note: The log is a circular buffer (default 1000 entries). When full, oldest entries are dropped.

## Real-World Examples

### Debugging API Issues

```json
// Find failed API calls
proxylog {proxy_id: "app", types: ["http"], url_pattern: "/api", status_codes: [400, 401, 403, 404, 500]}
→ Found POST /api/users returning 400

// Get details
proxylog {proxy_id: "app", types: ["http"], url_pattern: "/api/users", methods: ["POST"]}
→ {
    "request_body": "{\"email\": \"invalid\"}",
    "response_body": "{\"error\": \"Invalid email format\"}"
  }
```

### Performance Investigation

```json
// Find slow pages
proxylog {proxy_id: "app", types: ["performance"]}
→ Dashboard page: load_event = 3500ms

// Check resource loading
currentpage {proxy_id: "app", action: "get", session_id: "page-1"}
→ main.js took 2100ms to load (blocking)
```

### Error Correlation

```json
// See errors
proxylog {proxy_id: "app", types: ["error"]}
→ Error on /users page at line 142

// Check what API calls happened before error
currentpage {proxy_id: "app", action: "get", session_id: "page-2"}
→ GET /api/users returned 500 before the JavaScript error
```

### Multiple Environments

```json
// Proxy staging (each gets unique port based on URL hash)
proxy {action: "start", id: "staging", target_url: "https://staging.example.com"}

// Proxy production (read-only debugging)
proxy {action: "start", id: "prod", target_url: "https://example.com"}

// Compare behavior
proxylog {proxy_id: "staging", types: ["http"], url_pattern: "/api/users"}
proxylog {proxy_id: "prod", types: ["http"], url_pattern: "/api/users"}
```

## Configuration

### Log Size

Default: 1000 entries. Configure when starting:

```json
proxy {action: "start", id: "app", target_url: "...", max_log_size: 5000}
```

### Auto-Restart

Proxies automatically restart if the HTTP server crashes (max 5 restarts per minute). Check status for crash info:

```json
proxy {action: "status", id: "app"}
→ {
    "last_error": "bind: address already in use",
    "restart_count": 2
  }
```

## Best Practices

1. **Use Meaningful IDs** - `frontend`, `api`, `staging` not `proxy1`
2. **Check listen_addr** - Port may be auto-assigned if busy
3. **Clear Old Sessions** - `currentpage {action: "clear"}` periodically
4. **Monitor Dropped Logs** - Check stats for `dropped` count
5. **Use Page Sessions** - Easier than searching raw HTTP logs

## Remote Debugging & Live Device Testing

One of the most powerful features of agnt's proxy is the ability to share your local development server with remote devices while maintaining full instrumentation. This enables:

- Testing on real mobile devices (iOS, Android)
- Cross-browser testing with BrowserStack or similar services
- Sharing work-in-progress with teammates or stakeholders
- Debugging issues that only occur on specific devices

### Tunnel Provider Options

agnt provides built-in support for tunnel services, plus you can manually configure any tunnel provider.

#### Cloudflare Quick Tunnels (Recommended)

**Best for**: Most use cases - free, fast, no account required.

```json
// Start proxy with external access
proxy {action: "start", id: "app", target_url: "http://localhost:3000", bind_address: "0.0.0.0"}

// Start Cloudflare tunnel with auto-configuration
tunnel {action: "start", id: "app", provider: "cloudflare", local_port: 45849, proxy_id: "app"}
```

**Pros**:
- Free, no account required
- Fast and reliable
- HTTPS by default
- No bandwidth limits
- Automatic URL discovery

**Install**:
```bash
# macOS
brew install cloudflare/cloudflare/cloudflared

# Linux (Debian/Ubuntu)
curl -L -o cloudflared.deb https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64.deb
sudo dpkg -i cloudflared.deb

# Windows
winget install --id Cloudflare.cloudflared
```

#### ngrok

**Best for**: Stable URLs, webhook testing, request inspection dashboard.

```json
tunnel {action: "start", id: "app", provider: "ngrok", local_port: 45849, proxy_id: "app"}
```

**Pros**:
- Stable URLs (with paid plan)
- Built-in request inspection dashboard
- Webhook replay and integrations
- Custom domains available

**Install**:
```bash
# macOS
brew install ngrok/ngrok/ngrok

# Linux/Windows - download from https://ngrok.com/download

# Configure (required)
ngrok config add-authtoken <your-token>
```

#### Tailscale Funnel (Manual Setup)

**Best for**: Teams already using Tailscale, persistent URLs, secure sharing.

Tailscale Funnel isn't natively integrated, but works well with agnt's proxy using manual configuration:

```bash
# 1. Start your proxy on all interfaces
proxy {action: "start", id: "app", target_url: "http://localhost:3000", bind_address: "0.0.0.0"}
# Note the port from listen_addr (e.g., 45849)

# 2. In a terminal, start Tailscale Funnel
tailscale funnel 45849
# This gives you a URL like: https://your-machine.tailnet-name.ts.net/

# 3. Update the proxy with the public URL for proper URL rewriting
proxy {action: "start", id: "app", target_url: "http://localhost:3000", bind_address: "0.0.0.0", public_url: "https://your-machine.tailnet-name.ts.net"}
```

**Pros**:
- Persistent, memorable URLs (based on machine name)
- Integrates with existing Tailscale setup
- No third-party account needed if you have Tailscale
- End-to-end encrypted

**Requirements**:
- Tailscale installed and authenticated
- Funnel enabled on your tailnet (requires admin approval)

### Live Device Testing Workflow

#### Testing on Your Phone

```json
// 1. Start your dev server
run {script_name: "dev"}

// 2. Start instrumented proxy on all interfaces
proxy {
  action: "start",
  id: "mobile",
  target_url: "http://localhost:3000",
  bind_address: "0.0.0.0"
}
// Response: {listen_addr: "0.0.0.0:45849"}

// 3. Start tunnel
tunnel {
  action: "start",
  id: "mobile",
  provider: "cloudflare",
  local_port: 45849,
  proxy_id: "mobile"
}
// Response: {public_url: "https://random-words.trycloudflare.com"}

// 4. Open the URL on your phone - you now have:
//    - Full error capture from mobile browser
//    - Floating indicator to send messages to your AI agent
//    - Screenshot capabilities
//    - All __devtool diagnostics
```

#### Checking Mobile-Specific Errors

```json
// After testing on device, check for errors
proxylog {proxy_id: "mobile", types: ["error"]}

// Check for mobile-specific issues (touch events, viewport, etc.)
proxylog {proxy_id: "mobile", types: ["http"], url_pattern: "/api"}

// View page sessions from mobile
currentpage {proxy_id: "mobile"}
```

### BrowserStack Integration

For automated testing across many devices, combine agnt with [BrowserStack's MCP server](https://github.com/browserstack/mcp-server).

#### Setup Both MCP Servers

```json title="claude_desktop_config.json"
{
  "mcpServers": {
    "agnt": {
      "command": "agnt",
      "args": ["mcp"]
    },
    "browserstack": {
      "command": "npx",
      "args": ["@anthropic-ai/browserstack-mcp"],
      "env": {
        "BROWSERSTACK_USERNAME": "your_username",
        "BROWSERSTACK_ACCESS_KEY": "your_key",
        "BROWSERSTACK_LOCAL": "true"
      }
    }
  }
}
```

#### Workflow: Automated Device Testing

```json
// 1. Start instrumented proxy with tunnel
proxy {action: "start", id: "test", target_url: "http://localhost:3000", bind_address: "0.0.0.0"}
tunnel {action: "start", id: "test", provider: "cloudflare", local_port: 45849, proxy_id: "test"}
// Response: {public_url: "https://abc.trycloudflare.com"}

// 2. Use BrowserStack MCP to run tests on the tunnel URL
// (BrowserStack MCP commands would go here)

// 3. After tests, check captured data from all devices:
proxylog {proxy_id: "test", types: ["error"]}         // All JS errors
proxylog {proxy_id: "test", types: ["performance"]}   // Load times per device
currentpage {proxy_id: "test"}                        // Page sessions
```

#### What You Get

| From agnt | From BrowserStack |
|-----------|-------------------|
| JS error stack traces | Screenshots |
| HTTP request/response logs | Video recordings |
| Performance metrics | Device-specific logs |
| User interactions | Automated test results |
| DOM state capture | Cross-browser coverage |

### Debugging Tips for Remote Devices

#### 1. Check Connection Status

The floating indicator shows connection status with a green/red dot. If red:
- Verify the tunnel is running: `tunnel {action: "status", id: "app"}`
- Check WebSocket connection in browser console

#### 2. Mobile-Specific Diagnostics

```json
// Check viewport and responsive issues
proxy {action: "exec", id: "app", code: "window.__devtool.auditResponsive()"}

// Check touch targets (buttons too small?)
proxy {action: "exec", id: "app", code: "window.__devtool.findSmallTouchTargets()"}

// Check text readability
proxy {action: "exec", id: "app", code: "window.__devtool.checkTextFragility()"}
```

#### 3. Capture Device State

```json
// When a user reports an issue, capture everything:
proxy {action: "exec", id: "app", code: "window.__devtool.captureState()"}
// Returns: localStorage, sessionStorage, cookies, viewport, URL, etc.
```

#### 4. Clean Up After Testing

```json
tunnel {action: "stop", id: "mobile"}
proxy {action: "stop", id: "mobile"}
proc {action: "stop", process_id: "dev"}
```

## Security Notes

- **Development Only** - No authentication, allows all origins
- **Body Truncation** - Request/response bodies limited to 10KB in logs
- **Local Traffic** - Only proxy trusted local development servers
- **Tunnel Security** - Tunnels expose your dev server publicly; stop them when not needed

## Next Steps

- Explore the [Frontend Diagnostics API](/features/frontend-diagnostics)
- See [Mobile Testing Guide](/use-cases/mobile-testing) for complete workflow
- Read the [tunnel API Reference](/api/tunnel) for all options
- See [Debugging Web Apps](/use-cases/debugging-web-apps) use case
- Learn about [Performance Monitoring](/use-cases/performance-monitoring)
