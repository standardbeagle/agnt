# Walkthrough: visual collaboration — sketch → design → walkthrough

## What it is

agnt turns the running app in the browser into a shared canvas between the
developer and the coding agent. Three browser modes, all injected by the proxy
client script and all reachable from the agent via `proxy {action:"exec"}`,
form one loop:

1. **Sketch mode** (`internal/proxy/scripts/sketch.js`) — a Balsamiq-style
   wireframe surface the developer draws on, directly over the live page. Each
   sketch is logged as a `sketch` proxy-log entry that reaches the agent.
2. **Design mode** (`internal/proxy/scripts/design.js`) — select a real element,
   generate on-scheme alternatives, and iterate. Direct manipulation and the
   **style editor** (`internal/proxy/scripts/style-editor.js`) let you edit the
   selected element live.
3. **Walkthrough mode** (`internal/proxy/scripts/walkthrough.js`, driven by the
   `walkthrough` MCP tool) — the agent runs a live, narrated demo of the change
   it just shipped, highlighting elements and advancing by timer, click, or
   app-state condition.

Source of truth: `internal/proxy/scripts/sketch.js`, `design.js`,
`walkthrough.js`, `internal/tools/walkthrough_tools.go`, and the browser API
surface documented in `docs/mcp-tools.md`.

## Why it's unique

Most agent workflows are text-in / text-out: the developer *describes* a UI
change and hopes the agent pictures the same thing. This loop replaces
description with **shared pixels on the actual running page**:

- The developer doesn't write a ticket — they draw a wireframe *on top of the
  live component*, and the drawing itself becomes an agent-visible event.
- The agent doesn't guess at selectors — design mode hands it the real selected
  element, its scheme (design tokens), and its surrounding HTML context.
- The agent doesn't just claim "done" — it *performs* the finished change as a
  guided walkthrough the developer watches in their own browser.

Every artifact is anchored to a real DOM element on the real page, so nothing is
lost in translation between "what the developer imagined" and "what the agent
built."

## Real-world scenario

A developer wants a collapsible left sidebar added to a dashboard. Rather than
writing a spec, they open the live dashboard through the agnt proxy, sketch the
sidebar wireframe right where it should go, and let the agent take it from
there — implement it, iterate on the visual details in design mode, and then
demo the finished sidebar back to them.

## Step by step

### 1. Proxy the app (shared prerequisite)

```
proxy {action:"start", id:"dev", target_url:"http://localhost:3000"}
```

Open the returned `listen_addr` in a browser. The floating agnt indicator
appears; sketch/design/responsive are indicator modes.

### 2. Developer sketches the sidebar (browser-side)

The developer opens sketch mode from the indicator (or the agent can open it
with `proxy {action:"exec", id:"dev", code:"window.__devtool.sketch.open()"}`)
and draws the sidebar wireframe over the live page. Sketch strokes are captured
against the current page as the background.

### 3. The sketch reaches the agent

Saved sketches are recorded as `sketch` proxy-log entries. The agent picks them
up either by polling the log or through the incident inbox:

```
proxylog {proxy_id:"dev", action:"query", types:["sketch","interaction"]}
```

Expected: entries of type `sketch` carrying the wireframe payload (and any
`interaction`/panel-message notes the developer left). If the incident pipeline
is enabled (`alerts { incident-pipeline true }`), the same activity surfaces
through:

```
get_incidents {}
```

Either way, the drawing is now an agent-visible signal, not a verbal
description. The agent reads the wireframe and implements the collapsible
sidebar in the codebase.

### 4. Iterate visually in design mode

To tune the result, the agent (or developer) enters design mode and selects the
new sidebar element. From `docs/mcp-tools.md`, the browser API is
`design.start/stop/selectElement/next/previous/addAlternative/chat`:

```
proxy {action:"exec", id:"dev", code:"window.__devtool_design.start()"}
proxy {action:"exec", id:"dev", code:"window.__devtool_design.selectElement(document.querySelector('#sidebar'))"}
```

