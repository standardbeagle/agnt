# agnt Plugin

MCP server providing AI coding agent capabilities for Claude Code.

## Features

- **Project Detection**: Auto-detect Go, Node.js, Python projects
- **Process Management**: Run and manage long-running processes
- **Reverse Proxy**: HTTP proxy with traffic logging
- **Browser Instrumentation**: 50+ diagnostic primitives
- **Sketch Mode**: Excalidraw-like wireframing on your UI
- **Floating Indicator**: Quick access panel in browser

## Installation

```bash
# Add the marketplace
/plugin marketplace add standardbeagle/agnt

# Install the plugin
/plugin install agnt@agnt
```

## Requirements

- Node.js 18+ (for npm installation)
- Or Go 1.24+ (for building from source)

### Via npm

```bash
npm install -g @standardbeagle/agnt
```

### Building from Source

```bash
git clone https://github.com/standardbeagle/agnt.git
cd agnt
make install
```

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

## License

MIT
