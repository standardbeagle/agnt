---
sidebar_position: 13
---

# CSS Evaluation & Architecture

Functions for analyzing CSS architecture, specificity, containment, responsive strategies, consistency, and framework-specific patterns (including Tailwind CSS).

## detectContentAreas

Identify and classify content areas as CMS content, application components, or layout frames.

```javascript
window.__devtool.detectContentAreas()
```

**Returns:**
```javascript
{
  areas: [
    {
      selector: ".prose",
      type: "cms-prose",
      category: "cms",
      dimensions: { width: 800, height: 1200 },
      containment: {
        contain: "none",
        contentVisibility: "visible",
        containerType: "normal"
      },
      hasOverflow: false,
      childCount: 45
    },
    {
      selector: "nav.navbar",
      type: "app-navigation",
      category: "app",
      dimensions: { width: 1200, height: 64 },
      containment: { contain: "layout", ... },
      hasOverflow: false,
      childCount: 12
    }
  ],
  byCategory: {
    cms: [...],    // CMS content areas (prose, articles, editors)
    app: [...],    // Application components (nav, sidebar, forms)
    layout: [...]  // Layout frames (header, footer, containers)
  },
  summary: {
    total: 15,
    cms: 3,
    app: 8,
    layout: 4
  },
  recommendations: [
    {
      area: ".sidebar",
      type: "app-containment",
      message: "App component with many children lacks containment",
      fix: "Add contain: layout or contain: content for performance"
    }
  ],
  timestamp: 1699999999999
}
```

**Content Area Types:**
| Category | Type | Description |
|----------|------|-------------|
| cms | `cms-editable` | `[contenteditable]` elements |
| cms | `cms-wordpress` | WordPress content areas |
| cms | `cms-prose` | `.prose`, `.markdown-body`, `.rich-text` |
| cms | `cms-article` | `article`, `.article-body`, `.blog-post` |
| cms | `cms-editor` | CKEditor, Trix, ProseMirror |
| app | `app-navigation` | `nav`, `.navbar`, header nav |
| app | `app-sidebar` | `.sidebar`, `aside` |
| app | `app-toolbar` | `.toolbar`, `[role="toolbar"]` |
| app | `app-modal` | `.modal`, `[role="dialog"]` |
| app | `app-form` | `form`, `.form-group`, `fieldset` |
| app | `app-component` | `.card`, `.panel`, `.widget` |
| layout | `layout-header` | `header`, `[role="banner"]` |
| layout | `layout-footer` | `footer`, `[role="contentinfo"]` |
| layout | `layout-main` | `main`, `[role="main"]` |
| layout | `layout-container` | `.container`, `.wrapper` |
| layout | `layout-grid` | `.grid`, `.row`, `.columns` |

**Key Insight:** CMS areas need flexible styling (avoid strict containment), while app components benefit from containment for performance.

---

## auditCSSArchitecture

Analyze CSS architecture including specificity distribution, selector patterns, and naming conventions.

```javascript
window.__devtool.auditCSSArchitecture()
```

**Returns:**
```javascript
{
  stats: {
    totalSelectors: 450,
    bySpecificity: {
      low: 280,      // Specificity score <= 10
      medium: 120,   // Score 11-30
      high: 45,      // Score 31-100
      extreme: 5     // Score > 100
    },
    idSelectorCount: 8,
    deepNestingCount: 3,
    fragilePatternCount: 12,
    importantCount: 15,
    uniqueClasses: 234
  },
  idSelectors: [
    {
      selector: "#main-nav .item",
      specificity: [0, 1, 1, 0],
      source: "styles.css"
    }
  ],
  deepNesting: [
    {
      selector: "body .wrapper .content .article .text p",
      depth: 6,
      source: "inline"
    }
  ],
  fragilePatterns: [
    {
      selector: "div > *",
      pattern: "universal-child",
      message: "Universal child selector is fragile",
      source: "styles.css"
    }
  ],
  namingConvention: {
    patterns: {
      bem: 45,
      camelCase: 12,
      kebabCase: 156,
      utility: 89,
      other: 32
    },
    dominant: "kebabCase",
    consistency: 0.67
  },
  issues: [
    {
      type: "excessive-ids",
      severity: "warning",
      message: "8 selectors use IDs - high specificity, hard to override",
      fix: "Replace ID selectors with classes for better reusability"
    },
    {
      type: "important-overuse",
      severity: "error",
      message: "15 !important declarations found",
      fix: "Refactor CSS to avoid !important; use @layer for cascade control"
    }
  ],
  healthScore: 72,
  rating: "needs-improvement",
  timestamp: 1699999999999
}
```

