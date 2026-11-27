---
sidebar_position: 1
slug: /
title: Browser superpowers for AI coding agents
sidebar_label: Introduction
description: An MCP server that gives your AI coding agent eyes into the browser — screenshots, DOM and layout inspection, live error capture, quality audits, sketch and design mode, and guided walkthroughs.
---

import ModeVideo from '@site/src/components/ModeVideo';

# agnt

**Browser superpowers for AI coding agents.**

<ModeVideo src="/img/vhs-spiral-hero.webm" poster="/img/vhs-spiral-hero-poster.webp" label="A submit button half off-screen: three rounds of blind terminal automation fail, then agnt fixes it in one pass" />

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

## Everything agnt Does

One MCP server, one reverse proxy, one daemon. Behind them sits a full development toolkit:

### See

- **[Browser debugging](/features/frontend-diagnostics)** - screenshots, JavaScript errors with stack traces, DOM inspection, network traffic, and console output, all queryable by your AI.
- **[Interaction & mutation tracking](/api/proxylog)** - every click, input, and DOM change is logged so the AI knows what you did and what the page did in response.
- **[Incident inbox](/api/get_incidents)** - errors from every source (browser, HTTP, processes) deduplicated into one priority-ordered queue with remediation hints.
- **[Floating indicator messaging](/api/frontend/indicator)** - click the in-page indicator to send messages, select elements, and capture context straight to your AI.
- **Voice input** - speak annotations and commands instead of typing them; transcription streams through the proxy (Deepgram) with a Web Speech API fallback.

### Diagnose

- **[Layout diagnostics](/api/frontend/layout-diagnostics)** - `diagnoseLayoutIssues()` maps cause to symptom: containing-block traps, ineffective z-index, click interception, clipped descendants - and names the offending ancestor.
- **[Responsive audit](/api/responsive_audit)** + **[responsive mode](/api/frontend/responsive-mode)** - automated layout/overflow/a11y checks across viewport sizes, plus a live resizable preview with severity overlays.
- **[Accessibility audit](/api/frontend/accessibility)** - full axe-core audits plus contrast, tab order, and screen-reader-text primitives.
- **[API efficiency audit](/api/api_audit)** - detects N+1 patterns, request waterfalls, duplicate calls, and chatty page loads from the fetch/XHR buffer.
- **[Loading UX audit](/api/loading_audit)** - spinner cascades and fragmented concurrent loading, scored from the actual spinner timeline.
- **[Quality audit suite](/api/frontend/quality-auditing)** - `auditAll()` aggregates 8 scored audits: DOM, CSS, performance (Core Web Vitals: LCP/CLS/INP), security, SEO, accessibility, API efficiency, and loading UX.

### Test

- **[Visual regression](/api/snapshot)** - baseline and compare screenshots with the `snapshot` tool.
- **[Replay testing](/features/replay-testing)** - record live API traffic, then replay it against worker-served mocks, with fuzz presets to probe error handling (Pro; recording is alpha).
- **[Chaos engineering](/features/chaos-engineering)** - simulate slow networks, 500s, timeouts, rate limits, and out-of-order responses at the proxy, no code changes.

### Show

