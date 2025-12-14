# agnt Examples

This directory contains interactive demo pages for agnt's diagnostic features.

## Diagnostic CSS System Demo

`diagnostics-demo.html` demonstrates the comprehensive diagnostic CSS system for visual debugging.

### Quick Start

1. **Start a simple HTTP server:**
   ```bash
   cd examples
   python3 -m http.server 8000
   ```

2. **Start the agnt proxy:**
   ```bash
   agnt proxy start --id dev --target http://localhost:8000
   ```

3. **Open the demo page through the proxy:**
   ```
   http://localhost:{proxy_port}/diagnostics-demo.html
   ```
   (The proxy port is shown in the proxy start output)

4. **Test diagnostic modes:**
   - Click any button to enable a diagnostic mode
   - Try "Outline All" to see nested element boundaries (Pesticide mode)
   - Use "Show Typography Panel" to analyze text style consistency
   - Test "Show Grid" and "Show Flexbox" to visualize layouts
   - Open browser console for detailed results

### Available Diagnostic Modes

**Structure & Layout:**
- `outlineAll()` - Pesticide mode with nested outlines by depth
- `showSemanticElements()` - Color-code elements by HTML tag
- `showContainers()` - Highlight container elements
- `showGrid()` - Visualize CSS Grid layouts
- `showFlexbox()` - Visualize Flexbox layouts
- `showGaps()` - Highlight grid/flex gaps and gutters

**Typography:**
- `showTypographyPanel()` - Side-by-side comparison of all text styles
- `highlightInconsistentText()` - Highlight one-off font sizes
- `showTextBounds()` - Show text element boundaries

**Stacking & Layering:**
- `showStacking()` - Visualize z-index stacking contexts
- `opacity(0.5)` - Global opacity for layer visualization
- `showPositioned()` - Highlight absolute/fixed/sticky elements

**Interactive Elements:**
- `showInteractive()` - Highlight clickable elements
- `showFocusOrder()` - Number elements by tab order
- `showClickTargets()` - Enforce 44×44px minimum touch targets

**Responsive & Analysis:**
- `showViewportInfo()` - Display viewport dimensions
- `showColorPalette()` - Extract and display all colors used
- `showSpacingScale()` - Analyze all margin/padding values

**Control:**
- `list()` - Show active diagnostic modes
- `clear(mode)` - Remove specific diagnostic mode
- `clearAll()` - Remove all diagnostics

### Using in Your Projects

```javascript
// Enable multiple diagnostics for layout debugging
__devtool.diagnostics.outlineAll()
__devtool.diagnostics.showGrid()
__devtool.diagnostics.showGaps()

// Typography consistency check
const typo = __devtool.diagnostics.showTypographyPanel()
console.log(`Found ${typo.uniqueStyles} unique text styles`)

// Clear specific mode
__devtool.diagnostics.clear('outline-all')

// Clear everything
__devtool.diagnostics.clearAll()
```

### AI Agent Workflow

```
You: "The card grid spacing looks off"

Claude: *enables grid and gaps diagnostics*
Claude: *takes screenshot*
Claude: "I can see the grid-gap is inconsistent - some cards have 20px, others 15px"
Claude: *fixes CSS*
Claude: *enables diagnostics again and takes another screenshot*
Claude: "Fixed - all gaps now consistent at 20px"
```

---

## Visual Regression Testing Demo

`visual-regression-demo.html` demonstrates the visual regression testing feature.

### Quick Start

1. **Start a simple HTTP server:**
   ```bash
   cd examples
   python3 -m http.server 8000
   ```

2. **Start the agnt proxy:**
   ```bash
   agnt proxy start --id dev --target http://localhost:8000
   ```

3. **Open the demo page through the proxy:**
   ```
   http://localhost:{proxy_port}/visual-regression-demo.html
   ```
   (The proxy port is shown in the proxy start output)

4. **Test the feature:**
   - Click "Create Baseline" to capture the current state
   - Make visual changes using the modification buttons
   - Click "Compare to Baseline" to detect changes
   - Check the browser console for detailed results

### What It Demonstrates

The demo page shows:
- Creating a baseline snapshot
- Making visual changes (colors, sizes, adding elements)
- Comparing current state to baseline
- Detecting unexpected visual regressions

### Using in Real Projects

In your own application:

```javascript
// Before making changes
await __devtool.snapshot.createBaseline('before-refactor');

// Make changes...

// After changes
await __devtool.snapshot.compareToBaseline('before-refactor');
```

### With AI Agents

The real power comes when your AI agent uses this:

```
You: "Refactor the header component"

Claude: *creates baseline*
Claude: *refactors code*
Claude: *compares to baseline*
Claude: "Changes verified - only header affected as expected"
```

## Browser API Reference

### `__devtool.snapshot.createBaseline(name)`
Captures current page as a baseline.

```javascript
await __devtool.snapshot.createBaseline('before-css-update');
```

### `__devtool.snapshot.compareToBaseline(baselineName)`
Compares current page to a baseline.

```javascript
await __devtool.snapshot.compareToBaseline('before-css-update');
```

### `__devtool.snapshot.quickBaseline()`
Creates a baseline with auto-generated name.

```javascript
await __devtool.snapshot.quick();
// Creates: 'quick-2025-12-13T22-30-45'
```

### `__devtool.snapshot.captureCurrentPage()`
Returns PageCapture object for manual use.

```javascript
const page = await __devtool.snapshot.captureCurrentPage();
console.log(page);
// { url: "/", viewport: {width: 1920, height: 1080}, screenshot_data: "base64..." }
```

## MCP Tool Usage

When using the MCP tool directly:

```javascript
// Via MCP
await mcp.callTool('snapshot', {
  action: 'baseline',
  name: 'my-baseline',
  pages: [
    {
      url: '/',
      viewport: { width: 1920, height: 1080 },
      screenshot_data: 'base64_encoded_png_data...'
    }
  ]
});

await mcp.callTool('snapshot', {
  action: 'compare',
  baseline: 'my-baseline',
  pages: [/* current screenshots */]
});
```

## Troubleshooting

### "__devtool is not defined"
- Make sure you're accessing the page through the agnt proxy
- Check that the proxy is running and configured correctly

### "html2canvas not loaded"
- The proxy should inject html2canvas automatically
- Check browser console for script loading errors

### "Baseline not found"
- Baselines are stored in `~/.agnt/baselines/`
- Use `snapshot list` to see available baselines
- Make sure you created the baseline first

### Screenshots look wrong
- html2canvas has limitations with some CSS features
- Complex animations may not capture correctly
- Cross-origin images require CORS headers

## Advanced Usage

### Multiple Pages

```javascript
// Capture multiple pages for comprehensive testing
const pages = [];

// Navigate to each page and capture
for (const url of ['/', '/about', '/contact']) {
  // Navigate to page
  window.location.href = url;
  await new Promise(r => setTimeout(r, 1000)); // Wait for load

  const page = await __devtool.snapshot.captureCurrentPage();
  pages.push(page);
}

// Create baseline with all pages
await mcp.callTool('snapshot', {
  action: 'baseline',
  name: 'full-site',
  pages: pages
});
```

### Custom Thresholds

```javascript
// Very strict comparison (0.1% tolerance)
await mcp.callTool('snapshot', {
  action: 'baseline',
  name: 'strict-baseline',
  pages: pages,
  diff_threshold: 0.001
});
```

## Next Steps

- See `docs/visual-regression-usage.md` for complete usage guide
- See `VISUAL_REGRESSION_SPEC.md` for technical specification
- Future: CI/CD integration, multiple viewports, Claude vision analysis
