---
sidebar_position: 6
---

# get_errors

Unified error aggregation across all active processes and proxies. Collects, deduplicates, and formats errors from multiple sources into a single view.

:::note Legacy tool
The incident pipeline is always active. [`get_incidents`](/api/get_incidents) is the authoritative inbox and `get_errors` is its compatibility shim for session-scoped queries. Prefer `get_incidents` for new workflows; daemon-less and explicit cross-project compatibility paths retain the legacy aggregate view.
:::

## Synopsis

```json
get_errors {}
get_errors {proxy_id: "dev", since: "5m"}
```

## Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `process_id` | string | No | all | Filter to specific process |
| `proxy_id` | string | No | all active | Filter to specific proxy |
| `since` | string | No | none | Recency filter (RFC3339 or duration like `5m`, `1h`, `30s`) |
| `include_warnings` | bool | No | true | Include warnings alongside errors |
| `limit` | integer | No | 25 | Max errors returned |
| `raw` | bool | No | false | Return full JSON instead of compact text |

## Error Sources

| Source | Label | Description |
|--------|-------|-------------|
| Process output | `process:<id>` | Compile errors, panics, exceptions detected by AlertScanner pattern matching |
| Browser JS | `browser:js` | Runtime exceptions captured by injected `window.onerror` handler |
| HTTP responses | `proxy:http` | 4xx (warning) and 5xx (error) responses from proxied requests |
| Proxy diagnostics | `proxy:diagnostic` | Transport errors, connection failures, proxy-level issues |
| Custom logs | `browser:custom` | Application-level `__devtool.log()` calls with `error` or `warn` level |

## Compact Output (default)

The default output format is optimized for AI consumption — grouped by severity, deduplicated, with minimal noise.

```
=== Errors (2) ===

[browser:js] TypeError (3x, latest 5s ago)
  Cannot read property 'map' of undefined
  → src/components/List.tsx:42:15
  page: http://localhost:3000/dashboard

[proxy:http] 500 Internal Server Error (1x, 12s ago)
  POST /api/users → "database connection timeout"

=== Warnings (1) ===

[proxy:http] 404 Not Found (1x, 30s ago)
  GET /api/old-endpoint
```

Each entry shows:
- **Source** and **category** with occurrence count and recency
- **Message** (truncated to 200 chars)
- **Location** (first application code frame from stack trace, if available)
- **Page URL** (for browser-originated errors)

## Raw Output

With `raw: true`, the tool returns the full JSON array of unified error objects:

```json
get_errors {raw: true}
```

Response:
```json
[
  {
    "source": "browser:js",
    "severity": "error",
    "category": "TypeError",
    "message": "Cannot read property 'map' of undefined",
    "location": "src/components/List.tsx:42:15",
    "page": "http://localhost:3000/dashboard",
    "count": 3,
    "last_seen": "2024-01-15T10:30:05Z"
  },
  {
    "source": "proxy:http",
    "severity": "error",
    "category": "500 Internal Server Error",
    "message": "POST /api/users → \"database connection timeout\"",
    "location": "",
    "page": "",
    "count": 1,
    "last_seen": "2024-01-15T10:29:53Z"
  }
]
```

## Built-in Intelligence

### Deduplication

Identical errors are merged by `source + category + message + location`. The `count` field shows how many times the error occurred, and `last_seen` tracks the most recent occurrence.

### Stack Trace Reduction

Stack traces are reduced to the first application code frame, skipping:
- `node_modules/` and `webpack/` frames
- `node:internal/` runtime frames
- `<anonymous>` and `webpack-internal` frames
- Go `runtime/` frames

Supports JS (`at func (file:line:col)`), Go (`/path/file.go:line`), and Python (`File "path", line N`) stack formats.

### Noise Filtering

HTTP errors are automatically filtered to remove common noise:
- Redirects: 301, 302, 304
- Static asset 404s: `.map`, `favicon`, `.hot-update.`
- Dev tooling 404s: `__webpack_hmr`, `sockjs-node`, WebSocket URLs

