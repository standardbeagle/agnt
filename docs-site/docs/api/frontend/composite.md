---
sidebar_position: 10
---

# Composite Functions

High-level functions that combine multiple primitives for comprehensive analysis.

## inspect

Comprehensive element analysis combining 8+ primitives.

```javascript
window.__devtool.inspect(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  element: {
    tag: "button",
    id: "submit-btn",
    classes: ["btn", "btn-primary"],
    attributes: {type: "submit"}
  },
  position: {
    rect: {top: 200, left: 300, width: 120, height: 40},
    viewport: {x: 300, y: 200},
    scroll: {top: 100, left: 0}
  },
  box: {
    margin: {top: 8, right: 8, bottom: 8, left: 8},
    border: {top: 1, right: 1, bottom: 1, left: 1},
    padding: {top: 12, right: 24, bottom: 12, left: 24},
    content: {width: 70, height: 14}
  },
  layout: {
    display: "inline-flex",
    position: "relative",
    flexbox: {
      direction: "row",
      alignItems: "center",
      justifyContent: "center"
    }
  },
  stacking: {
    zIndex: "auto",
    createsContext: false,
    parentContext: "body"
  },
  visibility: {
    visible: true,
    inViewport: true,
    intersection: 1
  },
  accessibility: {
    role: "button",
    accessibleName: "Submit form",
    tabIndex: 0,
    focusable: true
  },
  contrast: {
    ratio: 4.52,
    passes: {AA: true, AAA: false}
  }
}
```

**When to use:**
- First look at an unfamiliar element
- Bug reports requiring full context
- Debugging complex styling issues

**Example:**
```json
proxy {action: "exec", id: "app", code: "window.__devtool.inspect('#broken-button')"}
```

## diagnoseLayout

Find all layout issues on the page.

```javascript
window.__devtool.diagnoseLayout()
```

**Parameters:** None

**Returns:**
```javascript
{
  overflows: [
    {
      selector: ".sidebar",
      overflow: "horizontal",
      excess: 150,
      scrollWidth: 400,
      clientWidth: 250
    }
  ],
  stackingIssues: [
    {
      selector: ".tooltip",
      issue: "hidden-by-context",
      message: "Element has z-index:100 but parent context limits stacking",
      suggestion: "Move element outside of stacking context or increase parent z-index"
    },
    {
      selector: ".modal",
      issue: "competing-context",
      message: "Multiple stacking contexts with similar z-index may cause overlap",
      elements: [".modal", ".dropdown"]
    }
  ],
  offscreenElements: [
    {
      selector: ".hidden-nav",
      position: "left",
      distance: -300
    }
  ],
  summary: {
    overflowCount: 1,
    stackingIssueCount: 2,
    offscreenCount: 1,
    totalIssues: 4
  },
  healthy: false
}
```

**When to use:**
- Page layout looks broken
- Elements overlapping unexpectedly
- Content being cut off or hidden

**Example:**
```javascript
const diagnosis = window.__devtool.diagnoseLayout()
if (!diagnosis.healthy) {
  console.log('Found', diagnosis.summary.totalIssues, 'layout issues')
  diagnosis.overflows.forEach(o => console.log(`Overflow: ${o.selector}`))
}
```

## showLayout

Visual debugging overlay showing layout structure.

```javascript
window.__devtool.showLayout(config)
```

**Parameters:**
- `config` (object, optional): Display configuration

**Config Options:**
| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `showMargins` | boolean | `true` | Show margin areas (orange) |
| `showPadding` | boolean | `true` | Show padding areas (green) |
| `showBorders` | boolean | `true` | Show border areas (blue) |
| `showGrid` | boolean | `false` | Show grid lines |
| `showFlexbox` | boolean | `false` | Highlight flex containers |
| `selector` | string | `'*'` | Limit to specific elements |

**Returns:**
```javascript
{
  overlayId: "layout-overlay-1",
  elementsHighlighted: 45,
  legend: {
    margin: "orange",
    padding: "green",
    border: "blue"
  }
}
```

**Example:**

Show all box models:
```javascript
window.__devtool.showLayout()
```

Show only for specific container:
```javascript
window.__devtool.showLayout({selector: '.card'})
```

Show grid/flex structure:
```javascript
window.__devtool.showLayout({
  showMargins: false,
  showPadding: false,
  showGrid: true,
  showFlexbox: true
})
```

