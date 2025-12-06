---
sidebar_position: 1
---

# Debugging Web Applications

A comprehensive guide to debugging web applications using devtool-mcp's proxy and frontend diagnostics.

## Overview

This use case covers the complete workflow for debugging frontend issues:

1. Set up the debugging environment
2. Capture and analyze HTTP traffic
3. Identify JavaScript errors
4. Debug layout and styling issues
5. Document and share findings

## Setting Up

### Start Your Development Server

```json
// Detect project and available scripts
detect {path: "."}
→ {type: "node", scripts: ["dev", "build", "test"]}

// Start dev server
run {script_name: "dev"}
→ {process_id: "dev", state: "running"}

// Verify it's ready
proc {action: "output", process_id: "dev", grep: "ready", tail: 5}
→ "Ready on http://localhost:3000"
```

### Create Debugging Proxy

```json
proxy {action: "start", id: "debug", target_url: "http://localhost:3000", port: 8080}
→ {id: "debug", listen_addr: ":8080", status: "running"}
```

Now browse to `http://localhost:8080` instead of port 3000.

## Debugging Workflows

### Investigating a User-Reported Bug

**Scenario:** User reports "the submit button doesn't work on the checkout page."

**Step 1: Navigate to the Problem Page**

Have the user (or navigate yourself) to the checkout page via the proxy.

**Step 2: Check for JavaScript Errors**

```json
proxylog {proxy_id: "debug", types: ["error"]}
→ {
    entries: [{
      message: "Cannot read property 'submit' of undefined",
      source: "/static/js/checkout.js",
      line: 234
    }]
  }
```

**Step 3: Check API Calls**

```json
proxylog {proxy_id: "debug", types: ["http"], url_pattern: "/api/checkout"}
→ {
    entries: [{
      method: "POST",
      url: "/api/checkout",
      status: 401,
      response_body: "{\"error\": \"Session expired\"}"
    }]
  }
```

**Step 4: Inspect the Button**

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.inspect('#submit-btn')"}
→ {
    visibility: {visible: true},
    accessibility: {role: "button", accessibleName: "Submit Order"},
    // Element exists and is visible
  }
```

**Conclusion:** The API returns 401 (session expired), causing the JavaScript error. The button itself is fine.

### Debugging Layout Issues

**Scenario:** Content is getting cut off on mobile.

**Step 1: Find Overflow Issues**

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.findOverflows()"}
→ [
    {selector: ".product-grid", overflow: "horizontal", excess: 200}
  ]
```

**Step 2: Get Details**

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.getOverflow('.product-grid')"}
→ {
    scrollWidth: 520,
    clientWidth: 320,
    hasHorizontalOverflow: true
  }
```

**Step 3: Highlight the Issue**

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.highlight('.product-grid', {color: 'rgba(255,0,0,0.3)', duration: 0})"}
```

**Step 4: Take Screenshot**

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.screenshot('overflow-issue')"}
```

**Step 5: Examine the CSS**

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.getComputed('.product-grid', ['display', 'grid-template-columns', 'gap'])"}
→ {
    display: "grid",
    "grid-template-columns": "repeat(3, 200px)",  // Fixed width - problem!
    gap: "16px"
  }
```

**Solution:** Change `repeat(3, 200px)` to `repeat(auto-fit, minmax(200px, 1fr))`.

### Debugging Z-Index Problems

**Scenario:** Dropdown menu appears behind a modal.

**Step 1: Find Stacking Contexts**

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.findStackingContexts()"}
→ [
    {selector: ".modal", zIndex: 1000, reason: "position: fixed"},
    {selector: ".dropdown", zIndex: 100, parentContext: ".header"}
  ]
```

**Step 2: Check Dropdown's Context**

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.getStacking('.dropdown')"}
→ {
    zIndex: 100,
    createsContext: true,
    parentContext: ".header",  // Constrained by header!
    reason: "z-index with position: absolute"
  }
```

**Step 3: Check Header's Z-Index**

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.getStacking('.header')"}
→ {
    zIndex: 50,  // Lower than modal's 1000
    createsContext: true
  }
