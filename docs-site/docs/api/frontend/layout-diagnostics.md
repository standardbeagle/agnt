---
sidebar_position: 5
---

# Layout Diagnostics

Functions for finding layout issues across the page.

## findOverflows

Find all elements with horizontal or vertical overflow.

```javascript
window.__devtool.findOverflows()
```

**Parameters:** None

**Returns:**
```javascript
[
  {
    selector: ".sidebar",
    element: {tag: "aside", classes: ["sidebar"]},
    overflow: "horizontal",
    scrollWidth: 400,
    clientWidth: 250,
    excess: 150,
    overflowStyle: "hidden"
  },
  {
    selector: ".content-area",
    element: {tag: "main", classes: ["content-area"]},
    overflow: "vertical",
    scrollHeight: 2000,
    clientHeight: 800,
    excess: 1200,
    overflowStyle: "auto"
  },
  {
    selector: ".table-wrapper",
    element: {tag: "div", classes: ["table-wrapper"]},
    overflow: "both",
    scrollWidth: 1200,
    clientWidth: 600,
    scrollHeight: 500,
    clientHeight: 300,
    excess: {horizontal: 600, vertical: 200},
    overflowStyle: "scroll"
  }
]
```

**Example:**
```javascript
const overflows = window.__devtool.findOverflows()
console.log(`Found ${overflows.length} elements with overflow`)
overflows.forEach(o => {
  console.log(`${o.selector}: ${o.overflow} overflow, ${o.excess}px excess`)
})
```

## findStackingContexts

Find all stacking contexts on the page.

```javascript
window.__devtool.findStackingContexts()
```

**Parameters:** None

**Returns:**
```javascript
[
  {
    selector: "#modal",
    element: {tag: "div", id: "modal", classes: ["modal"]},
    zIndex: 1000,
    reason: "position: fixed with z-index",
    parentContext: "html"
  },
  {
    selector: ".dropdown",
    element: {tag: "div", classes: ["dropdown", "open"]},
    zIndex: 100,
    reason: "position: absolute with z-index",
    parentContext: "#header"
  },
  {
    selector: ".animated-card",
    element: {tag: "div", classes: ["animated-card"]},
    zIndex: "auto",
    reason: "transform",
    parentContext: "#main"
  },
  {
    selector: ".overlay",
    element: {tag: "div", classes: ["overlay"]},
    zIndex: 50,
    reason: "opacity less than 1 (0.8)",
    parentContext: "html"
  }
]
```

**Reasons for creating stacking context:**
- `"root element"`
- `"position: fixed/absolute/relative/sticky with z-index"`
- `"position: fixed/sticky"`
- `"opacity less than 1 (value)"`
- `"transform"`
- `"filter"`
- `"perspective"`
- `"clip-path"`
- `"mask"`
- `"isolation: isolate"`
- `"mix-blend-mode"`
- `"will-change: opacity/transform"`
- `"contain: layout/paint"`

**Example:**
```javascript
const contexts = window.__devtool.findStackingContexts()
contexts.sort((a, b) => (b.zIndex || 0) - (a.zIndex || 0))
console.log('Stacking order:', contexts.map(c => `${c.selector} (z-index: ${c.zIndex})`))
```

## findOffscreen

Find elements positioned outside the viewport.

```javascript
window.__devtool.findOffscreen()
```

**Parameters:** None

**Returns:**
```javascript
[
  {
    selector: ".hidden-menu",
    element: {tag: "nav", classes: ["hidden-menu"]},
    position: "left",
    distance: -300,
    bounds: {top: 0, right: -100, bottom: 500, left: -400}
  },
  {
    selector: ".slide-out-panel",
    element: {tag: "aside", classes: ["slide-out-panel"]},
    position: "right",
    distance: 50,
    bounds: {top: 0, right: 1370, bottom: 800, left: 1320}
  },
  {
    selector: ".footer-extra",
    element: {tag: "div", classes: ["footer-extra"]},
    position: "below",
    distance: 500,
    bounds: {top: 1500, right: 800, bottom: 1700, left: 0}
  }
]
```

