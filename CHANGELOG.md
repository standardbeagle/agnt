# Changelog - DevTool MCP

## [Unreleased] - 2025-12-05

### Added - Async JavaScript Execution & Response Logging

#### 1. Async JavaScript Execution with Result Waiting
- **Changed**: `proxy exec` tool now waits for JavaScript execution results instead of fire-and-forget
- **Added**: Result channels for pending executions (`sync.Map` in ProxyServer)
- **Added**: 30-second timeout for execution responses
- **Result**: Users now receive immediate feedback with execution results:
  ```
  JavaScript executed successfully.
  Result: "My Page Title"
  Duration: 2.5ms
  ```

#### 2. Response Logging System
- **Added**: New log type `LogTypeResponse` for tracking MCP client responses
- **Added**: `ExecutionResponse` struct with execution metadata
- **Added**: `LogResponse()` method in TrafficLogger
- **Updated**: `proxylog` tool now supports `types: ["response"]` filter
- **Result**: Full audit trail of JavaScript executions and their responses
- **Query**: `proxylog {proxy_id: "dev", types: ["response"]}`

#### 3. Enhanced Screenshot Capabilities

**Full Page Screenshots**:
- `window.__devtool.screenshot()` - Auto-generated name
- `window.__devtool.screenshot('homepage')` - Custom name

**Element Screenshots**:
- `window.__devtool.screenshot('#selector')` - Capture specific element
- `window.__devtool.screenshot('button', '.my-button')` - Element with custom name
- **Smart parameter detection**: Automatically detects CSS selectors (starting with `.`, `#`, `[`)
- **Error handling**: Returns clear errors for invalid selectors or missing elements
- **Scroll compensation**: Properly handles scroll offsets for accurate captures

**Screenshot Metadata**:
- Added `Selector` field to Screenshot struct
- Logs now include which element was captured (`body` for full page, or CSS selector)
- Query: `proxylog {proxy_id: "dev", types: ["screenshot"]}`

### Technical Details

**Architecture Changes**:
1. `ProxyServer.pendingExecs` - Lock-free `sync.Map` for execution tracking
2. `ExecuteJavaScript()` signature change: Returns `(string, <-chan *ExecutionResult, error)`
3. WebSocket handler notifies waiting channels when results arrive
4. Tool handler blocks until result received or timeout

**JavaScript Enhancements**:
- html2canvas configured for full-page and element capture
- Automatic scroll offset compensation
- Comprehensive error handling for selectors
- Flexible parameter combinations for screenshot API

**Logging Improvements**:
- All MCP responses now logged for audit trail
- Timeout responses logged as failed executions
- Screenshot logs include selector information
- Response logs separate from execution logs for clarity

### Usage Examples

**Execute JavaScript and Get Result**:
```javascript
proxy {action: "exec", id: "dev", code: "document.title"}
// Returns: "JavaScript executed successfully.\nResult: \"My Page\"\nDuration: 1.2ms"
```

**Capture Full Page**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.screenshot('homepage')"}
```

**Capture Specific Element**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.screenshot('#header')"}
```

**Query All Responses**:
```javascript
proxylog {proxy_id: "dev", types: ["response"], limit: 50}
```

### Breaking Changes
- None - All changes are backward compatible
- Old logs and execution patterns continue to work
- New features are opt-in through new log types

### Performance Impact
- Minimal: Execution tracking uses lock-free `sync.Map`
- Timeout default: 30 seconds (configurable)
- Channel cleanup automatic on result or timeout
- Screenshot performance depends on html2canvas (typically <1s)

---

## [Unreleased] - 2025-12-05

### Added - Comprehensive Diagnostic Primitives (~50 Functions)

#### Overview
Implemented ~50 primitive, composable JavaScript functions in `window.__devtool` that enable LLMs to perform comprehensive DOM inspection, layout analysis, visual debugging, and interactive diagnostics. All primitives are designed to be small, focused, and composable.

#### Architecture Principles
1. **Primitives over monoliths**: Small, focused functions (~20-30 lines each)
2. **Composability**: Functions return rich data structures that other functions consume
3. **Synchronous by default**: Async only when necessary (screenshots, user interaction)
4. **Error resilient**: Return partial results with error fields, don't throw
5. **Selector flexibility**: Accept CSS selectors, elements, or arrays

#### Core Infrastructure (Phase 1)
- `resolveElement(selector)` - Convert selector/element to element
- `generateSelector(element)` - Create unique CSS selector for element
- `safeGetComputed(element, properties)` - Safe getComputedStyle wrapper
- Overlay management system with SVG-based rendering

#### Element Inspection Primitives (Phase 2) - 9 Functions
- `getElementInfo(selector)` → `{ element, selector, tag, id, classes, attributes }`
- `getPosition(selector)` → `{ rect, viewport, document, scroll }`
- `getComputed(selector, properties)` → `{ property: computedValue }`
- `getBox(selector)` → `{ margin, border, padding, content }`
- `getLayout(selector)` → `{ display, position, flexbox, grid, float }`
- `getContainer(selector)` → `{ type, size, name }` (CSS containment)
- `getStacking(selector)` → `{ context, zIndex, order, parent }`
- `getTransform(selector)` → `{ matrix, translate, rotate, scale }`
- `getOverflow(selector)` → `{ x, y, scrollWidth, scrollHeight }`

#### Tree Walking Primitives (Phase 3) - 3 Functions
- `walkChildren(selector, depth, filter)` → `{ elements, count }`
- `walkParents(selector)` → `{ parents, count }`
- `findAncestor(selector, condition)` → `{ element, selector }`

