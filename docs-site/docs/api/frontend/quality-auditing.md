---
sidebar_position: 12
---

import ModeVideo from '@site/src/components/ModeVideo';

# Quality & Performance Auditing

<ModeVideo src="/img/quality-audit.webm" poster="/img/quality-audit-poster.webp" label="Running a Full Page Audit from the floating indicator — the Audit menu opens over the panel, the audit runs, and the graded result attaches to the message composer ready to send to the agent." />

Consolidated audits surface runtime quality problems — DOM bloat, blocking time, memory pressure, and Core Web Vitals — in a single call each. No manual `PerformanceObserver` wiring: `auditPerformance()` and `auditPageQuality()` collect the signals, score them, and return prioritized recommendations.

## auditPerformance

Capture runtime performance signals — Core Web Vitals, blocking time, layout shift, long tasks, and resource timing — in one structured report.

```javascript
window.__devtool.auditPerformance()
```

**Returns:** Promise
```javascript
{
  cls: {
    score: 0.15,
    rating: "needs-improvement",  // "good" | "needs-improvement" | "poor"
    shifts: [
      { value: 0.08, startTime: 1523, sources: [".ad-banner", ".hero-image"] }
    ]
  },
  lcp: {
    value: 1850,                  // LCP time in ms
    element: "img.hero-image",
    rating: "good"                // good < 2500ms, needs-improvement 2500-4000ms, poor > 4000ms
  },
  inp: {
    p75INP: 180,                  // What Google uses for scoring
    worstINP: 450,
    rating: "good"                // good < 200ms, needs-improvement 200-500ms, poor > 500ms
  },
  tbt: {
    totalBlockingTime: 350,
    longTaskCount: 8,
    rating: "needs-improvement"   // good < 200ms, needs-improvement 200-600ms, poor > 600ms
  },
  longTasks: [
    { duration: 85, startTime: 234, name: "self" }
  ],
  resources: {
    byType: {
      script: { count: 12, totalSize: 450000, totalDuration: 1200 },
      img: { count: 25, totalSize: 1200000, totalDuration: 2500 }
    },
    largest: [ { url: "/images/hero.jpg", type: "img", size: 450000, duration: 800 } ],
    slowest: [ { url: "/api/data", type: "fetch", size: 5000, duration: 1200 } ],
    renderBlocking: [ { url: "/styles.css", type: "css", size: 45000, duration: 200 } ]
  },
  paint: { firstPaint: 450, firstContentfulPaint: 620 },
  totals: { pageWeight: 1855000, resourceCount: 46, loadTime: 2800 },
  timestamp: 1699999999999
}
```

**Core Web Vitals thresholds:**
| Metric | Good | Needs Improvement | Poor |
|--------|------|-------------------|------|
| LCP | < 2500ms | 2500-4000ms | > 4000ms |
| INP | < 200ms | 200-500ms | > 500ms |
| CLS | < 0.1 | 0.1-0.25 | > 0.25 |
| TBT | < 200ms | 200-600ms | > 600ms |

**Example:**
```javascript
const perf = await window.__devtool.auditPerformance()

console.log(`LCP: ${perf.lcp.value}ms (${perf.lcp.rating})`)
console.log(`INP p75: ${perf.inp.p75INP}ms (${perf.inp.rating})`)
console.log(`Page weight: ${(perf.totals.pageWeight / 1024 / 1024).toFixed(2)} MB`)

if (perf.cls.rating !== 'good') {
  console.log('Layout shift sources:', perf.cls.shifts)
}
```

---

## auditDOMComplexity

Audit DOM structure for performance issues — node count, depth, and oversized parents.

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
  thresholds: {
    nodes: { value: 2500, limit: 1500, exceeded: true },
    depth: { value: 45, limit: 32, exceeded: true },
    children: { value: 120, limit: 60, exceeded: true }
  },
  scores: { nodes: 40, depth: 20, children: 20, overall: 27 },
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

  // Find and highlight the problematic element
  if (dom.heavyParents.length > 0) {
    window.__devtool.highlight(dom.heavyParents[0].selector)
  }
}
```

---

## auditPageQuality

Run all quality checks and generate a comprehensive runtime-quality report with per-category scoring. Where `auditPerformance` focuses purely on performance signals, `auditPageQuality` folds in DOM complexity, event listeners, text fragility, and responsive risk.

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
    performance: { /* auditPerformance results */ },
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

## auditAll

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
  const perf = audit.details.performance

  return {
    lcp: perf.lcp?.value,
    inp: perf.inp?.p75INP,
    tbt: perf.tbt?.totalBlockingTime,
    cls: perf.cls?.score,
    domNodes: audit.details.dom.totalNodes,
    grade: audit.grade,
    score: audit.overallScore
  }
}

// Capture baseline
const baseline = await performanceBaseline()
console.log('Baseline:', baseline)

// After changes, compare
const current = await performanceBaseline()
console.log('Score change:', current.score - baseline.score)
```

