---
title: "What is MCP? Model Context Protocol Explained for Developers"
description: "Learn what the Model Context Protocol (MCP) is, how it works, and why it matters for AI coding agents. Practical guide with real examples."
keywords: [MCP, model context protocol, MCP server, MCP tutorial, AI coding agent tools]
sidebar_label: "What is MCP?"
---

# What is MCP? Model Context Protocol Explained for Developers

AI coding assistants are powerful, but they run in isolation. They can generate code, answer questions, and reason about architecture, yet they cannot see your browser, inspect a failing UI, capture a screenshot, or query your database. Every time you need the AI to understand what is actually happening in your running application, you copy-paste error messages, describe layouts in words, or manually relay information back and forth. The **Model Context Protocol** (MCP) exists to solve this problem.

## What is the Model Context Protocol?

The Model Context Protocol is an open standard that lets AI assistants connect to external tools through a single, standardized interface. Created by Anthropic and now adopted across the industry, MCP defines how an AI client (your coding assistant) communicates with an MCP server (a tool provider) so that the AI can take real actions in your development environment.

Think of it like USB for AI tools. Before USB, every peripheral needed its own proprietary connector. USB gave hardware a universal interface: one protocol, many devices. MCP does the same thing for AI tooling. An MCP server that provides browser debugging works with Claude Code, Cursor, Windsurf, Copilot, and any other MCP-compatible client without modification.

The protocol is intentionally simple. Communication happens over **stdio** (standard input/output) or **HTTP with server-sent events**, using JSON-RPC messages. The AI assistant acts as the client. The tool provider runs as the server. The server advertises what it can do, and the client calls those capabilities when the user's request warrants it.

### Key concepts

- **MCP Client**: The AI assistant (Claude Code, Cursor, etc.). It discovers tools, decides when to call them, and incorporates results into its responses.
- **MCP Server**: A program that exposes tools, resources, or prompts. It handles requests from the client, executes operations, and returns structured results.
- **Tools**: Individual capabilities a server exposes, like `screenshot`, `proxy`, or `detect`. Each tool has a name, a description, and a JSON schema defining its inputs and outputs.
- **Transport**: The communication channel. Stdio is the most common for local tools. HTTP (streamable or SSE) is used for remote servers.

## How MCP Works

Here is what happens when you ask your AI assistant to do something that requires an external tool:

```
┌─────────────────┐                    ┌─────────────────┐
│   AI Assistant   │                    │   MCP Server     │
│   (MCP Client)   │                    │   (Tool Provider) │
└────────┬────────┘                    └────────┬────────┘
         │                                       │
         │  1. Initialize: discover tools        │
         │──────────────────────────────────────>│
         │  ← Tool list (name, schema, desc)     │
         │<──────────────────────────────────────│
         │                                       │
         │  User: "Why is the modal hidden?"     │
         │                                       │
         │  2. AI decides to call `screenshot`   │
         │──────────────────────────────────────>│
         │                                       │
         │  3. Server captures browser screenshot│
         │                                       │
         │  4. Returns image + metadata          │
         │<──────────────────────────────────────│
         │                                       │
         │  5. AI decides to call `proxy exec`   │
         │     to inspect stacking contexts      │
         │──────────────────────────────────────>│
         │                                       │
         │  6. Server runs JS in browser,        │
         │     returns z-index analysis          │
         │<──────────────────────────────────────│
         │                                       │
         │  7. AI synthesizes answer:            │
         │     "The header creates a stacking    │
         │      context at z-index 1000..."      │
         │                                       │
```

The critical detail: **the AI decides** which tools to call and when. You do not manually invoke tools. You describe what you need, and the assistant selects the appropriate tool, constructs the right parameters, and interprets the results. The MCP server handles execution; the AI handles reasoning.

### The request lifecycle

1. **Discovery**: When the AI assistant starts, it connects to each registered MCP server and requests the list of available tools. Each tool comes with a JSON schema describing its parameters.
2. **User request**: You ask the AI something that requires external information or action.
3. **Tool selection**: The AI examines the available tools and picks the one (or ones) that can answer your question. It constructs a valid request based on the tool's schema.
4. **Execution**: The MCP server receives the request, performs the operation (take a screenshot, run a query, inspect the DOM), and returns structured results.
5. **Synthesis**: The AI incorporates the tool's output into its reasoning and generates a response.

Steps 3-5 can repeat multiple times in a single interaction. The AI might take a screenshot, realize it needs more context, inspect an element, then check for errors before giving you a final answer.

## What Can MCP Servers Do?

MCP servers span every category of developer tooling. Here are the major areas:

### Browser and UI

