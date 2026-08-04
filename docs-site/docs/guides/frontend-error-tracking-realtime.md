---
title: "Real-Time Frontend Error Tracking for AI Coding Agents"
description: "Automatic JavaScript error capture with full stack traces, deduplication, and noise filtering. Feed browser errors directly to your AI assistant."
keywords: [frontend error tracking, javascript error monitoring, browser error capture, AI coding agent, MCP server, real-time]
sidebar_label: "Real-Time Error Tracking"
---

# Real-Time Frontend Error Tracking for AI Coding Agents

Effective **frontend error tracking** during development requires seeing every error as it happens -- JavaScript exceptions, failed API calls, server crashes -- in one place, with enough context to act on them immediately. agnt captures all of these automatically through its reverse proxy, deduplicates them, strips noise, and makes them available to your AI coding agent through a single MCP tool call.

## The Problem

Frontend errors during development are scattered across at least three different surfaces. JavaScript runtime errors appear in the browser console. HTTP failures show up in the Network tab. Server-side exceptions print to your terminal. No single view connects them, and none of these surfaces are accessible to an AI coding agent.

This fragmentation is tolerable when you are debugging alone. You can hold the mental model of which tab to check. But when you are working with an AI assistant, every piece of context requires a manual copy-paste. The AI cannot open your DevTools. It cannot check your terminal. It sees only what you give it, and what you give it is usually incomplete.

Traditional error monitoring services like Sentry and Bugsnag solve this for production. They aggregate, deduplicate, and alert. But they are designed for deployed applications, not for `localhost:3000` with hot module replacement running. During development, errors are ephemeral. Refresh the page and the console clears. Restart the dev server and the terminal scrolls past the stack trace. The errors that matter most -- the ones you are actively causing while building features -- are the hardest to capture and hand off to an AI.

## The Traditional Approach

Without automated capture, feeding browser errors to an AI assistant follows a predictable and slow pattern:

1. Something breaks in the browser
2. Open DevTools and check the Console tab for JavaScript errors
3. Switch to the Network tab and look for red entries (failed HTTP requests)
4. Switch to the terminal running your dev server and scan for server-side exceptions
5. Copy the error message from the console
6. Copy the stack trace -- scrolling past 20 lines of `node_modules/react-dom` internals to find the one line that is actually your code
7. Paste it all into your AI assistant
8. Describe the page URL, what you clicked, and what you expected to happen
9. AI asks for more context: "Were there any failed network requests before this error?"
10. Switch back to the browser, find the failed request, copy the response body
11. Paste that too
12. AI fixes the symptom because it never saw the root cause

Each round trip takes 30-60 seconds. Three round trips to debug a single error chain is not unusual.

## The agnt Approach

agnt eliminates the copy-paste loop entirely. A reverse proxy sits between your browser and your dev server. Every HTTP response with `Content-Type: text/html` gets a small JavaScript payload injected into the `<head>`. That JavaScript installs error handlers, captures every exception and rejection, and sends them back to the proxy over a WebSocket. Your AI queries all captured errors with one tool call.

**Before** -- manual context gathering:
```
You: "There's an error on the settings page. Console says
'Cannot read properties of undefined (reading forEach)' at
SettingsForm.tsx line 87. Also I think there was a 500 error
from the API but I'm not sure which endpoint."
```

**After** -- automatic capture:
```
AI: get_incidents {}

=== Incidents (2) === [inbox: crit=0 err=2 warn=0 info=0 new=2]

[error:http_5xx] 500 Internal Server Error (1x, 3s ago)
  id: 0874475bbf1f6214
  GET /api/user/preferences → "column 'theme_mode' does not exist"

[error:browser_js] TypeError (4x, 1s ago)
  id: 9bcbf62d52ce066d
  Cannot read properties of undefined (reading 'forEach')
  at: src/components/SettingsForm.tsx:87:22
  → http://localhost:3000/settings
```

The AI sees both errors, their ordering (the 500 happened first), and the causal chain: a missing database column causes the API to fail, the component receives `undefined` instead of a preferences object, and the `forEach` call throws. One tool call, zero copy-pasting, root cause visible.

