# agnt Plugin

**Give your AI coding agent browser superpowers.**

MCP server plugin for Claude Code that bridges your AI agent and the browser, extending what's possible during vibe coding sessions.

## Features

- **Browser Superpowers** - Screenshots, DOM inspection, visual debugging
- **Floating Indicator** - Send messages from browser to agent
- **Sketch Mode** - Draw wireframes directly on your UI
- **Real-Time Errors** - Capture JS errors automatically
- **Process Management** - Run and manage dev servers
- **Token Efficiency** - Structured data uses fewer tokens than descriptions

## Installation

### From Marketplace

```bash
# Add the marketplace
/plugin marketplace add standardbeagle/agnt

# Install the plugin
/plugin install agnt@agnt
```

### Manual Installation

```bash
# Clone and install from source
git clone https://github.com/standardbeagle/agnt.git
cd agnt
make install
```

Or install via npm:

```bash
npm install -g @standardbeagle/agnt
```

## Slash Commands

| Command | Description |
|---------|-------------|
| `/dev-proxy` | Start a dev server with reverse proxy for browser debugging |
| `/check-errors` | Check for JavaScript errors in the browser |
| `/screenshot` | Take a screenshot of the current browser page |
| `/sketch-mode` | Open sketch mode for wireframing on the browser page |
| `/browser-debug` | Debug browser issues using agnt diagnostic tools |
| `/stop-all` | Stop all running processes and proxies |

## Subagents

| Agent | Description |
|-------|-------------|
| `browser-debugger` | Specialized agent for debugging browser issues |
| `process-manager` | Specialized agent for managing development processes |
| `ui-designer` | Specialized agent for UI design feedback and wireframing |

## MCP Tools

| Tool | Description |
|------|-------------|
| `detect` | Detect project type and available scripts |
| `run` | Run scripts or commands |
| `proc` | Manage processes: status, output, stop, list |
| `proxy` | Reverse proxy: start, stop, exec, toast |
| `proxylog` | Query proxy traffic logs |
| `currentpage` | View active page sessions |
| `daemon` | Manage background daemon |

## Quick Start

1. Start a dev server with proxy:
   ```
   /dev-proxy
   ```

2. Open the proxy URL in your browser (shown in output)

3. Check for errors:
   ```
   /check-errors
   ```

4. Take a screenshot:
   ```
   /screenshot
   ```

5. Open sketch mode for wireframing:
   ```
   /sketch-mode
   ```

## Usage Examples

### Start a Dev Server with Proxy

```
run {script_name: "dev", mode: "background"}
proxy {action: "start", id: "dev", target_url: "http://localhost:3000"}
proxylog {proxy_id: "dev", types: ["http", "error"]}
```

### Execute JavaScript in Browser

```
proxy {action: "exec", id: "dev", code: "__devtool.screenshot('homepage')"}
proxy {action: "toast", id: "dev", toast_message: "Done!", toast_type: "success"}
proxy {action: "exec", id: "dev", code: "__devtool.sketch.open()"}
```

## Browser API

The proxy injects `window.__devtool` into all proxied pages:

- `screenshot(name)` - Capture screenshot
- `log(message, level, data)` - Send custom log
- `inspect(selector)` - Get element info
- `interactions.getLastClickContext()` - Get last click details
- `mutations.highlightRecent(ms)` - Highlight recent DOM changes
- `sketch.open()` / `sketch.save()` - Wireframe mode
- `indicator.toggle()` - Toggle floating indicator
- And 40+ more diagnostic functions

## Keyboard Shortcuts

When running with `agnt run`:
- `Ctrl+P`: Toggle overlay menu

## Configuration

Example MCP configuration (`.mcp.json`):

```json
{
  "agnt": {
    "command": "agnt",
    "args": ["serve"],
    "env": {}
  }
}
```

## License

MIT