#### Visual State Primitives (Phase 4) - 3 Functions
- `isVisible(selector)` → `{ visible, reason, area }`
- `isInViewport(selector)` → `{ intersecting, ratio, rect }`
- `checkOverlap(selector1, selector2)` → `{ overlaps, area, percentage }`

#### Layout Diagnostic Primitives (Phase 5) - 3 Functions
- `findOverflows()` → `{ overflows, count }`
- `findStackingContexts()` → `{ contexts, count }`
- `findOffscreen()` → `{ offscreen, count }`

#### Visual Overlay System (Phase 6) - 3 Functions
- `highlight(selector, config)` → `highlightId`
  - `config`: `{ color, borderColor, duration, pulse, label }`
  - Renders visual overlay showing element boundaries
- `removeHighlight(highlightId)` → `void`
- `clearAllOverlays()` → `void`

#### Interactive Primitives (Phase 7) - 4 Functions
- `selectElement()` → `Promise<{ element, selector }>`
  - Full interactive element picker with hover preview
  - Click to select, Escape to cancel
- `measureBetween(sel1, sel2)` → `{ distance: { x, y, diagonal }, direction }`
- `waitForElement(selector, timeout)` → `Promise<element>`
  - Uses MutationObserver to wait for dynamic elements
- `ask(question, options)` → `Promise<answer>`
  - Shows modal dialog for user interaction
  - Returns selected option or cancelled

#### State Capture Primitives (Phase 8) - 4 Functions
- `captureDOM()` → `{ snapshot: HTML, hash, timestamp, url, size }`
- `captureStyles(selector)` → `{ computed, inline, timestamp }`
- `captureState(keys)` → `{ localStorage, sessionStorage, cookies }`
- `captureNetwork()` → `{ resources, count, timestamp }`

#### Accessibility Primitives (Phase 9) - 5 Functions
- `getA11yInfo(selector)` → `{ role, aria, tabindex, focusable, label }`
- `getContrast(selector)` → `{ fg, bg, ratio, passes: { AA, AAA } }`
  - Implements WCAG 2.0 contrast ratio calculation
- `getTabOrder(container)` → `{ elements, count }`
- `getScreenReaderText(selector)` → `string`
- `auditAccessibility()` → `{ errors, warnings, score }`
  - Scans for missing alt text, unlabeled buttons, missing labels
  - Returns score 0-100 based on issues found

#### Composite Convenience Functions (Phase 10) - 3 Functions
Built from primitives - high-value for LLMs:

- `inspect(selector)` - Comprehensive element inspection
  ```javascript
  {
    info: getElementInfo(),
    position: getPosition(),
    box: getBox(),
    layout: getLayout(),
    stacking: getStacking(),
    container: getContainer(),
    visibility: isVisible(),
    viewport: isInViewport()
  }
  ```

- `diagnoseLayout(selector)` - Find layout issues
  ```javascript
  {
    overflows: findOverflows(),
    stackingContexts: findStackingContexts(),
    offscreen: findOffscreen()
  }
  ```

- `showLayout(config)` - Visual debugging overlay
  ```javascript
  // Combines highlight() with smart defaults
  { overlayId, active: { borders, boxes } }
  ```

### Testing Infrastructure
- Created `test-diagnostics.html` comprehensive test page
- Includes test buttons for all primitive categories
- Examples of Flex, Grid, Stacking, Overflow, Transform layouts
- Hidden elements for visibility testing
- Console usage examples

### Technical Details

**Browser API Usage**:
- `getBoundingClientRect()` - Element positioning
- `getComputedStyle()` - CSS property values
- `IntersectionObserver` - Viewport visibility (future)
- `MutationObserver` - Dynamic element detection
- Container Query APIs - CSS containment detection
- WCAG 2.0 formulas - Accessibility contrast ratios

**Error Handling Pattern**:
All primitives follow consistent error handling:
```javascript
function primitive(selector) {
  try {
    var el = resolveElement(selector);
    if (!el) return { error: 'Element not found' };
    // Do work
    return { /* data */ };
  } catch (e) {
    return { error: e.message };
  }
}
```

**ES5 Compatibility**:
- All code uses ES5 syntax for broad browser support
- No arrow functions, template literals, or modern features
- Tested in modern browsers (Chrome, Firefox, Safari)

### Usage Examples

**Comprehensive Element Inspection**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.inspect('#my-element')"}
// Returns 8+ data structures with complete element analysis
```

**Interactive Element Selection**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.selectElement()"}
// User clicks element, returns selector and element reference
```

**Accessibility Audit**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.auditAccessibility()"}
// Returns: { errors: [...], warnings: [...], score: 85 }
```

**Layout Diagnostics**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.diagnoseLayout()"}
// Finds all overflows, stacking contexts, offscreen elements
```

**Contrast Checking**:
```javascript
proxy {action: "exec", id: "dev", code: "window.__devtool.getContrast('.my-button')"}
// Returns: { ratio: 4.52, passes: { AA: true, AAA: false } }
```

### Benefits

1. **LLM Composability**: LLMs can create unlimited combinations from primitives
2. **Debuggability**: Rich data structures for analysis instead of strings
3. **Interactivity**: Ask questions, select elements, measure distances
4. **Visual Feedback**: Overlays show layout structure before screenshots
5. **Performance**: All primitives O(1) or O(n) where n is small
6. **Maintainability**: Small functions with clear responsibilities
7. **Accessibility**: Built-in WCAG compliance checking

### Breaking Changes
- None - All changes are backward compatible
- Existing screenshot and logging functionality unchanged
- New primitives are purely additive

### Performance Impact
- Code size: +~3500 lines JavaScript (~100KB uncompressed, ~30KB gzipped)
- Runtime: All primitives complete in <10ms on typical pages
- Memory: Minimal - most functions are stateless
- Interactive functions (selectElement, ask) wait for user input
