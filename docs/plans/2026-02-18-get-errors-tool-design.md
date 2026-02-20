# get_errors MCP Tool Design

## Purpose

A single MCP tool that answers "What's broken right now?" by collecting errors from all sources (process output, browser JS, HTTP responses, proxy transport), deduplicating them, and presenting the minimum information needed to find and fix each bug.

## Architecture

**Approach: Aggregator in tool handler.** The `get_errors` handler queries existing data sources (TrafficLogger, AlertScanner ring buffer, ProcessManager) at call time and merges results into a unified sorted list. No new goroutines, no new event plumbing. Read-only aggregation over bounded buffers.

## Data Sources

| Source | What | Key Fields |
|--------|------|-----------|
| AlertScanner ring buffer (new) | Process output errors (panics, exceptions, compile errors) | message, category, severity, process_id, timestamp |
| TrafficLogger JS errors | Browser uncaught exceptions + promise rejections | message, source file:line:col, page URL |
| TrafficLogger HTTP errors | 4xx/5xx responses from the app | method, URL, status code, response body excerpt |
| TrafficLogger transport errors | Connection refused, timeouts | target, message |
| TrafficLogger diagnostics | Proxy-level errors | category, event, message, target URL |

## Default Filtering

1. **Recency**: Only errors since latest process restart (per process). For proxy errors, only since most recent proxy start.
2. **Deduplication**: Group identical errors (same message + location), show count + most recent timestamp.
3. **Stack trace reduction**: First application-code frame only. Strip framework internals (node_modules/, runtime frames, Go stdlib).
4. **Body truncation**: HTTP response bodies trimmed to first meaningful error message (max ~200 chars).
5. **Severity ordering**: Errors first, then warnings. Within each, most recent first.
6. **Noise filtering**: Skip 404s for common static assets (.map, favicon.ico, __webpack_hmr). Skip HTTP 304/301/302.

## Output Format

```
=== Errors (5) ===

[process:dev-server] COMPILE ERROR (2x, latest 3s ago)
  CS1002: ; expected
  → src/Controllers/HomeController.cs:42:15

[browser:js] TypeError (1x, 12s ago)
  Cannot read properties of undefined (reading 'map')
  → src/components/UserList.tsx:28:12
  page: http://localhost:3000/users

[proxy:http] 500 Internal Server Error (3x, latest 8s ago)
  POST /api/users → "Validation failed: email is required"

[proxy:transport] Connection Refused (1x, 45s ago)
  → localhost:5000

=== Warnings (2) ===

[process:dev-server] DEPRECATION (1x, 30s ago)
  BrowserModule.withServerTransition deprecated
  → src/app/app.module.ts:12
```

## Input Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| process_id | string | "" | Filter to specific process |
| proxy_id | string | "" | Filter to specific proxy |
| since | string | "" | Override recency filter (RFC3339 or duration like "5m") |
| include_warnings | bool | true | Include warning-level errors |
| limit | int | 25 | Max errors to return |
| raw | bool | false | Return full JSON with all fields |

## Ring Buffer Addition to AlertScanner

- Fixed-size ring buffer (200 entries) storing `AlertMatch` structs
- Written on every match before dedup/batching (captures everything)
- `RecentMatches(since time.Time) []*AlertMatch` method for retrieval
- Separate from the existing batch delivery path

## Differentiation from proxylog errors_only

1. Includes process output errors (proxylog only knows proxy traffic)
2. Automatic build-boundary filtering (no manual `since` needed)
3. Deduplication with occurrence counts
4. Stack trace reduction to first application frame
5. Noise filtering for static asset 404s
6. Unified severity ordering across all sources
