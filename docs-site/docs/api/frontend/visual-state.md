---
sidebar_position: 4
---

# Visual State

Functions for checking element visibility, viewport position, and overlap.

## isVisible

Check if an element is visible with detailed reason.

```javascript
window.__devtool.isVisible(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  visible: true,
  reason: null,
  details: {
    display: "block",
    visibility: "visible",
    opacity: 1,
    width: 200,
    height: 50,
    clipPath: "none",
    hidden: false
  }
}
```

When not visible:
```javascript
{
  visible: false,
  reason: "display: none",
  details: {
    display: "none",
    visibility: "visible",
    opacity: 1,
    width: 0,
    height: 0,
    clipPath: "none",
    hidden: false
  }
}
```

**Visibility Reasons:**
- `"display: none"`
- `"visibility: hidden"`
- `"opacity: 0"`
- `"zero dimensions"`
- `"hidden attribute"`
- `"clip-path: none visible area"`
- `"collapsed by parent"`

**Examples:**

```javascript
window.__devtool.isVisible('.modal')
→ {visible: true, reason: null, ...}

window.__devtool.isVisible('.hidden-panel')
→ {visible: false, reason: "display: none", ...}

window.__devtool.isVisible('.faded-element')
→ {visible: false, reason: "opacity: 0", ...}
```

## isInViewport

Check if an element is within the viewport.

```javascript
window.__devtool.isInViewport(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  inViewport: true,
  intersection: 1,  // 0 to 1, percentage visible
  fullyVisible: true,
  position: "visible",
  bounds: {
    elementTop: 200,
    elementBottom: 350,
    viewportTop: 0,
    viewportBottom: 800
  }
}
```

When partially visible:
```javascript
{
  inViewport: true,
  intersection: 0.5,
  fullyVisible: false,
  position: "partial-bottom",
  bounds: {...}
}
```

When not visible:
```javascript
{
  inViewport: false,
  intersection: 0,
  fullyVisible: false,
  position: "below",
  distanceToViewport: 200
}
```

**Positions:**
- `"visible"` - Fully in viewport
- `"partial-top"` - Cut off at top
- `"partial-bottom"` - Cut off at bottom
- `"partial-left"` - Cut off at left
- `"partial-right"` - Cut off at right
- `"above"` - Completely above viewport
- `"below"` - Completely below viewport
- `"left"` - Completely to the left
- `"right"` - Completely to the right

**Examples:**

```javascript
window.__devtool.isInViewport('#header')
→ {inViewport: true, fullyVisible: true, position: "visible"}

window.__devtool.isInViewport('.footer')
→ {inViewport: false, position: "below", distanceToViewport: 500}
```

## checkOverlap

Check if two elements overlap.

```javascript
window.__devtool.checkOverlap(selector1, selector2)
```

**Parameters:**
- `selector1` (string): CSS selector for first element
- `selector2` (string): CSS selector for second element

**Returns:**
```javascript
{
  overlaps: true,
  intersection: {
    top: 100,
    left: 200,
    width: 50,
    height: 30,
    area: 1500
  },
  element1Bounds: {top: 80, left: 150, width: 200, height: 100},
  element2Bounds: {top: 100, left: 200, width: 150, height: 80},
  overlapPercentage: {
    element1: 7.5,  // 7.5% of element1 is overlapped
    element2: 12.5  // 12.5% of element2 is overlapped
  },
  relative: "below-right"  // element2 is below and to the right of element1
}
```

When not overlapping:
```javascript
{
  overlaps: false,
  intersection: null,
  element1Bounds: {...},
  element2Bounds: {...},
  distance: {
    x: 50,
    y: 0,
    closest: 50
  },
  relative: "right"
}
```

**Relative Positions:**
- `"above"`, `"below"`, `"left"`, `"right"`
- `"above-left"`, `"above-right"`, `"below-left"`, `"below-right"`
- `"overlapping"` (when fully contained)

**Examples:**

```javascript
window.__devtool.checkOverlap('.tooltip', '.button')
→ {overlaps: true, intersection: {area: 500}, relative: "above"}

window.__devtool.checkOverlap('.sidebar', '.content')
→ {overlaps: false, distance: {x: 0, y: 0, closest: 0}, relative: "left"}
```

## Common Patterns

### Visibility Debugging

```javascript
const vis = window.__devtool.isVisible('#problem-element')
if (!vis.visible) {
  console.log('Element hidden because:', vis.reason)
  console.log('Details:', vis.details)
}
```

### Scroll Into View Check

```javascript
const vp = window.__devtool.isInViewport('.target-section')
if (!vp.inViewport) {
  console.log(`Element is ${vp.position}, ${vp.distanceToViewport}px away`)
}
```

### Detect Overlapping UI Elements

```javascript
const overlap = window.__devtool.checkOverlap('.modal', '.tooltip')
if (overlap.overlaps) {
  console.log(`Overlap area: ${overlap.intersection.area}px²`)
  console.log(`${overlap.overlapPercentage.element1}% of modal is covered`)
}
```

### Check If Element Is Clickable

```javascript
function isClickable(selector) {
  const visible = window.__devtool.isVisible(selector)
  const inView = window.__devtool.isInViewport(selector)
  const pos = window.__devtool.getPosition(selector)

  // Check if covered by other elements
  const elementAtPoint = document.elementFromPoint(
    pos.viewport.x + pos.rect.width / 2,
    pos.viewport.y + pos.rect.height / 2
  )

  return {
    clickable: visible.visible && inView.inViewport,
    visible: visible.visible,
    inViewport: inView.inViewport,
    covered: elementAtPoint !== document.querySelector(selector)
  }
}
```

### Find Overlapping Elements in a Grid

```javascript
const cards = document.querySelectorAll('.card')
const overlaps = []

for (let i = 0; i < cards.length; i++) {
  for (let j = i + 1; j < cards.length; j++) {
    const check = window.__devtool.checkOverlap(
      `.card:nth-child(${i + 1})`,
      `.card:nth-child(${j + 1})`
    )
    if (check.overlaps) {
      overlaps.push({card1: i, card2: j, area: check.intersection.area})
    }
  }
}

console.log('Overlapping cards:', overlaps)
```

### Lazy Load Detection

```javascript
function checkLazyLoadElements() {
  const images = document.querySelectorAll('img[data-src]')
  const toLoad = []

  images.forEach((img, i) => {
    const vp = window.__devtool.isInViewport(`img[data-src]:nth-of-type(${i + 1})`)
    if (vp.inViewport || vp.distanceToViewport < 200) {
      toLoad.push(img)
    }
  })

  return toLoad
}
```

## See Also

- [Layout Diagnostics](/api/frontend/layout-diagnostics) - Find layout issues
- [Visual Overlays](/api/frontend/visual-overlays) - Highlight elements
