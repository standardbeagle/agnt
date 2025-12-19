---
description: "Comprehensive frontend analysis including DOM, CSS, layout, and structure"
allowed-tools: ["mcp__agnt__proxy", "mcp__agnt__proxylog"]
---

Run a comprehensive frontend analysis on the current page using agnt's diagnostic tools.

## Steps

1. Analyze DOM complexity:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.auditDOMComplexity()"}
   ```

2. Audit CSS for issues:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.auditCSS()"}
   ```

3. Find layout overflows:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.findOverflows()"}
   ```

4. Find stacking contexts (z-index layers):
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.findStackingContexts()"}
   ```

5. Capture full page state:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.captureDOM()"}
   ```

6. Capture network resources:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.captureNetwork()"}
   ```

7. Take a screenshot:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.screenshot('frontend-analysis')"}
   ```

## DOM Complexity Analysis

The `auditDOMComplexity()` function returns:

| Metric | Healthy Range | Concern |
|--------|---------------|---------|
| `totalElements` | < 1500 | > 3000 impacts performance |
| `maxDepth` | < 20 | > 32 causes issues |
| `duplicateIds` | 0 | Any duplicate breaks JS/CSS |
| `scripts` | < 20 | > 50 slows loading |
| `stylesheets` | < 10 | > 20 blocks rendering |
| `iframes` | < 5 | Each adds overhead |

## CSS Analysis

The `auditCSS()` function checks:

| Issue | Description | Impact |
|-------|-------------|--------|
| `excessive-inline-styles` | Many elements with `style=""` | Maintainability, caching |
| `excessive-important` | Many `!important` rules | Specificity wars |

Returns:
- `inlineStyleCount`: Elements with inline styles
- `importantCount`: Rules using !important
- `stylesheetCount`: Total stylesheets

## Layout Analysis

### Overflows
Elements with content exceeding their bounds:
- Type: `hidden` (content clipped) or `scrollable`
- Dimensions: scroll vs client width/height

### Stacking Contexts
Elements creating new z-index layers:
- Reasons: positioned, opacity, transform, filter

## Element Inspection Commands

```
// Inspect a specific element comprehensively
proxy {action: "exec", id: "dev", code: "__devtool.inspect('#my-element')"}

// Get computed styles for an element
proxy {action: "exec", id: "dev", code: "__devtool.getComputed('#my-element')"}

// Get box model (margin, border, padding)
proxy {action: "exec", id: "dev", code: "__devtool.getBox('#my-element')"}

// Get flexbox/grid layout info
proxy {action: "exec", id: "dev", code: "__devtool.getLayout('#my-element')"}

// Get stacking context info
proxy {action: "exec", id: "dev", code: "__devtool.getStacking('#my-element')"}

// Walk DOM tree from element
proxy {action: "exec", id: "dev", code: "__devtool.walkChildren('#container', 3)"}
```

## Frontend Health Indicators

### Good
- DOM elements < 1500
- No duplicate IDs
- Minimal inline styles
- Few !important declarations
- Logical z-index usage

### Needs Attention
- DOM elements 1500-3000
- Some inline styles (> 10)
- Several !important rules
- Deep nesting (> 20 levels)

### Critical
- DOM elements > 3000
- Duplicate IDs present
- Excessive inline styles (> 50)
- Many !important (> 10)
- Very deep nesting (> 32)
