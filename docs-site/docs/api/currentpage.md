---
sidebar_position: 6
---

# currentpage

View active page sessions with grouped resources, errors, and metrics.

## Synopsis

```json
currentpage {proxy_id: "<id>", action: "<action>"}
```

## Overview

Page sessions group together:
- Initial HTML document request
- All associated resources (JS, CSS, images, fonts)
- Frontend JavaScript errors from that page
- Performance metrics (load time, paint timing)

This provides a high-level view of page activity, making it easier to debug issues than searching raw HTTP logs.

## Actions

| Action | Description |
|--------|-------------|
| `list` | List all active page sessions (default) |
| `get` | Get detailed information for a specific session |
| `clear` | Clear all page sessions |

## list (default)

List all active page sessions.

```json
currentpage {proxy_id: "app"}
```

Response:
```json
{
  "sessions": [
    {
      "id": "page-1",
      "url": "http://localhost:8080/dashboard",
      "started_at": "2024-01-15T10:30:00Z",
      "last_activity": "2024-01-15T10:30:05Z",
      "resource_count": 24,
      "error_count": 0,
      "status": "active"
    },
    {
      "id": "page-2",
      "url": "http://localhost:8080/users",
      "started_at": "2024-01-15T10:31:15Z",
      "last_activity": "2024-01-15T10:31:20Z",
      "resource_count": 18,
      "error_count": 2,
      "status": "active"
    }
  ],
  "active_count": 2
}
```

## get

Get detailed information for a specific session.

```json
currentpage {proxy_id: "app", action: "get", session_id: "page-2"}
```

Parameters:
| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `proxy_id` | string | Yes | Proxy ID |
| `session_id` | string | Yes | Session ID from list |

Response:
```json
{
  "session": {
    "id": "page-2",
    "url": "http://localhost:8080/users",
    "started_at": "2024-01-15T10:31:15Z",
    "last_activity": "2024-01-15T10:31:20Z",
    "document": {
      "url": "/users",
      "status": 200,
      "duration_ms": 45,
      "size": 12345
    },
    "resources": [
      {
        "url": "/static/js/main.js",
        "type": "script",
        "status": 200,
        "duration_ms": 123,
        "size": 45678
      },
      {
        "url": "/api/users",
        "type": "fetch",
        "status": 200,
        "duration_ms": 456,
        "size": 2340
      },
      {
        "url": "/static/css/styles.css",
        "type": "stylesheet",
        "status": 200,
        "duration_ms": 34,
        "size": 8901
      }
    ],
    "errors": [
      {
        "message": "Cannot read property 'map' of undefined",
        "source": "/static/js/main.js",
        "line": 142,
        "timestamp": "2024-01-15T10:31:18Z"
      }
    ],
    "performance": {
      "dom_content_loaded": 245,
      "load_event": 892,
      "first_paint": 156,
      "first_contentful_paint": 234
    }
  }
}
```

## clear

Clear all page sessions.

```json
currentpage {proxy_id: "app", action: "clear"}
```

Response:
```json
{
  "message": "Page sessions cleared",
  "cleared_count": 2
}
```

## Session Identification

### How Pages Are Detected

1. **Document Request**: HTML requests (`Accept: text/html`) create new sessions
2. **Resource Association**: Subsequent requests are matched via:
   - `Referer` header
   - Same origin heuristics
   - Timing correlation

### Resource Types

| Type | Description |
|------|-------------|
| `document` | Initial HTML page |
| `script` | JavaScript files |
| `stylesheet` | CSS files |
| `image` | Images (PNG, JPG, SVG, etc.) |
| `font` | Web fonts |
| `fetch` | API calls (XHR, fetch) |
| `other` | Other resources |

## Session Lifecycle

- **Created**: When HTML document is requested
- **Active**: Receiving resources/errors/metrics
- **Timeout**: After 5 minutes of inactivity
- **Max Sessions**: 100 (oldest removed when exceeded)

## Error Responses

### Proxy Not Found

```json
{
  "error": "proxy not found",
  "proxy_id": "nonexistent"
}
```

### Session Not Found

```json
{
  "error": "session not found",
  "session_id": "page-999"
}
```

## Real-World Patterns

### Debugging a Page

```json
// List active pages
currentpage {proxy_id: "app"}
→ Find the page with errors (error_count > 0)

// Get full details
currentpage {proxy_id: "app", action: "get", session_id: "page-2"}
→ See exactly what happened: failed API calls, JS errors, slow resources
```

### Performance Investigation

```json
// Get page session
currentpage {proxy_id: "app", action: "get", session_id: "page-1"}

// Check performance metrics
→ {
    performance: {
      load_event: 3500,  // 3.5 seconds - too slow!
      first_contentful_paint: 234
    },
    resources: [
      {url: "/static/js/main.js", duration_ms: 2100}  // Blocking resource
    ]
  }
```

### Error Correlation

```json
// Page has errors
currentpage {proxy_id: "app", action: "get", session_id: "page-2"}

// See that /api/users returned 500 before the JS error
→ {
    resources: [
      {url: "/api/users", status: 500, duration_ms: 100}
    ],
    errors: [
      {message: "Cannot read property 'map' of undefined"}
    ]
  }

// The error happened because API returned 500, causing undefined data
```

### Monitoring User Sessions

```json
// Clear sessions
currentpage {proxy_id: "app", action: "clear"}

// User performs actions...

// See what pages they visited
currentpage {proxy_id: "app"}
→ [
    {url: "/login", error_count: 0},
    {url: "/dashboard", error_count: 0},
    {url: "/users", error_count: 2}  // Problems here!
  ]
```

### Comparing Multiple Pages

```json
// User reports "page A works but page B is broken"

// Get both sessions
currentpage {proxy_id: "app", action: "get", session_id: "page-a"}
currentpage {proxy_id: "app", action: "get", session_id: "page-b"}

// Compare:
// - Different API calls?
// - Different status codes?
// - Different resources loaded?
```

## Integration with Other Tools

### With proxylog

```json
// Get page session for overview
currentpage {proxy_id: "app", action: "get", session_id: "page-1"}

// Then query proxylog for detailed HTTP info
proxylog {
  proxy_id: "app",
  types: ["http"],
  url_pattern: "/api/users",
  since: "2024-01-15T10:31:15Z"
}
```

### With exec

```json
// Identify problem page
currentpage {proxy_id: "app"}
→ {url: "/dashboard", error_count: 1}

// Inspect the page
proxy {action: "exec", id: "app", code: "window.__devtool.diagnoseLayout()"}

// Take screenshot
proxy {action: "exec", id: "app", code: "window.__devtool.screenshot('dashboard-error')"}
```

## See Also

- [proxylog](/api/proxylog) - Query raw traffic logs
- [proxy](/api/proxy) - Proxy management
- [Reverse Proxy Feature](/features/reverse-proxy)