Selecting an element captures its **scheme** (the app's own design tokens) so
generated alternatives stay on-scheme, plus its surrounding HTML context. From
there:

- `window.__devtool_design.addAlternative(html)` / `addAlternatives([...])` add
  candidate renderings; `next()` / `previous()` cycle the preview through them.
- `window.__devtool_design.chat("tighten the collapse animation")` sends a
  `design_chat` event (with a screenshot, the current + original HTML, selector,
  xpath, and metadata) back to the agent to drive the next iteration.
- The **style editor** (`window.__devtool` style-editor surface) opens on the
  selected element for direct, live property edits — width, padding, colors —
  without round-tripping through the codebase for every tweak.

Design-mode activity (`design_state`, `design_chat`, and the design-request
events) is logged the same way sketches are, so the agent stays in the loop as
the developer manipulates the element directly.

### 5. Agent demos the finished change with a walkthrough

Once the sidebar is implemented and tuned, the agent loads a live, narrated demo
using the `walkthrough` MCP tool. `walkthrough {action:"load", ...}` registers
the script and shows a replay launcher in the overlay without auto-starting:

```
walkthrough {action:"load", proxy_id:"dev", script:{
  id:"sidebar-demo",
  title:"New collapsible sidebar",
  steps:[
    {title:"Toggle", body:"Click the sidebar toggle to collapse it.",
     target:"#sidebar-toggle", advance:{type:"click-target"}},
    {title:"Collapsed state", body:"The sidebar animates to a rail.",
     target:"#sidebar", advance:{type:"auto", ms:4000}},
    {title:"Deep link", body:"Selecting a nav item routes and keeps state.",
     advance:{type:"wait", when:"url-contains", value:"/reports"}}
  ]
}}
```

Expected output:

```json
{ "success": true, "message": "walkthrough load on proxy \"dev\"",
  "result": "{\"ok\":true,\"id\":\"sidebar-demo\",\"steps\":3}" }
```

Then start playback (auto by default; `mode:"manual"` for user-driven stepping):

```
walkthrough {action:"start", proxy_id:"dev", script_id:"sidebar-demo"}
```

The overlay pops a floating, scrolling step list that narrates each step,
highlights the matching element, and advances by:

- `auto` — show for `ms` (default 5000), then advance;
- `click-target` — advance when the user clicks the highlighted `target`;
- `wait` — advance when an app-state condition holds
  (`when: url-contains | element-present | element-visible`).

Control it live with `walkthrough {action:"next"|"prev"|"play"|"pause"|"stop"|"status"|"list", proxy_id:"dev"}`.
`status` returns the current step / mode / running flag in `result`.

## Gotchas

- **There is no `design_edit` MCP tool.** Direct manipulation and the style
  editor are *browser-side* (`design.js` / `style-editor.js`). The agent reaches
  them through `proxy {action:"exec"}` calling `window.__devtool_design.*` and
  the style-editor surface — not through a dedicated design MCP tool. Design's
  agent-facing signal is the logged `design_chat` / `design_state` events, which
  you read via `proxylog` or `get_incidents`.
- **Sketches are agent-visible only as log/incident events.** Drawing on the
  page does not push anything into the agent's turn by itself; the agent must
  query `proxylog {types:["sketch"]}` (or `get_incidents`) to see them. If the
  developer expects the agent to react instantly, make sure the agent is polling
  or the incident pipeline is enabled.
- **Walkthrough requires daemon mode and a `proxy_id`.** All actions run through
  the proxy exec path against the chrome-frame walkthrough host; omitting
  `proxy_id` (or its `id` alias) returns `proxy_id required`.
- **`load` does not start playback.** It only registers the script and shows the
  replay launcher. Use `start` (with `script_id` or an inline `script`) to
  begin. `mode` is `auto` (default) or `manual`; any other value is rejected.
- **`wait` advance conditions are app-state, not time.** A `wait` step with a
  condition that never becomes true will stall the walkthrough on that step —
  use `next` to move past it, or prefer `auto`/`click-target` for steps whose
  condition you can't guarantee.
- **Design alternatives are anchored to the selected element's scheme.** Calling
  `chat`/`addAlternative` before `selectElement` is a no-op (`chat` logs "No
  element selected"). Always select first.
- **Sketch/design/responsive share the indicator's single mode slot.** They are
  sibling indicator modes; opening one is the developer's context switch, so
  don't assume two are active at once.
