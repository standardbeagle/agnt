---
sidebar_position: 2
---

# Getting Started

This guide will help you install, configure, and start using devtool-mcp with your AI assistant.

## Prerequisites

- **Go 1.24.2** or later
- An MCP-compatible AI assistant (Claude Code, Cursor, etc.)

## Installation

### From Source

```bash
# Clone the repository
git clone https://github.com/devtool-mcp/devtool-mcp.git
cd devtool-mcp

# Build the binary
make build

# Or install to $GOPATH/bin
make install
```

### Verify Installation

```bash
./devtool-mcp --version
# devtool-mcp version 0.1.0
```

## Configuration

### Claude Code

Add devtool-mcp to your Claude Code configuration (`.claude/settings.json`):

```json
{
  "mcpServers": {
    "devtool": {
      "command": "/path/to/devtool-mcp"
    }
  }
}
```

### Cursor

Add to your Cursor MCP settings:

```json
{
  "mcpServers": {
    "devtool": {
      "command": "/path/to/devtool-mcp"
    }
  }
}
```

### Other MCP Clients

devtool-mcp communicates over stdio. Configure your MCP client to spawn the binary and communicate over stdin/stdout.

## Quick Start

Once configured, your AI assistant has access to all devtool-mcp tools. Here's a typical workflow:

### 1. Detect Your Project

```json
detect {path: "."}
```

Returns your project type, package manager, and available scripts:

```json
{
  "type": "node",
  "package_manager": "pnpm",
  "name": "my-app",
  "scripts": ["dev", "build", "test", "lint", "typecheck"]
}
```

### 2. Start Your Dev Server

```json
run {script_name: "dev"}
```

Starts the dev server in the background. The AI can now:

- Check status: `proc {action: "status", process_id: "dev"}`
- View output: `proc {action: "output", process_id: "dev", tail: 50}`
- Stop it: `proc {action: "stop", process_id: "dev"}`

### 3. Set Up Proxy for Debugging

```json
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}
```

The proxy auto-assigns a stable port based on the target URL (check `listen_addr` in response). The proxy:

- Logs all HTTP requests and responses
- Captures frontend JavaScript errors
- Collects performance metrics
- Injects the `window.__devtool` API

### 4. Debug Frontend Issues

Query captured data:

```json
// View recent HTTP traffic
proxylog {proxy_id: "app", types: ["http"], limit: 20}

// Check for JavaScript errors
proxylog {proxy_id: "app", types: ["error"]}

// See page load performance
proxylog {proxy_id: "app", types: ["performance"]}
```

Execute browser commands:

```json
// Take a screenshot
proxy {action: "exec", id: "app", code: "window.__devtool.screenshot('debug')"}

// Inspect an element
proxy {action: "exec", id: "app", code: "window.__devtool.inspect('#problem-button')"}

// Run accessibility audit
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility()"}
```

## Example Session

Here's a complete debugging session:

```
User: "My app has a layout bug on the dashboard page"

AI: Let me help debug that. First, I'll check your project setup.

[detect {path: "."}]
→ Node.js project with pnpm, scripts: dev, build, test

AI: I'll start the dev server and set up debugging.

[run {script_name: "dev"}]
→ Dev server started on port 3000

[proxy {action: "start", id: "app", target_url: "http://localhost:3000"}]
→ Proxy running on port 45849

AI: Please open http://localhost:45849/dashboard in your browser.
    Once loaded, I'll analyze the layout.

[User navigates to the page]

[proxy {action: "exec", id: "app", code: "window.__devtool.diagnoseLayout()"}]
→ Found 2 overflow issues, 1 stacking context problem

[proxy {action: "exec", id: "app", code: "window.__devtool.findOverflows()"}]
→ Element .sidebar has horizontal overflow (1024px content in 250px container)

AI: I found the issue. The .sidebar element has horizontal overflow.
    Let me highlight it for you.

[proxy {action: "exec", id: "app", code: "window.__devtool.highlight('.sidebar', {color: 'rgba(255,0,0,0.3)'})"}]

AI: The red highlighted area shows the overflow. The sidebar content
    is 1024px wide but the container is only 250px.

    Should I fix the CSS?
```

## Next Steps

- Learn about [Project Detection](/features/project-detection)
- Master [Process Management](/features/process-management)
- Explore the [Reverse Proxy](/features/reverse-proxy)
- Deep dive into [Frontend Diagnostics](/features/frontend-diagnostics)
