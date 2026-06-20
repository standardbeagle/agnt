---
sidebar_position: 13
---

# CSS Evaluation & Architecture

One call returns a structured CSS report — no manual stylesheet parsing or specificity math. `auditCSS()` analyzes architecture, specificity, containment, responsive strategy, design-system consistency, content areas, and Tailwind usage, then rolls them into a single graded result.

## auditCSS

Comprehensive CSS audit. Returns structured findings across every CSS-quality dimension.

```javascript
window.__devtool.auditCSS(options?)
```

**Parameters:**
- `options.includeTailwind` (boolean): Include Tailwind audit (default: true)

**What it covers:**
| Section | Findings |
|---------|----------|
| `architecture` | specificity distribution, ID selectors, deep nesting, fragile patterns, `!important` overuse, naming conventions |
| `containment` | `contain` / `content-visibility` / container-query usage, optimization candidates |
| `responsive` | media-queries vs container-queries strategy, breakpoint inventory |
| `consistency` | unique colors, font sizes, font families, spacing, border radii (design-system drift) |
| `contentAreas` | classifies areas as CMS content, application components, or layout frames |
| `tailwind` | detection, utility/arbitrary usage, breakpoint patterns, best practices (when detected) |

**Returns:**
```javascript
{
  architecture: {
    stats: {
      totalSelectors: 450,
      bySpecificity: { low: 280, medium: 120, high: 45, extreme: 5 },
      idSelectorCount: 8,
      deepNestingCount: 3,
      fragilePatternCount: 12,
      importantCount: 15,
      uniqueClasses: 234
    },
    namingConvention: { dominant: "kebabCase", consistency: 0.67 },
    healthScore: 72,
    rating: "needs-improvement"
  },
  containment: {
    containment: { ratio: "2.50%" },
    candidates: [
      { selector: ".comments-section", reason: "Many children (45)", suggestedContain: "contain: content" },
      { selector: ".footer-section", reason: "Below fold", suggestedContain: "content-visibility: auto" }
    ],
    recommendations: [
      "Consider adding contain: content to card/panel components",
      "Use content-visibility: auto for below-fold sections"
    ]
  },
  responsive: {
    strategy: "hybrid",  // "media-queries-only" | "hybrid" | "container-queries-primary"
    mediaQueries: { count: 45, uniqueBreakpoints: 8 },
    containerQueries: { count: 12 }
  },
  consistency: {
    colors: { uniqueCount: 34, isConsistent: false, topValue: "rgb(0, 0, 0)" },
    fontSizes: { uniqueCount: 12, isConsistent: true, topValue: "16px" },
    fontFamilies: { uniqueCount: 2, isConsistent: true, topValue: "Inter" },
    spacing: { uniqueCount: 18, isConsistent: false },
    consistencyScore: 68,
    rating: "moderate"
  },
  contentAreas: {
    byCategory: {
      cms: [...],    // CMS content areas (prose, articles, editors)
      app: [...],    // Application components (nav, sidebar, forms)
      layout: [...]  // Layout frames (header, footer, containers)
    },
    summary: { total: 15, cms: 3, app: 8, layout: 4 }
  },
  tailwind: {  // present only when Tailwind detected
    detected: true,
    config: { darkMode: "class" },
    usage: {
      totalClasses: 234,
      utilityClasses: 189,
      responsiveClasses: 45,
      customClasses: 45,
      arbitraryValues: 12
    },
    healthScore: 85,
    rating: "good"
  },
  summary: {
    totalSelectors: 450,
    uniqueClasses: 234,
    namingConvention: "utility",
    responsiveStrategy: "hybrid",
    uniqueColors: 34,
    uniqueFontSizes: 12,
    usingTailwind: true,
    tailwindUtilities: 189,
    tailwindArbitrary: 12
  },
  issues: [
    // All issues from all sections combined
    {
      type: "important-overuse",
      severity: "error",
      message: "15 !important declarations found",
      fix: "Refactor CSS to avoid !important; use @layer for cascade control"
    },
    {
      type: "color-inconsistency",
      severity: "warning",
      message: "34 unique colors - consider a design system",
      fix: "Define a color palette and use CSS custom properties"
    }
  ],
  overallScore: 78,
  grade: "C",  // A (90+), B (80-89), C (70-79), D (60-69), F (<60)
  timestamp: 1699999999999
}
```

