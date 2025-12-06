---
sidebar_position: 6
---

# Visual Overlays

Functions for visually highlighting elements during debugging.

## highlight

Create a visual highlight overlay on an element.

```javascript
window.__devtool.highlight(selector, config)
```

**Parameters:**
- `selector` (string): CSS selector
- `config` (object, optional): Highlight configuration

**Config Options:**
| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `color` | string | `'rgba(59, 130, 246, 0.3)'` | Background color |
| `border` | string | `'2px solid rgb(59, 130, 246)'` | Border style |
| `duration` | number | `3000` | Auto-remove after ms (0 = permanent) |
| `label` | string | `null` | Label text to display |
| `labelPosition` | string | `'top'` | Label position: `top`, `bottom`, `left`, `right` |

**Returns:**
```javascript
{
  highlightId: "hl-1",
  selector: ".my-element",
  bounds: {top: 100, left: 200, width: 300, height: 50}
}
```

**Examples:**

Basic highlight (3 second duration):
```javascript
window.__devtool.highlight('.problematic-button')
```

Custom color:
```javascript
window.__devtool.highlight('.error-field', {
  color: 'rgba(255, 0, 0, 0.3)',
  border: '2px solid red'
})
```

Permanent highlight:
```javascript
window.__devtool.highlight('#debug-target', {duration: 0})
```

With label:
```javascript
window.__devtool.highlight('.suspicious-element', {
  color: 'rgba(255, 165, 0, 0.3)',
  label: 'Overflow source',
  labelPosition: 'bottom'
})
```

## removeHighlight

Remove a specific highlight.

```javascript
window.__devtool.removeHighlight(highlightId)
```

**Parameters:**
- `highlightId` (string): ID returned from highlight()

**Returns:**
```javascript
{
  removed: true,
  highlightId: "hl-1"
}
```

If not found:
```javascript
{
  removed: false,
  error: "Highlight not found"
}
```

**Example:**
```javascript
const hl = window.__devtool.highlight('.temp-debug')
// ... do debugging ...
window.__devtool.removeHighlight(hl.highlightId)
```

## clearAllOverlays

Remove all visual overlays created by devtool.

```javascript
window.__devtool.clearAllOverlays()
```

**Parameters:** None

**Returns:**
```javascript
{
  cleared: 5,
  message: "Cleared 5 overlays"
}
```

**Example:**
```javascript
// Add multiple highlights
window.__devtool.highlight('.item-1')
window.__devtool.highlight('.item-2')
window.__devtool.highlight('.item-3')

// Clear all at once
window.__devtool.clearAllOverlays()
```

## Common Patterns

### Debug and Screenshot

```javascript
// Highlight the problem element
window.__devtool.highlight('.broken-layout', {
  color: 'rgba(255, 0, 0, 0.3)',
  border: '3px solid red',
  label: 'Bug here',
  duration: 0  // Keep it visible
})

// Take a screenshot
window.__devtool.screenshot('bug-report')

// Clean up
window.__devtool.clearAllOverlays()
```

### Highlight Multiple Elements

```javascript
// Highlight all elements with same issue
const overflows = window.__devtool.findOverflows()
overflows.forEach((o, i) => {
  window.__devtool.highlight(o.selector, {
    color: 'rgba(255, 0, 0, 0.2)',
    label: `Overflow #${i + 1}`,
    duration: 0
  })
})

// Later: clear all
window.__devtool.clearAllOverlays()
```

### Color-Coded Debugging

```javascript
function highlightByType(selector, type) {
  const colors = {
    error: {color: 'rgba(255, 0, 0, 0.3)', border: '2px solid red'},
    warning: {color: 'rgba(255, 165, 0, 0.3)', border: '2px solid orange'},
    info: {color: 'rgba(0, 0, 255, 0.3)', border: '2px solid blue'},
    success: {color: 'rgba(0, 255, 0, 0.3)', border: '2px solid green'}
  }

  return window.__devtool.highlight(selector, {
    ...colors[type],
    duration: 0
  })
}

// Usage
highlightByType('.invalid-input', 'error')
highlightByType('.deprecation-warning', 'warning')
highlightByType('.new-feature', 'info')
```

### Highlight Hierarchy

```javascript
function highlightHierarchy(selector) {
  const parents = window.__devtool.walkParents(selector)
  const colors = ['red', 'orange', 'yellow', 'green', 'blue', 'purple']

  parents.slice(0, 6).forEach((parent, i) => {
    const sel = parent.id ? `#${parent.id}` :
                parent.classes.length ? `.${parent.classes[0]}` : parent.tag
    window.__devtool.highlight(sel, {
      color: colors[i],
      border: `2px dashed ${colors[i]}`,
      label: `Level ${i + 1}`,
      duration: 0
    })
  })
}

highlightHierarchy('.deeply-nested-element')
```

### Flash Highlight

```javascript
async function flashHighlight(selector, times = 3) {
  for (let i = 0; i < times; i++) {
    const hl = window.__devtool.highlight(selector, {
      color: 'rgba(255, 255, 0, 0.5)',
      duration: 0
    })
    await new Promise(r => setTimeout(r, 200))
    window.__devtool.removeHighlight(hl.highlightId)
    await new Promise(r => setTimeout(r, 200))
  }
}

flashHighlight('.target-element')
```

### Highlight on Hover (via exec)

```javascript
// Run in browser console or via exec
document.addEventListener('mouseover', (e) => {
  window.__devtool.clearAllOverlays()
  window.__devtool.highlight(e.target, {duration: 0})
})

document.addEventListener('mouseout', () => {
  window.__devtool.clearAllOverlays()
})
```

### Stacking Context Visualization

```javascript
const contexts = window.__devtool.findStackingContexts()
contexts.forEach((ctx, i) => {
  // Color by z-index
  const zIndex = typeof ctx.zIndex === 'number' ? ctx.zIndex : 0
  const hue = (zIndex % 360)

  window.__devtool.highlight(ctx.selector, {
    color: `hsla(${hue}, 70%, 50%, 0.2)`,
    label: `z: ${ctx.zIndex}`,
    duration: 0
  })
})
```

## Overlay Styling

Overlays are positioned absolutely and follow the element:
- Use pointer-events: none (don't interfere with clicks)
- Z-index is set high (99999) to be visible
- Labels have white background and padding

## Performance Notes

- Overlays are DOM elements added to the page
- Too many overlays can affect performance
- Always clean up with clearAllOverlays() when done
- Auto-remove (duration) is preferred for temporary debugging

## See Also

- [Layout Diagnostics](/api/frontend/layout-diagnostics) - Find elements to highlight
- [Composite Functions](/api/frontend/composite) - showLayout creates overlays
