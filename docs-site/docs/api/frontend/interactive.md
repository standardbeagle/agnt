---
sidebar_position: 7
---

# Interactive Functions

Functions that require user interaction or wait for events.

## selectElement

Interactive element picker - user clicks to select.

```javascript
window.__devtool.selectElement()
```

**Parameters:** None

**Returns:** Promise that resolves when user clicks:
```javascript
{
  selector: "#user-avatar",
  element: {
    tag: "img",
    id: "user-avatar",
    classes: ["avatar", "rounded"],
    attributes: {src: "/images/user.png", alt: "User"}
  },
  position: {x: 150, y: 200},
  path: "html > body > header > div.user-info > img#user-avatar"
}
```

When cancelled (Escape key):
```javascript
{
  cancelled: true,
  message: "Selection cancelled by user"
}
```

**User Experience:**
- Cursor changes to crosshair
- Hovered elements are highlighted
- Click to select
- Press Escape to cancel

**Example via MCP:**
```json
proxy {action: "exec", id: "app", code: "window.__devtool.selectElement()"}
```

The exec waits for the Promise to resolve.

## measureBetween

Measure the distance between two elements.

```javascript
window.__devtool.measureBetween(selector1, selector2)
```

**Parameters:**
- `selector1` (string): CSS selector for first element
- `selector2` (string): CSS selector for second element

**Returns:**
```javascript
{
  distance: {
    x: 0,
    y: 150,
    diagonal: 150
  },
  direction: "below",
  gap: {
    horizontal: 0,
    vertical: 100  // Empty space between elements
  },
  element1: {
    center: {x: 400, y: 100},
    bounds: {top: 50, left: 300, width: 200, height: 100}
  },
  element2: {
    center: {x: 400, y: 350},
    bounds: {top: 250, left: 300, width: 200, height: 200}
  }
}
```

**Direction values:**
- `"above"`, `"below"`, `"left"`, `"right"`
- `"above-left"`, `"above-right"`, `"below-left"`, `"below-right"`
- `"overlapping"` (centers are at same position)

**Example:**
```javascript
window.__devtool.measureBetween('#header', '#footer')
→ {distance: {y: 850}, gap: {vertical: 800}, direction: "below"}
```

## waitForElement

Wait for an element to appear in the DOM.

```javascript
window.__devtool.waitForElement(selector, timeout)
```

**Parameters:**
- `selector` (string): CSS selector to wait for
- `timeout` (number, optional): Maximum wait time in ms (default: 5000)

**Returns:** Promise that resolves when element appears:
```javascript
{
  found: true,
  element: {
    tag: "div",
    classes: ["loading-complete"],
    id: ""
  },
  waited: 1234  // milliseconds
}
```

When timeout:
```javascript
{
  found: false,
  timeout: true,
  waited: 5000,
  message: "Element not found within 5000ms"
}
```

**Example:**
```javascript
// Wait for loading to complete
await window.__devtool.waitForElement('.data-loaded', 10000)

// Then inspect the content
window.__devtool.inspect('.data-content')
```

**How it works:**
- Uses MutationObserver for efficiency
- Checks immediately first
- No polling - responds as soon as element appears

## ask

Show a modal dialog to ask the user a question.

```javascript
window.__devtool.ask(question, options, config)
```

**Parameters:**
- `question` (string): The question to display
- `options` (string[]): Array of answer options
- `config` (object, optional): Configuration

**Config Options:**
| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `timeout` | number | `30000` | Auto-dismiss timeout in ms |
| `allowCancel` | boolean | `true` | Show cancel button |
| `defaultOption` | number | `null` | Pre-selected option index |

**Returns:** Promise that resolves with user selection:
```javascript
{
  answer: "Option A",
  index: 0,
  cancelled: false
}
```

When cancelled:
```javascript
{
  answer: null,
  index: null,
  cancelled: true
}
```

When timeout:
```javascript
{
  answer: null,
  index: null,
  timeout: true
}
```

**Examples:**

Simple yes/no:
```javascript
window.__devtool.ask('Is this the correct element?', ['Yes', 'No'])
```

Multiple options:
```javascript
window.__devtool.ask('Which layout looks better?', [
  'Grid layout',
  'Flex layout',
  'Table layout',
  'None of these'
])
```

With timeout:
```javascript
window.__devtool.ask('Continue with changes?', ['Yes', 'No'], {
  timeout: 10000
})
```

**User Experience:**
- Modal appears centered on screen
- Click option button to select
- Click cancel or press Escape to dismiss
- Auto-dismisses after timeout

## Common Patterns

### Interactive Element Selection Workflow

```javascript
// Let user select element
const selected = await window.__devtool.selectElement()
if (selected.cancelled) {
  console.log('User cancelled selection')
  return
}

// Inspect the selected element
const info = window.__devtool.inspect(selected.selector)

// Ask what to do
const action = await window.__devtool.ask('What would you like to do?', [
  'Show layout',
  'Check accessibility',
  'Measure from here'
])

if (action.answer === 'Show layout') {
  window.__devtool.showLayout()
} else if (action.answer === 'Check accessibility') {
  const a11y = window.__devtool.getA11yInfo(selected.selector)
  console.log(a11y)
} else if (action.answer === 'Measure from here') {
  const second = await window.__devtool.selectElement()
  const measure = window.__devtool.measureBetween(selected.selector, second.selector)
  console.log(measure)
}
```

### Wait for Dynamic Content

```javascript
// Click a button that loads content
document.querySelector('#load-data').click()

// Wait for content to appear
const result = await window.__devtool.waitForElement('.data-container', 10000)

if (result.found) {
  // Now safe to inspect
  const data = window.__devtool.inspect('.data-container')
  console.log(`Loaded in ${result.waited}ms`)
}
```

### Spacing Verification

```javascript
// Measure spacing between elements
const headerToNav = window.__devtool.measureBetween('#header', '#nav')
const navToContent = window.__devtool.measureBetween('#nav', '#content')

console.log('Header to nav gap:', headerToNav.gap.vertical, 'px')
console.log('Nav to content gap:', navToContent.gap.vertical, 'px')

// Check consistency
if (headerToNav.gap.vertical !== navToContent.gap.vertical) {
  console.warn('Inconsistent spacing!')
}
```

### User Confirmation Before Changes

```javascript
// Show what will change
window.__devtool.highlight('.affected-elements', {color: 'yellow', duration: 0})

// Ask for confirmation
const confirm = await window.__devtool.ask(
  'These elements will be modified. Proceed?',
  ['Yes, proceed', 'No, cancel'],
  {timeout: 60000}
)

window.__devtool.clearAllOverlays()

if (confirm.answer === 'Yes, proceed') {
  // Make changes
} else {
  console.log('User cancelled')
}
```

### Multi-Step Selection

```javascript
async function measureMultiple() {
  const points = []

  while (true) {
    const action = await window.__devtool.ask(
      `${points.length} points selected. What next?`,
      ['Add point', 'Finish', 'Cancel']
    )

    if (action.answer === 'Cancel' || action.cancelled) {
      return null
    }

    if (action.answer === 'Finish') {
      break
    }

    const point = await window.__devtool.selectElement()
    if (!point.cancelled) {
      points.push(point)
      window.__devtool.highlight(point.selector, {
        label: `Point ${points.length}`,
        duration: 0
      })
    }
  }

  return points
}
```

## See Also

- [Visual Overlays](/api/frontend/visual-overlays) - Highlight selected elements
- [Element Inspection](/api/frontend/element-inspection) - Inspect after selection