**Issue Types:**
| Type | Severity | Description |
|------|----------|-------------|
| `excessive-ids` | warning | Many ID selectors (>5) causing specificity issues |
| `deep-nesting` | warning | Selectors with >4 levels of nesting |
| `important-overuse` | error | Too many `!important` declarations (>10) |
| `specificity-wars` | warning | >10% of selectors have extreme specificity |

**Fragile Patterns Detected:**
- `universal-descendant`: `* ` - Universal selector with descendant
- `universal-child`: `> *` - Universal child selector
- `positional`: `:nth-child(n)` - Fragile to DOM changes
- `partial-class`: `[class*=]` - Partial class matching
- `partial-id`: `[id*=]` - Partial ID matching
- `root-id`: `body #id` - Unnecessary root qualification
- `bare-element`: `div` - Affects all instances globally

---

## auditCSSContainment

Analyze CSS containment usage and identify candidates for optimization.

```javascript
window.__devtool.auditCSSContainment()
```

**Returns:**
```javascript
{
  containment: {
    usage: {
      none: 450,
      layout: 12,
      paint: 8,
      size: 3,
      style: 5,
      content: 10,
      strict: 2
    },
    elements: [
      {
        selector: ".card-list",
        contain: "content",
        childCount: 24
      }
    ],
    ratio: "2.50%"
  },
  contentVisibility: {
    visible: 480,
    auto: 15,
    hidden: 5
  },
  containerQueries: {
    usage: {
      inlineSize: 8,
      size: 2,
      normal: 490
    },
    inUse: true
  },
  candidates: [
    {
      selector: ".comments-section",
      reason: "Many children (45)",
      suggestedContain: "contain: content"
    },
    {
      selector: ".footer-section",
      reason: "Below fold (consider content-visibility: auto)",
      suggestedContain: "content-visibility: auto"
    }
  ],
  issues: [
    {
      type: "missing-containment",
      severity: "info",
      message: "Many elements could benefit from CSS containment",
      fix: "Add contain: content or contain: layout to isolated components"
    }
  ],
  recommendations: [
    "Consider adding contain: content to card/panel components",
    "Use content-visibility: auto for below-fold sections",
    "Consider container queries for truly responsive components"
  ],
  timestamp: 1699999999999
}
```

**Containment Values:**
| Value | Effect |
|-------|--------|
| `layout` | Element layout is independent of siblings |
| `paint` | Element descendants don't display outside bounds |
| `size` | Element size is independent of children |
| `style` | Counter and quote styles are scoped |
| `content` | Shorthand for `layout paint style` |
| `strict` | Shorthand for `layout paint size style` |

---

## auditResponsiveStrategy

Analyze responsive CSS strategy: media queries vs container queries.

```javascript
window.__devtool.auditResponsiveStrategy()
```

**Returns:**
```javascript
{
  strategy: "hybrid",  // "media-queries-only" | "hybrid" | "container-queries-primary"
  mediaQueries: {
    count: 45,
    breakpoints: [
      { breakpoint: "768px", count: 18 },
      { breakpoint: "1024px", count: 15 },
      { breakpoint: "640px", count: 12 }
    ],
    uniqueBreakpoints: 8
  },
  containerQueries: {
    count: 12,
    containers: [
      {
        selector: ".card-container",
        containerType: "inline-size",
        containerName: "card"
      }
    ]
  },
  issues: [
    {
      type: "too-many-breakpoints",
      severity: "info",
      message: "Using 8 different breakpoints",
      fix: "Consolidate to 3-5 standard breakpoints for consistency"
    }
  ],
  recommendations: [
    {
      priority: 1,
      message: "Consider container queries for component-level responsiveness",
      benefit: "Components respond to their container, not viewport - more reusable"
    }
  ],
  suggestion: "Good use of modern CSS responsive features",
  timestamp: 1699999999999
}
```

**Common Breakpoints:**
```
320px  - Mobile portrait
375px  - iPhone
480px  - Mobile landscape
640px  - Small tablet (Tailwind sm)
768px  - Tablet (Tailwind md)
1024px - Laptop (Tailwind lg)
1280px - Desktop (Tailwind xl)
1536px - Large desktop (Tailwind 2xl)
```

---

## auditCSSConsistency

Analyze design system consistency: colors, fonts, spacing, and border radii.

```javascript
window.__devtool.auditCSSConsistency()
```

