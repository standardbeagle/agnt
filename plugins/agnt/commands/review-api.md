---
description: "Review API calls, responses, and network traffic"
allowed-tools: ["mcp__agnt__proxy", "mcp__agnt__proxylog", "mcp__agnt__currentpage"]
---

Review API calls and network traffic captured by the agnt proxy.

## Steps

### 1. Get Page Session Overview
```
currentpage {proxy_id: "dev"}
```

### 2. List All HTTP Traffic
```
proxylog {proxy_id: "dev", types: ["http"], limit: 50}
```

### 3. Filter API Calls Only
```
proxylog {proxy_id: "dev", types: ["http"], url_pattern: "/api/", limit: 30}
```

### 4. Check for Failed Requests
```
proxylog {proxy_id: "dev", types: ["http"], status_codes: [400, 401, 403, 404, 500, 502, 503], limit: 20}
```

### 5. Capture Network Performance
```
proxy {action: "exec", id: "dev", code: "__devtool.captureNetwork()"}
```

### 6. Review Custom Logs (API debug messages)
```
proxylog {proxy_id: "dev", types: ["custom"], limit: 20}
```

## Filtering HTTP Traffic

### By Method
```
proxylog {proxy_id: "dev", types: ["http"], methods: ["POST"], limit: 20}
proxylog {proxy_id: "dev", types: ["http"], methods: ["GET"], limit: 20}
proxylog {proxy_id: "dev", types: ["http"], methods: ["PUT", "DELETE"], limit: 20}
```

### By URL Pattern
```
proxylog {proxy_id: "dev", types: ["http"], url_pattern: "/api/users", limit: 20}
proxylog {proxy_id: "dev", types: ["http"], url_pattern: "/graphql", limit: 20}
proxylog {proxy_id: "dev", types: ["http"], url_pattern: ".json", limit: 20}
```

### By Status Code
```
// Success only
proxylog {proxy_id: "dev", types: ["http"], status_codes: [200, 201], limit: 20}

// Client errors
proxylog {proxy_id: "dev", types: ["http"], status_codes: [400, 401, 403, 404], limit: 20}

// Server errors
proxylog {proxy_id: "dev", types: ["http"], status_codes: [500, 502, 503, 504], limit: 20}
```

### By Time Range
```
proxylog {proxy_id: "dev", types: ["http"], since: "5m", limit: 20}
proxylog {proxy_id: "dev", types: ["http"], since: "1h", limit: 50}
```

## HTTP Log Entry Fields

Each HTTP log entry contains:

| Field | Description |
|-------|-------------|
| `method` | HTTP method (GET, POST, etc.) |
| `url` | Request URL |
| `status` | Response status code |
| `duration` | Request duration in ms |
| `request_headers` | Request headers |
| `response_headers` | Response headers |
| `request_body` | Request body (truncated if large) |
| `response_body` | Response body (truncated if large) |
| `timestamp` | When the request was made |

## Network Performance Analysis

The `captureNetwork()` function provides:

```
proxy {action: "exec", id: "dev", code: "__devtool.captureNetwork()"}
```

Returns for each resource:
- `name`: URL
- `type`: initiator (script, fetch, xhr, img, css)
- `duration`: Load time in ms
- `size`: Transfer size in bytes
- `startTime`: When loading started

## API Quality Checklist

### Response Quality
- [ ] All endpoints return appropriate status codes
- [ ] Error responses include helpful messages
- [ ] Response times are acceptable (< 500ms)
- [ ] Large payloads are paginated

### Security
- [ ] Authentication required where needed
- [ ] No sensitive data in URLs
- [ ] CORS headers configured correctly
- [ ] No credentials in response bodies

### Best Practices
- [ ] Consistent response formats (JSON)
- [ ] Proper HTTP methods (GET for reads, POST for creates)
- [ ] Appropriate caching headers
- [ ] Content-Type headers correct

## Debug API Issues

```
// Log custom API debug message
proxy {action: "exec", id: "dev", code: "__devtool.log('API Debug', 'info', {endpoint: '/api/users', issue: 'slow response'})"}

// Capture current application state
proxy {action: "exec", id: "dev", code: "__devtool.captureState()"}

// Check for CORS or fetch errors
proxylog {proxy_id: "dev", types: ["error"], limit: 10}
```

## Get Traffic Statistics
```
proxylog {proxy_id: "dev", action: "stats"}
```

Returns counts by type and overall statistics.
