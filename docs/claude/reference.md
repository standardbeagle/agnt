# Reference

Technical reference for agnt. For architecture details, see [architecture.md](architecture.md).

## Audit Tools

Five audit functions available via `__devtool.*`:

### auditDOMComplexity()

Analyzes DOM structure and element counts.

```javascript
__devtool.auditDOMComplexity()
// Returns: {totalElements, maxDepth, duplicateIds[], elementsWithId, forms, images, links, scripts, stylesheets, iframes}
```

### auditCSS()

Checks for CSS issues: excessive inline styles (>10), excessive `!important` (>5).

```javascript
__devtool.auditCSS()
// Returns: {issues[], inlineStyleCount, importantCount, stylesheetCount}
```

### auditSecurity()

Detects security issues:
- Mixed content (HTTP resources on HTTPS pages)
- Forms with HTTP action URLs
- `target="_blank"` links missing `rel="noopener"`
- Password fields with autocomplete enabled

```javascript
__devtool.auditSecurity()
// Returns: {issues[], count, errors, warnings}
```

### auditPageQuality()

Checks page structure:
- Missing viewport meta tag
- Missing meta description
- Missing or multiple H1 headings
- Missing lang attribute
- Missing/empty title

```javascript
__devtool.auditPageQuality()
// Returns: {issues[], count, title, lang, viewport}
```

### auditAccessibility()

Checks accessibility issues:
- Images without alt text
- Form inputs without labels
- Buttons without accessible names
- Links without href or text

```javascript
__devtool.auditAccessibility()
// Returns: {issues[], count, errors, warnings}
```

## Log Entry Types

The proxy logger supports 14 entry types:

| Type | Description |
|------|-------------|
| `http` | HTTP request/response pairs |
| `error` | Frontend JavaScript errors with stack traces |
| `performance` | Page load and resource timing |
| `custom` | Custom log via `__devtool.log()` |
| `screenshot` | Screenshot captures |
| `execution` | JavaScript execution requests |
| `response` | JavaScript execution responses |
| `interaction` | User interactions (clicks, keyboard, scroll) |
| `mutation` | DOM mutations |
| `panel_message` | Messages from floating indicator |
| `sketch` | Sketches from sketch mode |
| `design_state` | Element selected for design iteration |
| `design_request` | Request for design alternatives |
| `design_chat` | Chat message about current design |

## Directory Filtering

`proc list` and `proxy list` filter by current directory by default. Use `global: true` to see all.

## Platform Support

**Linux/macOS**:
- Process groups via `Setpgid: true`
- Signals: SIGTERM → SIGKILL
- PTY: `github.com/creack/pty`

**Windows** (10 1809+):
- ConPTY for terminal emulation
- Job Objects for process groups
- Named Pipes for daemon IPC: `\\.\pipe\devtool-mcp-<username>`

## Configuration

Default values (hardcoded in `main.go`):

```go
ManagerConfig{
    DefaultTimeout:    0,                    // No timeout
    MaxOutputBuffer:   256 * 1024,          // 256KB
    GracefulTimeout:   5 * time.Second,
    HealthCheckPeriod: 10 * time.Second,
}
```

## Testing Strategy

**Test files**:
- `internal/process/ringbuf_test.go`: RingBuffer thread safety
- `internal/process/lifecycle_test.go`: Process state transitions
- `internal/project/detector_test.go`: Project detection
- `internal/proxy/logger_test.go`: Traffic logger
- `internal/proxy/injector_test.go`: JavaScript injection
- `internal/overlay/filter_test.go`: ANSI parsing
- `internal/overlay/gate_test.go`: Output gating

**Test config pattern**:
```go
pm := process.NewProcessManager(process.ManagerConfig{
    MaxOutputBuffer:   1024,
    GracefulTimeout:   100 * time.Millisecond,
    HealthCheckPeriod: 0, // Disable for tests
})
```

## MCP Constraints

- **Tool names**: `^[a-zA-Z0-9_-]{1,128}$`
- **Transport**: Stdio only (logs to stderr)
- **Errors**: Return `CallToolResult{IsError: true}`, not Go errors

## Process Constraints

- **Output buffer**: 256KB per stream (stdout/stderr separate)
- **Graceful shutdown**: 5s SIGTERM → SIGKILL (normal), immediate SIGKILL (<3s deadline)
- **Health checks**: 10s period

## Proxy Constraints

- **Default port**: Hash-based from target URL (10000-60000)
- **Traffic log**: 1000 entries circular buffer
- **Body truncation**: 10KB max in logs
- **Reserved path**: `/__devtool_metrics` (WebSocket)
- **Injection**: Only `text/html` responses
- **Auto-restart**: Max 5/minute

## Common Gotchas

1. `Register()` returns `ErrProcessExists` if ID exists
2. Use `CompareAndSwapState()` for atomic state transitions
3. Check `truncated` flag when reading RingBuffer output
4. Check `pm.IsShuttingDown()` before registering processes
5. Go → Node → Python detection order (first match wins)
6. `/__devtool_metrics` shadows backend routes with same path
7. Always check `listen_addr` in proxy response for actual port

## Future Expansion

- KDL config file support (`internal/config/kdl.go`)
- Process labels (supported but not exposed to MCP)
- Persistent logs, HAR export
- SSL/TLS support
- WebSocket frame logging