**Returns:**
```javascript
{
  colors: {
    values: [
      { value: "rgb(0, 0, 0)", count: 145 },
      { value: "rgb(255, 255, 255)", count: 98 },
      { value: "rgb(59, 130, 246)", count: 45 }
    ],
    uniqueCount: 34,
    isConsistent: false,
    topValue: "rgb(0, 0, 0)"
  },
  fontSizes: {
    values: [
      { value: "16px", count: 234 },
      { value: "14px", count: 89 },
      { value: "18px", count: 45 }
    ],
    uniqueCount: 12,
    isConsistent: true,
    topValue: "16px"
  },
  fontFamilies: {
    values: [
      { value: "Inter", count: 450 },
      { value: "monospace", count: 34 }
    ],
    uniqueCount: 2,
    isConsistent: true,
    topValue: "Inter"
  },
  spacing: {
    values: [
      { value: "16px", count: 156 },
      { value: "8px", count: 134 },
      { value: "24px", count: 89 }
    ],
    uniqueCount: 18,
    isConsistent: false
  },
  borderRadius: {
    values: [
      { value: "8px", count: 67 },
      { value: "4px", count: 45 }
    ],
    uniqueCount: 5,
    isConsistent: true
  },
  issues: [
    {
      type: "color-inconsistency",
      severity: "warning",
      message: "34 unique colors - consider a design system",
      fix: "Define a color palette and use CSS custom properties"
    }
  ],
  consistencyScore: 68,
  rating: "moderate",
  recommendations: [
    "Define CSS custom properties for colors (--color-primary, etc.)",
    "Adopt a spacing scale (multiples of 4px or 8px)"
  ],
  timestamp: 1699999999999
}
```

**Consistency Thresholds:**
| Metric | Consistent | Moderate | Inconsistent |
|--------|------------|----------|--------------|
| Colors | &lt;15 | 15-30 | &gt;30 |
| Font Sizes | &lt;10 | 10-15 | &gt;15 |
| Font Families | &lt;3 | 3-5 | &gt;5 |
| Spacing | &lt;12 | 12-20 | &gt;20 |

---

## auditTailwind

Tailwind CSS-specific analysis with detection, usage patterns, and best practices.

```javascript
window.__devtool.auditTailwind()
```

**Returns:**
```javascript
{
  detected: true,
  version: null,  // Cannot reliably detect version from DOM
  config: {
    darkMode: "class",  // "class" | "media" | null
    prefix: null,
    important: null
  },
  usage: {
    totalClasses: 234,
    utilityClasses: 189,
    responsiveClasses: 45,
    stateVariants: 28,
    customClasses: 45,
    arbitraryValues: 12
  },
  patterns: {
    breakpoints: {
      sm: 12,
      md: 45,
      lg: 34,
      xl: 18,
      "2xl": 8
    },
    colors: {
      blue: 45,
      gray: 89,
      red: 12
    },
    spacing: {
      "4": 67,
      "8": 45,
      "2": 34
    }
  },
  issues: [
    {
      type: "excessive-arbitrary-values",
      severity: "warning",
      message: "High usage of arbitrary values (12 / 234)",
      fix: "Extend Tailwind config with custom values instead of using arbitrary syntax",
      details: { count: 12, percentage: "5.1%" }
    },
    {
      type: "long-class-strings",
      severity: "info",
      message: "8 elements have more than 15 utility classes",
      fix: "Extract to components using @apply or create component abstractions"
    }
  ],
  recommendations: [
    {
      priority: 2,
      type: "extend-theme",
      message: "Many arbitrary values could be added to theme configuration",
      action: "Extract common arbitrary values to tailwind.config.js theme.extend"
    },
    {
      priority: 3,
      type: "mobile-first",
      message: "No sm: breakpoint usage - ensure mobile-first approach",
      action: "Base styles should be mobile, use sm:/md:/lg: for larger screens"
    }
  ],
  healthScore: 85,
  rating: "good",
  timestamp: 1699999999999
}
```

**Tailwind Issues Detected:**
| Type | Severity | Description |
|------|----------|-------------|
| `excessive-arbitrary-values` | warning | >20% of classes use arbitrary syntax `[...]` |
| `inconsistent-breakpoints` | info | Some breakpoints used much less than others |
| `no-responsive-styles` | warning | No responsive prefixes on 50+ utility classes |
| `mixed-methodology` | info | Mixing many custom classes with utilities |
| `long-class-strings` | info | Elements with >15 utility classes |
| `deprecated-utilities` | warning | Using Tailwind v2 utilities removed in v3 |

**Tailwind v3 Patterns Detected:**
- Arbitrary values: `bg-[#1da1f2]`, `w-[calc(100%-2rem)]`
- Arbitrary variants: `[&:nth-child(3)]:underline`
- Container queries: `@lg:grid-cols-3`
- Has variants: `has-[:checked]:ring-2`

---

## auditCSS

Comprehensive CSS audit combining all analysis functions.

```javascript
window.__devtool.auditCSS(options?)
```

**Parameters:**
- `options.includeTailwind` (boolean): Include Tailwind audit (default: true)

