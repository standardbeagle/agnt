# Browser Features

Documentation for agnt's browser-side features. For core architecture, see [architecture.md](architecture.md).

## Floating Indicator

A draggable indicator that appears on proxied pages, providing quick access to agnt features.

**Default behavior**: Shown automatically on all proxied pages. Position and visibility are persisted in localStorage.

**API**:
```javascript
__devtool.indicator.show()        // Show indicator
__devtool.indicator.hide()        // Hide indicator
__devtool.indicator.toggle()      // Toggle visibility
__devtool.indicator.togglePanel() // Toggle expanded panel
```

**Panel features**:
- Text input for sending messages to AI agent
- Screenshot area selection
- Element selection for logging
- Quick access to sketch/design modes

Messages sent from the panel are logged as `panel_message` type.

## Sketch Mode

Excalidraw-like wireframing directly on top of your UI.

**Tools**: select, rectangle, ellipse, line, arrow, freedraw, text, note (sticky), button, input, image (placeholder), eraser

**API**:
```javascript
__devtool.sketch.open()              // Enter sketch mode
__devtool.sketch.close()             // Exit sketch mode
__devtool.sketch.setTool('rectangle') // Select tool
__devtool.sketch.save()              // Save and send to MCP
__devtool.sketch.toJSON()            // Export as JSON
__devtool.sketch.fromJSON(data)      // Import from JSON
__devtool.sketch.undo()              // Undo
__devtool.sketch.redo()              // Redo
__devtool.sketch.clear()             // Clear all
```

**Keyboard shortcuts** (in sketch mode):
- `Escape`: Close
- `Delete/Backspace`: Delete selected
- `Ctrl+Z/Ctrl+Y`: Undo/Redo
- `Ctrl+A`: Select all
- `Ctrl+C/V`: Copy/Paste

Sketches are logged as `sketch` type with JSON data and PNG image.

## Design Mode

AI-assisted UI iteration with live preview of design alternatives.

**Workflow**:
1. Call `__devtool.design.start()` or click "Design" in indicator
2. Hover to preview selectors, click to select element
3. AI receives context and generates alternatives
4. Navigate alternatives with Prev/Next
5. Refine with natural language chat

**API**:
```javascript
__devtool.design.start()               // Start selection mode
__devtool.design.stop()                // Exit design mode
__devtool.design.selectElement(el)     // Select programmatically
__devtool.design.next()                // Next alternative
__devtool.design.previous()            // Previous alternative
__devtool.design.addAlternative(html)  // Add alternative (for AI)
__devtool.design.chat(message)         // Send refinement request
__devtool.design.getState()            // Get current state
```

**Events sent to AI agent**:
- `design_state`: Initial context when element selected
- `design_request`: Request for new alternatives
- `design_chat`: Chat message about current design

## Session Recorder

Record and replay user interactions for bug reproduction and testing.

**Recorded events**: click, input, change, keydown, scroll, submit

**API**:
```javascript
__devtool.recorder.start()              // Start recording
__devtool.recorder.stop()               // Stop and save
__devtool.recorder.replay()             // Replay from start
__devtool.recorder.replay(null, {speed: 2}) // 2x speed
__devtool.recorder.replayFrom(10)       // Replay from event #10
__devtool.recorder.stopReplay()         // Stop replay
__devtool.recorder.getRecording()       // Get stored data
__devtool.recorder.getStatus()          // Get state
__devtool.recorder.clear()              // Clear recording
```

Recordings persist in sessionStorage across page refreshes.

## Tunnel Integration

Expose local dev servers publicly for mobile device testing.

**Supported providers**: Cloudflare (`cloudflared`), ngrok

**Setup**:
```bash
# 1. Start proxy on all interfaces
proxy {action: "start", id: "dev", target_url: "http://localhost:3000", bind_address: "0.0.0.0"}

# 2. Start tunnel
tunnel {action: "start", id: "dev", provider: "cloudflare", local_port: 12345, proxy_id: "dev"}

# 3. Check status
tunnel {action: "status", id: "dev"}
```

When `proxy_id` is specified, the tunnel auto-updates the proxy's `public_url` for correct URL rewriting.

**BrowserStack**: For automated mobile testing, use BrowserStack's official MCP server alongside agnt tunnels. See https://github.com/browserstack/mcp-server
