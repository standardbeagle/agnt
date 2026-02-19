---
title: "Chaos Engineering for Frontend Applications"
description: "Test your frontend against slow networks, API failures, and race conditions. Built-in chaos engineering for web applications using AI coding agents."
keywords: [frontend chaos engineering, chaos testing, resilience testing, fault injection, web application, AI coding agent]
sidebar_label: "Chaos Engineering"
---

# Chaos Engineering for Frontend Applications

Frontend chaos engineering finds the bugs that only appear in production: the spinner that never stops, the form that submits twice, the dashboard that shows stale data after a network blip. agnt lets your AI coding agent inject these failures systematically through the proxy, then observe exactly what breaks using `get_errors` and the `window.__devtool` diagnostics API.

## The Problem

Frontend code is written for the happy path. APIs respond in milliseconds, networks are stable, backends never return errors. Every demo works. Every staging test passes. Then a user on a train with a flaky LTE connection hits your checkout page, the payment API takes 8 seconds to respond, and the user clicks "Pay" three times because nothing happened. Three charges. One angry customer.

These failures are structurally difficult to test. They depend on timing, network conditions, and the specific order in which asynchronous operations resolve. A loading spinner might work when the API takes 200ms but fail to appear when it takes 50ms -- because the response arrives before React finishes its render cycle. A retry mechanism might work for a clean 500 error but corrupt state when the connection drops mid-response. You cannot find these bugs by reading code. You have to create the conditions and watch what happens.

Manual approaches do not scale. Chrome DevTools network throttling applies uniformly -- it cannot simulate an API that fails 30% of the time while the CDN works fine. Stopping your server mid-request is imprecise and non-repeatable. And none of these approaches give your AI agent visibility into what actually broke.

## The Traditional Approach

The standard toolkit for testing frontend resilience:

1. **DevTools network throttling** -- set "Slow 3G" and click around. Imprecise, applies to all requests equally, and your AI cannot see what you are doing.
2. **Manually stopping the server** -- kill the process mid-request. Non-repeatable, tests only one failure mode, and you lose the server logs.
3. **Conditional backend code** -- add `if (process.env.CHAOS) return 500` to your API routes. Pollutes production code, only tests errors you predicted, and requires redeployment for each scenario.
4. **Hope** -- ship it and wait for the bug reports.

None of these let an AI agent systematically inject failures and observe the results.

## The agnt Approach

agnt's `proxy exec` feature executes arbitrary JavaScript in the browser through the reverse proxy. This means the AI can intercept `fetch` and `XMLHttpRequest` at the browser level, injecting failures directly into the network layer your application uses. Combined with `get_errors` to observe the impact, the AI can run structured chaos experiments: inject a failure, check what broke, fix it, verify the fix, move to the next scenario.

The workflow is straightforward:

```json
// 1. Start the proxy
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}

// 2. Inject chaos via JavaScript
proxy {action: "exec", id: "app", code: "<chaos script>"}

// 3. Interact with the app (or let the AI trigger actions)

// 4. Check what broke
get_errors {}
```

Because the chaos is injected client-side via `fetch` interception, it affects exactly what real network failures affect -- your application's actual HTTP calls. No mock servers, no backend changes.

## Chaos Scenarios

Each scenario below shows the JavaScript that gets injected via `proxy exec`. These are not built-in presets -- they are plain scripts the AI writes and injects as needed.

### Flaky API: Random 500 Errors

Fail 30% of API calls with a 500 status. This tests error boundaries, retry logic, and whether your UI shows meaningful error messages or just a blank screen.

```json
proxy {action: "exec", id: "app", code: "
  const originalFetch = window.fetch;
  window.fetch = async function(...args) {
    const url = typeof args[0] === 'string' ? args[0] : args[0]?.url || '';
    if (url.includes('/api/') && Math.random() < 0.3) {
      return new Response(JSON.stringify({error: 'Internal Server Error'}), {
        status: 500,
        headers: {'Content-Type': 'application/json'}
      });
    }
    return originalFetch.apply(this, args);
  };
  'Chaos: 30% API failure rate active';
"}
```

### Slow Network: High Latency

Add 2-5 seconds of latency to every request. This exposes missing loading states, spinners that never appear, and timeouts set too aggressively.

```json
proxy {action: "exec", id: "app", code: "
  const originalFetch = window.fetch;
  window.fetch = async function(...args) {
    const delay = 2000 + Math.random() * 3000;
    await new Promise(r => setTimeout(r, delay));
    return originalFetch.apply(this, args);
  };
  'Chaos: 2-5s latency on all requests';
"}
```

### Race Conditions: Randomized Response Order

Randomize response timing so that earlier requests may resolve after later ones. This is how real networks behave and is the source of stale-data bugs in search inputs, filters, and pagination.

```json
proxy {action: "exec", id: "app", code: "
  const originalFetch = window.fetch;
  window.fetch = async function(...args) {
    const delay = Math.random() * 4000;
    await new Promise(r => setTimeout(r, delay));
    return originalFetch.apply(this, args);
  };
  'Chaos: randomized response timing (0-4s)';
"}
```

### Partial Failures: Targeted Endpoint Errors

Fail specific endpoints while the rest of the application works normally. This tests whether a single broken microservice cascades into a full-page crash or degrades gracefully.

```json
proxy {action: "exec", id: "app", code: "
  const failEndpoints = ['/api/payments', '/api/inventory'];
  const originalFetch = window.fetch;
  window.fetch = async function(...args) {
    const url = typeof args[0] === 'string' ? args[0] : args[0]?.url || '';
    if (failEndpoints.some(ep => url.includes(ep))) {
      return new Response(JSON.stringify({error: 'Service Unavailable'}), {
        status: 503,
        headers: {'Content-Type': 'application/json'}
      });
    }
    return originalFetch.apply(this, args);
  };
  'Chaos: payments and inventory endpoints returning 503';
"}
```

