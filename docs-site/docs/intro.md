---
sidebar_position: 1
slug: /
---

# agnt

**agnt** gives your AI coding agent browser superpowers. It's a powerful MCP (Model Context Protocol) server that bridges the gap between AI assistants and browser-based development workflows.

## What is agnt?

agnt enables AI coding agents to:

- **See What You See** - Take screenshots, inspect DOM, capture visual state
- **Debug in Real-Time** - Capture JavaScript errors, performance metrics, and network traffic
- **Receive Browser Messages** - Floating indicator lets users send messages directly from the browser
- **Sketch Ideas Together** - Excalidraw-like wireframing directly on your UI
- **Iterate on Designs** - AI-assisted UI design with live preview of alternatives

Plus traditional development tooling:
- **Project Detection** - Automatically detect project types (Go, Node.js, Python) and available scripts
- **Process Management** - Start, monitor, and control long-running processes with output capture
- **Reverse Proxy** - Intercept HTTP traffic with automatic frontend instrumentation

## Why agnt?

Modern AI-assisted development requires agents that can:

1. **Understand Your Project** - Automatically detect build systems, package managers, and available commands
2. **Run and Monitor Tasks** - Execute builds, tests, and dev servers with real-time output streaming
3. **Debug Frontend Issues** - Capture JavaScript errors, performance metrics, and inspect live DOM
4. **Interact with Users** - Take screenshots, highlight elements, and receive messages from the browser
5. **Audit Quality** - Run accessibility, security, SEO, and layout robustness audits

agnt provides all of this through a single, efficient MCP server.

## Key Features

### Browser Superpowers

```javascript
// Execute in browser via proxy
window.__devtool.screenshot('current-state')
→ Screenshot saved and available via proxylog

window.__devtool.inspect('#my-button')
→ Complete analysis: position, styles, accessibility, stacking context

window.__devtool.auditAccessibility()
→ {errors: [...], warnings: [...], score: 85}

window.__devtool.checkTextFragility()
→ {issues: [...], summary: {errors: 2, warnings: 3}}

window.__devtool.checkResponsiveRisk()
→ {issues: [...], summary: {errors: 1, warnings: 5}}
```

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
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}
→ Proxy running on port 45849 with:
   - Stable port based on target URL (same URL always gets same port)
   - All HTTP traffic logged
   - Frontend errors captured
   - Performance metrics collected
   - 50+ diagnostic primitives injected
   - Floating indicator for browser-to-agent messaging
```

## Architecture

agnt is built with performance and reliability in mind:

- **Daemon Architecture** - Persistent state survives client disconnections
- **Lock-Free Design** - Uses `sync.Map` and atomics for maximum concurrency
- **Graceful Shutdown** - Clean process termination with signal handling
- **Bounded Memory** - Ring buffers prevent unbounded output growth
- **Zero Dependencies** - Pure Go server with injected JavaScript requiring no external libraries

## Getting Started

Ready to get started? Head to the [Getting Started](/getting-started) guide to install and configure agnt.

## MCP Protocol

agnt implements the [Model Context Protocol](https://modelcontextprotocol.io) specification, making it compatible with any MCP-enabled AI assistant including Claude Code, Cursor, and others.

The server communicates over stdio and exposes eight primary tools:

| Tool | Purpose |
|------|---------|
| `detect` | Project type and script detection |
| `run` | Execute scripts and commands |
| `proc` | Process management and output retrieval |
| `proxy` | Reverse proxy lifecycle management |
| `proxylog` | Query proxy traffic and metrics |
| `currentpage` | View active page sessions |
| `tunnel` | Tunnel management for mobile testing |
| `daemon` | Daemon status and management |

## License

agnt is open source software.
