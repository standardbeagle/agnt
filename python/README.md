# devtool-mcp

mcp-name: io.github.standardbeagle/devtool-mcp

An MCP (Model Context Protocol) server that provides comprehensive development tooling capabilities to AI assistants.

## Features

- **Project Detection** - Automatically detect project types (Go, Node.js, Python) and available scripts
- **Process Management** - Start, monitor, and control long-running processes with output capture
- **Reverse Proxy** - Intercept HTTP traffic with automatic frontend instrumentation
- **Frontend Diagnostics** - 50+ primitives for DOM inspection, layout debugging, and accessibility auditing

## Installation

```bash
# Using pip
pip install devtool-mcp

# Using uv
uv pip install devtool-mcp

# Using pipx (recommended for CLI tools)
pipx install devtool-mcp
```

## Usage

Add to your MCP client configuration:

```json
{
  "mcpServers": {
    "devtool": {
      "command": "devtool-mcp"
    }
  }
}
```

Or if using uvx:

```json
{
  "mcpServers": {
    "devtool": {
      "command": "uvx",
      "args": ["devtool-mcp"]
    }
  }
}
```

## Documentation

For full documentation, visit: https://standardbeagle.github.io/devtool-mcp/

## MCP Tools

| Tool | Description |
|------|-------------|
| `detect` | Detect project type and available scripts |
| `run` | Execute scripts or raw commands |
| `proc` | Monitor, query output, stop processes |
| `proxy` | Manage reverse proxies |
| `proxylog` | Query traffic logs and errors |
| `currentpage` | View grouped page sessions |
| `daemon` | Manage the background daemon |

## License

MIT
