---
title: "Frontend Performance Monitoring with AI Coding Agents"
description: "Monitor Core Web Vitals, load times, and resource performance with AI. Automatic performance capture and AI-assisted optimization suggestions."
keywords: [frontend performance monitoring, Core Web Vitals, page speed, AI coding agent, MCP server, performance optimization]
sidebar_label: "Performance Monitoring"
---

import ModeVideo from '@site/src/components/ModeVideo';

# Frontend Performance Monitoring with AI Coding Agents

**Frontend performance monitoring** is one of those tasks that developers know matters but rarely do well during active development. An **AI coding agent** with access to real performance data can change that -- catching regressions as they happen, identifying bottlenecks from timing data, and suggesting targeted fixes based on actual measurements rather than guesswork.

The gap between "this page feels slow" and "this specific API call takes 2.8 seconds and this image is 480KB uncompressed" is where most performance work stalls. A component re-renders 50 times per keystroke, but on a fast development machine with hot caches, the developer never notices. An API call takes 3 seconds, but the loading spinner masks the wait. A 600KB JavaScript bundle ships because nobody checked the bundle size after adding that one dependency. These problems compound silently until a Lighthouse audit returns a score of 38 and nobody knows where to start.

The deeper issue is that performance data is scattered. Paint timings live in the Performance API. Network waterfalls live in the Network tab. Server response times live in the proxy logs. Pulling these together into a coherent picture requires switching between tools, copying timestamps, and mentally correlating events that happened milliseconds apart.

<ModeVideo src="/img/quality-audit.webm" poster="/img/quality-audit-poster.webp" label="Running a Full Page Audit from the floating indicator — the Audit menu opens over the panel, the audit runs, and the graded result attaches to the message composer ready to send to the agent." />

## The Traditional Approach

The standard toolkit for frontend performance work has three main components:

**Lighthouse** gives a point-in-time score. Run it, get a number, see some suggestions. But it does not track changes between edits. You optimize an image, re-run Lighthouse, and the score goes up by 2 points -- was that the image, or did the test server happen to respond faster this time? Lighthouse cannot tell you.

**The Performance tab in DevTools** captures detailed traces with flame charts, frame rendering timelines, and layout shift markers. It is powerful but overwhelming. Most developers open it, stare at the waterfall for thirty seconds, close it, and go back to guessing.

**Manual network waterfall inspection** catches obvious problems -- a 5MB image, a 4-second API call -- but misses patterns. You might spot that `/api/products` is slow, but not that it is called three times on the same page by three different components.

None of these approaches feed data directly to an AI that can analyze patterns, correlate events, and suggest code changes.

## The agnt Approach

agnt captures performance metrics automatically through its reverse proxy. The injected JavaScript uses the browser's Performance API to collect paint timings, navigation events, and resource loading data, then sends it all to the proxy over a WebSocket connection. Your AI agent queries this data using MCP tools and identifies bottlenecks from actual measurements.

```json
// Start your dev server and proxy
run {script_name: "dev"}
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}
```

Open the proxy URL in your browser. From this point, every page load captures navigation timing, paint metrics, and resource entries automatically. No browser extensions, no code instrumentation, no configuration.

## What Gets Captured

When a page loads through the agnt proxy, the injected JavaScript collects three categories of performance data and sends them as `performance` log entries.

**Paint metrics** -- First Paint (FP) and First Contentful Paint (FCP) timestamps from `performance.getEntriesByType('paint')`. These tell your AI exactly when the user first saw pixels and when meaningful content appeared.

**Navigation timing** -- `DOMContentLoaded` and `load` event timestamps from the Navigation Timing API. These mark when the DOM was parsed and when all resources (images, stylesheets, scripts) finished loading.

**Resource entries** -- Every resource loaded by the page: scripts, stylesheets, images, fonts, fetch calls. Each entry includes the resource name, transfer size, and load duration. This is the data that reveals which specific resources are dragging down page load.

Additionally, every HTTP request flowing through the proxy gets logged with a `duration_ms` field, giving your AI server-side response time for every API call without any backend instrumentation.

## Querying Performance Data

The `proxylog` tool with a `performance` type filter returns captured metrics.

```json
// Get all performance entries
proxylog {proxy_id: "app", types: ["performance"]}
```

```json
// Response
{
  "entries": [
    {
      "type": "performance",
      "timestamp": "2026-02-18T14:22:05Z",
      "url": "http://localhost:34521/dashboard",
      "navigation": {
        "dom_content_loaded": 820,
        "load_event": 2450
      },
      "paint": {
        "first_paint": 310,
        "first_contentful_paint": 485
      },
      "resources": [
        {"name": "main.js", "duration": 380, "size": 245000},
        {"name": "vendor.js", "duration": 290, "size": 412000},
        {"name": "styles.css", "duration": 85, "size": 18200},
        {"name": "hero.png", "duration": 520, "size": 483000}
      ]
    }
  ]
}
```

For HTTP-level timing, query the traffic logs directly:

```json
// API response times
proxylog {proxy_id: "app", types: ["http"], url_pattern: "/api"}
```

Each entry includes `duration_ms`, so your AI can immediately spot that `GET /api/dashboard/stats` takes 2800ms while every other endpoint responds in under 100ms.

## Core Web Vitals

Core Web Vitals -- LCP, INP, and CLS -- require PerformanceObserver for accurate measurement. Use `proxy exec` to collect them from the live page:

