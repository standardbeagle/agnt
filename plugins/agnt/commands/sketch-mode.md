---
description: "Open sketch mode for wireframing on the browser page"
allowed-tools: ["mcp__agnt__proxy", "mcp__agnt__proxylog"]
---

Open the sketch mode overlay for creating wireframes directly on the browser page.

## Steps

1. Open sketch mode in the browser:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.sketch.open()"}
   ```

2. Inform the user that sketch mode is now active with these tools:
   - **select**: Select and move elements
   - **rectangle**: Draw rectangles
   - **ellipse**: Draw ellipses/circles
   - **line**: Draw straight lines
   - **arrow**: Draw arrows
   - **freedraw**: Free-hand drawing
   - **text**: Add text labels
   - **note**: Sticky note (Balsamiq-style)
   - **button**: Button wireframe element
   - **input**: Input field wireframe
   - **image**: Image placeholder
   - **eraser**: Delete elements

3. Keyboard shortcuts in sketch mode:
   - `Escape`: Close sketch mode
   - `Delete/Backspace`: Delete selected elements
   - `Ctrl+Z`: Undo
   - `Ctrl+Shift+Z` or `Ctrl+Y`: Redo
   - `Ctrl+A`: Select all
   - `Ctrl+C`: Copy selection
   - `Ctrl+V`: Paste

4. When the user is done sketching, they can:
   - Click "Save & Send" to save the sketch and send it to the agent
   - Or you can save programmatically:
     ```
     proxy {action: "exec", id: "dev", code: "__devtool.sketch.save()"}
     ```

5. To retrieve saved sketches:
   ```
   proxylog {proxy_id: "dev", types: ["sketch"], limit: 5}
   ```

Note: Sketch mode creates an Excalidraw-like overlay. The sketchy rendering uses configurable roughness for a hand-drawn look.
