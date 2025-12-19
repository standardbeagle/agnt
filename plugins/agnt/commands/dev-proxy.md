---
description: "Start a dev server with reverse proxy for browser debugging"
allowed-tools: ["mcp__agnt__detect", "mcp__agnt__run", "mcp__agnt__proxy", "mcp__agnt__proc"]
---

Start a development server with the agnt reverse proxy for browser debugging.

## Steps

1. First, detect the project type to find available scripts:
   ```
   detect {path: "."}
   ```

2. Start the dev server in background mode (look for "dev", "start", or "serve" scripts):
   ```
   run {script_name: "dev", mode: "background"}
   ```

3. Wait 3-5 seconds for the server to start, then start the proxy:
   ```
   proxy {action: "start", id: "dev", target_url: "http://localhost:3000"}
   ```
   Note: Adjust the port based on what the dev server actually uses.

4. Report the proxy URL to the user so they can open it in their browser.

5. Explain that the proxy provides:
   - JavaScript error capture
   - Performance metrics
   - DOM mutation tracking
   - User interaction logging
   - The `__devtool` API for diagnostics
   - Floating indicator for sending messages to the agent
   - Sketch mode for wireframing

The user can now open the proxy URL in their browser to get browser superpowers.
