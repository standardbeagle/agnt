---
sidebar_position: 12
---

# Quality & Performance Auditing

Functions for detecting runtime quality issues including jank, stuttering, long tasks, memory pressure, DOM complexity, and Core Web Vitals.

## Frame Rate & Animation

### observeFrameRate

Monitor frame rate and detect jank/stuttering using requestAnimationFrame timing.

```javascript
window.__devtool.observeFrameRate(options?)
```

**Parameters:**
- `options.duration` (number): Observation duration in ms (default: 5000)
- `options.threshold` (number): Frame time threshold for jank detection in ms (default: 50)

**Returns:** Observer object
```javascript
{
  stop: function() { /* Returns results */ },
  isRunning: function() { /* Returns boolean */ },
  getResults: function() { /* Returns results if complete */ }
}
```

**Results:**
```javascript
{
  totalFrames: 298,
  duration: 5000,
  avgFPS: 59.6,
  avgFrameTime: 16.78,
  maxFrameTime: 85.2,
  minFrameTime: 14.1,
  p95FrameTime: 18.5,
  p99FrameTime: 45.2,
  jankFrames: 3,
  totalDroppedFrames: 8,
  smoothness: 94.2,           // % of frames under 16.67ms
  rating: "smooth",           // "smooth" | "moderate-jank" | "janky"
  jankEvents: [
    { delta: 85, droppedFrames: 4, timestamp: 1523 }
  ],
  timestamp: 1699999999999
}
```

**Example:**
```javascript
// Start 5-second observation
const obs = window.__devtool.observeFrameRate({ duration: 5000 })

// Trigger animations/scrolling during observation...

// Get results (auto-stops after duration)
setTimeout(() => {
  const results = obs.getResults()
  console.log(`Average FPS: ${results.avgFPS}`)
  console.log(`Smoothness: ${results.smoothness}%`)
  console.log(`Jank events: ${results.jankFrames}`)
}, 5500)

// Or stop early
const results = obs.stop()
```

---

### observeLongAnimationFrames

Observe Long Animation Frames (LoAF) with script attribution. Chrome 123+ only.

```javascript
window.__devtool.observeLongAnimationFrames(callback?)
```

**Parameters:**
- `callback` (function): Called on each long animation frame (>50ms)

**Returns:** Observer object
```javascript
{
  stop: function() { /* Returns summary */ },
  getCurrent: function() { /* Returns current stats */ }
}
```

**Callback Entry:**
```javascript
{
  duration: 85,
  blockingDuration: 35,
  startTime: 1523,
  renderStart: 1570,
  styleAndLayoutStart: 1565,
  firstUIEventTimestamp: 1520,
  scripts: [
    {
      sourceURL: "https://example.com/app.js",
      sourceFunctionName: "handleClick",
      invoker: "BUTTON#submit.onclick",
      invokerType: "event-listener",
      duration: 45,
      executionStart: 1525,
      forcedStyleAndLayoutDuration: 12,  // Layout thrashing!
      pauseDuration: 0
    }
  ]
}
```

**Example:**
```javascript
const obs = window.__devtool.observeLongAnimationFrames(entry => {
  if (entry.scripts.length > 0) {
    console.log(`Long frame caused by: ${entry.scripts[0].sourceURL}`)
    if (entry.scripts[0].forcedStyleAndLayoutDuration > 0) {
      console.log('Layout thrashing detected!')
    }
  }
})

// ... interact with page ...

const summary = obs.stop()
console.log(`Total blocking: ${summary.totalBlockingDuration}ms`)
console.log(`Forced layouts: ${summary.totalForcedLayoutDuration}ms`)
```

---

## Core Web Vitals

### observeINP

Observe Interaction to Next Paint (INP) - Core Web Vital since March 2024.

```javascript
window.__devtool.observeINP(callback?)
```

**Parameters:**
- `callback` (function): Called when a new worst INP is recorded

**Thresholds:**
- Good: < 200ms
- Needs Improvement: 200-500ms
- Poor: > 500ms

