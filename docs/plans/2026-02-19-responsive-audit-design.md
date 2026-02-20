# Responsive Audit Feature Design

**Date:** 2026-02-19
**Status:** Design Complete, Ready for Implementation
**Estimated Effort:** ~3 days

## Overview

A responsive audit tool that detects layout issues, content overflows, and viewport-specific accessibility problems across multiple viewport sizes. Uses hidden iframes loading through the existing proxy infrastructure to get true responsive behavior at each size.

## Problem Statement

When developing responsive web applications, issues often appear only at specific viewport sizes:

- Content hidden by `overflow:hidden` or container constraints
- Margins/padding squeezing content to unusable widths
- Elements forcing horizontal scroll on mobile
- Touch targets too small on mobile devices
- Font sizes triggering iOS zoom behavior

Current tools require manual resizing or visual inspection of long pages (5+ screen heights on desktop, even more on mobile), making comprehensive responsive testing impractical.

## Solution

Leverage the existing proxy infrastructure to load the current page in hidden iframes at target viewport widths. Each iframe renders the page at its natural size, causing media queries to evaluate correctly. Run automated analysis in each iframe, then aggregate results into a structured report.

### Key Insight

Since all pages load through the proxy, we can create iframes pointing at the same proxy URL. The iframe's width determines which media queries match, giving us accurate responsive rendering without cloning DOM or snapshot complexity.

```
┌─────────────────────────────────────────────────────────────┐
│  Main Page (via proxy)                                      │
│                                                             │
│  ┌─────────────────────────────────────────────────────────┐│
│  │  Hidden iframe pool (sequential loading)                ││
│  │                                                         ││
│  │  [375px iframe] → analyze → cleanup                     ││
│  │  [768px iframe] → analyze → cleanup                     ││
│  │  [1440px iframe] → analyze → cleanup                    ││
│  │                                                         ││
│  └─────────────────────────────────────────────────────────┘│
│                                                             │
│  Return aggregated results to MCP tool                      │
└─────────────────────────────────────────────────────────────┘
```

## Architecture

### Components

1. **responsive.js** - New browser module for iframe orchestration and detection
2. **responsive_audit.go** - New MCP tool that triggers the audit
3. **Integration** - Hooks into existing `__devtool` API

### Data Flow

```
MCP Tool Request
       ↓
Create iframe at viewport width
       ↓
Wait for page load (via proxy)
       ↓
Run analysis in iframe context:
  - detectLayoutIssues()
  - detectOverflowIssues()
  - auditAccessibility() (existing)
  - checkResponsiveRisk() (existing)
       ↓
Collect results, cleanup iframe
       ↓
Repeat for next viewport
       ↓
Aggregate and format results
       ↓
Return to MCP tool
```

## API Design

### MCP Tool: `responsive_audit`

**Input Schema:**
```typescript
{
  viewports?: Viewport[],  // Default: mobile/tablet/desktop presets
  checks?: ('layout' | 'overflow' | 'a11y')[],  // Default: all
  raw?: boolean  // Default: false (compact text), true = JSON
}

interface Viewport {
  name: string;
  width: number;
  height: number;
}
```

**Default Viewports:**
```javascript
const DEFAULT_VIEWPORTS = [
  { name: 'mobile', width: 375, height: 667 },
  { name: 'tablet', width: 768, height: 1024 },
  { name: 'desktop', width: 1440, height: 900 }
];
```

### Browser API: `window.__devtool.responsive`

```javascript
window.__devtool.responsive = {
  // Run full audit across viewports
  audit: async (options) => ResponsiveAuditResult,

  // Individual detection functions (run in iframe context)
  detectLayoutIssues: (viewportWidth) => Issue[],
  detectOverflowIssues: (viewportWidth) => Issue[]
};
```

## Detection Algorithms

### 1. Layout Issues

Detects structural problems that affect content visibility and usability.

| Check | Severity | Trigger |
|-------|----------|---------|
| Collapsed content | critical | Element has text but height < 1px |
| Fixed header coverage | warning/info | Fixed element covers > 25% of viewport (mobile) |
| Margin/padding squeeze | warning | Margins+padding > 30% of content width, content < 200px |