- **[Sketch mode](/api/frontend/sketch-mode)** - draw wireframes and annotations directly on your live UI; the AI receives the drawing with element context.
- **[Design mode](/api/frontend/design-mode)** - the AI proposes design alternatives you preview live, with a style editor for iterating on CSS in place.
- **[Walkthrough mode](/features/walkthroughs)** - the AI runs a live guided demo of what it just built: step cards, element highlighting, gesture affordances, auto or click-to-advance.
- **[Public walkthrough sharing](/features/walkthroughs#publishing-a-walkthrough)** - publish that demo behind a one-time share token as a self-contained public page, with anonymous feedback, instant revoke, and no public port bound unless you opt in.

### Run

- **[Process management](/features/process-management)** - start, monitor, and stop dev servers with bounded output capture and graceful shutdown; state survives client disconnects.
- **[Reverse proxy](/features/reverse-proxy)** - transparent HTTP interception, traffic logging, and JS instrumentation in front of any dev server.
- **[Tunnels](/api/tunnel)** - expose your dev server to real phones via Cloudflare, ngrok, or Tailscale, with full instrumentation intact.
- **[Project detection](/features/project-detection)** - Go/Node/Python projects and their scripts detected with zero configuration.
- **[Detachable & remote sessions](/features/remote-sessions)** - the daemon owns the PTY, so your agent session survives a dropped terminal; `agnt ssh` drives a remote host's agnt with forwarded proxy ports, automatic reconnect, and `agnt push` for file delivery.

## Replaces 12+ Tools in Your Dev Loop

agnt doesn't replace your production monitoring stack. It replaces the pile of separate tools you juggle *while developing* - and makes each one queryable by your AI instead of only by your eyeballs.

| You're using | agnt equivalent | Docs |
|--------------|-----------------|------|
| Chrome DevTools (manual inspection) | DOM/layout/console inspection via MCP - the AI inspects instead of you | [Frontend Diagnostics](/features/frontend-diagnostics) |
| Lighthouse | Scored quality audits with Core Web Vitals (LCP/CLS/INP) | [Quality Auditing](/api/frontend/quality-auditing) |
| axe DevTools | Full axe-core accessibility audits, on demand | [Accessibility](/api/frontend/accessibility) |
| Percy / Chromatic (local runs) | Baseline/compare screenshot regression | [snapshot](/api/snapshot) |
| Responsively App | Live responsive preview + automated viewport audits | [Responsive Mode](/api/frontend/responsive-mode) |
| Toxiproxy / DevTools throttling | Chaos presets: latency, errors, timeouts, reordering | [Chaos Engineering](/features/chaos-engineering) |
| Manual tunnel setup | Managed Cloudflare/ngrok/Tailscale tunnels for device testing | [tunnel](/api/tunnel) |
| LogRocket-style session replay (dev-time) | Interaction + mutation tracking per page session | [proxylog](/api/proxylog) / [currentpage](/api/currentpage) |
| Sentry (dev-time error tracking) | Automatic error capture + deduplicated incident inbox | [get_incidents](/api/get_incidents) |
| Excalidraw + screenshot annotation | Sketch mode - draw on the live UI itself | [Sketch Mode](/api/frontend/sketch-mode) |
| PM2 / nodemon / foreman (dev servers) | Daemon-backed process management with output capture | [Process Management](/features/process-management) |
| Postman (inspecting your own app's API) | Traffic log queries + API efficiency audits | [proxylog](/api/proxylog) / [api_audit](/api/api_audit) |
| MSW / hand-written API mocks | Record real traffic, replay it against a worker mock (Pro; recording is alpha) | [Replay Testing](/features/replay-testing) |
| Loom / screen-recorded demos | Live guided walkthrough on the real app, publishable as a token-gated page | [Walkthroughs](/features/walkthroughs) |
| tmux / screen for long agent runs | Daemon-owned sessions that survive disconnects, locally or over SSH | [Remote Sessions](/features/remote-sessions) |

Each row is scoped to the development workflow: agnt won't monitor production for you, but during a coding session, your AI can do everything above without you switching apps.

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

**Or configure once with `.agnt.kdl`:**
```kdl title=".agnt.kdl"
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

hooks {
    on-response {
        toast true      // Browser notifications
        indicator true  // Flash indicator
    }
}
```

Then run `agnt run claude` and everything starts automatically.

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
| `get_incidents` | Authoritative pull from the always-active incident inbox |
| `responsive_audit` | Responsive audit across viewport sizes |
| `api_audit` | API-efficiency audit (waterfall, N+1, duplicate, chatty-load) |
| `loading_audit` | Loading-UX audit (spinner cascade + fragmentation) |
| `snapshot` | Visual regression: baseline/compare screenshots |
| `replaytest` | Record → worker-mock → replay frontend testing (Pro; recording is alpha) |
| `walkthrough` | Live guided demo in the browser overlay: step list, highlights, gestures |
| `publish` | Public walkthrough shares: create/status/list/rotate/revoke + feedback read |
| `session` | `agnt run` session management and scheduled messages for AI agents |
| `store` | Persistent key-value storage with scoped namespaces |
| `tunnel` | Mobile testing via Cloudflare/ngrok/Tailscale |
| `daemon` | Daemon management |
| `watch` | Stream daemon events via `agnt monitor` |
| `channel_reply` | Reply to the browser overlay (channel mode) |

---

## Next Steps

- [Getting Started](/getting-started) - Installation and setup
- [Use Cases](/use-cases) - Detailed workflows
- [Chaos Engineering](/features/chaos-engineering) - Test the unhappy paths
- [Walkthroughs & Public Sharing](/features/walkthroughs) - Demo what the AI built, then share it
- [API Reference](/api/proxy) - Full tool documentation
- [Roadmap](/roadmap) - What's shipping next