**Returns:** Observer object

**Callback Data:**
```javascript
{
  type: "new-worst",
  inp: 250,
  interaction: {
    type: "pointerup",
    duration: 250,
    startTime: 5000,
    target: "button.submit",
    inputDelay: 15,
    processingTime: 180,
    presentationDelay: 55
  },
  rating: "needs-improvement"
}
```

**Stop Results:**
```javascript
{
  interactions: [...],          // Last 50 interactions
  totalInteractions: 150,
  worstINP: 450,
  worstInteraction: {...},
  p75INP: 180,                  // What Google uses for scoring
  rating: "good",
  breakdown: {
    good: 140,
    needsImprovement: 8,
    poor: 2
  }
}
```

**Example:**
```javascript
const obs = window.__devtool.observeINP(data => {
  if (data.rating === 'poor') {
    console.log(`Slow interaction on: ${data.interaction.target}`)
    console.log(`Input delay: ${data.interaction.inputDelay}ms`)
    console.log(`Processing: ${data.interaction.processingTime}ms`)
  }
})

// User interacts with page...

const results = obs.stop()
console.log(`INP (p75): ${results.p75INP}ms - ${results.rating}`)
```

---

### observeLCP

Observe Largest Contentful Paint (LCP) - Core Web Vital.

```javascript
window.__devtool.observeLCP(callback?)
```

**Thresholds:**
- Good: < 2500ms
- Needs Improvement: 2500-4000ms
- Poor: > 4000ms

**Returns:** Observer object

**Callback/Results:**
```javascript
{
  value: 1850,                  // LCP time in ms
  size: 125000,                 // Element size
  element: "img.hero-image",    // LCP element selector
  elementTag: "img",
  url: "/images/hero.jpg",      // For images
  loadTime: 1200,
  renderTime: 1850,
  rating: "good"
}
```

**Example:**
```javascript
const obs = window.__devtool.observeLCP(entry => {
  console.log(`LCP candidate: ${entry.element} at ${entry.value}ms`)
})

// Wait for page to stabilize
setTimeout(() => {
  const result = obs.stop()
  console.log(`Final LCP: ${result.finalLCP.value}ms`)
  console.log(`LCP element: ${result.finalLCP.element}`)
}, 5000)
```

---

## DOM & Memory

### auditDOMComplexity

Audit DOM structure for performance issues.

```javascript
window.__devtool.auditDOMComplexity()
```

**Thresholds (Lighthouse):**
- Total nodes: < 1500 (optimal < 800)
- Max depth: < 32 (optimal < 15)
- Max children: < 60 (optimal < 30)

**Returns:**
```javascript
{
  totalNodes: 2500,
  maxDepth: 45,
  maxChildren: 120,
  deepestElement: "div.nested > div > div > ...",
  largestParent: "ul.product-list",
  heavyParents: [
    { selector: "ul.product-list", childCount: 120 }
  ],
  topTags: [
    { tag: "div", count: 800 },
    { tag: "span", count: 450 }
  ],
  depthDistribution: { "1": 5, "2": 20, ... },
  thresholds: {
    nodes: { value: 2500, limit: 1500, exceeded: true },
    depth: { value: 45, limit: 32, exceeded: true },
    children: { value: 120, limit: 60, exceeded: true }
  },
  scores: {
    nodes: 40,
    depth: 20,
    children: 20,
    overall: 27
  },
  rating: "poor",
  recommendations: [
    "Reduce DOM nodes (current: 2500, recommended: <1500). Consider virtualization for lists.",
    "Flatten DOM structure (current depth: 45, recommended: <32)."
  ],
  timestamp: 1699999999999
}
```

**Example:**
```javascript
const dom = window.__devtool.auditDOMComplexity()

if (dom.rating === 'poor') {
  console.log('DOM complexity issues:')
  dom.recommendations.forEach(r => console.log(`  - ${r}`))

  // Find the problematic element
  if (dom.heavyParents.length > 0) {
    window.__devtool.highlight(dom.heavyParents[0].selector)
  }
}
```