**Positions:**
- `"above"` - Above the viewport
- `"below"` - Below the viewport
- `"left"` - To the left of viewport
- `"right"` - To the right of viewport

**Example:**
```javascript
const offscreen = window.__devtool.findOffscreen()
console.log('Hidden elements:')
offscreen.forEach(e => {
  console.log(`${e.selector} is ${Math.abs(e.distance)}px ${e.position}`)
})
```

## Common Patterns

### Debug Horizontal Scroll Issue

```javascript
// Find what's causing horizontal scroll
const overflows = window.__devtool.findOverflows()
const horizontalOverflows = overflows.filter(o =>
  o.overflow === 'horizontal' || o.overflow === 'both'
)

console.log('Horizontal overflow sources:')
horizontalOverflows.forEach(o => {
  console.log(`${o.selector}: content is ${o.scrollWidth}px in ${o.clientWidth}px container`)
  window.__devtool.highlight(o.selector, {color: 'rgba(255,0,0,0.3)'})
})
```

### Z-Index Debugging

```javascript
// Understand z-index stacking
const contexts = window.__devtool.findStackingContexts()

// Group by parent context
const tree = {}
contexts.forEach(c => {
  const parent = c.parentContext || 'root'
  tree[parent] = tree[parent] || []
  tree[parent].push(c)
})

// Print stacking tree
Object.entries(tree).forEach(([parent, children]) => {
  console.log(`\n${parent}:`)
  children.sort((a, b) => (b.zIndex || 0) - (a.zIndex || 0))
  children.forEach(c => console.log(`  ${c.selector} (z-index: ${c.zIndex}) - ${c.reason}`))
})
```

### Find Hidden Navigation

```javascript
// Find slide-out menus and hidden panels
const offscreen = window.__devtool.findOffscreen()
const leftHidden = offscreen.filter(e => e.position === 'left')
const rightHidden = offscreen.filter(e => e.position === 'right')

console.log('Left slide-out panels:', leftHidden.map(e => e.selector))
console.log('Right slide-out panels:', rightHidden.map(e => e.selector))
```

### Comprehensive Layout Audit

```javascript
function auditLayout() {
  const overflows = window.__devtool.findOverflows()
  const stackingContexts = window.__devtool.findStackingContexts()
  const offscreen = window.__devtool.findOffscreen()

  return {
    issues: {
      overflows: overflows.length,
      unexpectedStackingContexts: stackingContexts.filter(c =>
        c.reason !== 'root element' && c.zIndex === 'auto'
      ).length,
      offscreenElements: offscreen.length
    },
    details: {
      horizontalOverflows: overflows.filter(o => o.overflow === 'horizontal' || o.overflow === 'both'),
      highZIndex: stackingContexts.filter(c => c.zIndex > 100),
      hiddenLeft: offscreen.filter(e => e.position === 'left')
    }
  }
}

const audit = auditLayout()
console.log('Layout audit:', audit.issues)
```

### Fix Overflow Loop

```javascript
// Iteratively find and inspect overflow issues
function debugOverflowChain(selector) {
  const parents = window.__devtool.walkParents(selector)

  console.log('Checking overflow chain for', selector)

  parents.forEach(parent => {
    const overflow = window.__devtool.getOverflow(parent.selector || parent.tag)
    if (overflow.hasHorizontalOverflow || overflow.hasVerticalOverflow) {
      console.log(`  ${parent.tag}${parent.id ? '#' + parent.id : ''}: has overflow`)
      console.log(`    Content: ${overflow.scrollWidth}x${overflow.scrollHeight}`)
      console.log(`    Container: ${overflow.clientWidth}x${overflow.clientHeight}`)
    }
  })
}
```

## Performance Notes

- These functions scan significant portions of the DOM
- Results are computed fresh each call (not cached)
- Use sparingly on very large pages
- Consider limiting scope with CSS selectors when debugging specific areas

## See Also

- [Composite Functions](/api/frontend/composite) - diagnoseLayout combines these
- [Visual Overlays](/api/frontend/visual-overlays) - Highlight found issues
- [Element Inspection](/api/frontend/element-inspection) - Deep dive on specific elements
