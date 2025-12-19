# agnt

**Give your AI coding agent browser superpowers.**

agnt is a new kind of tool designed for the age of AI-assisted development. It acts as a bridge between your AI coding agent and the browser, extending what's possible during vibe coding sessions.

## What Does It Do?

When you're in the flow with Claude Code, Cursor, or other AI tools, agnt lets your agent:

- **See what you see** - Screenshots, DOM inspection, visual debugging
- **Hear from you directly** - Send messages from browser to agent
- **Sketch ideas together** - Draw wireframes directly on your UI
- **Debug in real-time** - Capture errors, network traffic, performance
- **Extend its thinking window** - Structured data uses fewer tokens than descriptions

## Installation

```bash
pip install agnt
# or
uv pip install agnt
# or
pipx install agnt
```

## Quick Start

### As MCP Server (Claude Code, Cursor, etc.)

Add to your MCP configuration:

```json
{
  "mcpServers": {
    "agnt": {
      "command": "agnt",
      "args": ["mcp"]
    }
  }
}
```

Or with uvx:

```json
{
  "mcpServers": {
    "agnt": {
      "command": "uvx",
      "args": ["agnt", "mcp"]
    }
  }
}
```

### As PTY Wrapper

Wrap your AI coding tool with overlay features:

```bash
agnt run claude --dangerously-skip-permissions
agnt run cursor
agnt run aider
```

This adds a terminal overlay menu (Ctrl+P) and enables browser-to-terminal messaging.

## Core Features

### Browser Superpowers

Start a proxy and your agent gains eyes into the browser:

```
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}
```

Now your agent can:
- Take screenshots
- Inspect any element
- Audit accessibility
- See what you clicked

### Floating Indicator

Every proxied page gets a floating bug icon. Click to:
- Send messages to your agent
- Take area screenshots
- Select elements to log
- Open sketch mode

### Sketch Mode

Draw directly on your UI:
- Shapes: rectangles, circles, arrows, freehand
- Wireframes: buttons, inputs, sticky notes
- Save and send to agent instantly

### Real-Time Error Capture

JavaScript errors automatically captured and available to your agent - no more forgetting to mention them.

## MCP Tools

| Tool | Description |
|------|-------------|
| `detect` | Auto-detect project type and scripts |
| `run` | Run scripts or commands |
| `proc` | Manage processes |
| `proxy` | Reverse proxy with instrumentation |
| `proxylog` | Query traffic logs |
| `currentpage` | View page sessions |
| `daemon` | Manage background service |

## Browser API

The proxy injects `window.__devtool` with 50+ diagnostic functions:

```javascript
__devtool.screenshot('name')              // Capture screenshot
__devtool.inspect('#element')             // Full element analysis
__devtool.auditAccessibility()            // A11y audit
__devtool.sketch.open()                   // Enter sketch mode
__devtool.interactions.getLastClick()     // Last click details
```

## Configuration

Create `.agnt.kdl` in your project:

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
- [Full Docs](https://standardbeagle.github.io/agnt/)

## License

MIT
