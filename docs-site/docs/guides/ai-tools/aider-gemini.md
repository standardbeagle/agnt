---
title: "agnt with Aider and Gemini CLI - Browser Debugging"
description: "Add browser debugging to Aider and Google Gemini CLI. Capture errors, inspect pages, and debug visually with agnt's MCP tools."
keywords: [Aider, Gemini CLI, browser debugging, MCP server, error capture, AI coding agent]
sidebar_label: "Aider & Gemini CLI"
---

# agnt with Aider and Gemini CLI

Aider and Gemini CLI are terminal-based AI coding agents. They can edit your code, run commands, and reason about your project -- but they have no way to see what is happening in the browser. When your app throws a JavaScript error, returns a 500 from an API route, or renders a layout incorrectly, these tools are blind to it. **agnt adds browser debugging** by wrapping them in a PTY and injecting browser events directly into their stdin.

Unlike Claude Code, which registers MCP servers natively, Aider and Gemini CLI do not speak MCP. agnt works around this with `agnt run`, which wraps any terminal-based AI tool in a pseudo-terminal, auto-starts your dev server and proxy, and feeds browser context into the agent's input stream as plain text.

## Installation

Install the agnt binary first:

```bash
go install github.com/standardbeagle/agnt/cmd/agnt@latest
```

Verify the installation:

```bash
agnt --version
```

No MCP registration step is needed. `agnt run` handles the integration entirely through PTY wrapping and stdin injection.

## Using agnt run

`agnt run` wraps your AI tool in a PTY and creates a feedback loop between the browser and the agent:

```bash
# Wrap Aider
agnt run aider

# Wrap Gemini CLI
agnt run gemini
```

When you run this command, agnt does the following:

1. Spawns the AI tool inside a pseudo-terminal (PTY)
2. Reads your `.agnt.kdl` project config and auto-starts any configured dev servers and proxies
3. Injects a system prompt into the agent's stdin after a 500ms delay, informing it about available browser context
4. Listens for browser events through the reverse proxy
5. Routes browser events (errors, screenshots, indicator messages) into the agent's stdin as synthetic input

The data flow looks like this:

```
Browser --> Proxy --> HTTP POST --> Overlay (port 19191) --> PTY stdin --> AI Agent
```

The agent receives browser events as regular text input -- no special protocol support needed.

## Project Configuration

Create an `.agnt.kdl` file in your project root to automate the setup. When `agnt run` starts, it reads this config and launches everything automatically.

```kdl
agent {
    command "aider"
}

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

With this config in place, `agnt run` starts your dev server, launches the reverse proxy with browser instrumentation, and opens Aider -- all in one command. To use Gemini CLI instead, change the agent command:

```kdl
agent {
    command "gemini"
}
```

## Your First Debugging Session

Here is a walkthrough of a typical session with Aider wrapped by agnt.

```bash
agnt run aider
```

agnt starts, reads `.agnt.kdl`, launches `npm run dev`, starts the proxy on a stable port, and opens Aider. After 500ms, agnt injects a system prompt into Aider's stdin telling it about available browser context.

Open the proxy URL (printed in the terminal output) in your browser. The proxy instruments the page with agnt's JavaScript runtime, enabling error capture and the floating indicator.

Navigate to the page with the bug. If the page throws a JavaScript error or receives a failed API response, agnt captures it and injects a summary into Aider's stdin:

```
[agnt] Browser error detected:
  TypeError: Cannot read property 'items' of undefined
  → src/components/OrderList.tsx:38:12
  page: http://localhost:45221/orders

[agnt] HTTP error:
  500 Internal Server Error
  POST /api/orders → "connection pool exhausted"
```

Aider now has the full error context without you copying anything from DevTools. It can read the relevant source files and propose a fix, knowing both the frontend symptom and the backend cause.

## Terminal-Based Workflow Tips

### The Floating Indicator Pipeline

When you browse through the proxy, a floating indicator appears in the browser. Clicking it opens a text input. Messages you type there travel through the proxy and arrive in the agent's terminal stdin:

```
Browser indicator --> Proxy WebSocket --> Overlay HTTP POST --> PTY stdin --> Aider/Gemini
```

This means you can send context to the agent from the browser without switching windows. Type "the sidebar is overlapping the main content" in the indicator, and Aider receives it as input text. The agent can then inspect the page state or ask you follow-up questions.

### Screenshots and Element Selection

The indicator also has buttons for screenshots and element selection. When you take a screenshot or select an element, the result is sent to the agent's stdin as a structured message. The agent sees what you see, even though it is a terminal-based tool.

### Working Without .agnt.kdl

If you do not have a project config, you can still use `agnt run` -- it will wrap the agent in a PTY and listen for browser events. You will need to start the proxy separately through the MCP tools. The `.agnt.kdl` approach is simpler for repeated use since it automates the full setup.

### Handling Long Sessions

agnt's reverse proxy maintains a circular traffic log (1000 entries by default). Errors are deduplicated and the most recent occurrences are preserved. The agent always sees the current state, not stale errors from earlier in the session.

### Multiple Services

If your project has both a frontend and API server, configure multiple scripts and proxies in `.agnt.kdl`. Errors from all proxies are captured and injected into the agent's stdin, giving it visibility across your entire stack.

## See Also

- [Getting Started](/getting-started) -- Installation and configuration guide
- [agnt with Claude Code](/guides/ai-tools/claude-code) -- Integration for Claude Code (uses native MCP instead of PTY wrapping)
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) -- Workflow guide for error triage
- [get_incidents API Reference](/api/get_incidents) -- Unified error tool documentation