Clean up:
```javascript
window.__devtool.clearAllOverlays()
```

## Common Patterns

### Quick Debug Session

```javascript
// 1. Get overview of problem element
const info = window.__devtool.inspect('.broken-component')
console.log(info)

// 2. Check for layout issues
const layout = window.__devtool.diagnoseLayout()
if (layout.overflows.some(o => o.selector.includes('broken'))) {
  console.log('Found overflow issue!')
}

// 3. Visualize
window.__devtool.showLayout({selector: '.broken-component'})
window.__devtool.screenshot('debug')
```

### Compare Two Elements

```javascript
const btn1 = window.__devtool.inspect('.btn-old')
const btn2 = window.__devtool.inspect('.btn-new')

// Compare dimensions
console.log('Size:', btn1.box.content, 'vs', btn2.box.content)

// Compare spacing
console.log('Padding:', btn1.box.padding, 'vs', btn2.box.padding)

// Compare accessibility
console.log('A11y:', btn1.accessibility, 'vs', btn2.accessibility)
```

### Find What's Causing Layout Shift

```javascript
// Run diagnosis
const diagnosis = window.__devtool.diagnoseLayout()

// Look for potential CLS causes
const clsCauses = []

// Check images without dimensions
document.querySelectorAll('img:not([width]):not([height])').forEach(img => {
  clsCauses.push({type: 'image', src: img.src})
})

// Check for overflow issues
diagnosis.overflows.forEach(o => {
  clsCauses.push({type: 'overflow', selector: o.selector})
})

// Check for elements appearing/disappearing
diagnosis.offscreenElements.forEach(e => {
  if (e.distance < 50) {
    clsCauses.push({type: 'near-viewport', selector: e.selector})
  }
})

console.log('Potential CLS causes:', clsCauses)
```

### Full Page Audit

```javascript
async function fullAudit() {
  const results = {
    layout: window.__devtool.diagnoseLayout(),
    accessibility: window.__devtool.auditAccessibility(),
    performance: window.__devtool.captureNetwork(),
    state: window.__devtool.captureState(['localStorage', 'sessionStorage'])
  }

  const score = {
    layout: results.layout.healthy ? 100 :
            100 - (results.layout.summary.totalIssues * 10),
    accessibility: results.accessibility.score,
    performance: results.performance.summary.totalDuration < 1000 ? 100 :
                 results.performance.summary.totalDuration < 3000 ? 75 : 50
  }

  return {
    overallScore: Math.round((score.layout + score.accessibility + score.performance) / 3),
    scores: score,
    details: results
  }
}

const audit = await fullAudit()
console.log(`Overall score: ${audit.overallScore}/100`)
```

### Interactive Debugging Session

```javascript
async function interactiveDebug() {
  // Let user select element
  const selected = await window.__devtool.selectElement()
  if (selected.cancelled) return

  // Show full inspection
  const info = window.__devtool.inspect(selected.selector)
  console.log('Element info:', info)

  // Ask what to check
  const check = await window.__devtool.ask('What would you like to check?', [
    'Layout issues',
    'Accessibility',
    'Show box model',
    'Measure to another element'
  ])

  switch (check.answer) {
    case 'Layout issues':
      const layout = window.__devtool.diagnoseLayout()
      console.log(layout)
      break

    case 'Accessibility':
      const a11y = window.__devtool.getA11yInfo(selected.selector)
      const contrast = window.__devtool.getContrast(selected.selector)
      console.log('A11y:', a11y, 'Contrast:', contrast)
      break

    case 'Show box model':
      window.__devtool.showLayout({selector: selected.selector})
      break

    case 'Measure to another element':
      const second = await window.__devtool.selectElement()
      if (!second.cancelled) {
        const measure = window.__devtool.measureBetween(selected.selector, second.selector)
        console.log('Distance:', measure.distance)
      }
      break
  }
}

interactiveDebug()
```

## Performance Notes

- `inspect` calls 8+ primitives - use when you need comprehensive data
- `diagnoseLayout` scans entire page - can be slow on complex pages
- `showLayout` adds overlays to DOM - clean up when done
- Consider using specific primitives when you know what you need

## See Also

- [Element Inspection](/api/frontend/element-inspection) - Individual primitives
- [Layout Diagnostics](/api/frontend/layout-diagnostics) - Underlying functions
- [Use Cases](/use-cases/debugging-web-apps) - Real-world examples