### Full Suite Grade

```javascript
async function fullGrade() {
  const all = await window.__devtool.auditAll()
  console.log(`Overall grade: ${all.grade} (${all.overallScore}/100)`)
  return all
}
```

---

## auditAnimations

Compositor-load audit: the performance class JS profilers cannot see. An infinite CSS animation never touches the DOM and never runs script, yet it forces the compositor to commit a frame at the display refresh rate forever — pegging the browser's GPU process from a visually static page, and draining batteries on mobile.

```javascript
window.__devtool.audit.auditAnimations()
window.__devtool.audit.auditAnimations({sampleMs: 2000})   // adds a bounded rAF idle sample (async)
window.__devtool.audit.auditAnimations({raw: true})        // full findings instead of grouped
```

Data source is `document.getAnimations()` — the declarative registry that reports an animation exists and is running regardless of whether anything observable changes, which is why this works where MutationObserver and point-in-time visual checks are blind.

**Finding types:**

| Type | Severity | Meaning |
|------|----------|---------|
| `infinite-animation` | warning | Infinite-iteration animation on a visible element — the frame pump; the page can never go idle |
| `layout-property-animation` | error | Animation touches a layout property (`width`, `top`, `margin`, ...) — main-thread style + layout every frame |
| `viewport-overlay-amplifier` | info → error | Full-viewport fixed textured overlay (noise/grain layer) repainted on every commit |
| `backdrop-filter-amplifier` | info → error | Large-area (≥25% of viewport) `backdrop-filter`/`filter` — a per-commit blur pass |
| `will-change-overuse` | warning | Layer-promotion hints scattered across many elements |

Amplifiers escalate to `error` only while a frame pump is live; without one, an overlay costs a single paint and reports as `info`. Small-area filters (a blurred composer bar) are deliberately not flagged.

**Frame sampling:** `{sampleMs: N}` resolves with `frameSample: {frames, sampleMs, effectiveFps}`. `effectiveFps` at the display refresh rate on a visually static page convicts the pump; ≤5 fps means the page idles. The probe is time-bounded: in a backgrounded or occluded tab where the browser throttles rAF, it resolves with `rafStarved: true` and the summary reports the sample inconclusive rather than hanging. Treat the sample as one signal rather than an oracle — a purely compositor-driven animation can commit without firing page rAF, and the GPU-process CPU number itself lives outside every page API.

Without `document.getAnimations()` support the audit returns `notApplicable` rather than an unmeasured passing grade.

`auditAnimations` is **not** folded into `auditAll` / `auditPageQuality`: its headline signal depends on the display it runs on (refresh rate, DPI), so averaging it into a portable page grade would make that grade machine-dependent. Run it directly — the full investigation workflow is in the [GPU & Compositor Debugging guide](/guides/gpu-compositor-debugging-ai).

## Browser Compatibility

| Function | Chrome | Firefox | Safari | Edge |
|----------|--------|---------|--------|------|
| `auditPerformance` | Yes | Partial | Partial | Yes |
| `auditDOMComplexity` | Yes | Yes | Yes | Yes |
| `auditPageQuality` | Yes | Partial | Partial | Yes |
| `auditAll` | Yes | Partial | Partial | Yes |
| `auditAnimations` | Yes | Yes | Yes | Yes |

Memory and INP/LoAF signals within `auditPerformance` are Chromium-only and degrade cleanly on other browsers.

---

## See Also

- [Layout Robustness](/api/frontend/layout-robustness) - Text fragility and responsive risk detection
- [Performance Monitoring](/use-cases/performance-monitoring) - Performance use cases
- [Core Web Vitals](https://web.dev/vitals/) - Google's Web Vitals documentation
