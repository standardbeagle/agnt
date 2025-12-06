---
sidebar_position: 3
---

# Performance Monitoring

Using devtool-mcp to analyze, diagnose, and improve web application performance.

## Overview

devtool-mcp provides multiple tools for performance analysis:

- **Proxy traffic logs** - Request/response timing
- **Performance metrics** - Page load, paint timing
- **Network capture** - Resource loading analysis
- **Process monitoring** - Build and runtime performance

## Setting Up Performance Monitoring

### Start with Proxy

```json
// Start your app
run {script_name: "dev"}

// Create proxy for monitoring
proxy {action: "start", id: "perf", target_url: "http://localhost:3000", port: 8080}
```

Browse through `http://localhost:8080` to capture metrics.

## Analyzing Page Load Performance

### Get Performance Metrics

```json
proxylog {proxy_id: "perf", types: ["performance"]}
→ {
    entries: [{
      url: "http://localhost:8080/dashboard",
      navigation: {
        dom_content_loaded: 1250,  // DOM ready
        load_event: 2800           // Fully loaded
      },
      paint: {
        first_paint: 450,              // First pixels
        first_contentful_paint: 680    // First content
      },
      resources: [...]
    }]
  }
```

### Interpret Results

| Metric | Good | Needs Work | Poor |
|--------|------|------------|------|
| First Paint | < 1s | 1-2s | > 2s |
| First Contentful Paint | < 1.5s | 1.5-2.5s | > 2.5s |
| DOM Content Loaded | < 2s | 2-4s | > 4s |
| Load Event | < 3s | 3-5s | > 5s |

## Analyzing Network Performance

### Capture Network Data

```json
proxy {action: "exec", id: "perf", code: "window.__devtool.captureNetwork()"}
→ {
    summary: {
      totalResources: 42,
      totalTransferSize: 2500000,
      totalDuration: 3200,
      byType: {
        script: {count: 8, size: 1800000},
        stylesheet: {count: 3, size: 150000},
        image: {count: 25, size: 450000},
        fetch: {count: 6, size: 100000}
      }
    },
    entries: [...]
  }
```

### Find Slow Resources

```json
// Get all resources sorted by duration
proxy {action: "exec", id: "perf", code: `
  const network = window.__devtool.captureNetwork();
  const slow = network.entries
    .filter(e => e.duration > 500)
    .sort((a, b) => b.duration - a.duration)
    .slice(0, 10);
  slow
`}
→ Top 10 slowest resources
```

### Find Large Resources

```json
proxy {action: "exec", id: "perf", code: `
  const network = window.__devtool.captureNetwork();
  const large = network.entries
    .filter(e => e.transferSize > 100000)
    .sort((a, b) => b.transferSize - a.transferSize);
  large.map(e => ({name: e.name, size: Math.round(e.transferSize/1024) + 'KB'}))
`}
→ Large resources with sizes
```

## API Performance Analysis

### Track API Response Times

```json
proxylog {proxy_id: "perf", types: ["http"], url_pattern: "/api"}
→ {
    entries: [
      {url: "/api/users", duration_ms: 45},
      {url: "/api/products", duration_ms: 1200},  // Slow!
      {url: "/api/cart", duration_ms: 89}
    ]
  }
```

### Find Slow Endpoints

```json
// Filter for slow API calls (>500ms)
proxylog {proxy_id: "perf", types: ["http"], url_pattern: "/api"}
// Then filter results for duration_ms > 500
```

### Analyze Request Patterns

```json
proxylog {proxy_id: "perf", action: "stats"}
→ {
    by_type: {http: 150},
    // Look at request volume
  }
```

## Build Performance

### Monitor Build Times

```json
// Time your build
run {script_name: "build", mode: "foreground"}
→ {runtime: "45.2s"}

// Check for issues
proc {action: "output", process_id: "build", grep: "(warning|slow|large)"}
```

### Analyze Bundle Size

```json
// Run build analysis
run {raw: true, command: "npx", args: ["source-map-explorer", "dist/main.js"], mode: "foreground-raw"}

// Or with bundle analyzer
run {script_name: "build:analyze", mode: "foreground"}
```

## Real-Time Monitoring

### Page Session Analysis

```json
// See all loaded pages
currentpage {proxy_id: "perf"}
→ {
    sessions: [
      {url: "/dashboard", resource_count: 35, error_count: 0},
      {url: "/products", resource_count: 42, error_count: 2}
    ]
  }

// Get details for slow page
currentpage {proxy_id: "perf", action: "get", session_id: "page-1"}
→ Full breakdown of resources and timing
```

### Live Traffic Analysis

```json
// Recent traffic with timing
proxylog {proxy_id: "perf", types: ["http"], since: "1m", limit: 50}
```