---

### captureMemoryMetrics

Capture JavaScript heap memory metrics (Chrome only).

```javascript
window.__devtool.captureMemoryMetrics()
```

**Returns:**
```javascript
{
  available: true,              // false if not Chrome
  jsHeap: {
    usedSize: 52428800,
    totalSize: 67108864,
    sizeLimit: 2147483648,
    usedMB: 50.0,
    totalMB: 64.0,
    limitMB: 2048.0,
    percentUsed: 2.4,
    percentAllocated: 3.1
  },
  pressure: "low",              // "low" | "moderate" | "high" | "critical"
  measureMemoryAvailable: true, // Modern API available?
  timestamp: 1699999999999
}
```

**Example:**
```javascript
// Snapshot before operation
const before = window.__devtool.captureMemoryMetrics()

// Perform operation...
heavyOperation()

// Snapshot after
const after = window.__devtool.captureMemoryMetrics()

const growth = after.jsHeap.usedMB - before.jsHeap.usedMB
console.log(`Memory growth: ${growth.toFixed(2)}MB`)

if (after.pressure === 'high' || after.pressure === 'critical') {
  console.warn('High memory pressure detected!')
}
```

---

### measureMemoryDetailed

Detailed memory measurement with attribution (requires cross-origin isolation).

```javascript
window.__devtool.measureMemoryDetailed()
```

**Note:** Requires COOP/COEP headers for cross-origin isolation. May take up to 20 seconds.

**Returns:** Promise
```javascript
{
  totalBytes: 52428800,
  totalMB: 50.0,
  breakdown: [
    {
      bytes: 30000000,
      types: ["JS"],
      attribution: [
        { url: "https://example.com/app.js", scope: "Window" }
      ]
    }
  ],
  timestamp: 1699999999999
}
```

---

### auditEventListeners

Audit inline event handlers for maintainability and potential leaks.

```javascript
window.__devtool.auditEventListeners()
```

**Returns:**
```javascript
{
  totalInlineHandlers: 85,
  elementsWithHandlers: 42,
  topElements: [
    { selector: "button.action", handlers: ["onclick", "onmouseover"], count: 2 }
  ],
  issues: [
    {
      type: "excessive-inline-handlers",
      message: "Found 85 inline event handlers. Consider event delegation.",
      severity: "warning"
    }
  ],
  recommendations: [
    "Consider using addEventListener() instead of inline handlers for better maintainability.",
    "Use event delegation on parent containers to reduce listener count."
  ],
  note: "This audit only detects inline HTML handlers. Use Chrome DevTools getEventListeners() for comprehensive listener inspection.",
  timestamp: 1699999999999
}
```

---

### estimateTBT

Estimate Total Blocking Time (TBT) from recorded long tasks.

```javascript
window.__devtool.estimateTBT()
```

**Thresholds:**
- Good: < 200ms
- Needs Improvement: 200-600ms
- Poor: > 600ms

**Returns:**
```javascript
{
  totalBlockingTime: 350,
  longTaskCount: 8,
  longTasks: [
    {
      duration: 120,
      blockingTime: 70,         // duration - 50ms
      startTime: 500,
      name: "self"
    }
  ],
  rating: "needs-improvement",
  context: {
    fcp: 850,
    note: "TBT measures blocking time between FCP and TTI. Lower is better."
  },
  thresholds: {
    good: "< 200ms",
    needsImprovement: "200-600ms",
    poor: "> 600ms"
  },
  timestamp: 1699999999999
}
```

---

## Comprehensive Audit

### auditPageQuality

Run all quality checks and generate a comprehensive report with scoring.

```javascript
window.__devtool.auditPageQuality()
```

