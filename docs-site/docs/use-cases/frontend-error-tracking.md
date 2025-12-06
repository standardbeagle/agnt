---
sidebar_position: 6
---

# Frontend Error Tracking

Capturing, analyzing, and debugging JavaScript errors using devtool-mcp's proxy.

## Overview

The proxy automatically captures:

- **Uncaught exceptions** - `window.onerror`
- **Unhandled promise rejections** - `unhandledrejection` events
- **Custom errors** - Via `window.__devtool.error()`

All errors include stack traces, source locations, and page context.

## Viewing Captured Errors

### Query Error Logs

```json
proxylog {proxy_id: "app", types: ["error"]}
→ {
    entries: [
      {
        type: "error",
        timestamp: "2024-01-15T10:32:15Z",
        message: "Cannot read property 'map' of undefined",
        source: "http://localhost:8080/static/js/main.js",
        line: 142,
        column: 23,
        stack: "TypeError: Cannot read property 'map' of undefined\n    at UserList (main.js:142:23)\n    at renderWithHooks...",
        url: "http://localhost:8080/users",
        userAgent: "Mozilla/5.0..."
      }
    ]
  }
```

### Filter by Time

```json
// Errors in the last 5 minutes
proxylog {proxy_id: "app", types: ["error"], since: "5m"}

// Errors in a specific window
proxylog {proxy_id: "app", types: ["error"], since: "2024-01-15T10:00:00Z", until: "2024-01-15T11:00:00Z"}
```

### Get Error Statistics

```json
proxylog {proxy_id: "app", action: "stats"}
→ {
    by_type: {
      http: 1500,
      error: 12,
      performance: 45
    }
  }
```

## Correlating Errors with Traffic

### Find What API Calls Preceded an Error

```json
// Get error timestamp
proxylog {proxy_id: "app", types: ["error"]}
→ Error at 10:32:15 on /users page

// Check API calls around that time
proxylog {
  proxy_id: "app",
  types: ["http"],
  url_pattern: "/api",
  since: "2024-01-15T10:32:00Z",
  until: "2024-01-15T10:32:20Z"
}
→ {
    entries: [
      {url: "/api/users", status: 500, response_body: "{\"error\": \"...\"}", timestamp: "10:32:14"}
    ]
  }
```

The API returned 500, causing the subsequent JavaScript error.

### Use Page Sessions

```json
// Get page session that had errors
currentpage {proxy_id: "app"}
→ {sessions: [{id: "page-1", error_count: 2, url: "/users"}]}

// Get full context
currentpage {proxy_id: "app", action: "get", session_id: "page-1"}
→ {
    resources: [
      {url: "/api/users", status: 500}
    ],
    errors: [
      {message: "Cannot read property 'map'..."}
    ],
    performance: {...}
  }
```

## Debugging Specific Errors

### Inspect Error Location

```json
// From error log, we know the component is UserList at line 142
// Inspect the component in the browser

proxy {action: "exec", id: "app", code: "window.__devtool.inspect('.user-list')"}
→ {
    element: {tag: "div", classes: ["user-list"]},
    visibility: {visible: true},
    // Check if element rendered at all
  }
```

### Check Application State

```json
// What's in localStorage/sessionStorage?
proxy {action: "exec", id: "app", code: "window.__devtool.captureState(['localStorage', 'sessionStorage'])"}

// Check for cached data
→ {
    localStorage: {users: null},  // Expected to have user data
    sessionStorage: {}
  }
```

### Reproduce the Error

```json
// Wait for the problematic component
proxy {action: "exec", id: "app", code: "window.__devtool.waitForElement('.user-list', 10000)"}

// If it never appears, the error prevented rendering
```

## Custom Error Logging

### Log from Application Code

```javascript
// In your application code
try {
  await fetchUsers();
} catch (error) {
  window.__devtool?.error('Failed to fetch users', {
    error: error.message,
    stack: error.stack,
    userId: currentUser.id
  });
}
```

### Query Custom Logs

```json
proxylog {proxy_id: "app", types: ["custom"]}
→ {
    entries: [
      {
        level: "error",
        message: "Failed to fetch users",
        data: {error: "Network error", userId: 123},
        timestamp: "..."
      }
    ]
  }
```

## Error Monitoring Patterns

### Real-Time Error Watch

```json
// Check for new errors periodically
proxylog {proxy_id: "app", types: ["error"], since: "1m"}
```

