---
title: "agnt with Windsurf and GitHub Copilot - Browser Debugging"
description: "Add browser superpowers to Windsurf and GitHub Copilot. Real-time error capture, visual debugging, and DOM inspection via MCP."
keywords: [Windsurf, GitHub Copilot, browser debugging, MCP server, error capture, visual debugging]
sidebar_label: "Windsurf & Copilot"
---

# agnt with Windsurf and GitHub Copilot

Windsurf and GitHub Copilot are powerful AI coding assistants, but neither can see what is happening in your browser. When a component renders incorrectly, an API call returns a 500, or a CSS layout breaks on resize, the AI is guessing. **agnt gives both tools direct browser debugging access** -- JavaScript errors, HTTP failures, DOM inspection, and live diagnostics -- through the Model Context Protocol (MCP).

Both Windsurf and Copilot support MCP servers. Once agnt is configured, your AI assistant can start dev servers, launch a reverse proxy that instruments every page, capture errors automatically, and inspect elements -- all without you opening DevTools.

## Installation

Install the agnt binary first. Both tools need the same binary; only the MCP configuration differs.

```bash
go install github.com/standardbeagle/agnt/cmd/agnt@latest
```

Verify the installation:

```bash
agnt --version
```

### Windsurf MCP Setup

Windsurf supports MCP servers through its settings. Open Windsurf settings and add agnt to your MCP server configuration:

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

In Windsurf, navigate to **Settings > MCP Servers** (or edit the MCP config file directly). Add the entry above and restart Windsurf. The agnt tools appear in Cascade's tool list automatically.

### GitHub Copilot MCP Setup

GitHub Copilot supports MCP servers in VS Code. Add agnt to your VS Code `settings.json`:

```json
{
  "github.copilot.chat.mcp.servers": {
    "agnt": {
      "command": "agnt",
      "args": ["mcp"]
    }
  }
}
```

Alternatively, create a `.vscode/mcp.json` file in your project root to share the configuration with your team:

```json
{
  "servers": {
    "agnt": {
      "command": "agnt",
      "args": ["mcp"]
    }
  }
}
```

Restart VS Code after adding the configuration. Copilot Chat will discover the agnt tools on the next session.

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

With this config in place, your AI assistant can call `detect` to discover the project and auto-start everything in one step. No repeated manual setup between sessions.

## Your First Debugging Session

Here is a realistic session showing what the AI does when you report a problem. The tool calls are the same regardless of whether you are using Windsurf or Copilot.

```
You: "The user profile page is broken -- the avatar does not load
      and the stats section is blank."

AI: Let me investigate.

[detect {path: "."}]
--> Node.js project, package manager: npm, scripts: dev, build, test

[run {script_name: "dev"}]
--> Dev server started, process ID: dev

[proxy {action: "start", id: "app", target_url: "http://localhost:3000"}]
--> Proxy running on http://localhost:38742

AI: Open http://localhost:38742/profile in your browser.
    The proxy instruments the page so I can see errors and DOM state.

[get_errors {}]
--> 3 errors found:
    [browser:js] TypeError (5x) -- Cannot read property 'avatar' of null
      --> src/components/UserProfile.tsx:18:22
    [proxy:http] 401 Unauthorized (1x)
      --> GET /api/user/me
    [browser:js] ReferenceError (1x) -- statsData is not defined
      --> src/components/UserStats.tsx:31:5

[proxy {action: "exec", id: "app",
  code: "window.__devtool.inspect('.profile-avatar')"}]
--> { tagName: "img", src: "", naturalWidth: 0, naturalHeight: 0,
      display: "block", visibility: { visible: true } }

AI: Found the root cause. The /api/user/me endpoint returns 401,
    which means the auth token is missing or expired. The UserProfile
    component receives null instead of a user object and crashes
    when accessing .avatar. The blank stats section is a separate
    ReferenceError -- statsData is referenced before it is assigned
    on line 31.

    Let me fix the auth header first, then the variable scoping issue.
```

The AI identified three errors, traced them to a single root cause (the 401), and found the unrelated scoping bug -- all without you touching the browser console.

## Windsurf vs Copilot: Workflow Differences

Both tools use the same agnt MCP tools, but the workflows differ in practice.

| Aspect | Windsurf (Cascade) | GitHub Copilot Chat |
|--------|-------------------|-------------------|
| **Tool invocation** | Cascade calls MCP tools autonomously during multi-step plans | Copilot calls tools when prompted in chat or agent mode |
| **MCP config location** | Windsurf settings or MCP config file | VS Code `settings.json` or `.vscode/mcp.json` |
| **Multi-step debugging** | Cascade chains tool calls naturally in its planning flow | Best results when you ask Copilot to investigate step by step |
| **System prompt** | No `agnt run` integration -- use direct MCP config | No `agnt run` integration -- use direct MCP config |
| **Inline context** | Cascade can reference open editor files alongside agnt output | Copilot Chat can reference `#file` and `#selection` alongside agnt output |

Neither tool uses `agnt run` -- that wrapper is specific to terminal-based agents like Claude Code. Both Windsurf and Copilot connect to agnt purely through their native MCP server support.

## Tips for Best Results

- **Start every debugging session with `get_errors {}`** to give the AI the full error landscape. This surfaces root causes that would otherwise be hidden behind cascading failures.
- **Use `.agnt.kdl` for auto-start** so the proxy and dev server launch automatically. This eliminates setup friction and ensures errors are captured from the first page load.
- **Ask the AI to use `proxy exec`** for targeted inspection. The `window.__devtool` API has 50+ diagnostic primitives -- layout analysis, accessibility auditing, computed styles, DOM traversal -- that give the AI precise information about what is happening on screen.
- **Filter errors by time** with `get_errors {since: "2m"}` when actively debugging. This narrows the view to errors that occurred since your last change.
- **Use the floating indicator** in the proxy page to send messages directly to your AI session. Click it, type what is wrong, and the context flows through to your assistant.
- **Combine with editor context** -- reference specific files in Copilot (`#file:UserProfile.tsx`) or let Cascade read open tabs alongside agnt error output for maximum context.

## See Also

- [Getting Started](/getting-started) -- Full installation and configuration guide
- [agnt with Claude Code](/guides/ai-tools/claude-code) -- Integration guide for Claude Code
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) -- Workflow guide for error triage
- [get_errors API Reference](/api/get_errors) -- Unified error tool documentation