## Running a Chaos Test

A structured chaos test follows five steps. Here is the full sequence an AI agent would run:

```json
// Step 1: Start the proxy and open the app
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}

// Step 2: Confirm the app works before injecting chaos
get_errors {}
// Expected: no errors

// Step 3: Inject the chaos scenario
proxy {action: "exec", id: "app", code: "
  const originalFetch = window.fetch;
  window.fetch = async function(...args) {
    const url = typeof args[0] === 'string' ? args[0] : args[0]?.url || '';
    if (url.includes('/api/') && Math.random() < 0.3) {
      return new Response(JSON.stringify({error: 'Internal Server Error'}), {
        status: 500, headers: {'Content-Type': 'application/json'}
      });
    }
    return originalFetch.apply(this, args);
  };
  'Chaos active';
"}

// Step 4: Trigger user actions and check for errors
get_errors {}

// Step 5: Inspect the page state
proxy {action: "exec", id: "app", code: "window.__devtool.screenshot('chaos-test')"}
proxy {action: "exec", id: "app", code: "window.__devtool.auditPageQuality()"}
```

After step 4, the AI sees exactly which components threw errors, which API calls failed, and whether the UI is in a broken state. It can then fix the issue and re-run the test to verify.

## What to Verify

When chaos is active, the AI should check for these specific behaviors:

**Error boundaries render.** Components that depend on failed API calls should show an error state, not a blank screen or a crash. The AI can verify this with `window.__devtool.inspect('.error-boundary')` or by checking whether the page still has meaningful content.

**Loading states appear.** Under high latency, spinners or skeleton screens should be visible. If the UI freezes with no feedback, users will assume the app is broken. The AI can take a screenshot during the delay to verify.

**Retry logic works.** After a transient 500, does the UI offer a retry button? Does automatic retry actually re-fetch? The AI can inject a temporary failure, wait for the retry, then check `get_errors` to see if the error count stabilized.

**No data corruption.** After race conditions or partial failures, the data on screen should be consistent. A product list should not show items from two different queries. The AI can use `proxy exec` to read DOM state and compare it against what the API would have returned.

**User can recover.** After any failure, the user should be able to navigate away, refresh, or retry without the app staying stuck. The AI can simulate recovery by removing the chaos (reloading the page clears the fetch override) and verifying the app returns to normal.

## Real-World Example: E-Commerce Checkout

A payment API that takes 8 seconds to respond. Three questions every checkout flow must answer:

```json
// Inject slow payment API
proxy {action: "exec", id: "app", code: "
  const originalFetch = window.fetch;
  window.fetch = async function(...args) {
    const url = typeof args[0] === 'string' ? args[0] : args[0]?.url || '';
    if (url.includes('/api/checkout') || url.includes('/api/payment')) {
      await new Promise(r => setTimeout(r, 8000));
    }
    return originalFetch.apply(this, args);
  };
  'Chaos: 8s payment API delay';
"}
```

**Does the UI show a loading state?** After clicking "Pay", is there a spinner or a "Processing payment..." message? Or does the button just sit there, looking clickable?

```json
// Check immediately after clicking Pay
proxy {action: "exec", id: "app", code: "
  const btn = document.querySelector('[data-testid=\"pay-button\"]');
  const loading = document.querySelector('.payment-loading');
  JSON.stringify({
    buttonDisabled: btn?.disabled,
    buttonText: btn?.textContent,
    loadingVisible: loading !== null
  });
"}
```

**Does double-click prevention work?** If the user clicks "Pay" three times during the 8-second delay, does the app send three payment requests or one? The AI can check the proxy traffic log:

```json
proxylog {proxy_id: "app", types: ["http"], url_pattern: "/api/payment"}
```

If there are three POST entries, the submit button is not being disabled during the request.

**Does timeout handling work?** If the payment API takes longer than the client-side timeout, does the UI show "Something went wrong, please try again" or does it hang indefinitely? The AI injects a longer delay and checks `get_errors` for timeout-related errors:

```json
get_errors {since: "30s"}
```

Each of these checks takes the AI about 10 seconds. In under a minute, it has tested three critical checkout failure modes that would take a human tester significant effort to reproduce manually.

## Cleaning Up

Reloading the page clears all injected chaos since the fetch override only exists in the current page's JavaScript context. For a clean reset without navigation:

```json
proxy {action: "exec", id: "app", code: "
  if (window.__originalFetch) {
    window.fetch = window.__originalFetch;
    'Chaos removed';
  } else {
    'No chaos was active';
  }
"}
```

To make cleanup reliable, store the original reference when injecting chaos:

```json
proxy {action: "exec", id: "app", code: "
  window.__originalFetch = window.__originalFetch || window.fetch;
  window.fetch = async function(...args) {
    // ... chaos logic
    return window.__originalFetch.apply(this, args);
  };
"}
```

## See Also

- [Chaos Engineering Feature](/features/chaos-engineering) -- built-in proxy-level chaos presets for network simulation without JavaScript injection
- [proxy API Reference](/api/proxy) -- full `proxy exec` documentation and parameter reference
- [get_errors API Reference](/api/get_errors) -- error querying with filters, severity mapping, and output formats
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) -- the error debugging workflow that pairs with chaos testing
- [Frontend Error Tracking](/use-cases/frontend-error-tracking) -- broader patterns for monitoring and responding to frontend errors
