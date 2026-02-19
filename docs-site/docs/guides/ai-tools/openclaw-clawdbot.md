---
title: "agnt with OpenClaw and ClawdBot - Browser Debugging"
description: "Use agnt with OpenClaw and ClawdBot for browser debugging, error capture, and visual feedback. Full MCP integration guide."
keywords: [OpenClaw, ClawdBot, browser debugging, MCP server, error capture, AI coding agent]
sidebar_label: "OpenClaw & ClawdBot"
---

# agnt with OpenClaw and ClawdBot

OpenClaw and ClawdBot are terminal-based AI coding agents with strong MCP support, but they have no way to see what is happening in your browser. **agnt** is an MCP server that bridges that gap, giving these agents direct access to browser debugging, error capture, screenshots, DOM inspection, and live page diagnostics. When wrapped with `agnt run`, browser events flow directly into the agent's terminal as synthetic stdin, creating a complete feedback loop between the browser and the AI.

## Installation

Install the agnt binary first, then register it as an MCP server with your tool.

### Install the Binary

```bash
go install github.com/standardbeagle/agnt/cmd/agnt@latest
```

Verify the install:

```bash
agnt --version
```

### Register the MCP Server

**OpenClaw** -- add agnt to your MCP configuration:

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

**ClawdBot** -- same configuration format. Add the `agnt` entry to ClawdBot's MCP server config file and restart the agent.

Both tools discover agnt's capabilities automatically once the MCP server is registered. No additional plugin or extension is needed.

## Using agnt run for System Prompt Injection

Terminal-based agents like OpenClaw and ClawdBot benefit most from `agnt run`. This command wraps the agent in a PTY and enables two features that are not available through MCP alone:

1. **System prompt injection** -- agnt sends an initial stdin message after a 500ms delay, telling the agent about available browser tools and how to use them.
2. **Browser-to-terminal events** -- the floating indicator in the browser sends messages directly to the agent's terminal stdin. When a user clicks the indicator and types "this button is broken", the agent receives that text as input.

Start OpenClaw or ClawdBot through agnt run:

```bash
agnt run openclaw
```

```bash
agnt run clawdbot
```

The agent starts normally, but now it knows about agnt's browser tools from the injected system prompt, and it receives browser events in real time. This is the recommended way to use agnt with these tools.

Without `agnt run`, the MCP tools still work, but the agent will not receive the system prompt context or browser-to-terminal messages.

## Project Configuration

Create an `.agnt.kdl` file in your project root to automate dev server and proxy startup:

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

When `agnt run openclaw` or `agnt run clawdbot` starts, agnt reads this config, starts the dev server, launches the proxy with browser instrumentation, and injects the system prompt -- all before the agent receives its first user message.

## First Debugging Session

Here is a typical session after setup. The agent drives the entire workflow through MCP tool calls.

```
You: "The login form does not show validation errors."

Agent: I'll set up browser debugging and investigate.

[detect {path: "."}]
--> Node.js project, scripts: dev, build, test

[run {script_name: "dev"}]
--> Dev server running on port 3000

[proxy {action: "start", id: "app", target_url: "http://localhost:3000"}]
--> Proxy running on port 38421

Agent: Open http://localhost:38421/login in your browser
       and submit the form with empty fields.

[get_errors {}]
--> 1 error found:
    [browser:js] TypeError (2x) -- Cannot read property 'message' of null
      --> src/components/LoginForm.tsx:28:12

[proxy {action: "exec", id: "app",
  code: "window.__devtool.getElementInfo('.error-message')"}]
--> { exists: true, visible: false, display: "none",
     computedStyle: { display: "none" } }

Agent: Found the issue. The validation error container exists in the DOM
       but has display: none. The component sets the error state to null
       instead of an object with a message property. Two fixes needed:
       1. Initialize error state as { message: "" } instead of null
       2. Toggle visibility based on message length, not truthiness

       Let me apply the fix.
```

The agent identified both the JavaScript error and the hidden element without you opening DevTools.

## Tips for Best Results

- **Always use `agnt run`** with OpenClaw and ClawdBot. The system prompt injection and browser-to-terminal event flow are what make the integration powerful. Running through MCP alone misses the feedback loop.
- **Start with `get_errors {}`** after the proxy is running. This gives the agent a unified view of JavaScript errors, network failures, and proxy-level issues in one call, with deduplication and noise filtering built in.
- **Use the floating indicator** in the browser to send messages to the agent. Click it, type what you see, and the agent receives your message as stdin. No window switching needed.
- **Configure `.agnt.kdl`** so every session starts with the dev server and proxy already running. This eliminates repeated setup commands and lets the agent focus on the problem immediately.
- **Let the agent run `window.__devtool` diagnostics** through `proxy exec`. There are 50+ primitives covering element inspection, layout analysis, accessibility auditing, and DOM complexity checks.
- **Use `snapshot`** before and after fixes to verify visual regressions. The agent can compare snapshots to confirm the fix did not break other parts of the page.

## See Also

- [Getting Started](/getting-started) -- Full installation and configuration guide
- [agnt with Claude Code](/guides/ai-tools/claude-code) -- Integration guide for Claude Code
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) -- Workflow guide for error triage
- [get_errors API Reference](/api/get_errors) -- Unified error tool documentation