## How the Proxy Captures Errors

The capture mechanism has three stages: injection, collection, and storage.

**Stage 1: JavaScript Injection.** When the proxy forwards an HTTP response, it checks the `Content-Type` header. If the response is `text/html`, the proxy injects a `<script>` block before the closing `</head>` tag. If there is no `</head>`, it falls back to inserting after `<head>`, after `<body>`, after `<html>`, or as a last resort, prepends it to the document. The injection is transparent -- your application code does not change.

**Stage 2: Error Collection.** The injected script installs two global error handlers:

```javascript
// Captures synchronous errors: TypeError, ReferenceError, SyntaxError, etc.
window.addEventListener('error', function(event) {
  send('error', {
    message: event.message,
    source: event.filename,
    lineno: event.lineno,
    colno: event.colno,
    stack: event.error ? event.error.stack : '',
    url: window.location.href,
    timestamp: new Date().toISOString()
  });
});

// Captures unhandled Promise rejections
window.addEventListener('unhandledrejection', function(event) {
  send('error', {
    message: String(event.reason),
    stack: event.reason && event.reason.stack ? event.reason.stack : '',
    url: window.location.href,
    timestamp: new Date().toISOString()
  });
});
```

These handlers send error data to the proxy via a WebSocket connection to `/__devtool_metrics` on the same host. The WebSocket reconnects automatically (up to 5 attempts) if the connection drops.

**Stage 3: Storage.** The proxy's `TrafficLogger` stores errors in a circular buffer (1000 entries by default). Entries are typed -- `error`, `http`, `diagnostic`, `custom` -- and timestamped. The buffer overwrites the oldest entries when full, so recent errors are always available without unbounded memory growth.

HTTP errors are captured separately. Every request flowing through the proxy is logged. Any response with a status code of 400 or higher is stored with the method, URL, status code, and an extracted error message from the response body.

## The get_incidents Tool

The `get_incidents` tool queries all captured errors across all active proxies and running processes. It returns a deduplicated, sorted, noise-filtered view.

```json
// All errors and warnings
get_incidents {}

// Errors only, no warnings
get_incidents {severity: ["critical", "error"]}

// Errors from the last 2 minutes
get_incidents {since: "2m"}

// Errors from a specific proxy
get_incidents {proxy_id: "frontend"}

// Full JSON output for programmatic analysis
get_incidents {raw: true, limit: 50}
```

The default output format is compact text optimized for AI consumption:

```
=== Incidents (4) === [inbox: crit=0 err=3 warn=1 info=0 new=4]

[error:browser_js] TypeError (12x, 2s ago)
  id: fe92efeed672d386
  Cannot read properties of null (reading 'classList')
  at: src/components/Modal.tsx:34:18
  → http://localhost:3000/dashboard

[error:http_5xx] 500 Internal Server Error (1x, 8s ago)
  id: df9611784a94b258
  POST /api/orders → "constraint violation: orders_customer_fk"

[error:build_fail] COMPILE ERROR (1x, 15s ago)
  id: 7e6317f61640481b
  src/utils/format.ts(12,5): error TS2322: Type 'string' is not assignable to type 'number'

[warning:http_4xx] 404 Not Found (2x, 5s ago)
  id: 11f75a9d652d1265
  GET /api/feature-flags → "endpoint not implemented"
```

Each entry shows the source (`browser:js`, `proxy:http`, `process:<id>`), the error category, occurrence count with recency, the message, the first application code frame from the stack trace, and the page URL. Errors sort before warnings, and within each group, the most recent appear first.

## Custom Error Logging

The injected JavaScript exposes `window.__devtool.log()` for application-level error reporting. Use this to capture errors that your code handles gracefully but that your AI should still know about.

```javascript
// Log a handled error
try {
  await processPayment(order);
} catch (err) {
  showUserNotification("Payment failed, please retry");
  window.__devtool.log("Payment processing failed: " + err.message, "error", {
    orderId: order.id,
    amount: order.total,
    provider: order.paymentMethod
  });
}

// Log a warning about degraded behavior
if (cachedData && Date.now() - cachedData.fetchedAt > 60000) {
  window.__devtool.log("Serving stale cache data (>60s old)", "warn", {
    endpoint: "/api/products",
    age: Date.now() - cachedData.fetchedAt
  });
}
```

