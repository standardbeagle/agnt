---
sidebar_position: 1
---

# Frontend API Overview

The proxy injects `window.__devtool` into all HTML pages, providing ~50 primitive functions for DOM inspection, layout debugging, visual debugging, and accessibility auditing.

## Accessing the API

When browsing through the proxy, access functions directly in the browser console:

```javascript
window.__devtool.inspect('#my-element')
```

Or execute remotely via MCP:

```json
proxy {action: "exec", id: "app", code: "window.__devtool.inspect('#my-element')"}
```

## Design Principles

### 1. Primitives Over Monoliths

Small, focused functions (~20-30 lines each) that do one thing well:

```javascript
// Good: focused primitives
getPosition('#el')  // Just position
getBox('#el')       // Just box model
getComputed('#el', ['display'])  // Just specific styles

// Also available: composite functions when needed
inspect('#el')  // Combines 8+ primitives
```

### 2. Composability

Functions return rich data structures for chaining:

```javascript
// Get position, then check if visible
const pos = window.__devtool.getPosition('#tooltip')
const visible = window.__devtool.isInViewport('#tooltip')
```

### 3. Error Resilient

Return `{error: ...}` instead of throwing exceptions:

```javascript
window.__devtool.getPosition('.nonexistent')
→ {error: "Element not found", selector: ".nonexistent"}

// Never throws - safe to chain
```

### 4. ES5 Compatible

Works in all modern browsers without transpilation. No external dependencies.

## Function Categories

| Category | Count | Purpose |
|----------|-------|---------|
| [Element Inspection](/api/frontend/element-inspection) | 9 | Get element properties, styles, layout |
| [Tree Walking](/api/frontend/tree-walking) | 3 | Navigate DOM hierarchy |
| [Visual State](/api/frontend/visual-state) | 3 | Check visibility and overlap |
| [Layout Diagnostics](/api/frontend/layout-diagnostics) | 3 | Find layout issues |
| [Visual Overlays](/api/frontend/visual-overlays) | 3 | Highlight and debug visually |
| [Interactive](/api/frontend/interactive) | 4 | User interaction and waiting |
| [State Capture](/api/frontend/state-capture) | 4 | Capture DOM, styles, storage |
| [Accessibility](/api/frontend/accessibility) | 5 | A11y inspection and auditing |
| [Composite](/api/frontend/composite) | 3 | High-level analysis |

## Quick Reference

### Most Used Functions

```javascript
// Comprehensive element analysis
window.__devtool.inspect('#element')

// Take a screenshot
window.__devtool.screenshot('name')

// Interactive element picker
window.__devtool.selectElement()

// Accessibility audit
window.__devtool.auditAccessibility()

// Find layout problems
window.__devtool.diagnoseLayout()

// Highlight an element
window.__devtool.highlight('#element', {color: 'red'})
```

### Element Information

```javascript
window.__devtool.getElementInfo('#el')    // Basic info
window.__devtool.getPosition('#el')       // Position/dimensions
window.__devtool.getBox('#el')            // Box model
window.__devtool.getComputed('#el', [...])  // Computed styles
window.__devtool.getLayout('#el')         // Layout properties
window.__devtool.getStacking('#el')       // Z-index context
```

### Visibility Checks

```javascript
window.__devtool.isVisible('#el')         // Is it visible?
window.__devtool.isInViewport('#el')      // Is it on screen?
window.__devtool.checkOverlap('#a', '#b') // Do elements overlap?
```

### Debugging

```javascript
window.__devtool.findOverflows()          // Find all overflows
window.__devtool.findStackingContexts()   // Find z-index contexts
window.__devtool.findOffscreen()          // Find hidden elements
```

### User Interaction

```javascript
window.__devtool.selectElement()          // Click to select
window.__devtool.ask('Question?', ['A', 'B'])  // Ask user
window.__devtool.waitForElement('.loading-done')  // Wait for element
```

## Async Functions

Most functions are synchronous. These return Promises:

| Function | Why Async |
|----------|-----------|
| `selectElement()` | Waits for user click |
| `ask(question, options)` | Waits for user input |
| `waitForElement(selector, timeout)` | Waits for DOM change |
| `screenshot(name)` | Async canvas operations |

Example:
```javascript
// In browser console
const result = await window.__devtool.selectElement()

// Via MCP exec (automatically awaits)
proxy {action: "exec", id: "app", code: "window.__devtool.selectElement()"}
```

## Performance

- All primitives complete in under 10ms on typical pages
- Synchronous operations are instant (under 1ms)
- No memory leaks - overlays are cleaned up
- Minimal DOM impact - read-only where possible

## Connection Status

The frontend connects via WebSocket to send metrics:

```javascript
window.__devtool.isConnected()
→ true

window.__devtool.getStatus()
→ {connected: true, reconnectAttempts: 0, lastMessage: "..."}
```

Auto-reconnects with exponential backoff if disconnected.

## Custom Logging

Send logs to the proxy:

```javascript
window.__devtool.log('message', 'info', {extra: 'data'})
window.__devtool.debug('debug message')
window.__devtool.info('info message')
window.__devtool.warn('warning')
window.__devtool.error('error', {stack: '...'})
```

Query via:
```json
proxylog {proxy_id: "app", types: ["custom"]}
```

## Error Handling

All functions handle errors gracefully:

```javascript
// Element not found
window.__devtool.getPosition('.missing')
→ {error: "Element not found", selector: ".missing"}

// Invalid selector
window.__devtool.getPosition('###invalid')
→ {error: "Invalid selector", selector: "###invalid"}

// Cannot compute
window.__devtool.getContrast('.gradient-bg')
→ {error: "Cannot compute contrast for gradient background"}
```

## Testing

Use `test-diagnostics.html` in the repository for interactive testing:
- Test buttons for each category
- Various layout patterns for testing
- Console examples

## Next Steps

- [Element Inspection](/api/frontend/element-inspection) - Basic element info
- [Accessibility](/api/frontend/accessibility) - A11y auditing
- [Composite Functions](/api/frontend/composite) - High-level analysis