```json
proxy {action: "exec", id: "app", code: `
  const nav = performance.getEntriesByType('navigation')[0];
  const paint = performance.getEntriesByType('paint');
  const fcp = paint.find(p => p.name === 'first-contentful-paint');

  // LCP requires observing over time; get the latest entry
  const lcpEntries = performance.getEntriesByType('largest-contentful-paint');
  const lcp = lcpEntries.length ? lcpEntries[lcpEntries.length - 1] : null;

  ({
    TTFB: Math.round(nav?.responseStart),
    FCP: Math.round(fcp?.startTime),
    LCP: lcp ? Math.round(lcp.startTime) : 'not yet recorded',
    domContentLoaded: Math.round(nav?.domContentLoadedEventEnd),
    loadEvent: Math.round(nav?.loadEventEnd)
  })
`}
```

| Metric | Good | Needs Improvement | Poor |
|--------|------|-------------------|------|
| LCP | < 2.5s | 2.5-4s | > 4s |
| INP | < 200ms | 200-500ms | > 500ms |
| CLS | < 0.1 | 0.1-0.25 | > 0.25 |
| TTFB | < 800ms | 800-1800ms | > 1800ms |

For a broader quality check that includes performance-related issues like excessive DOM depth, use the built-in audit:

```json
proxy {action: "exec", id: "app", code: "window.__devtool.auditPageQuality()"}
```

This returns grouped findings covering DOM complexity, resource loading patterns, and rendering performance indicators.

## Identifying Bottlenecks

The real value of AI-assisted performance monitoring is pattern recognition across multiple data sources. Your AI can correlate resource timing, HTTP logs, and DOM diagnostics in a single analysis pass.

**Find the heaviest resources:**

```json
proxy {action: "exec", id: "app", code: `
  const network = window.__devtool.captureNetwork();
  network.entries
    .filter(e => e.transferSize > 100000)
    .sort((a, b) => b.transferSize - a.transferSize)
    .map(e => ({
      name: e.name.split('/').pop(),
      size: Math.round(e.transferSize / 1024) + 'KB',
      duration: Math.round(e.duration) + 'ms',
      type: e.initiatorType
    }))
`}
```

**Find render-blocking resources:**

```json
proxy {action: "exec", id: "app", code: `
  const network = window.__devtool.captureNetwork();
  network.entries.filter(e =>
    e.renderBlockingStatus === 'blocking' ||
    (e.initiatorType === 'script' && !e.name.includes('async'))
  )
`}
```

**Check DOM complexity as a performance factor:**

```json
proxy {action: "exec", id: "app", code: "window.__devtool.auditDOMComplexity()"}
```

Excessive DOM nodes (over 1500) directly impact layout calculation, style recalculation, and paint times. The audit flags deep nesting, wide parent nodes, and total node count.

## Real-World Example: A 5-Second Page Load

A dashboard page takes 5 seconds to become interactive. The developer's description is "it feels slow." The AI starts with the performance log:

```json
proxylog {proxy_id: "app", types: ["performance"]}
```

Result: `load_event: 5200`, `first_contentful_paint: 1800`. FCP alone is over the 1.5-second threshold. The AI digs into resources:

```json
proxylog {proxy_id: "app", types: ["http"], url_pattern: "/api"}
```

```json
// Results show the culprit
{
  "entries": [
    {"url": "/api/dashboard/widgets", "duration_ms": 89, "status": 200},
    {"url": "/api/dashboard/stats", "duration_ms": 2840, "status": 200},
    {"url": "/api/dashboard/activity", "duration_ms": 145, "status": 200}
  ]
}
```

The `/api/dashboard/stats` endpoint takes 2.8 seconds. The AI checks resources next:

```json
proxy {action: "exec", id: "app", code: `
  window.__devtool.captureNetwork().entries
    .filter(e => e.transferSize > 50000)
    .sort((a, b) => b.transferSize - a.transferSize)
    .slice(0, 5)
    .map(e => ({name: e.name.split('/').pop(), size: Math.round(e.transferSize/1024) + 'KB', duration: Math.round(e.duration) + 'ms'}))
`}
```

```json
[
  {"name": "hero-banner.png", "size": "483KB", "duration": "520ms"},
  {"name": "vendor.js", "size": "402KB", "duration": "290ms"},
  {"name": "main.js", "size": "239KB", "duration": "380ms"}
]
```

Two problems identified from data, not guesswork:

1. **A 2.8-second API call** (`/api/dashboard/stats`) -- the AI reads the route handler, finds an unindexed database query doing a full table scan, and adds the missing index.

2. **A 483KB unoptimized PNG** (`hero-banner.png`) -- the AI converts it to WebP with lossy compression, dropping it to 68KB, and adds `loading="lazy"` since it is below the fold.

After the fixes, the AI re-checks:

```json
proxylog {proxy_id: "app", types: ["performance"]}
// load_event: 1800, first_contentful_paint: 620
```

Load time drops from 5.2 seconds to 1.8 seconds. FCP drops from 1.8 seconds to 620ms. Both improvements are measurable and attributable to specific changes -- not "I ran Lighthouse and the score went up."

## See Also

- [proxylog API Reference](/api/proxylog) -- full parameter docs for traffic log queries
- [Performance Monitoring Use Case](/use-cases/performance-monitoring) -- optimization workflows and best practices
- [Quality & Performance Auditing](/api/frontend/quality-auditing) -- frame rate monitoring, DOM complexity audits, and jank detection