```javascript
function detectLayoutIssues(viewportWidth) {
  const issues = [];
  const isMobile = viewportWidth < 768;

  for (const el of document.querySelectorAll('*')) {
    if (!isVisible(el)) continue;

    const rect = el.getBoundingClientRect();
    const style = getComputedStyle(el);

    // Collapsed content
    if (rect.height < 1 && el.textContent.trim().length > 0) {
      issues.push({
        type: 'layout', severity: 'critical',
        selector: getSelector(el),
        message: 'collapsed content, element has text but zero height'
      });
    }

    // Fixed elements covering viewport (mobile only)
    if (isMobile && style.position === 'fixed') {
      const coverage = rect.height / window.innerHeight;
      if (coverage > 0.25) {
        issues.push({
          type: 'layout',
          severity: coverage > 0.4 ? 'warning' : 'info',
          selector: getSelector(el),
          message: `fixed element covers ${Math.round(coverage*100)}% of viewport`
        });
      }
    }

    // Margin/padding squeeze
    const contentWidth = rect.width;
    const containerWidth = el.parentElement?.getBoundingClientRect().width || viewportWidth;
    const totalSqueeze =
      parseFloat(style.paddingLeft) + parseFloat(style.paddingRight) +
      parseFloat(style.marginLeft) + parseFloat(style.marginRight);

    if (totalSqueeze > contentWidth * 0.3 && contentWidth < 200) {
      issues.push({
        type: 'layout', severity: 'warning',
        selector: getSelector(el),
        message: `margins/padding squeeze content to ${Math.round(contentWidth)}px`
      });
    }
  }

  return issues;
}
```

### 2. Overflow Issues

Detects content that extends beyond its container or the viewport.

| Check | Severity | Trigger |
|-------|----------|---------|
| Horizontal scroll | critical | Element right edge > viewport width + 5px |
| Content clipped | warning | `overflow:hidden` container with scrollWidth > clientWidth |
| Truncated text | info | Text-overflow ellipsis without title/aria-label |
| Image squeezed | warning | Image displayed < 10px but has natural size > 1000px² |

```javascript
function detectOverflowIssues(viewportWidth) {
  const issues = [];

  for (const el of document.querySelectorAll('*')) {
    if (!isVisible(el)) continue;

    const rect = el.getBoundingClientRect();
    const style = getComputedStyle(el);

    // Horizontal overflow
    if (rect.right > viewportWidth + 5) {
      if (style.overflowX !== 'auto' && style.overflowX !== 'scroll') {
        issues.push({
          type: 'overflow', severity: 'critical',
          selector: getSelector(el),
          message: `forces horizontal scroll +${Math.round(rect.right - viewportWidth)}px`
        });
      }
    }

    // Content clipped by overflow:hidden
    if (style.overflow === 'hidden' || style.overflowX === 'hidden') {
      if (el.scrollWidth > el.clientWidth + 10) {
        issues.push({
          type: 'overflow', severity: 'warning',
          selector: getSelector(el),
          message: 'content clipped by overflow:hidden'
        });
      }
    }

    // Truncated text without tooltip
    if (style.textOverflow === 'ellipsis' || style.textOverflow === 'clip') {
      if (el.scrollWidth > el.clientWidth && !el.title && !el.getAttribute('aria-label')) {
        issues.push({
          type: 'overflow', severity: 'info',
          selector: getSelector(el),
          message: 'truncated text without title/tooltip'
        });
      }
    }

    // Images hidden/squeezed by container
    if (el.tagName === 'IMG' && (rect.width < 10 || rect.height < 10)) {
      if (el.naturalWidth * el.naturalHeight > 1000) {
        issues.push({
          type: 'overflow', severity: 'warning',
          selector: getSelector(el),
          message: 'image squeezed or hidden by container'
        });
      }
    }
  }

  return issues;
}
```

### 3. Accessibility (Viewport-Specific)

Reuses existing `auditAccessibility()` but filters for viewport-relevant issues.

| Check | Severity | Viewport | Trigger |
|-------|----------|----------|---------|
| Touch target size | warning | < 768px | Interactive element < 44x44px |
| iOS zoom trigger | warning | < 768px | Input font-size < 16px |
| Readability | info | < 768px | Text font-size < 12px |

```javascript
function filterViewportRelevant(a11yResults, viewportWidth) {
  const isMobile = viewportWidth < 768;

  // On mobile, promote touch target and font size issues
  if (isMobile) {
    return a11yResults.fixable.filter(issue => {
      // Touch targets
      if (issue.type === 'touch-target' || issue.type.includes('target')) {
        return true;
      }
      // Font size issues
      if (issue.message.includes('font-size') || issue.message.includes('zoom')) {
        return true;
      }
      return issue.severity === 'critical';
    });
  }

  // Desktop: filter out mobile-only issues
  return a11yResults.fixable.filter(issue => {
    return !issue.type.includes('touch') && !issue.type.includes('zoom');
  });
}
```

## Output Format

### Compact Text (Default)

Optimized for AI consumption, not human visual scanning:

```
=== Responsive Audit: 3 viewports ===

MOBILE (375px) - 5 issues
  ⚠ [layout] .hero-content - margin collapse, content squeezed to 280px
  ⚠ [overflow] body - horizontal scroll +45px from .banner-image
  ⚠ [a11y] input.email - font-size 14px triggers iOS zoom (min 16px)
  ○ [layout] .nav-fixed - covers 18% of viewport (acceptable)
  ○ [overflow] .truncate-text - ellipsis without title

TABLET (768px) - 2 issues
  ⚠ [overflow] .sidebar - content clipped, scrollHeight exceeds clientHeight
  ○ [layout] .card-grid - 3 columns at 240px each, tight margins

DESKTOP (1440px) - 0 issues

SUMMARY: 7 issues (3 critical, 4 minor)
PATTERNS: 2 mobile-only, 1 tablet-only, 0 cross-viewport
```

