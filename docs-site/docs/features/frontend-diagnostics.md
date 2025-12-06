---
sidebar_position: 4
---

# Frontend Diagnostics API

The proxy injects `window.__devtool` into all HTML pages, providing 50+ primitives for comprehensive DOM inspection, layout debugging, and accessibility auditing.

## Overview

When browsing through the proxy, every page has access to:

```javascript
window.__devtool.inspect('#my-element')     // Comprehensive element analysis
window.__devtool.screenshot('bug-report')   // Capture screenshots
window.__devtool.auditAccessibility()       // Full a11y audit
window.__devtool.ask('Which looks better?', ['A', 'B'])  // User interaction
```

Execute these via the proxy:

```json
proxy {action: "exec", id: "app", code: "window.__devtool.inspect('#header')"}
```

## Design Principles

1. **Primitives over Monoliths** - Small, focused functions (~20-30 lines)
2. **Composability** - Functions return rich data for chaining
3. **Error Resilient** - Return `{error: ...}` instead of throwing
4. **ES5 Compatible** - Works in all modern browsers
5. **Zero Dependencies** - Pure browser APIs

## Quick Reference

| Category | Functions |
|----------|-----------|
| [Element Inspection](#element-inspection) | `getElementInfo`, `getPosition`, `getComputed`, `getBox`, `getLayout`, `getContainer`, `getStacking`, `getTransform`, `getOverflow` |
| [Tree Walking](#tree-walking) | `walkChildren`, `walkParents`, `findAncestor` |
| [Visual State](#visual-state) | `isVisible`, `isInViewport`, `checkOverlap` |
| [Layout Diagnostics](#layout-diagnostics) | `findOverflows`, `findStackingContexts`, `findOffscreen` |
| [Visual Overlays](#visual-overlays) | `highlight`, `removeHighlight`, `clearAllOverlays` |
| [Interactive](#interactive) | `selectElement`, `measureBetween`, `waitForElement`, `ask` |
| [State Capture](#state-capture) | `captureDOM`, `captureStyles`, `captureState`, `captureNetwork` |
| [Accessibility](#accessibility) | `getA11yInfo`, `getContrast`, `getTabOrder`, `getScreenReaderText`, `auditAccessibility` |
| [Composite](#composite-functions) | `inspect`, `diagnoseLayout`, `showLayout` |

## Element Inspection

### getElementInfo

Basic element information:

```javascript
window.__devtool.getElementInfo('#nav-button')
→ {
    tag: "button",
    id: "nav-button",
    classes: ["btn", "btn-primary"],
    attributes: {type: "submit", disabled: ""},
    textContent: "Submit"
  }
```

### getPosition

Element position and dimensions:

```javascript
window.__devtool.getPosition('#sidebar')
→ {
    rect: {top: 60, left: 0, width: 250, height: 800},
    viewport: {x: 0, y: 60},
    scroll: {top: 120, left: 0},
    offsetParent: "body"
  }
```

### getComputed

Computed CSS properties:

```javascript
window.__devtool.getComputed('#header', ['display', 'position', 'z-index'])
→ {
    display: "flex",
    position: "fixed",
    "z-index": "1000"
  }
```

### getBox

Box model breakdown:

```javascript
window.__devtool.getBox('.card')
→ {
    margin: {top: 16, right: 16, bottom: 16, left: 16},
    border: {top: 1, right: 1, bottom: 1, left: 1},
    padding: {top: 24, right: 24, bottom: 24, left: 24},
    content: {width: 300, height: 200}
  }
```

### getLayout

Layout properties:

```javascript
window.__devtool.getLayout('.container')
→ {
    display: "grid",
    position: "relative",
    flexbox: null,
    grid: {
      columns: "repeat(3, 1fr)",
      rows: "auto",
      gap: "16px"
    },
    float: "none"
  }
```

### getStacking

Stacking context information:

```javascript
window.__devtool.getStacking('.modal')
→ {
    zIndex: 1000,
    createsContext: true,
    reason: "z-index with position: fixed",
    parentContext: "body"
  }
```

### getTransform

Transform matrix decomposition:

```javascript
window.__devtool.getTransform('.rotated')
→ {
    matrix: [0.866, 0.5, -0.5, 0.866, 0, 0],
    decomposed: {
      translate: {x: 0, y: 0},
      rotate: 30,
      scale: {x: 1, y: 1},
      skew: {x: 0, y: 0}
    }
  }
```

### getOverflow

Overflow detection:

```javascript
window.__devtool.getOverflow('.scrollable')
→ {
    overflowX: "auto",
    overflowY: "scroll",
    scrollWidth: 1200,
    scrollHeight: 800,
    clientWidth: 400,
    clientHeight: 600,
    hasHorizontalOverflow: true,
    hasVerticalOverflow: true
  }
```

## Tree Walking

### walkChildren

Traverse descendants:

```javascript
window.__devtool.walkChildren('.nav', 2)
→ {
    element: {tag: "nav", classes: ["nav"]},
    children: [
      {
        element: {tag: "ul", classes: ["nav-list"]},
        children: [
          {element: {tag: "li", classes: ["nav-item"]}},
          {element: {tag: "li", classes: ["nav-item"]}}
        ]
      }
    ]
  }
```

With filter:

```javascript
window.__devtool.walkChildren('body', 3, 'button')
→ Only button elements up to depth 3
```

### walkParents

Traverse ancestors:

```javascript
window.__devtool.walkParents('.nested-item')
→ [
    {tag: "li", classes: ["item"]},
    {tag: "ul", classes: ["list"]},
    {tag: "div", classes: ["container"]},
    {tag: "main", id: "content"},
    {tag: "body"},
    {tag: "html"}
  ]
```

### findAncestor

Find first matching ancestor:

```javascript
window.__devtool.findAncestor('.button', '[data-modal]')
→ {tag: "div", classes: ["modal"], attributes: {"data-modal": "confirm"}}
```

## Visual State

### isVisible

Check visibility with reason:

```javascript
window.__devtool.isVisible('.hidden-element')
→ {
    visible: false,
    reason: "display: none",
    details: {
      display: "none",
      visibility: "visible",
      opacity: 1,
      clipPath: "none"
    }
  }
```

### isInViewport

Check viewport intersection:

```javascript
window.__devtool.isInViewport('.footer')
→ {
    inViewport: false,
    intersection: 0,
    position: "below",
    distanceToViewport: 450
  }
```

### checkOverlap

Check if elements overlap:

```javascript
window.__devtool.checkOverlap('.tooltip', '.button')
→ {
    overlaps: true,
    intersection: {width: 50, height: 20, area: 1000},
    relative: "above-right"
  }
```

## Layout Diagnostics

### findOverflows

Find all elements with overflow:

```javascript
window.__devtool.findOverflows()
→ [
    {
      selector: ".sidebar",
      scrollWidth: 1024,
      clientWidth: 250,
      overflow: "horizontal"
    },
    {
      selector: ".content",
      scrollHeight: 2000,
      clientHeight: 800,
      overflow: "vertical"
    }
  ]
```

### findStackingContexts

Locate all stacking contexts:

```javascript
window.__devtool.findStackingContexts()
→ [
    {selector: ".modal", zIndex: 1000, reason: "position: fixed + z-index"},
    {selector: ".header", zIndex: 100, reason: "position: sticky + z-index"},
    {selector: ".tooltip", zIndex: 50, reason: "transform"}
  ]
```

### findOffscreen

Find elements outside viewport:

```javascript
window.__devtool.findOffscreen()
→ [
    {selector: ".hidden-menu", position: "left", distance: -300},
    {selector: ".footer-extra", position: "below", distance: 500}
  ]
```

## Visual Overlays

### highlight

Highlight an element:

```javascript
window.__devtool.highlight('.problem-element', {
  color: 'rgba(255, 0, 0, 0.3)',
  border: '2px solid red',
  duration: 5000
})
→ {highlightId: "hl-1", selector: ".problem-element"}
```

Persistent highlight (no auto-remove):

```javascript
window.__devtool.highlight('#debug-target', {duration: 0})
→ Stays until manually removed
```

### removeHighlight

Remove specific highlight:

```javascript
window.__devtool.removeHighlight('hl-1')
→ {removed: true}
```

### clearAllOverlays

Remove all visual overlays:

```javascript
window.__devtool.clearAllOverlays()
→ {cleared: 5}
```

## Interactive

### selectElement

Interactive element picker:

```javascript
window.__devtool.selectElement()
→ Promise that resolves when user clicks:
  {
    selector: "#user-avatar",
    element: {tag: "img", classes: ["avatar"]},
    position: {x: 150, y: 200}
  }
```

User sees a crosshair cursor and highlight on hover. Click to select, Escape to cancel.

### measureBetween

Measure distance between elements:

```javascript
window.__devtool.measureBetween('#header', '#footer')
→ {
    distance: {x: 0, y: 850, diagonal: 850},
    direction: "below",
    gap: {horizontal: 0, vertical: 800}
  }
```

### waitForElement

Wait for dynamic element:

```javascript
window.__devtool.waitForElement('.loading-complete', 5000)
→ Promise resolves when element appears or times out
  {
    found: true,
    element: {tag: "div", classes: ["loading-complete"]},
    waited: 1234
  }
```

### ask

Show modal for user input:

```javascript
window.__devtool.ask('Which layout do you prefer?', ['Grid', 'Flexbox', 'Float'])
→ Promise resolves with user selection:
  {answer: "Grid", index: 0}
```

Timeout example:

```javascript
window.__devtool.ask('Is this correct?', ['Yes', 'No'], {timeout: 30000})
```

## State Capture

### captureDOM

Capture full page HTML:

```javascript
window.__devtool.captureDOM()
→ {
    html: "<!DOCTYPE html><html>...",
    hash: "a1b2c3d4",
    timestamp: "2024-01-15T10:30:00Z",
    size: 45678
  }
```

### captureStyles

Capture all styles for element:

```javascript
window.__devtool.captureStyles('.button')
→ {
    inline: {color: "red"},
    computed: {
      display: "inline-flex",
      padding: "8px 16px",
      // ... all computed styles
    }
  }
```

### captureState

Capture browser state:

```javascript
window.__devtool.captureState(['localStorage', 'sessionStorage', 'cookies'])
→ {
    localStorage: {theme: "dark", user: "..."},
    sessionStorage: {cart: "..."},
    cookies: {session_id: "..."}
  }
```

### captureNetwork

Get resource timing data:

```javascript
window.__devtool.captureNetwork()
→ {
    entries: [
      {
        name: "https://example.com/api/users",
        type: "fetch",
        duration: 145,
        transferSize: 2340
      },
      {
        name: "https://example.com/main.js",
        type: "script",
        duration: 89,
        transferSize: 45678
      }
    ],
    count: 42
  }
```

## Accessibility

### getA11yInfo

Get accessibility attributes:

```javascript
window.__devtool.getA11yInfo('#nav-button')
→ {
    role: "button",
    ariaLabel: "Open navigation",
    ariaExpanded: "false",
    tabIndex: 0,
    focusable: true
  }
```

### getContrast

Check color contrast:

```javascript
window.__devtool.getContrast('.button')
→ {
    foreground: "#ffffff",
    background: "#2196f3",
    ratio: 4.52,
    passes: {
      AA_normal: true,
      AA_large: true,
      AAA_normal: false,
      AAA_large: true
    }
  }
```

### getTabOrder

Get document tab order:

```javascript
window.__devtool.getTabOrder()
→ [
    {selector: "a.skip-link", tabIndex: 0, order: 1},
    {selector: "#search-input", tabIndex: 0, order: 2},
    {selector: "button.menu", tabIndex: 0, order: 3},
    // ...
  ]
```

### getScreenReaderText

Get screen reader announcement:

```javascript
window.__devtool.getScreenReaderText('.icon-button')
→ {
    text: "Close dialog",
    sources: ["aria-label"],
    role: "button"
  }
```

### auditAccessibility

Full page accessibility audit:

```javascript
window.__devtool.auditAccessibility()
→ {
    score: 85,
    errors: [
      {
        type: "missing-alt",
        selector: "img.hero-image",
        message: "Image missing alt text"
      }
    ],
    warnings: [
      {
        type: "low-contrast",
        selector: ".muted-text",
        message: "Contrast ratio 3.2:1 below AA threshold"
      }
    ],
    passes: 42,
    total: 50
  }
```

## Composite Functions

### inspect

Comprehensive element analysis (combines 8+ primitives):

```javascript
window.__devtool.inspect('#my-button')
→ {
    element: {tag: "button", id: "my-button", classes: ["btn"]},
    position: {rect: {...}, viewport: {...}},
    box: {margin: {...}, padding: {...}},
    layout: {display: "inline-flex", position: "relative"},
    stacking: {zIndex: "auto", createsContext: false},
    visibility: {visible: true},
    accessibility: {role: "button", ariaLabel: "Submit form"},
    contrast: {ratio: 4.52, passes: {AA: true}}
  }
```

### diagnoseLayout

Find all layout issues:

```javascript
window.__devtool.diagnoseLayout()
→ {
    overflows: [...],
    stackingIssues: [...],
    offscreenElements: [...],
    summary: {
      overflowCount: 2,
      stackingContextCount: 5,
      offscreenCount: 3
    }
  }
```

### showLayout

Visual debugging overlay:

```javascript
window.__devtool.showLayout({
  showMargins: true,
  showPadding: true,
  showGrid: true
})
→ {overlayId: "layout-1"}
```

Displays colored overlays showing margins (orange), padding (green), and grid lines.

## Screenshots

### screenshot

Capture the current viewport:

```javascript
window.__devtool.screenshot('bug-report')
→ {
    name: "bug-report",
    path: "/tmp/devtool-screenshots/bug-report-1705312200.png",
    size: {width: 1920, height: 1080}
  }
```

Access via proxylog:

```json
proxylog {proxy_id: "app", types: ["screenshot"]}
```

## Logging

### log, debug, info, warn, error

Send custom logs to the proxy:

```javascript
window.__devtool.log('Custom message', 'info', {extra: 'data'})
window.__devtool.debug('Debug info')
window.__devtool.warn('Warning message')
window.__devtool.error('Error occurred', {stack: '...'})
```

Query via:

```json
proxylog {proxy_id: "app", types: ["custom"]}
```

## Connection Status

### isConnected

Check WebSocket connection:

```javascript
window.__devtool.isConnected()
→ true
```

### getStatus

Detailed connection status:

```javascript
window.__devtool.getStatus()
→ {
    connected: true,
    reconnectAttempts: 0,
    lastMessage: "2024-01-15T10:30:00Z"
  }
```

## Error Handling

All primitives return error objects instead of throwing:

```javascript
window.__devtool.getPosition('.nonexistent')
→ {error: "Element not found", selector: ".nonexistent"}

window.__devtool.getContrast('.element-with-gradient')
→ {error: "Cannot compute contrast for gradient background"}
```

## Performance

- All primitives complete in under 10ms on typical pages
- Synchronous operations (getPosition, getBox) are instant
- Async operations (selectElement, ask) wait for user input
- No external dependencies - pure browser APIs

## Next Steps

- See [Element Inspection API](/api/frontend/element-inspection)
- Try [Accessibility Auditing](/use-cases/accessibility-auditing)
- Explore [Debugging Web Apps](/use-cases/debugging-web-apps)