### Error Grouping

```json
proxy {action: "exec", id: "app", code: `
  // Group errors by message (simplified)
  const logs = await fetch('/__devtool_api/errors').then(r => r.json());
  const grouped = {};

  logs.forEach(err => {
    const key = err.message.replace(/\\d+/g, 'N');  // Normalize numbers
    grouped[key] = grouped[key] || {count: 0, examples: []};
    grouped[key].count++;
    if (grouped[key].examples.length < 3) {
      grouped[key].examples.push(err);
    }
  });

  grouped
`}
```

### Error Rate Tracking

```json
// Check error count
proxylog {proxy_id: "app", action: "stats"}
→ {by_type: {error: 12}}

// Compare to traffic
// If 12 errors in 1500 requests = 0.8% error rate
```

## Handling Specific Error Types

### Uncaught Exceptions

```json
proxylog {proxy_id: "app", types: ["error"]}
→ {
    message: "TypeError: Cannot read property 'x' of undefined",
    source: "main.js",
    line: 100
  }
```

Debug approach:
1. Check the source file and line
2. Inspect variables at that location
3. Check API responses that might have returned unexpected data

### Promise Rejections

```json
→ {
    message: "Unhandled Promise Rejection: NetworkError",
    source: "api.js",
    line: 45
  }
```

Debug approach:
1. Check network logs for failed requests
2. Add `.catch()` handlers to promises
3. Check if network was available

### Syntax Errors

```json
→ {
    message: "SyntaxError: Unexpected token '<'",
    source: "app.js",
    line: 1
  }
```

This usually means:
- JS file returned HTML (404 page)
- Build error produced invalid JS
- Script src is wrong

Check:
```json
proxylog {proxy_id: "app", types: ["http"], url_pattern: "app.js"}
→ Check Content-Type and response body
```

## Creating Error Reports

### Capture Error Context

```json
// When error is reported:

// 1. Screenshot
proxy {action: "exec", id: "app", code: "window.__devtool.screenshot('error-state')"}

// 2. DOM state
proxy {action: "exec", id: "app", code: "window.__devtool.captureDOM()"}

// 3. Application state
proxy {action: "exec", id: "app", code: "window.__devtool.captureState(['localStorage'])"}

// 4. Recent network
proxy {action: "exec", id: "app", code: "window.__devtool.captureNetwork()"}

// 5. Error details
proxylog {proxy_id: "app", types: ["error"], limit: 5}
```

### Automated Error Report

```json
proxy {action: "exec", id: "app", code: `
  const report = {
    timestamp: new Date().toISOString(),
    url: window.location.href,
    userAgent: navigator.userAgent,
    viewport: {
      width: window.innerWidth,
      height: window.innerHeight
    },
    state: window.__devtool.captureState(['localStorage']),
    recentErrors: [],  // Would need API call to get these
    screenshot: await window.__devtool.screenshot('error-report')
  };

  JSON.stringify(report, null, 2)
`}
```

## Integration with Error Tracking Services

### Send to External Service

```javascript
// In your app, forward errors
window.addEventListener('error', (event) => {
  // Log to devtool
  window.__devtool?.log(event.message, 'error', {
    source: event.filename,
    line: event.lineno
  });

  // Also send to Sentry/Datadog/etc
  Sentry.captureException(event.error);
});
```

### Compare with Production

```json
// Proxy captures same data as production error tracking
// Use for debugging before errors reach production
```

## Best Practices

1. **Always check errors first** - Many bugs start with JS errors
2. **Correlate with API calls** - Errors often caused by bad data
3. **Use page sessions** - See full context of error
4. **Capture state** - localStorage, DOM, network
5. **Screenshot** - Visual state at error time
6. **Check console too** - Some errors only in console

## Common Error Patterns

| Error | Likely Cause | Debug Steps |
|-------|--------------|-------------|
| `undefined is not a function` | Method on null | Check API response |
| `Cannot read property X` | Null reference | Trace data flow |
| `Network Error` | CORS, offline, 500 | Check HTTP logs |
| `Unexpected token <` | HTML instead of JS | Check Content-Type |
| `Script error.` | Cross-origin script | Check CORS headers |

## See Also

- [Debugging Web Apps](/use-cases/debugging-web-apps) - General debugging
- [Reverse Proxy](/features/reverse-proxy) - Proxy setup
- [proxylog](/api/proxylog) - Full API reference
