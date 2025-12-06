---
sidebar_position: 1
slug: /
---

# devtool-mcp

**devtool-mcp** is a powerful MCP (Model Context Protocol) server that provides comprehensive development tooling capabilities to AI assistants. It enables seamless project management, process control, and frontend debugging through a unified interface.

## What is devtool-mcp?

devtool-mcp bridges the gap between AI assistants and development workflows by providing:

- **Project Detection** - Automatically detect project types (Go, Node.js, Python) and available scripts
- **Process Management** - Start, monitor, and control long-running processes with output capture
- **Reverse Proxy** - Intercept HTTP traffic with automatic frontend instrumentation
- **Frontend Diagnostics** - 50+ primitives for DOM inspection, layout debugging, and accessibility auditing

## Why devtool-mcp?

Modern development workflows require AI assistants that can:

1. **Understand Your Project** - Automatically detect build systems, package managers, and available commands
2. **Run and Monitor Tasks** - Execute builds, tests, and dev servers with real-time output streaming
3. **Debug Frontend Issues** - Capture JavaScript errors, performance metrics, and inspect live DOM
4. **Interact with Users** - Take screenshots, highlight elements, and ask users questions in the browser

devtool-mcp provides all of this through a single, efficient MCP server.

## Key Features

### Intelligent Project Detection

```json
detect {path: "."}
→ {
    "type": "node",
    "package_manager": "pnpm",
    "scripts": ["dev", "build", "test", "lint"]
  }
```

### Robust Process Management

```json
run {script_name: "dev"}
→ Process started in background, accessible via proc tool

proc {action: "output", process_id: "dev", tail: 20}
→ Last 20 lines of output with real-time streaming
```

### Transparent Reverse Proxy

```json
proxy {action: "start", id: "app", target_url: "http://localhost:3000", port: 8080}
→ Proxy running with:
   - All HTTP traffic logged
   - Frontend errors captured
   - Performance metrics collected
   - 50+ diagnostic primitives injected
```

### Rich Frontend API

```javascript
// Execute in browser via proxy
window.__devtool.inspect('#my-button')
→ Complete analysis: position, styles, accessibility, stacking context

window.__devtool.screenshot('current-state')
→ Screenshot saved and available via proxylog

window.__devtool.auditAccessibility()
→ {errors: [...], warnings: [...], score: 85}
```

## Architecture

devtool-mcp is built with performance and reliability in mind:

- **Lock-Free Design** - Uses `sync.Map` and atomics for maximum concurrency
- **Graceful Shutdown** - Clean process termination with signal handling
- **Bounded Memory** - Ring buffers prevent unbounded output growth
- **Zero Dependencies** - Pure Go server with injected JavaScript requiring no external libraries

## Getting Started

Ready to get started? Head to the [Getting Started](/getting-started) guide to install and configure devtool-mcp.

## MCP Protocol

devtool-mcp implements the [Model Context Protocol](https://modelcontextprotocol.io) specification, making it compatible with any MCP-enabled AI assistant including Claude Code, Cursor, and others.

The server communicates over stdio and exposes six primary tools:

| Tool | Purpose |
|------|---------|
| `detect` | Project type and script detection |
| `run` | Execute scripts and commands |
| `proc` | Process management and output retrieval |
| `proxy` | Reverse proxy lifecycle management |
| `proxylog` | Query proxy traffic and metrics |
| `currentpage` | View active page sessions |

## License

devtool-mcp is open source software.