Custom errors appear in `get_incidents` output with the source `browser:custom`:

```
[browser:custom] CUSTOM ERROR (1x, 3s ago)
  Payment processing failed: insufficient funds
  page: http://localhost:3000/checkout
```

The `level` parameter accepts `"error"`, `"warn"`, `"info"`, or `"debug"`. Only `"error"` and `"warn"` levels surface in `get_incidents` results. The `"info"` and `"debug"` levels are stored in the traffic log and can be queried via `proxylog`.

## Noise Filtering

Development environments produce a constant stream of irrelevant HTTP errors. agnt filters these out automatically so your AI focuses on real problems.

**Filtered out:**
- Favicon 404s (`/favicon.ico`)
- Source map request failures (`.map` files)
- Hot Module Replacement artifacts (`.hot-update.`, `__webpack_hmr`)
- Dev server WebSocket endpoints (`sockjs-node`, `ws://`, `wss://`)
- Redirects and cache responses (301, 302, 304 status codes)

**Not filtered:**
- Application API failures (any 4xx/5xx to your routes)
- JavaScript runtime errors (all `window.onerror` events)
- Unhandled Promise rejections
- Custom `window.__devtool.log()` entries

Stack traces are also cleaned up. Frames from `node_modules/`, `webpack/`, `node:internal/`, `<anonymous>`, and `webpack-internal` are stripped. The tool extracts the first application code frame -- the line in *your* source code where the error originated -- and displays that as the location. This works for JavaScript, Go, and Python stack trace formats.

## Real-World Example: Tracking Down a Data Corruption Bug

A user reports that editing a product description sometimes saves the wrong data. The bug is intermittent -- it happens maybe one in five times. Reproducing it manually while copying errors is impractical.

With agnt's proxy running, you trigger the edit flow several times. Then the AI checks:

```
AI: get_incidents {since: "2m"}

=== Incidents (3) === [inbox: crit=0 err=2 warn=1 info=0 new=3]

[error:http_5xx] 500 Internal Server Error (2x, 8s ago)
  id: 542eff39be5499ac
  PUT /api/products/42 → "deadlock detected"

[error:browser_js] TypeError (2x, 7s ago)
  id: d6e45a38f89fa794
  Cannot read properties of undefined (reading 'updatedAt')
  at: src/hooks/useProductMutation.tsx:56:31
  → http://localhost:3000/products/42/edit

[warning:http_4xx] 409 Conflict (3x, 12s ago)
  id: 6ff2de96a86836dc
  PUT /api/products/42 → "row was modified by another transaction"
```

The error pattern tells the whole story. The product edit triggers a `PUT` request. Sometimes a concurrent process (perhaps a background sync job or an optimistic UI retry) sends a second `PUT` for the same product. The database detects either a deadlock (500) or a write conflict (409). On the 500 case, the frontend tries to read `updatedAt` from the error response and throws a TypeError.

The AI now has three pieces of information it would never get from a single copy-pasted console error: the 409 conflicts showing concurrent writes, the deadlock confirming a database-level race, and the frontend TypeError revealing missing error handling for non-200 responses. It can suggest fixing all three layers -- adding optimistic locking, handling the 409 with a retry, and guarding the `updatedAt` access.

The two-minute window captured five separate events across two error sources. Without real-time tracking, you would have seen one TypeError in the console, assumed it was a simple null check, and missed the concurrency bug entirely.

## See Also

- [get_incidents API Reference](/api/get_incidents) -- full parameter documentation, severity mapping, and output format details
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) -- step-by-step setup and walkthrough of the error capture workflow
- [proxylog API Reference](/api/proxylog) -- detailed per-request traffic logs when you need to go deeper than aggregated errors
- [Frontend Error Tracking Use Case](/use-cases/frontend-error-tracking) -- broader patterns for error monitoring workflows