**Issue Types (across all sections):**
| Type | Severity | Description |
|------|----------|-------------|
| `excessive-ids` | warning | Many ID selectors (>5) causing specificity issues |
| `deep-nesting` | warning | Selectors with >4 levels of nesting |
| `important-overuse` | error | Too many `!important` declarations (>10) |
| `specificity-wars` | warning | >10% of selectors have extreme specificity |
| `missing-containment` | info | Elements that could benefit from CSS containment |
| `too-many-breakpoints` | info | Using many distinct breakpoints |
| `color-inconsistency` | warning | Many unique colors - consider a design system |
| `excessive-arbitrary-values` | warning | >20% of Tailwind classes use arbitrary syntax `[...]` |
| `long-class-strings` | info | Elements with >15 Tailwind utility classes |

**Fragile patterns detected** (reported in `architecture.fragilePatterns`):
- `universal-descendant`: `* ` — Universal selector with descendant
- `universal-child`: `> *` — Universal child selector
- `positional`: `:nth-child(n)` — Fragile to DOM changes
- `partial-class`: `[class*=]` — Partial class matching
- `bare-element`: `div` — Affects all instances globally

**Content area classification** (in `contentAreas`): CMS areas (`prose`, `article`, editors) need flexible styling and should avoid strict containment, while app components (`nav`, `sidebar`, `modal`, `form`) benefit from containment for performance.

**Example:**
```javascript
// Full CSS audit
const audit = window.__devtool.auditCSS()
console.log(`CSS Grade: ${audit.grade} (${audit.overallScore}/100)`)
console.log(`Strategy: ${audit.summary.responsiveStrategy}`)
console.log(`Using Tailwind: ${audit.summary.usingTailwind}`)

audit.issues.forEach(issue => {
  console.log(`[${issue.severity}] ${issue.type}: ${issue.message}`)
})

// Skip Tailwind audit
const basicAudit = window.__devtool.auditCSS({ includeTailwind: false })
```

---

## Common Patterns

### Pre-Deploy CSS Check

```javascript
function checkCSSQuality() {
  const audit = window.__devtool.auditCSS()
  const issues = []

  // Architecture issues
  if (audit.architecture.stats.importantCount > 20) {
    issues.push(`Too many !important (${audit.architecture.stats.importantCount})`)
  }

  // Consistency issues
  if (audit.consistency.colors.uniqueCount > 50) {
    issues.push(`Too many colors (${audit.consistency.colors.uniqueCount})`)
  }

  // Tailwind issues
  if (audit.tailwind && audit.tailwind.detected) {
    if (audit.tailwind.usage.arbitraryValues > audit.tailwind.usage.totalClasses * 0.3) {
      issues.push('Excessive Tailwind arbitrary values')
    }
  }

  return {
    pass: audit.grade !== 'F' && audit.grade !== 'D' && issues.length === 0,
    grade: audit.grade,
    score: audit.overallScore,
    issues: issues
  }
}
```

### Design System Audit

```javascript
function auditDesignSystem() {
  const { consistency } = window.__devtool.auditCSS()

  console.log('Design System Audit:')
  console.log(`  Consistency Score: ${consistency.consistencyScore}/100 (${consistency.rating})`)
  console.log(`  Colors: ${consistency.colors.uniqueCount} unique`)
  console.log(`  Font sizes: ${consistency.fontSizes.uniqueCount} unique`)
  console.log(`  Spacing values: ${consistency.spacing.uniqueCount} unique`)

  if (consistency.recommendations) {
    consistency.recommendations.forEach(r => console.log(`  - ${r}`))
  }
}
```

### CMS vs App Styling Strategy

```javascript
function analyzeContentStrategy() {
  const { contentAreas } = window.__devtool.auditCSS()

  console.log('Content Area Analysis:')
  console.log(`  CMS areas: ${contentAreas.summary.cms}`)
  console.log(`  App components: ${contentAreas.summary.app}`)
  console.log(`  Layout frames: ${contentAreas.summary.layout}`)

  // CMS areas with containment (potential issue - CMS content needs flexible styling)
  const cmsWithContainment = contentAreas.byCategory.cms.filter(a =>
    a.containment.contain !== 'none' && a.containment.contain !== 'style'
  )
  cmsWithContainment.forEach(a => {
    console.log(`  Warning: ${a.selector} has contain: ${a.containment.contain}`)
  })
}
```

---

## Performance Notes

- `auditCSS` runs all sections in one pass; the architecture analysis walks all stylesheets and may be slow with many external sheets.
- Consistency analysis samples 500 elements for performance.
- Cross-origin stylesheets throw security errors and are skipped.

## See Also

- [Layout Robustness](/api/frontend/layout-robustness) - Text and responsive fragility
- [Quality Auditing](/api/frontend/quality-auditing) - Performance and quality metrics
- [Accessibility](/api/frontend/accessibility) - A11y auditing
