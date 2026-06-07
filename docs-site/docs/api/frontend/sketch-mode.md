---
sidebar_position: 15
---

# Sketch Mode

An Excalidraw-like wireframing overlay injected into the page. The developer draws shapes and UI primitives directly on top of the live page, then sends the sketch to the AI agent as a build instruction — "make it look like this."

## API

```javascript
window.__devtool.sketch.open()       // enter sketch mode
window.__devtool.sketch.close()      // exit
window.__devtool.sketch.toggle()     // toggle
window.__devtool.sketch.save()       // save and send to the agent
window.__devtool.sketch.setTool(t)   // select a drawing tool
window.__devtool.sketch.undo()       // undo last action
window.__devtool.sketch.redo()       // redo
window.__devtool.sketch.clear()      // erase everything
window.__devtool.sketch.toJSON()     // export the sketch as JSON
window.__devtool.sketch.fromJSON(j)  // import a sketch from JSON
window.__devtool.sketch.toDataURL()  // export as a PNG data URL
```

## Tools

Pass any of these to `setTool(name)`:

| Group | Tools |
|-------|-------|
| Shapes | `rectangle`, `ellipse`, `line`, `arrow`, `freedraw`, `text` |
| Wireframe primitives | `button`, `input`, `note`, `image` (placeholder) |

Full editing is supported: select, move, resize, delete.

## Keyboard Shortcuts

| Key | Action |
|-----|--------|
| `Escape` | Close sketch mode |
| `Delete` | Erase selected element |
| `Ctrl+Z` | Undo |
| `Ctrl+Shift+Z` | Redo |

## Sending to the Agent

`save()` logs a `sketch` event carrying the drawing (as structured JSON and/or a rendered image) and the page context. The agent receives it via the channel sink, PTY injection, or proxy log:

```json
proxylog {proxy_id: "dev", types: ["sketch"]}
```

## Export / Import

```javascript
// Persist a sketch
const data = window.__devtool.sketch.toJSON()

// Restore it later
window.__devtool.sketch.fromJSON(data)

// Get a PNG for embedding or comparison
const png = window.__devtool.sketch.toDataURL()
```

## See Also

- [Design Mode](/api/frontend/design-mode) — iterate on existing elements rather than drawing from scratch
- [Floating Indicator](/api/frontend/indicator) — where sketch mode is launched
