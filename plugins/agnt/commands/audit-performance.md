---
description: "Analyze page performance including load times and network resources"
allowed-tools: ["mcp__devtool__proxy", "mcp__devtool__proxylog"]
---

Analyze the performance of the current browser page using agnt's diagnostic tools.

## Steps

1. Capture network performance data:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.captureNetwork()"}
   ```

2. Check for layout issues that affect performance:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.findOverflows()"}
   ```

3. Analyze DOM complexity (affects rendering performance):
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.auditDOMComplexity()"}
   ```

4. Check for stacking contexts (can cause repaint issues):
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.findStackingContexts()"}
   ```

5. Query the proxy logs for performance metrics:
   ```
   proxylog {proxy_id: "dev", types: ["performance"], limit: 10}
   ```

## Interpreting Results

### Network Analysis (`captureNetwork`)
- `resources`: Array of all loaded resources
- `count`: Total number of resources
- Each resource includes:
  - `name`: URL of the resource
  - `type`: initiator type (script, img, css, etc.)
  - `duration`: Load time in ms
  - `size`: Transfer size in bytes
  - `startTime`: When loading started

### DOM Complexity (`auditDOMComplexity`)
- `totalElements`: Total DOM nodes (aim for < 1500)
- `maxDepth`: Maximum nesting depth (aim for < 32)
- `duplicateIds`: IDs used more than once (should be 0)
- `scripts`: Number of scripts (minimize)
- `stylesheets`: Number of stylesheets (minimize)

### Layout Issues (`findOverflows`)
- Elements with scroll overflow (potential layout thrashing)
- Hidden overflow elements (may cause reflow)

### Stacking Contexts
- Elements creating new stacking contexts
- Reasons: positioned with z-index, opacity < 1, transform, filter

## Performance Best Practices

1. **Reduce DOM size** - Fewer elements = faster rendering
2. **Minimize network requests** - Bundle and lazy-load
3. **Avoid layout thrashing** - Batch DOM reads/writes
4. **Optimize images** - Use appropriate formats and sizes
5. **Reduce JavaScript** - Minimize, defer, or async load
6. **Use efficient CSS** - Avoid deep nesting and complex selectors

## Performance Metrics from Proxy Logs

The proxy automatically captures Navigation Timing API metrics:
- `domContentLoaded`: Time to DOMContentLoaded
- `loadEventEnd`: Time to full page load
- `firstPaint`: Time to first paint
- `largestContentfulPaint`: Time to LCP (Core Web Vital)