**Returns:** Promise
```javascript
{
  scores: {
    dom: 70,
    tbt: 60,
    memory: 100,
    eventListeners: 70,
    text: 85,
    responsive: 90,
    cls: 100
  },
  overallScore: 78,
  grade: "C",                   // A (90+), B (80-89), C (70-79), D (60-69), F (<60)

  criticalIssues: [
    { category: "dom", message: "Excessive DOM nodes: 2500" },
    { category: "performance", message: "High Total Blocking Time: 450ms" }
  ],

  recommendations: [
    {
      priority: 1,
      category: "performance",
      issue: "TBT is 450ms",
      fix: "Break up long tasks. Use web workers for heavy computation."
    },
    {
      priority: 2,
      category: "dom",
      issue: "DOM has 2500 nodes",
      fix: "Target <1500 nodes. Use virtualization for long lists."
    }
  ],

  details: {
    dom: { /* auditDOMComplexity results */ },
    memory: { /* captureMemoryMetrics results */ },
    tbt: { /* estimateTBT results */ },
    eventListeners: { /* auditEventListeners results */ },
    performance: { /* capturePerformanceMetrics results */ },
    textFragility: { summary: {...}, issueCount: 3 },
    responsiveRisk: { summary: {...}, issueCount: 2 }
  },

  timestamp: 1699999999999
}
```

**Example:**
```javascript
window.__devtool.auditPageQuality().then(audit => {
  console.log(`Page Quality: ${audit.grade} (${audit.overallScore}/100)`)

  if (audit.criticalIssues.length > 0) {
    console.log('\nCritical Issues:')
    audit.criticalIssues.forEach(i => {
      console.log(`  [${i.category}] ${i.message}`)
    })
  }

  console.log('\nTop Recommendations:')
  audit.recommendations.slice(0, 3).forEach(r => {
    console.log(`  ${r.priority}. [${r.category}] ${r.issue}`)
    console.log(`     Fix: ${r.fix}`)
  })

  // Detailed breakdown
  console.log('\nScores by Category:')
  Object.entries(audit.scores).forEach(([k, v]) => {
    if (v !== null) console.log(`  ${k}: ${v}/100`)
  })
})
```

---

### auditAll

Aggregate **eight scored audits** into a single weighted overall grade. Where `auditPageQuality` focuses on runtime performance signals, `auditAll` rolls up the full audit suite.

```javascript
window.__devtool.auditAll()
```

**The eight audits and their weights:**

| Audit | Weight | Source |
|-------|--------|--------|
| Security | 1.5 | `auditSecurity` |
| Accessibility | 1.3 | `auditAccessibility` |
| Performance | 1.2 | `auditPageQuality` |
| API efficiency | 1.1 | `__devtool_audit_api.auditAPIEfficiency` ([api_audit](/api/api_audit)) |
| Loading / spinner | 1.1 | `__devtool_audit_loading.auditLoading` ([loading_audit](/api/loading_audit)) |
| SEO | 1.0 | `auditSEO` |
| DOM | 0.8 | `auditDOMComplexity` |
| CSS | 0.7 | `auditCSS` |

Each audit is guarded — `auditAll` degrades cleanly when a module is absent rather than failing.

**Temporal audits:** the API-efficiency and loading audits read the fetch/XHR buffer and the spinner timeline respectively. Both are populated by browsing, so they need a **fresh page load** to produce meaningful results. Run them via the dedicated [api_audit](/api/api_audit) / [loading_audit](/api/loading_audit) MCP tools when you want them in isolation.

---

## Common Patterns

### Performance Regression Testing

```javascript
async function performanceBaseline() {
  const audit = await window.__devtool.auditPageQuality()

  return {
    lcp: window.__devtool.observeLCP().getCurrent()?.value,
    tbt: audit.details.tbt.totalBlockingTime,
    cls: audit.details.performance.cls?.score,
    domNodes: audit.details.dom.totalNodes,
    memoryMB: audit.details.memory.jsHeap?.usedMB,
    grade: audit.grade,
    score: audit.overallScore
  }
}

// Capture baseline
const baseline = await performanceBaseline()
console.log('Baseline:', baseline)

// After changes, compare
const current = await performanceBaseline()
console.log('Current:', current)
console.log('Score change:', current.score - baseline.score)
```