## Performance Optimization Workflow

### Step 1: Baseline Measurement

```json
// Navigate to page
// Get current performance
proxylog {proxy_id: "perf", types: ["performance"]}
→ {navigation: {load_event: 4500}}  // 4.5s - too slow

proxy {action: "exec", id: "perf", code: "window.__devtool.captureNetwork()"}
→ {summary: {totalTransferSize: 3500000}}  // 3.5MB
```

### Step 2: Identify Bottlenecks

```json
// Find blocking resources
proxy {action: "exec", id: "perf", code: `
  const network = window.__devtool.captureNetwork();
  network.entries
    .filter(e => e.renderBlockingStatus === 'blocking' || e.type === 'script')
    .sort((a, b) => b.duration - a.duration)
`}

// Find large JS bundles
proxy {action: "exec", id: "perf", code: `
  const network = window.__devtool.captureNetwork();
  network.entries
    .filter(e => e.type === 'script' && e.transferSize > 50000)
`}
```

### Step 3: Make Improvements

Based on findings:
- Add code splitting for large bundles
- Lazy load below-fold images
- Add caching headers
- Compress assets

### Step 4: Verify Improvement

```json
// After changes, measure again
proxylog {proxy_id: "perf", types: ["performance"]}
→ {navigation: {load_event: 2100}}  // 2.1s - improved!

proxy {action: "exec", id: "perf", code: "window.__devtool.captureNetwork()"}
→ {summary: {totalTransferSize: 1200000}}  // 1.2MB - improved!
```

## Core Web Vitals

### Collect Web Vitals

```json
proxy {action: "exec", id: "perf", code: `
  const perf = window.__devtool.captureNetwork();
  const paint = performance.getEntriesByType('paint');
  const nav = performance.getEntriesByType('navigation')[0];

  ({
    LCP: paint.find(p => p.name === 'largest-contentful-paint')?.startTime,
    FCP: paint.find(p => p.name === 'first-contentful-paint')?.startTime,
    TTFB: nav?.responseStart,
    // CLS and FID require PerformanceObserver
  })
`}
```

### Targets

| Metric | Good | Needs Improvement | Poor |
|--------|------|-------------------|------|
| LCP | < 2.5s | 2.5-4s | > 4s |
| FID | < 100ms | 100-300ms | > 300ms |
| CLS | < 0.1 | 0.1-0.25 | > 0.25 |
| TTFB | < 800ms | 800-1800ms | > 1800ms |

## Layout Performance

### Find Layout Thrashing

```json
proxy {action: "exec", id: "perf", code: "window.__devtool.diagnoseLayout()"}
→ {
    overflows: [...],
    summary: {overflowCount: 3}
  }
```

### Check for Forced Reflow

```json
// This is a code inspection rather than runtime check
// Look for patterns that cause layout thrashing
proxy {action: "exec", id: "perf", code: `
  // Check for large DOM trees
  document.querySelectorAll('*').length
`}
```

## Monitoring Dashboard Example

```json
// Create a simple performance dashboard
proxy {action: "exec", id: "perf", code: `
  const metrics = {};

  // Page load
  const perf = proxylog({types: ['performance']});
  metrics.pageLoad = perf.entries[0]?.navigation?.load_event;

  // Resource count
  const network = window.__devtool.captureNetwork();
  metrics.resourceCount = network.summary.totalResources;
  metrics.totalSize = Math.round(network.summary.totalTransferSize / 1024) + 'KB';

  // Error count
  const errors = proxylog({types: ['error']});
  metrics.errorCount = errors.entries.length;

  metrics
`}
```

## Best Practices

1. **Always baseline first** - Measure before optimizing
2. **Focus on user-centric metrics** - LCP, FID, CLS
3. **Check both fast and slow connections** - Use browser throttling
4. **Monitor over time** - Performance can regress
5. **Test real pages** - Home page often differs from inner pages
6. **Check mobile** - Often slower than desktop

## Common Performance Issues

| Issue | Detection | Solution |
|-------|-----------|----------|
| Large JS bundles | `captureNetwork()` by type | Code splitting |
| Render-blocking CSS | Check resource timing | Inline critical CSS |
| Slow API calls | `proxylog` by URL pattern | Caching, optimization |
| Too many requests | `captureNetwork()` count | Bundling, sprites |
| Large images | `captureNetwork()` by size | Compression, lazy load |

## See Also

- [Reverse Proxy](/features/reverse-proxy) - Traffic capture
- [Frontend Diagnostics](/features/frontend-diagnostics) - Network capture
- [Debugging Web Apps](/use-cases/debugging-web-apps) - General debugging
