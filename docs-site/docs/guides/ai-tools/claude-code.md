---
title: "agnt with Claude Code - Browser Superpowers for Claude"
description: "Give Claude Code browser debugging superpowers. Screenshots, DOM inspection, error capture, and visual feedback via MCP. Install in one command."
keywords: [Claude Code, browser debugging, MCP server, screenshots, DOM inspection, frontend debugging]
sidebar_label: "Claude Code"
---

# agnt with Claude Code

Claude Code is a powerful AI coding assistant that lives in your terminal, but it cannot see what is happening in your browser. When you report a visual bug, a layout glitch, or a failing network request, Claude is working blind. **agnt fixes that.** It gives Claude Code direct access to screenshots, JavaScript errors, DOM state, network traffic, and performance metrics — all through the Model Context Protocol (MCP).

With agnt installed, Claude Code can start your dev server, open a debugging proxy, capture browser errors, inspect elements, and take screenshots — without you leaving the terminal. This turns Claude Code into a full-stack browser debugging assistant.

## Installation

There are three ways to connect agnt to Claude Code. Pick whichever fits your workflow.

### Marketplace (Recommended)

One command installs the binary and registers the MCP server:

```bash
claude mcp add agnt --plugin agnt@agnt-marketplace
```

### Manual CLI

If you already have the `agnt` binary installed (via `go install` or from source):

```bash
claude mcp add agnt -s user -- agnt mcp
```

### Config File

Add agnt directly to your `claude_desktop_config.json`:

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

Restart Claude Code after editing the config file.

## What Claude Code Gets

Once connected, Claude Code gains access to these MCP tools:

| Tool | What it does |
|------|-------------|
| `detect` | Auto-detect project type (Node, Go, Python) and available scripts |
| `run` | Start dev servers and shell commands in background or foreground |
| `proc` | Monitor process output, status, and manage lifecycle |
| `proxy` | Start a reverse proxy with automatic browser instrumentation |
| `proxylog` | Query captured traffic logs, errors, and screenshots |
| `get_errors` | Unified error view across all processes and proxies |
| `currentpage` | Track active browser sessions and page state |
| `snapshot` | Capture visual snapshots for regression testing |

Every tool works over stdio. Claude Code calls them the same way it calls any MCP tool — no extra setup needed.

## Your First Debugging Session

Here is what a real Claude Code browser debugging session looks like. You describe the problem; Claude does the rest.

```
You: "My app has a bug on the dashboard page."

Claude: Let me set up debugging for your project.

[detect {path: "."}]
→ Node.js project, package manager: pnpm, scripts: dev, build, test

[run {script_name: "dev"}]
→ Dev server started, process ID: dev

[proxy {action: "start", id: "app", target_url: "http://localhost:3000"}]
→ Proxy running on port 45849

Claude: Open http://localhost:45849/dashboard in your browser.
        The proxy instruments the page so I can see what's happening.

[get_errors {}]
→ 2 errors found:
  [browser:js] TypeError (3x) — Cannot read property 'map' of undefined
    → src/components/Dashboard.tsx:42:15
  [proxy:http] 500 Internal Server Error
    → POST /api/dashboard-data

[proxy {action: "exec", id: "app",
  code: "window.__devtool.inspect('.dashboard-card')"}]
→ { display: "flex", position: "absolute",
    parent: { position: "static" },
    visibility: { visible: false, reason: "clipped by overflow" } }

Claude: Found two issues:
  1. The API returns 500 on /api/dashboard-data, causing the TypeError
     when the component tries to .map() over undefined data.
  2. The .dashboard-card has position: absolute but its parent
     uses position: static — the card renders off-screen.

  Let me fix the parent positioning first, then we'll address the API error.
```

Claude identified both the JavaScript error and the CSS problem without you opening DevTools.

## Project Configuration

Create an `.agnt.kdl` file in your project root to automate setup:

```kdl
scripts {
    dev {
        run "npm run dev"
        autostart true
    }
}

proxies {
    app {
        target "http://localhost:3000"
        autostart true
    }
}
```

With this config, running `agnt run claude` starts the dev server, launches the proxy, injects browser context into Claude's system prompt, and opens Claude Code — all in one command. No manual setup needed on each session.

You can also run `/agnt:setup-project` inside Claude Code to generate this config interactively.

## Claude Code-Specific Features

### System Prompt Injection

When you start Claude Code through `agnt run claude`, agnt automatically injects context about available browser tools into Claude's system prompt using the `--append-system-prompt` flag. Claude immediately knows it has browser access and how to use the debugging primitives.

### Floating Indicator

When you browse through the proxy, a floating indicator appears in your browser. Click it to type a message that goes directly to Claude's terminal stdin. This lets you send context — "this button is broken" or "check the sidebar layout" — without switching to your terminal.

### Sketch Mode

Open sketch mode from the floating indicator to draw wireframes directly on the live page. Draw rectangles, arrows, and text annotations over the UI. Claude interprets your sketches as design intent and can generate or modify code to match what you drew.

## Tips for Best Results

- **Start with `get_errors {}`** to see all current issues across processes and proxies in one call. This gives Claude the full picture before diving into specifics.
- **Use the floating indicator** to send browser context to Claude without switching windows. Click a broken element, type what is wrong, and Claude gets the message.
- **Configure `.agnt.kdl`** for auto-start so every debugging session begins instantly. No repeated setup commands.
- **Use `snapshot`** before and after fixes for visual regression testing. Claude can compare the snapshots to verify the fix did not break other parts of the page.
- **Let Claude use `proxy exec`** to run `window.__devtool` diagnostics. There are 50+ primitives for layout analysis, accessibility auditing, performance capture, and DOM inspection.

## See Also

- [Getting Started](/getting-started) — Full installation and configuration guide
- [agnt with Cursor](/guides/ai-tools/cursor) — Integration guide for Cursor
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) — Workflow guide for error triage
- [get_errors API Reference](/api/get_errors) — Unified error tool documentation
