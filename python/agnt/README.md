# agnt

MCP server for AI coding agents - process management, reverse proxy with traffic logging, browser instrumentation, and sketch mode.

## Installation

```bash
pip install agnt
# or
uv pip install agnt
```

## Quick Start

### As MCP Server (Claude Code, etc.)

Add to your Claude Code MCP configuration:

```json
{
  "mcpServers": {
    "agnt": {
      "command": "agnt",
      "args": ["serve"]
    }
  }
}
```

Or install as a Claude Code plugin:

```bash
/plugin marketplace add standardbeagle/agnt
/plugin install agnt@agnt
```

### As PTY Wrapper

Wrap your AI coding tool with overlay features:

```bash
agnt run claude --dangerously-skip-permissions
agnt run gemini
agnt run copilot
```

## Features

- **Project Detection**: Auto-detect Go, Node.js, Python projects
- **Process Management**: Run and manage long-running processes
- **Reverse Proxy**: HTTP proxy with traffic logging
- **Browser Instrumentation**: 50+ diagnostic primitives
- **Sketch Mode**: Excalidraw-like wireframing on your UI
- **Floating Indicator**: Quick access panel in browser

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

```
# Start a proxy for your dev server
proxy {action: "start", id: "dev", target_url: "http://localhost:3000"}

# Execute JavaScript in connected browsers
proxy {action: "exec", id: "dev", code: "__devtool.screenshot('homepage')"}

# Show toast notification
proxy {action: "toast", id: "dev", toast_message: "Build complete!", toast_type: "success"}
```

## Browser API

The proxy injects `window.__devtool` with 50+ functions:

- `screenshot(name)` - Capture screenshot
- `inspect(selector)` - Get element info
- `sketch.open()` / `sketch.save()` - Wireframe mode
- `indicator.toggle()` - Toggle floating indicator
- And many more...

## Configuration

Create `.agnt.kdl` in your project root:

```kdl
scripts {
    dev {
        command "npm"
        args "run" "dev"
        autostart true
    }
}

proxies {
    frontend {
        target "http://localhost:3000"
        autostart true
    }
}
```

## Documentation

- [GitHub](https://github.com/standardbeagle/agnt)
- [Documentation](https://standardbeagle.github.io/agnt/)

## License

MIT
