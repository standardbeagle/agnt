---
title: "agnt with Cursor - Browser Debugging for Cursor IDE"
description: "Add browser debugging to Cursor IDE. Capture JavaScript errors, inspect DOM elements, and take screenshots directly from your AI coding assistant."
keywords: [Cursor, browser debugging, MCP server, Cursor IDE, DOM inspection, error tracking]
sidebar_label: "Cursor"
---

# agnt with Cursor

Cursor is an AI-native code editor with built-in Composer for chat-driven development, but it has no visibility into what your browser is rendering. When you describe a visual bug or a failing API call, Cursor's AI is guessing. **agnt changes that.** It connects Cursor to your browser through an MCP server that exposes screenshots, JavaScript errors, DOM state, and network traffic directly to Composer.

With agnt configured, Cursor can start your dev server, proxy browser traffic, capture runtime errors, inspect elements, and take screenshots — all from within Composer. No manual DevTools required.

## Installation

Install the agnt binary first, then register it as an MCP server in Cursor.

### Install the Binary

```bash
go install github.com/standardbeagle/agnt/cmd/agnt@latest
```

Verify the installation:

```bash
agnt --version
```

### Register the MCP Server

1. Open **Cursor Settings** (Cmd+, on macOS, Ctrl+, on Linux/Windows)
2. Navigate to **Cursor Settings > MCP**
3. Click **Add new MCP server**
4. Enter the following configuration:

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

Restart Cursor after adding the server. You should see agnt listed as a connected MCP server in the MCP settings panel.

:::tip
Unlike Claude Code, Cursor does not need `agnt run` to wrap its process. Configure the MCP server directly and Composer will call agnt tools through the standard MCP protocol.
:::

## What Cursor Gets

Once connected, Composer gains access to these MCP tools:

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

## Project Configuration

Create an `.agnt.kdl` file in your project root to skip manual setup each session:

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

With this config, agnt auto-starts the dev server and proxy when the MCP server initializes. Composer can immediately call `get_errors` or `proxy exec` without any setup commands.

## Your First Debugging Session

Here is what a real Composer debugging session looks like. You describe the problem; Cursor does the rest.

```
You: "The signup form submits but nothing happens. No error visible on screen."

Cursor: I'll set up browser debugging and investigate.

[detect {path: "."}]
→ Node.js project, package manager: npm, scripts: dev, build, test

[run {script_name: "dev"}]
→ Dev server started, process ID: dev

[proxy {action: "start", id: "app", target_url: "http://localhost:5173"}]
→ Proxy running on port 38412

Cursor: Open http://localhost:38412/signup in your browser and submit the form.
        The proxy captures all errors and network traffic automatically.

[get_errors {}]
→ 1 error found:
  [proxy:http] 422 Unprocessable Entity
    → POST /api/auth/signup — {"error": "email field required"}

[proxy {action: "exec", id: "app",
  code: "window.__devtool.getElementInfo('form.signup')"}]
→ { tag: "form", children: 3,
    inputs: [
      { name: "username", type: "text", value: "testuser" },
      { name: "mail", type: "email", value: "test@example.com" }
    ] }

Cursor: Found the issue. The API expects a field named "email" but
        the form input is named "mail". The 422 response gets swallowed
        because there is no error handler on the fetch call.

        I'll rename the input field and add error handling.
```

Cursor identified the field name mismatch and the missing error handler without you opening DevTools or reading network logs.

## Cursor-Specific Features

### Composer Workflow

Composer is where agnt tools appear. When you ask Cursor to debug a browser issue, Composer calls agnt tools in sequence — detect the project, start the server, open the proxy, then query errors. You can also call tools explicitly by referencing them in your prompt: "use `get_errors` to check for JavaScript exceptions."

### Tool Approval

By default, Cursor may prompt you to approve each MCP tool call. For a smoother debugging workflow:

1. Open **Cursor Settings > MCP**
2. Look for tool approval or auto-approve settings
3. Enable auto-approval for the agnt server, or approve tools individually during your first session

Once approved, subsequent calls to the same tool run without prompts.

### Multi-File Context

Cursor excels at multi-file edits. After agnt identifies an error with stack traces pointing to specific files, Cursor can open those files, apply fixes across components, and verify the fix with another `get_errors` call — all in a single Composer conversation.

### Inline Editing with Browser Context

Use Cursor's inline editing (Cmd+K / Ctrl+K) alongside Composer. Ask Composer to capture the current error state with agnt, then use inline edit on the specific file and line that agnt identified. The combination of browser diagnostics and inline editing is faster than switching between DevTools and your editor.

## Tips for Best Results

- **Start with `get_errors {}`** to see all current issues across processes and proxies in one call. This gives Cursor the full picture before diving into specifics.
- **Configure `.agnt.kdl`** for auto-start so the proxy is ready the moment you open Composer. No repeated setup commands each session.
- **Use `proxy exec`** to run `window.__devtool` diagnostics from Composer. There are 50+ primitives for layout analysis, accessibility auditing, and DOM inspection.
- **Use the floating indicator** in your browser to send messages directly to agnt. Click a broken element, describe what is wrong, and the context flows through the proxy logs.
- **Use `snapshot`** before and after fixes. Ask Cursor to compare the snapshots to verify the fix did not introduce regressions.
- **Reference specific tools in your prompt** when Cursor does not call them automatically. Saying "use `proxylog` to check recent network requests" guides Composer to the right tool.

## See Also

- [Getting Started](/getting-started) -- Full installation and configuration guide
- [agnt with Claude Code](/guides/ai-tools/claude-code) -- Integration guide for Claude Code
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) -- Workflow guide for error triage
- [get_errors API Reference](/api/get_errors) -- Unified error tool documentation
