---
sidebar_position: 3
---

# Reverse Proxy

The reverse proxy is devtool-mcp's most powerful feature, enabling comprehensive frontend debugging through transparent HTTP interception and JavaScript instrumentation.

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
proxy {action: "start", id: "app", target_url: "http://localhost:3000", port: 8080}
```

Browse to `http://localhost:8080` instead of port 3000. Everything works normally, but now you have:

- Complete HTTP traffic logs
- JavaScript error capture
- Performance metrics
- 50+ diagnostic primitives via `window.__devtool`

## Proxy Management

### Start a Proxy

```json
proxy {action: "start", id: "myapp", target_url: "http://localhost:3000", port: 8080}
→ {
    "id": "myapp",
    "status": "running",
    "target_url": "http://localhost:3000",
    "listen_addr": ":8080"
  }
```

### Auto Port Assignment

If port 8080 is busy, the proxy finds an available port:

```json
proxy {action: "start", id: "app", target_url: "http://localhost:3000", port: 8080}
→ {
    "id": "app",
    "listen_addr": ":45123",  // Different port assigned
    "message": "Port 8080 was busy, using 45123"
  }
```

Or explicitly request auto-assignment:

```json
proxy {action: "start", id: "app", target_url: "http://localhost:3000", port: 0}
→ {listen_addr: ":49152"}  // System assigns port
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
// Proxy staging
proxy {action: "start", id: "staging", target_url: "https://staging.example.com", port: 8080}

// Proxy production (read-only debugging)
proxy {action: "start", id: "prod", target_url: "https://example.com", port: 8081}

// Compare behavior
proxylog {proxy_id: "staging", types: ["http"], url_pattern: "/api/users"}
proxylog {proxy_id: "prod", types: ["http"], url_pattern: "/api/users"}
```

## Configuration

### Log Size

Default: 1000 entries. Configure when starting:

```json
proxy {action: "start", id: "app", target_url: "...", port: 8080, max_log_size: 5000}
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

## Security Notes

- **Development Only** - No authentication, allows all origins
- **Body Truncation** - Request/response bodies limited to 10KB in logs
- **Local Traffic** - Only proxy trusted local development servers

## Next Steps

- Explore the [Frontend Diagnostics API](/features/frontend-diagnostics)
- See [Debugging Web Apps](/use-cases/debugging-web-apps) use case
- Learn about [Performance Monitoring](/use-cases/performance-monitoring)