**Legend:**
- `⚠` = warning (degrades experience)
- `○` = info (worth knowing)
- `[layout]`, `[overflow]`, `[a11y]` = issue category

### JSON (when `raw: true`)

```javascript
{
  viewports: {
    mobile: {
      width: 375,
      issues: [
        {
          type: 'layout',
          severity: 'warning',
          selector: '.hero-content',
          message: 'margin collapse, content squeezed to 280px'
        }
      ]
    },
    tablet: {
      width: 768,
      issues: [...]
    },
    desktop: {
      width: 1440,
      issues: []
    }
  },
  summary: {
    total: 7,
    critical: 3,
    minor: 4
  },
  patterns: {
    mobileOnly: 2,
    tabletOnly: 1,
    crossViewport: 0
  }
}
```

## Implementation Phases

### Phase 1: Core Infrastructure (~1 day)

**Files:**
- `internal/proxy/scripts/responsive.js` (new)
- `internal/proxy/scripts/embed.go` (modify - add to bundle)
- `internal/tools/responsive_audit.go` (new)

**Tasks:**
1. Create `responsive.js` module with iframe pool management
2. Implement sequential viewport loading:
   - Create hidden iframe
   - Set width/height
   - Point at same proxy URL
   - Wait for load event
   - Run analysis
   - Cleanup iframe
3. Create basic MCP tool that triggers audit
4. Add module reference to `api.js`

### Phase 2: Detection Logic (~1 day)

**Files:**
- `internal/proxy/scripts/responsive.js` (continue)

**Tasks:**
1. Implement `detectLayoutIssues(viewportWidth)`
   - Collapsed content detection
   - Fixed element coverage (mobile)
   - Margin/padding squeeze
2. Implement `detectOverflowIssues(viewportWidth)`
   - Horizontal scroll detection
   - Clipped content detection
   - Truncated text detection
   - Image squeeze detection
3. Implement `filterViewportRelevant(a11yResults, viewportWidth)`
4. Integration with existing `auditAccessibility()` and `checkResponsiveRisk()`

### Phase 3: Aggregation & Output (~0.5 day)

**Files:**
- `internal/proxy/scripts/responsive.js` (continue)
- `internal/tools/responsive_audit.go` (continue)

**Tasks:**
1. Implement `aggregateResults(viewportResults)`
   - Cross-viewport pattern detection (mobileOnly, alwaysBroken, progressiveWorse)
2. Implement `formatCompact(results)` - text output
3. Implement `formatJSON(results)` - raw JSON output
4. Wire up `raw` parameter in MCP tool

### Phase 4: Testing & Polish (~0.5 day)

**Tasks:**
1. Test against real pages with known responsive issues
2. Tune severity thresholds based on real-world results
3. Handle edge cases:
   - Slow loading pages (timeout handling)
   - Errors in iframe (graceful degradation)
   - Multiple concurrent audits (debouncing)
4. Update CLAUDE.md documentation

## Technical Considerations

### Why Sequential Loading

Load viewports one at a time to avoid:
- Memory pressure from multiple full-page iframes
- Race conditions in analysis
- Overwhelming the dev server

With default 3 viewports, total audit time is ~3-6 seconds (1-2s per viewport load + analysis).

### Reusing Existing Infrastructure

The feature leverages existing components:

| Component | Usage |
|-----------|-------|
| Proxy injection | Analysis scripts already in each iframe |
| `auditAccessibility()` | Full axe-core integration |
| `checkResponsiveRisk()` | Static responsive analysis |
| `findOverflows()` | Existing overflow detection |
| `__devtool_utils` | Selector generation, visibility checks |

### Selector Stability

Uses existing `utils.generateSelector()` for stable, concise selectors that work across viewport variations.

## Future Enhancements

Not in initial scope, but potential additions:

1. **Custom viewport presets** - Allow users to define project-specific viewports
2. **Screenshot capture** - Capture screenshot at each viewport for visual diff
3. **Watch mode** - Re-run audit on file changes during development
4. **Historical tracking** - Store audit results, track regressions over time
5. **Performance metrics** - Add CLS/LCP checks at each viewport

## Success Criteria

1. Detects common responsive issues (horizontal scroll, content squeeze, touch targets)
2. Returns results in < 10 seconds for typical pages
3. Output is actionable for AI agents without human interpretation
4. No false positives on well-designed responsive pages
5. Works with existing proxy infrastructure without configuration changes