**Returns:**
```javascript
{
  architecture: { /* auditCSSArchitecture results */ },
  containment: { /* auditCSSContainment results */ },
  responsive: { /* auditResponsiveStrategy results */ },
  consistency: { /* auditCSSConsistency results */ },
  contentAreas: { /* detectContentAreas results */ },
  tailwind: { /* auditTailwind results - if detected */ },
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
    // All issues from all audits combined
  ],
  overallScore: 78,
  grade: "C",  // A (90+), B (80-89), C (70-79), D (60-69), F (<60)
  timestamp: 1699999999999
}
```

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

### Tailwind Migration Check

```javascript
function assessTailwindUsage() {
  const tw = window.__devtool.auditTailwind()

  if (!tw.detected) {
    console.log('Tailwind not detected')
    return
  }

  console.log('Tailwind Usage Summary:')
  console.log(`  Utility classes: ${tw.usage.utilityClasses}`)
  console.log(`  Custom classes: ${tw.usage.customClasses}`)
  console.log(`  Arbitrary values: ${tw.usage.arbitraryValues}`)

  const utilityRatio = tw.usage.utilityClasses / tw.usage.totalClasses
  console.log(`  Utility adoption: ${(utilityRatio * 100).toFixed(1)}%`)

  if (tw.recommendations.length > 0) {
    console.log('\nRecommendations:')
    tw.recommendations.forEach(r => {
      console.log(`  [P${r.priority}] ${r.message}`)
      console.log(`       Action: ${r.action}`)
    })
  }
}
```

### CMS vs App Styling Strategy

```javascript
function analyzeContentStrategy() {
  const areas = window.__devtool.detectContentAreas()

  console.log('Content Area Analysis:')
  console.log(`  CMS areas: ${areas.summary.cms}`)
  console.log(`  App components: ${areas.summary.app}`)
  console.log(`  Layout frames: ${areas.summary.layout}`)

  // CMS areas with containment (potential issue)
  const cmsWithContainment = areas.byCategory.cms.filter(a =>
    a.containment.contain !== 'none' && a.containment.contain !== 'style'
  )

  if (cmsWithContainment.length > 0) {
    console.log('\nWarning: CMS areas with layout containment:')
    cmsWithContainment.forEach(a => {
      console.log(`  ${a.selector}: contain: ${a.containment.contain}`)
    })
    console.log('  Fix: CMS content needs flexible styling - use contain: none or style')
  }

  // App components without containment (performance opportunity)
  const appNoContainment = areas.byCategory.app.filter(a =>
    a.containment.contain === 'none' && a.childCount > 10
  )

  if (appNoContainment.length > 0) {
    console.log('\nOpportunity: App components that could benefit from containment:')
    appNoContainment.forEach(a => {
      console.log(`  ${a.selector}: ${a.childCount} children`)
    })
  }
}
```

### Design System Audit

```javascript
function auditDesignSystem() {
  const consistency = window.__devtool.auditCSSConsistency()

  console.log('Design System Audit:')
  console.log(`  Consistency Score: ${consistency.consistencyScore}/100`)
  console.log(`  Rating: ${consistency.rating}`)

  console.log('\nMetrics:')
  console.log(`  Colors: ${consistency.colors.uniqueCount} unique`)
  console.log(`  Font sizes: ${consistency.fontSizes.uniqueCount} unique`)
  console.log(`  Font families: ${consistency.fontFamilies.uniqueCount} unique`)
  console.log(`  Spacing values: ${consistency.spacing.uniqueCount} unique`)

  if (consistency.recommendations.length > 0) {
    console.log('\nRecommendations:')
    consistency.recommendations.forEach(r => console.log(`  - ${r}`))
  }

  // Check if using CSS custom properties
  const rootStyles = getComputedStyle(document.documentElement)
  const hasColorVars = rootStyles.getPropertyValue('--color-primary') ||
                       rootStyles.getPropertyValue('--tw-ring-color')

  console.log(`\nUsing CSS custom properties: ${hasColorVars ? 'Yes' : 'No'}`)
}
```

---

## Performance Notes

- `auditCSSArchitecture` analyzes all stylesheets - may be slow with many external sheets
- `auditCSSConsistency` samples 500 elements for performance
- `auditTailwind` scans all class attributes - scales with DOM size
- `auditCSS` runs all audits - use individual functions for targeted analysis
- Cross-origin stylesheets throw security errors and are skipped

## See Also

- [Layout Robustness](/api/frontend/layout-robustness) - Text and responsive fragility
- [Quality Auditing](/api/frontend/quality-auditing) - Performance and quality metrics
- [Accessibility](/api/frontend/accessibility) - A11y auditing