### Error Message Extraction

For HTTP errors, the tool extracts meaningful error messages from response bodies:
- **JSON**: Checks `message`, `error`, `detail`, `error_description` fields
- **HTML**: Strips tags and extracts text content
- **Plain text**: Used directly (truncated to 200 chars)

### Sorting

Results are sorted by severity (errors before warnings), then by recency (most recent first) within each group.

## Examples

### Quick Error Check

```json
// All current errors and warnings
get_errors {}

// Errors only, no warnings
get_errors {include_warnings: false}
```

### Filtered Queries

```json
// Errors from a specific proxy
get_errors {proxy_id: "dev"}

// Process errors from a specific script
get_errors {process_id: "dev-server"}

// Errors from the last 5 minutes
get_errors {since: "5m"}

// Errors since a specific time
get_errors {since: "2024-01-15T10:00:00Z"}
```

### Detailed Investigation

```json
// Full JSON for programmatic analysis
get_errors {raw: true, limit: 50}

// All errors from a failing dev server
get_errors {process_id: "dev-server", raw: true}
```

## Dual Mode Operation

### Daemon Mode (default)

Full functionality. Process alerts are collected via the daemon's AlertScanner (pattern matching on process stdout/stderr) and stored in the daemon's alert ring buffer (500 entries). Proxy errors are queried from each proxy's traffic log.

Data flow for process alerts:
```
Process → PTY → AlertScanner → daemon IPC (REPORT) → AlertStore → get_errors (QUERY)
```

### Legacy Mode (no daemon)

Only proxy errors are available. Process output alerts require the daemon's AlertScanner and IPC pipeline, which are not present in legacy mode. The tool description notes this limitation.

## Severity Mapping

| Source | Condition | Severity |
|--------|-----------|----------|
| Process output | `severity: "error"` | error |
| Process output | `severity: "warning"` or `"info"` | warning |
| Browser JS | All runtime exceptions | error |
| HTTP response | Status 500-599 | error |
| HTTP response | Status 400-499 | warning |
| HTTP response | Connection/transport error | error |
| Proxy diagnostic | `level: "error"` | error |
| Proxy diagnostic | `level: "warning"` | warning |
| Custom log | `level: "error"` | error |
| Custom log | `level: "warn"` | warning |

## Error Responses

### Proxy Not Found

```json
{
  "error": "proxy \"nonexistent\" not found: ..."
}
```

### Daemon Not Connected

In daemon mode, if the daemon connection fails:
```json
{
  "error": "daemon not connected: ..."
}
```

## Real-World Patterns

### Development Debugging Loop

```json
// Start dev server and proxy
run {script_name: "dev", id: "app"}
proxy {action: "start", target: "http://localhost:3000", id: "dev"}

// Quick error check after making changes
get_errors {}

// Deep dive into browser errors
get_errors {proxy_id: "dev", raw: true}

// Check only process compilation errors
get_errors {process_id: "app", include_warnings: false}
```

### Error Triage

```json
// Get everything
get_errors {limit: 50}

// Focus on the last minute
get_errors {since: "1m"}

// Only server errors (skip 4xx warnings)
get_errors {include_warnings: false}
```

### Correlating with Proxy Logs

```json
// See aggregated errors first
get_errors {proxy_id: "dev"}

// Then drill into specific proxy traffic for context
proxylog {proxy_id: "dev", types: ["http"], status_codes: [500]}
proxylog {proxy_id: "dev", types: ["error"]}
```

## See Also

- [proxylog](/api/proxylog) - Detailed proxy traffic logs (raw entries, no deduplication)
- [proc](/api/proc) - Process management and raw output
- [proxy](/api/proxy) - Proxy lifecycle management
- [Frontend Error Tracking](/use-cases/frontend-error-tracking) - Use case guide
