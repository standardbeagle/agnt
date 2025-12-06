---
sidebar_position: 5
---

# proxylog

Query and analyze proxy traffic logs including HTTP requests, errors, and performance metrics.

## Synopsis

```json
proxylog {proxy_id: "<id>", ...filters}
```

## Actions

| Action | Description |
|--------|-------------|
| `query` | Search logs with filters (default) |
| `stats` | Get log statistics |
| `clear` | Clear all logs for a proxy |

## Log Types

| Type | Description |
|------|-------------|
| `http` | HTTP request/response pairs |
| `error` | Frontend JavaScript errors |
| `performance` | Page load and resource timing |
| `custom` | Custom logs from `__devtool.log()` |
| `screenshot` | Screenshots from `__devtool.screenshot()` |
| `execution` | JavaScript execution results |
| `response` | Execution responses returned to MCP |

## query (default)

Search logs with filters.

### Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `proxy_id` | string | Yes | Proxy ID |
| `action` | string | No | `query` (default), `stats`, or `clear` |
| `types` | string[] | No | Log types to include |
| `methods` | string[] | No | HTTP methods (GET, POST, etc.) |
| `status_codes` | integer[] | No | HTTP status codes |
| `url_pattern` | string | No | URL substring to match |
| `since` | string | No | Start time (RFC3339 or duration like "5m") |
| `until` | string | No | End time (RFC3339) |
| `limit` | integer | No | Maximum results (default: 100) |

### HTTP Log Queries

```json
// Recent HTTP traffic
proxylog {proxy_id: "app", types: ["http"], limit: 20}

// Only GET requests
proxylog {proxy_id: "app", types: ["http"], methods: ["GET"]}

// POST and PUT only
proxylog {proxy_id: "app", types: ["http"], methods: ["POST", "PUT"]}

// Server errors
proxylog {proxy_id: "app", types: ["http"], status_codes: [500, 502, 503]}

// API requests
proxylog {proxy_id: "app", types: ["http"], url_pattern: "/api"}

// Failed API calls
proxylog {
  proxy_id: "app",
  types: ["http"],
  url_pattern: "/api",
  status_codes: [400, 401, 403, 404, 500]
}
```

Response:
```json
{
  "entries": [
    {
      "type": "http",
      "timestamp": "2024-01-15T10:30:00Z",
      "method": "POST",
      "url": "/api/users",
      "status": 201,
      "duration_ms": 45,
      "request_headers": {
        "Content-Type": "application/json"
      },
      "response_headers": {
        "Content-Type": "application/json"
      },
      "request_body": "{\"name\": \"John\"}",
      "response_body": "{\"id\": 123, \"name\": \"John\"}"
    }
  ],
  "count": 1
}
```

### Error Log Queries

```json
// All JavaScript errors
proxylog {proxy_id: "app", types: ["error"]}
```

Response:
```json
{
  "entries": [
    {
      "type": "error",
      "timestamp": "2024-01-15T10:32:15Z",
      "message": "Cannot read property 'map' of undefined",
      "source": "http://localhost:8080/static/js/main.js",
      "line": 142,
      "column": 23,
      "stack": "TypeError: Cannot read property 'map' of undefined\n    at UserList (main.js:142:23)\n    at renderWithHooks...",
      "url": "http://localhost:8080/users"
    }
  ],
  "count": 1
}
```

### Performance Log Queries

```json
// Page load metrics
proxylog {proxy_id: "app", types: ["performance"]}
```

Response:
```json
{
  "entries": [
    {
      "type": "performance",
      "timestamp": "2024-01-15T10:30:05Z",
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
  ],
  "count": 1
}
```

### Time-Based Queries

```json
// Last 5 minutes
proxylog {proxy_id: "app", types: ["http"], since: "5m"}

// Last hour
proxylog {proxy_id: "app", types: ["error"], since: "1h"}

// Specific time range
proxylog {
  proxy_id: "app",
  types: ["http"],
  since: "2024-01-15T10:00:00Z",
  until: "2024-01-15T10:30:00Z"
}
```

### Custom Logs

```json
// Logs from __devtool.log()
proxylog {proxy_id: "app", types: ["custom"]}
```

Response:
```json
{
  "entries": [
    {
      "type": "custom",
      "timestamp": "2024-01-15T10:35:00Z",
      "level": "info",
      "message": "User clicked submit",
      "data": {"userId": 123, "formId": "signup"}
    }
  ]
}
```

### Screenshots

```json
// Captured screenshots
proxylog {proxy_id: "app", types: ["screenshot"]}
```

Response:
```json
{
  "entries": [
    {
      "type": "screenshot",
      "timestamp": "2024-01-15T10:36:00Z",
      "name": "bug-report",
      "path": "/tmp/devtool-screenshots/bug-report-1705312560.png",
      "size": {"width": 1920, "height": 1080}
    }
  ]
}
```

## stats

Get log statistics.

```json
proxylog {proxy_id: "app", action: "stats"}
```

Response:
```json
{
  "total_entries": 1542,
  "by_type": {
    "http": 1489,
    "error": 8,
    "performance": 45
  },
  "dropped": 542,
  "max_entries": 1000
}
```

## clear

Clear all logs.

```json
proxylog {proxy_id: "app", action: "clear"}
```

Response:
```json
{
  "message": "Logs cleared",
  "cleared_count": 1542
}
```

## Response Body Handling

- Bodies are limited to 10KB in logs
- Larger bodies are truncated
- Original traffic is unaffected
- Binary data is base64 encoded

## Circular Buffer

- Default size: 1000 entries
- When full, oldest entries are dropped
- Check `dropped` in stats for data loss
- Configure with `max_log_size` on proxy start

## Error Responses

### Proxy Not Found

```json
{
  "error": "proxy not found",
  "proxy_id": "nonexistent"
}
```

### Invalid Time Format

```json
{
  "error": "invalid time format",
  "since": "yesterday"
}
```

## Real-World Patterns

### Debugging API Issues

```json
// Find failed requests
proxylog {
  proxy_id: "app",
  types: ["http"],
  url_pattern: "/api",
  status_codes: [400, 401, 403, 404, 500]
}

// Check request/response for specific endpoint
proxylog {
  proxy_id: "app",
  types: ["http"],
  url_pattern: "/api/users",
  methods: ["POST"]
}
```

### Error Investigation

```json
// See all errors
proxylog {proxy_id: "app", types: ["error"]}

// Correlate with HTTP traffic
proxylog {proxy_id: "app", types: ["http", "error"], since: "5m"}
```

### Performance Analysis

```json
// Get page load times
proxylog {proxy_id: "app", types: ["performance"]}

// Find slow API calls
proxylog {
  proxy_id: "app",
  types: ["http"],
  url_pattern: "/api"
}
// Then filter results for duration_ms > threshold
```

### Session Debugging

```json
// Combine with currentpage for full context
currentpage {proxy_id: "app", action: "get", session_id: "page-1"}

// Get HTTP traffic for that page
proxylog {
  proxy_id: "app",
  types: ["http"],
  url_pattern: "/dashboard"
}
```

## See Also

- [proxy](/api/proxy) - Proxy management
- [currentpage](/api/currentpage) - Page session tracking
- [Reverse Proxy Feature](/features/reverse-proxy)