### Animation Smoothness Check

```javascript
async function checkAnimationSmoothness() {
  console.log('Starting 5s frame rate observation...')
  console.log('Scroll, animate, or interact with the page')

  const frameObs = window.__devtool.observeFrameRate({ duration: 5000 })
  const loafObs = window.__devtool.observeLongAnimationFrames()

  await new Promise(r => setTimeout(r, 5500))

  const frames = frameObs.getResults()
  const loaf = loafObs.stop()

  console.log(`\nFrame Rate Results:`)
  console.log(`  Average FPS: ${frames.avgFPS}`)
  console.log(`  Smoothness: ${frames.smoothness}%`)
  console.log(`  Jank events: ${frames.jankFrames}`)
  console.log(`  Dropped frames: ${frames.totalDroppedFrames}`)

  if (loaf.count > 0) {
    console.log(`\nLong Animation Frames: ${loaf.count}`)
    console.log(`  Total blocking: ${loaf.totalBlockingDuration}ms`)
    if (loaf.worstFrame) {
      console.log(`  Worst frame: ${loaf.worstFrame.duration}ms`)
      if (loaf.worstFrame.scripts.length > 0) {
        console.log(`  Caused by: ${loaf.worstFrame.scripts[0].sourceURL}`)
      }
    }
  }

  return { frames, loaf }
}
```

### Memory Leak Detection

```javascript
async function detectMemoryLeak(operation, iterations = 10) {
  const samples = []

  for (let i = 0; i < iterations; i++) {
    await operation()

    // Force GC if available (Chrome with --expose-gc flag)
    if (window.gc) window.gc()

    const mem = window.__devtool.captureMemoryMetrics()
    if (mem.available) {
      samples.push(mem.jsHeap.usedMB)
    }

    await new Promise(r => setTimeout(r, 100))
  }

  if (samples.length < 2) {
    return { error: 'Memory API not available' }
  }

  // Check for consistent growth
  const growth = samples[samples.length - 1] - samples[0]
  const avgGrowth = growth / iterations

  return {
    startMB: samples[0],
    endMB: samples[samples.length - 1],
    totalGrowth: growth,
    avgGrowthPerIteration: avgGrowth,
    samples: samples,
    likelyLeak: avgGrowth > 0.5  // More than 0.5MB growth per iteration
  }
}

// Usage
const result = await detectMemoryLeak(async () => {
  // Your operation that might leak
  createAndRemoveElements()
}, 20)

if (result.likelyLeak) {
  console.warn(`Possible memory leak: ${result.avgGrowthPerIteration.toFixed(2)}MB per iteration`)
}
```

---

## Browser Compatibility

| Function | Chrome | Firefox | Safari | Edge |
|----------|--------|---------|--------|------|
| `observeFrameRate` | Yes | Yes | Yes | Yes |
| `observeLongAnimationFrames` | 123+ | No | No | 123+ |
| `observeINP` | Yes | Yes | Partial | Yes |
| `observeLCP` | Yes | Yes | Yes | Yes |
| `auditDOMComplexity` | Yes | Yes | Yes | Yes |
| `captureMemoryMetrics` | Yes | No | No | Yes |
| `measureMemoryDetailed` | Yes* | No | No | Yes* |
| `auditEventListeners` | Yes | Yes | Yes | Yes |
| `estimateTBT` | Yes | Partial | Partial | Yes |
| `auditPageQuality` | Yes | Partial | Partial | Yes |

\* Requires cross-origin isolation (COOP/COEP headers)

---

## See Also

- [Layout Robustness](/api/frontend/layout-robustness) - Text fragility and responsive risk detection
- [Performance Monitoring](/use-cases/performance-monitoring) - Performance use cases
- [Core Web Vitals](https://web.dev/vitals/) - Google's Web Vitals documentation
