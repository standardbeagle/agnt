---
sidebar_position: 2
---

# Element Inspection

Functions for getting element properties, positions, styles, and layout information.

## getElementInfo

Get basic element information.

```javascript
window.__devtool.getElementInfo(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  tag: "button",
  id: "submit-btn",
  classes: ["btn", "btn-primary", "large"],
  attributes: {
    type: "submit",
    disabled: "",
    "data-action": "save"
  },
  textContent: "Save Changes"
}
```

**Example:**
```javascript
window.__devtool.getElementInfo('#nav-menu')
→ {tag: "nav", id: "nav-menu", classes: ["main-nav"], ...}
```

## getPosition

Get element position and dimensions.

```javascript
window.__devtool.getPosition(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  rect: {
    top: 100,
    right: 400,
    bottom: 150,
    left: 200,
    width: 200,
    height: 50
  },
  viewport: {
    x: 200,
    y: 100
  },
  scroll: {
    top: 500,
    left: 0
  },
  offsetParent: "div.container"
}
```

**Example:**
```javascript
window.__devtool.getPosition('.modal')
→ {rect: {top: 200, left: 300, width: 600, height: 400}, ...}
```

## getComputed

Get computed CSS properties.

```javascript
window.__devtool.getComputed(selector, properties)
```

**Parameters:**
- `selector` (string): CSS selector
- `properties` (string[]): CSS property names to retrieve

**Returns:**
```javascript
{
  display: "flex",
  position: "relative",
  "z-index": "10",
  color: "rgb(255, 255, 255)"
}
```

**Example:**
```javascript
window.__devtool.getComputed('.card', ['display', 'position', 'box-shadow'])
→ {display: "block", position: "relative", "box-shadow": "0 2px 4px rgba(0,0,0,0.1)"}
```

## getBox

Get box model breakdown.

```javascript
window.__devtool.getBox(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  margin: {top: 16, right: 16, bottom: 16, left: 16},
  border: {top: 1, right: 1, bottom: 1, left: 1},
  padding: {top: 24, right: 24, bottom: 24, left: 24},
  content: {width: 300, height: 200}
}
```

**Example:**
```javascript
window.__devtool.getBox('.button')
→ {margin: {top: 0, ...}, padding: {top: 12, right: 24, ...}, ...}
```

## getLayout

Get layout properties.

```javascript
window.__devtool.getLayout(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  display: "grid",
  position: "relative",
  flexbox: null,
  grid: {
    columns: "repeat(3, 1fr)",
    rows: "auto auto",
    gap: "16px"
  },
  float: "none",
  clear: "none"
}
```

For flex containers:
```javascript
{
  display: "flex",
  flexbox: {
    direction: "row",
    wrap: "wrap",
    justifyContent: "space-between",
    alignItems: "center",
    gap: "8px"
  },
  grid: null
}
```

**Example:**
```javascript
window.__devtool.getLayout('.container')
→ {display: "flex", flexbox: {direction: "column", ...}, ...}
```

## getContainer

Get CSS containment information.

```javascript
window.__devtool.getContainer(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  contain: "layout paint",
  containerType: "inline-size",
  containerName: "sidebar"
}
```

**Example:**
```javascript
window.__devtool.getContainer('.responsive-card')
→ {contain: "none", containerType: "normal", containerName: "none"}
```

## getStacking

Get stacking context information.

```javascript
window.__devtool.getStacking(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  zIndex: 1000,
  createsContext: true,
  reason: "z-index with position: fixed",
  parentContext: "body",
  isolatesBlending: false
}
```

Reasons an element creates a stacking context:
- `"z-index with position: relative/absolute/fixed/sticky"`
- `"opacity less than 1"`
- `"transform"`
- `"filter"`
- `"perspective"`
- `"isolation: isolate"`
- `"will-change"`
- `"mix-blend-mode"`

**Example:**
```javascript
window.__devtool.getStacking('.dropdown-menu')
→ {zIndex: 100, createsContext: true, reason: "z-index with position: absolute"}
```

## getTransform

Get transform matrix decomposition.

```javascript
window.__devtool.getTransform(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  hasTransform: true,
  matrix: [0.866, 0.5, -0.5, 0.866, 100, 50],
  decomposed: {
    translate: {x: 100, y: 50},
    rotate: 30,
    scale: {x: 1, y: 1},
    skew: {x: 0, y: 0}
  },
  transformOrigin: "50% 50%"
}
```

For elements without transforms:
```javascript
{
  hasTransform: false,
  matrix: null,
  decomposed: null,
  transformOrigin: "50% 50%"
}
```

**Example:**
```javascript
window.__devtool.getTransform('.rotated-card')
→ {hasTransform: true, decomposed: {rotate: 15, scale: {x: 1.2, y: 1.2}}, ...}
```

## getOverflow

Get overflow state and scroll dimensions.

```javascript
window.__devtool.getOverflow(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  overflowX: "auto",
  overflowY: "scroll",
  scrollWidth: 1200,
  scrollHeight: 2000,
  clientWidth: 400,
  clientHeight: 600,
  scrollLeft: 0,
  scrollTop: 150,
  hasHorizontalOverflow: true,
  hasVerticalOverflow: true,
  canScrollX: true,
  canScrollY: true
}
```

**Example:**
```javascript
window.__devtool.getOverflow('.scrollable-content')
→ {hasHorizontalOverflow: false, hasVerticalOverflow: true, scrollHeight: 3000, ...}
```

## Common Patterns

### Check if Element is Positioned

```javascript
const layout = window.__devtool.getLayout('#element')
const isPositioned = layout.position !== 'static'
```

### Get Total Element Size Including Margin

```javascript
const box = window.__devtool.getBox('#element')
const totalWidth = box.content.width +
  box.padding.left + box.padding.right +
  box.border.left + box.border.right +
  box.margin.left + box.margin.right
```

### Check if Element Creates Stacking Context

```javascript
const stacking = window.__devtool.getStacking('.modal')
if (stacking.createsContext) {
  console.log('Creates stacking context because:', stacking.reason)
}
```

### Debug Overflow Issues

```javascript
const overflow = window.__devtool.getOverflow('.container')
if (overflow.hasHorizontalOverflow) {
  console.log(`Content is ${overflow.scrollWidth - overflow.clientWidth}px wider than container`)
}
```

## See Also

- [getElementInfo vs inspect](/api/frontend/composite#inspect) - When to use composite
- [Layout Diagnostics](/api/frontend/layout-diagnostics) - Find layout issues
