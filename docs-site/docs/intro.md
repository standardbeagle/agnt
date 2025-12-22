---
sidebar_position: 1
slug: /
---

# agnt

## Stop Describing Bugs. Let Your AI See Them.

Every time you tell Claude "the button looks weird" or "there's an error somewhere," you're spending tokens on descriptions your AI could just *see*.

**agnt gives your AI coding agent eyes into the browser.**

```
You: "The modal is behind the header"
     ↓
AI:  *takes screenshot*
     *inspects stacking contexts*
     *finds: modal z-index: 100, header creates new stacking context at z-index: 1000*
     "The header creates a stacking context. Move the modal to a sibling of header, or increase its z-index above 1000."
```

No more alt-tabbing to describe what you see. No more pasting error messages. No more explaining layouts in words.

## The Three Superpowers

### 1. AI Sees What You See

Your AI agent gets automatic access to:
- **Screenshots** on demand
- **JavaScript errors** with full stack traces as they happen
- **DOM state** - computed styles, positions, accessibility info
- **Network traffic** - every request and response
- **Performance metrics** - load times, paint events, CLS

### 2. You Can Show, Not Tell

Click the floating indicator in your browser to:
- **Send messages** directly to your AI without typing
- **Draw wireframes** on your live UI with sketch mode
- **Select elements** to log their details
- **Capture interactions** - what you clicked, what changed

### 3. Test the Unhappy Paths

Built-in chaos engineering lets you simulate:
- **Slow networks** - 3G, flaky WiFi, bandwidth throttling
- **API failures** - 500 errors, timeouts, rate limits
- **Race conditions** - responses arriving out of order
- **Connection drops** - mid-response disconnects

```bash
proxy {action: "chaos", id: "app", preset: "flaky-api"}
# Your app now experiences random failures - watch what breaks
```

## What This Looks Like

**Before agnt:**
> "I'm seeing a TypeError on line 42 that says cannot read property map of undefined, and there's also this weird layout issue where the card is overlapping the sidebar, and when I click the submit button nothing happens but I'm not sure if that's related..."

**With agnt:**
```bash
proxylog {types: ["error"]}
→ TypeError: Cannot read property 'map' of undefined
  at ProductList (products.js:42:15)
  Data: products = null, expected array

window.__devtool.inspect('.card')
→ {position: "absolute", zIndex: 1, parent: {overflow: "visible"},
   overlaps: [".sidebar"]}

window.__devtool.interactions.getLastClickContext()
→ {element: "button.submit", handler: null, formValid: false}
```

Structured data. Fewer tokens. Faster fixes.

## Key Features

| Feature | What It Does |
|---------|-------------|
| **Error Capture** | JavaScript errors automatically logged with stack traces |
| **Screenshots** | Capture visual state on demand |
| **DOM Inspection** | Full element analysis - styles, position, accessibility |
| **Sketch Mode** | Draw wireframes directly on your UI |
| **Design Mode** | AI generates design alternatives you can preview live |
| **Chaos Testing** | Simulate slow networks, API failures, race conditions |
| **Mobile Testing** | Tunnel to real phones via Cloudflare/ngrok/Tailscale |
| **Traffic Logging** | All HTTP requests and responses captured |
| **Process Management** | Run and monitor dev servers |

## Quick Start

**Install:**
```bash
npm install -g @standardbeagle/agnt
```

**Configure MCP:**
```json title="claude_desktop_config.json"
{
  "mcpServers": {
    "agnt": {
      "command": "agnt",
      "args": ["mcp"]
    }
  }
}
```

**Use:**
```bash
# Start dev server
run {script_name: "dev"}

# Start proxy to capture errors and traffic
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}

# Open http://localhost:45849 in your browser
# Now your AI sees everything
```

Ready for more? See the [Getting Started](/getting-started) guide.

---

## agnt vs Browser Automation Tools

agnt is **not** a browser automation tool. It doesn't click buttons or fill forms. It instruments *your* browser session so your AI can see what you're doing.

| | agnt | Playwright/Puppeteer MCP |
|---|------|--------------------------|
| **Who controls the browser?** | You | The AI |
| **Purpose** | Development debugging | E2E testing, automation |
| **Error capture** | Automatic | Manual |
| **Two-way communication** | Yes (floating indicator) | No |
| **Sketch/wireframe** | Yes | No |
| **Click/type/navigate** | No | Yes |

**Use both together** - agnt for development, Playwright for automated testing:

```json
{
  "mcpServers": {
    "agnt": { "command": "agnt", "args": ["mcp"] },
    "playwright": { "command": "npx", "args": ["@playwright/mcp@latest"] }
  }
}
```

---

## Use Cases

### Hard-to-Debug Issues

- **Layout bugs** - "It looks weird" → AI inspects stacking contexts, overflows, computed styles
- **Race conditions** - Chaos testing exposes timing bugs
- **Mobile issues** - Tunnel to real devices with full instrumentation

### Development Workflow

- **Error tracking** - Errors captured before you notice them
- **Performance** - Load times, CLS, resource timing
- **Accessibility** - Built-in a11y audits

### Communication

- **Sketch ideas** - Draw on your UI to show what you want
- **Design iteration** - AI generates alternatives, you preview live
- **Documentation** - Annotated screenshots for PRs and docs

See the [Use Cases Overview](/use-cases) for detailed workflows.

---

## Architecture

agnt runs as a daemon with persistent state:

- Processes and proxies survive client disconnections
- Lock-free design for maximum concurrency
- Bounded memory with ring buffers
- Zero-dependency frontend JavaScript

The proxy injects 50+ diagnostic functions into every page, accessible via `window.__devtool`.

---

## MCP Tools

| Tool | Purpose |
|------|---------|
| `detect` | Auto-detect project type and scripts |
| `run` | Execute scripts and commands |
| `proc` | Process management and output |
| `proxy` | Reverse proxy with instrumentation |
| `proxylog` | Query traffic, errors, screenshots |
| `currentpage` | View active page sessions |
| `tunnel` | Mobile testing via Cloudflare/ngrok |
| `daemon` | Daemon management |

---

## Next Steps

- [Getting Started](/getting-started) - Installation and setup
- [Use Cases](/use-cases) - Detailed workflows
- [Chaos Engineering](/features/chaos-engineering) - Test the unhappy paths
- [API Reference](/api/proxy) - Full tool documentation
