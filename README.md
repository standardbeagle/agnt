# devtool-mcp

An MCP (Model Context Protocol) server that provides comprehensive development tooling capabilities to AI assistants.

[![Go Version](https://img.shields.io/badge/Go-1.24.2-blue.svg)](https://go.dev/)
[![MCP](https://img.shields.io/badge/MCP-Compatible-green.svg)](https://modelcontextprotocol.io)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## Overview

devtool-mcp bridges the gap between AI assistants and development workflows by providing:

- **Project Detection** - Automatically detect project types (Go, Node.js, Python) and available scripts
- **Process Management** - Start, monitor, and control long-running processes with output capture
- **Reverse Proxy** - Intercept HTTP traffic with automatic frontend instrumentation
- **Frontend Diagnostics** - 50+ primitives for DOM inspection, layout debugging, and accessibility auditing

## Documentation

**[Full Documentation →](https://standardbeagle.github.io/devtool-mcp/)**

### Run Documentation Locally

```bash
cd docs-site
npm install
npm start
```

Then open [http://localhost:3000/devtool-mcp/](http://localhost:3000/devtool-mcp/).

### Deploy to GitHub Pages

Documentation is automatically deployed to GitHub Pages when changes are pushed to the `main` branch. You can also manually trigger deployment from the Actions tab.

To set up GitHub Pages for your fork:
1. Go to repository Settings → Pages
2. Set Source to "GitHub Actions"
3. Push changes to trigger deployment

## Quick Start

### Installation

```bash
# From source
git clone https://github.com/standardbeagle/devtool-mcp.git
cd devtool-mcp
make build

# Or install directly
go install github.com/standardbeagle/devtool-mcp@latest
```

### Configuration

Add to your MCP client configuration:

```json
{
  "mcpServers": {
    "devtool": {
      "command": "/path/to/devtool-mcp"
    }
  }
}
```

### Usage Example

```json
// Detect project type
detect {path: "."}
→ {type: "node", scripts: ["dev", "build", "test"]}

// Start dev server
run {script_name: "dev"}
→ {process_id: "dev", state: "running"}

// Set up debugging proxy
proxy {action: "start", id: "app", target_url: "http://localhost:3000", port: 8080}
→ {listen_addr: ":8080"}

// Debug frontend issues
proxy {action: "exec", id: "app", code: "window.__devtool.inspect('#my-button')"}
→ Full element analysis including position, styles, accessibility
```

## Features

### Project Detection

Automatically detect Go, Node.js, and Python projects with available commands:

```json
detect {path: "."}
→ {
    type: "node",
    package_manager: "pnpm",
    scripts: ["dev", "build", "test", "lint"]
  }
```

### Process Management

Run scripts with output capture, filtering, and graceful shutdown:

```json
run {script_name: "test", mode: "foreground"}
→ {exit_code: 0, runtime: "12.3s"}

proc {action: "output", process_id: "test", grep: "FAIL"}
→ Filter output for failures
```

### Reverse Proxy

Transparent HTTP proxy with traffic logging and frontend instrumentation:

```json
proxy {action: "start", id: "debug", target_url: "http://localhost:3000", port: 8080}

// All traffic is logged
proxylog {proxy_id: "debug", types: ["http", "error"]}

// JavaScript errors are captured automatically
proxylog {proxy_id: "debug", types: ["error"]}
→ {message: "TypeError...", stack: "...", source: "main.js:142"}
```

### Frontend Diagnostics

50+ primitives injected into all HTML pages:

```javascript
window.__devtool.inspect('#element')      // Comprehensive analysis
window.__devtool.diagnoseLayout()         // Find layout issues
window.__devtool.auditAccessibility()     // A11y audit with score
window.__devtool.screenshot('bug')        // Capture screenshot
window.__devtool.highlight('.item')       // Visual debugging
window.__devtool.selectElement()          // Interactive picker
```

## MCP Tools

| Tool | Description |
|------|-------------|
| `detect` | Detect project type and available scripts |
| `run` | Execute scripts or raw commands |
| `proc` | Monitor, query output, stop processes |
| `proxy` | Manage reverse proxies |
| `proxylog` | Query traffic logs and errors |
| `currentpage` | View grouped page sessions |

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    MCP Tools Layer                           │
│  detect │ run │ proc │ proxy │ proxylog │ currentpage       │
├─────────────────────────────────────────────────────────────┤
│                  Business Logic Layer                        │
│   ProjectDetector │ ProcessManager │ ProxyManager            │
├─────────────────────────────────────────────────────────────┤
│                   Infrastructure Layer                       │
│          RingBuffer │ TrafficLogger │ PageTracker            │
└─────────────────────────────────────────────────────────────┘
```

Key design decisions:
- **Lock-free concurrency** using `sync.Map` and atomics
- **Bounded memory** with ring buffers for output capture
- **Graceful shutdown** with signal handling and process groups
- **Zero dependencies** for injected frontend JavaScript

## Development

```bash
# Build
make build

# Run tests
make test

# Format and lint
make fmt
make vet
make lint

# Run with coverage
make test-coverage
```

## Documentation Structure

```
docs-site/
├── docs/
│   ├── intro.md                    # Overview
│   ├── getting-started.md          # Installation & setup
│   ├── features/                   # Feature guides
│   │   ├── project-detection.md
│   │   ├── process-management.md
│   │   ├── reverse-proxy.md
│   │   └── frontend-diagnostics.md
│   ├── concepts/                   # Architecture
│   │   ├── architecture.md
│   │   ├── lock-free-design.md
│   │   └── graceful-shutdown.md
│   ├── api/                        # API reference
│   │   ├── detect.md
│   │   ├── run.md
│   │   ├── proc.md
│   │   ├── proxy.md
│   │   ├── proxylog.md
│   │   ├── currentpage.md
│   │   └── frontend/              # Frontend API (50+ functions)
│   │       ├── overview.md
│   │       ├── element-inspection.md
│   │       ├── accessibility.md
│   │       └── ...
│   └── use-cases/                  # Real-world guides
│       ├── debugging-web-apps.md
│       ├── automated-testing.md
│       ├── performance-monitoring.md
│       ├── ci-cd-integration.md
│       ├── accessibility-auditing.md
│       └── frontend-error-tracking.md
```

## Requirements

- Go 1.24.2 or later
- MCP-compatible AI assistant (Claude Code, Cursor, etc.)

## License

MIT

## Contributing

Contributions welcome! Please read the documentation first to understand the architecture and design decisions.