```

**Problem:** The dropdown's z-index (100) is relative to `.header` (50), which is lower than the modal (1000).

**Solution:** Either increase header's z-index above 1000, or move the dropdown outside the header's stacking context.

### Performance Investigation

**Scenario:** Page loads slowly.

**Step 1: Check Performance Metrics**

```json
proxylog {proxy_id: "debug", types: ["performance"]}
→ {
    entries: [{
      navigation: {dom_content_loaded: 2500, load_event: 4500},
      paint: {first_paint: 1800, first_contentful_paint: 2200}
    }]
  }
```

**Step 2: Analyze Resources**

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.captureNetwork()"}
→ {
    summary: {
      totalResources: 45,
      totalTransferSize: 2500000,
      byType: {
        script: {count: 12, size: 1800000}  // 1.8MB of JS!
      }
    },
    entries: [
      {name: "vendor.js", duration: 2100, size: 1200000}
    ]
  }
```

**Step 3: Check for Slow API Calls**

```json
proxylog {proxy_id: "debug", types: ["http"], url_pattern: "/api"}
→ Find any API calls with high duration_ms
```

**Step 4: Get Page Session Overview**

```json
currentpage {proxy_id: "debug", action: "get", session_id: "page-1"}
→ Full breakdown of resources, errors, and timing
```

## Interactive Debugging

### Let User Show You the Problem

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.selectElement()"}
```

Ask the user to click on the problematic element. The response includes the selector and element details.

### Ask User Questions

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.ask('Does this look correct?', ['Yes', 'No', 'Partially'])"}
```

### Wait for Dynamic Content

```json
proxy {action: "exec", id: "debug", code: "window.__devtool.waitForElement('.results-loaded', 10000)"}
```

## Documentation Workflow

### Capture State for Bug Report

```json
// Screenshot
proxy {action: "exec", id: "debug", code: "window.__devtool.screenshot('bug-state')"}

// DOM snapshot
proxy {action: "exec", id: "debug", code: "window.__devtool.captureDOM()"}

// Element details
proxy {action: "exec", id: "debug", code: "window.__devtool.inspect('.problem-element')"}

// Recent errors
proxylog {proxy_id: "debug", types: ["error"], since: "5m"}

// Recent traffic
proxylog {proxy_id: "debug", types: ["http"], url_pattern: "/api", since: "5m"}
```

### Create Visual Guide

```json
// Highlight relevant elements
proxy {action: "exec", id: "debug", code: "window.__devtool.highlight('.step-1', {label: 'Step 1', duration: 0})"}
proxy {action: "exec", id: "debug", code: "window.__devtool.highlight('.step-2', {label: 'Step 2', duration: 0})"}

// Screenshot with highlights
proxy {action: "exec", id: "debug", code: "window.__devtool.screenshot('annotated-steps')"}

// Clean up
proxy {action: "exec", id: "debug", code: "window.__devtool.clearAllOverlays()"}
```

## Complete Debugging Session Example

```
User: "The product page is broken"

AI: Let me investigate. First, I'll set up debugging.

[run {script_name: "dev"}]
[proxy {action: "start", id: "debug", target_url: "http://localhost:3000"}]

AI: Please navigate to http://localhost:8080/products/123

[User navigates]

AI: Let me check for errors.

[proxylog {proxy_id: "debug", types: ["error"]}]
→ "TypeError: products.map is not a function"

AI: Found a JavaScript error. Let me check the API.

[proxylog {proxy_id: "debug", types: ["http"], url_pattern: "/api/products"}]
→ {status: 200, response_body: "{\"product\": {...}}"}

AI: The API returned a single product object, not an array.
    The code expects an array (products.map).

    Let me show you exactly where:

[proxy {action: "exec", id: "debug", code: "window.__devtool.highlight('.products-list', {color: 'red'})"}]

AI: The products-list container is highlighted. The component expects
    an array but receives a single object.

    Fix: Change the API endpoint to return [{...}] or update the
    component to handle single products.
```

## Best Practices

1. **Start with errors** - Check `proxylog` for errors first
2. **Check API responses** - Many frontend bugs are caused by unexpected API data
3. **Use page sessions** - `currentpage` gives a unified view
4. **Screenshot liberally** - Visual evidence helps communication
5. **Clean up highlights** - Use `clearAllOverlays()` when done
6. **Document findings** - Capture state for sharing

## See Also

- [Reverse Proxy](/features/reverse-proxy) - Proxy configuration
- [Frontend Diagnostics](/features/frontend-diagnostics) - All available primitives
- [Performance Monitoring](/use-cases/performance-monitoring) - Deep performance analysis
