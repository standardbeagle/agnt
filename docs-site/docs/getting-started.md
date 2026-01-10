---
sidebar_position: 2
---

# Getting Started

This guide will help you install, configure, and start using agnt with your AI coding assistant.

## Prerequisites

- **Go 1.24.2** or later (for building from source)
- An MCP-compatible AI assistant (Claude Code, Cursor, etc.)

## Installation

### Claude Code Marketplace (Recommended)

The easiest way to install agnt is through the Claude Code marketplace:

```bash
# Install from marketplace (automatically configures MCP)
claude mcp add agnt --plugin agnt@agnt-marketplace
```

This single command downloads the latest agnt binary and registers it as an MCP server.

### From Go

```bash
go install github.com/standardbeagle/agnt/cmd/agnt@latest
```

### From Source

```bash
# Clone the repository
git clone https://github.com/standardbeagle/agnt.git
cd agnt

# Build the binary
make build

# Or install to $GOPATH/bin
make install
```

### Verify Installation

```bash
agnt --version
# agnt version 0.7.10
```

## Configuration

### Claude Code CLI

```bash
# Register as MCP server
claude mcp add agnt -s user -- agnt mcp
```

### Manual Configuration

Add agnt to your Claude Code configuration (`claude_desktop_config.json`):

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

### Other MCP Clients

agnt communicates over stdio. Configure your MCP client to spawn `agnt mcp` and communicate over stdin/stdout.

### Project Configuration (.agnt.kdl)

Create a `.agnt.kdl` file in your project root to auto-start scripts and proxies:

```kdl
// Scripts to run automatically
scripts {
    dev {
        run "npm run dev"
        autostart true
    }
}

// Reverse proxies for browser debugging
proxies {
    frontend {
        target "http://localhost:3000"
        autostart true
    }
}

// Browser notifications when AI responds
hooks {
    on-response {
        toast true
        indicator true
    }
}

// Toast notification settings
toast {
    duration 4000
    position "bottom-right"
    max-visible 3
}
```

Run `/setup-project` in Claude Code to interactively generate this configuration.

**Script Options:**
- `run` - Shell command (e.g., `"npm run dev"`)
- `command` / `args` - Command with arguments
- `autostart` - Start automatically (`true`/`false`)
- `env` - Environment variables block
- `cwd` - Working directory
- `url-matchers` - Patterns for URL auto-detection

**Proxy Options:**
- `target` - Full target URL
- `port` / `host` - Shorthand for localhost proxies
- `script` - Link to a script for URL auto-detection
- `autostart` - Start automatically
- `max-log-size` - Max log entries (default: 1000)

**Common Framework URL Matchers:**
| Framework | url-matchers Pattern |
|-----------|---------------------|
| Next.js / Vite | `"(Local\|Network):\\s*{url}"` |
| Wails | `"DevServer URL:\\s*{url}"` |
| Astro | `"Local\\s+{url}"` |
| Jekyll | `"Server address:\\s*{url}"` |
| Hugo | `"Web Server.*available at {url}"` |

## Quick Start

Once configured, your AI assistant has access to all agnt tools. Here's a typical workflow:

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

### 3. Set Up Proxy for Browser Superpowers

```json
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}
```

The proxy auto-assigns a stable port based on the target URL (check `listen_addr` in response). The proxy:

- Logs all HTTP requests and responses
- Captures frontend JavaScript errors
- Collects performance metrics
- Injects the `window.__devtool` API with 50+ diagnostic primitives
- Shows a floating indicator for browser-to-agent messaging

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

// Check text fragility
proxy {action: "exec", id: "app", code: "window.__devtool.checkTextFragility()"}

// Check responsive risks
proxy {action: "exec", id: "app", code: "window.__devtool.checkResponsiveRisk()"}
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

[proxy {action: "exec", id: "app", code: "window.__devtool.checkTextFragility()"}]
→ Found text with long words that may overflow on mobile

AI: I found layout issues. Let me check responsive risks.

[proxy {action: "exec", id: "app", code: "window.__devtool.checkResponsiveRisk()"}]
→ 3 elements have fixed widths that break on mobile

AI: The dashboard has several issues:
    1. Text with long words needs word-break
    2. Fixed-width elements need max-width: 100%
    3. Some touch targets are too small

    Should I fix these issues?
```

## Using the Floating Indicator

When you browse through the proxy, you'll see a floating bug icon. Click it to:

- **Send messages** directly to your AI agent
- **Take screenshots** of selected areas
- **Run audits** from a dropdown menu
- **Open sketch mode** for wireframing

## Next Steps

- Learn about [Project Detection](/features/project-detection)
- Master [Process Management](/features/process-management)
- Explore the [Reverse Proxy](/features/reverse-proxy)
- Deep dive into [Frontend Diagnostics](/features/frontend-diagnostics)
- Check [Layout Robustness](/api/frontend/layout-robustness) audits
