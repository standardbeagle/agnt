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
- **Test on Real Devices** - Built-in tunnels (Cloudflare, ngrok, Tailscale) for mobile and BrowserStack testing

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

## agnt vs Other Browser Tools

There are several MCP servers for browser interaction. Here's how agnt differs:

| Tool | Approach | Best For |
|------|----------|----------|
| **agnt** | Instruments YOUR browser session | Development debugging, live collaboration |
| [Playwright MCP](https://github.com/microsoft/playwright-mcp) | AI controls a new browser | E2E testing, web scraping, form automation |
| [Puppeteer MCP](https://www.npmjs.com/package/@modelcontextprotocol/server-puppeteer) | AI controls headless Chrome | Screenshots, automated testing |
| [Chrome DevTools MCP](https://github.com/AJaySi/chrome-devtools-mcp) | Exposes DevTools to AI | Debugging Chrome sessions |

### Key Differences

**agnt is for development, not automation.**

| Capability | agnt | Playwright/Puppeteer MCP |
|------------|------|--------------------------|
| Who controls the browser? | You | The AI |
| Works with your dev server? | Yes, via proxy | Connects to any URL |
| Captures errors automatically? | Yes | No |
| Two-way communication? | Yes (floating indicator) | No |
| Process management? | Yes (run, proc tools) | No |
| Sketch/wireframe on UI? | Yes | No |
| Click buttons, fill forms? | No | Yes |
| Navigate to new pages? | No | Yes |
| Run E2E test suites? | No | Yes |

### When to Use What

**Use agnt when:**
- You're actively developing and want AI assistance debugging
- You need your AI agent to see what you see in real-time
- You want to send messages/sketches from browser to AI
- You need error capture, performance metrics, traffic logs
- You're testing on real mobile devices via tunnels

**Use Playwright/Puppeteer MCP when:**
- You need the AI to autonomously navigate websites
- You're building automated test suites
- You need web scraping or form automation
- You want the AI to click, type, and interact

**Use both together:**
```json title="claude_desktop_config.json"
{
  "mcpServers": {
    "agnt": {
      "command": "agnt",
      "args": ["mcp"]
    },
    "playwright": {
      "command": "npx",
      "args": ["@playwright/mcp@latest"]
    }
  }
}
```

- Use **agnt** for development workflow (run dev server, capture errors, debug issues)
- Use **Playwright** when you need the AI to autonomously test or navigate

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

## Use Cases

agnt excels at workflows that were previously impossible or tedious:

- **[Layout Debugging](/use-cases#hard-to-diagnose-layout-issues)** - Diagnose z-index conflicts, overflow issues, stacking contexts
- **[Responsive Testing](/use-cases#responsive-design-testing)** - Find elements that break on mobile, test on real devices
- **[i18n Testing](/use-cases#internationalization-i18n-testing)** - Catch text overflow before it reaches production
- **[Better Tests](/use-cases#writing-better-frontend-tests)** - Generate selectors, capture state, write targeted assertions
- **[Documentation](/use-cases#creating-documentation-with-screenshots)** - Annotated screenshots, bug reports, PR visuals
- **[Design Flow](/use-cases#design-iteration-flow)** - Iterate on UI with AI-generated alternatives

See the [Use Cases Overview](/use-cases) for detailed workflows.

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