Servers like [agnt](https://github.com/standardbeagle/agnt) give AI agents direct access to the browser: screenshots on demand, DOM inspection with computed styles, real-time JavaScript error capture, network traffic logging, accessibility auditing, and visual debugging. Instead of describing a bug, your AI can see it.

### Databases

MCP servers can connect to PostgreSQL, MySQL, SQLite, or any database. The AI can query data, inspect schemas, and understand your data model without you pasting SQL results into chat.

### File systems and code

While AI assistants can often read files in your project, MCP servers can extend that reach: accessing files outside the sandbox, watching for changes, or performing bulk operations across repositories.

### External APIs

GitHub, Jira, Linear, Slack, cloud providers: MCP servers can wrap any API so the AI can create issues, check CI status, query logs, or manage infrastructure as part of a natural conversation.

### Testing and quality

Run test suites, capture results, analyze coverage reports, or perform visual regression testing. The AI gets structured test output instead of you pasting terminal logs.

## Why MCP Matters

### Before MCP: fragmented integrations

Before MCP, every AI tool needed custom integrations. Want Claude to access your database? Build a Claude plugin. Want Cursor to do the same thing? Build a Cursor extension. Want Copilot to have it too? Build another integration. Each AI assistant had its own plugin format, its own API, and its own restrictions. Tool authors had to maintain separate implementations for every client.

### After MCP: write once, works everywhere

With MCP, a tool author writes one server. That server works with every MCP-compatible client. The ecosystem compounds: as more clients adopt MCP, every existing server becomes instantly available to those clients. As more servers get built, every existing client becomes more capable.

This is the same dynamic that made HTTP successful. HTTP standardized how browsers and servers communicate, which meant any browser could talk to any server. MCP standardizes how AI assistants and tools communicate, which means any AI assistant can use any tool.

### Practical benefits for developers

- **No vendor lock-in**: Switch from Claude Code to Cursor to Windsurf. Your MCP servers come with you.
- **Composable tooling**: Combine a browser server, a database server, and a deployment server. The AI orchestrates them together.
- **Local-first**: MCP servers run on your machine. Your data does not leave your environment unless a specific server is designed to call an external API.

## Getting Started with MCP

Configuring an MCP server takes a single command. Here is how to add [agnt](https://github.com/standardbeagle/agnt) (browser debugging for AI agents) to different clients:

### Claude Code CLI

```bash
# Install the binary, then register it as an MCP server
npm install -g @standardbeagle/agnt
claude mcp add agnt -s user -- agnt mcp

# Or install the Claude Code plugin (skills + slash commands)
# /plugin marketplace add standardbeagle/standardbeagle-tools
# /plugin install agnt@standardbeagle-tools
```

### Claude Desktop (JSON config)

Add to your `claude_desktop_config.json`:

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

### Cursor, Windsurf, and other clients

Most MCP-compatible clients use a similar JSON configuration format. The key fields are always the same: a command to run and the arguments to pass it. Check your client's documentation for the exact config file location.

### Verifying it works

Once configured, your AI assistant will discover the server's tools on startup. Ask it something like "what tools do you have for browser debugging?" and it should list the tools from your MCP server.

For the full specification, including transport details, JSON-RPC message formats, and advanced features like resources and prompts, see the [MCP Specification](https://modelcontextprotocol.io).

## MCP in Practice: Browser Debugging with agnt

Here is a concrete example of what MCP enables. Without MCP, debugging a visual bug requires you to manually inspect the page, copy the relevant information, and paste it to your AI. With an MCP server like agnt, the workflow becomes:

**You**: "The checkout button isn't visible on mobile."

**AI** (using MCP tools automatically):
1. Calls `proxy exec` to run `__devtool.screenshot("checkout-page")` and captures a screenshot of the current page state.
2. Calls `proxy exec` to run `__devtool.getElementInfo("button.checkout")` and gets the button's computed styles, position, and dimensions.
3. Calls `proxy exec` to run `__devtool.findOverflows()` and discovers the button is positioned outside the viewport on small screens.
4. Calls `get_incidents` to check for any JavaScript errors on the page.

**AI responds**: "The checkout button has `position: absolute; right: -20px` which pushes it off-screen on viewports under 768px. Here's the fix..." and provides a CSS correction based on actual computed values it inspected.

The entire interaction happens through MCP tool calls. You described the problem; the AI investigated it using real browser data. No screenshots pasted into chat. No manually copying CSS values. The AI saw the problem the same way you would in DevTools.

## See Also

- [Getting Started](/getting-started) -- Install and configure agnt
- [Best MCP Servers for Web Development](/guides/ecosystem/best-mcp-servers-web-dev) -- Curated list of MCP servers for frontend and web development
- [MCP Specification](https://modelcontextprotocol.io) -- Official protocol documentation
- [Architecture](/concepts/architecture) -- How agnt is built under the hood
