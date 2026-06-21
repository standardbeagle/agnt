# Walkthrough Mode — Design Spec

Date: 2026-06-20
Status: Approved (via `/goal` directive)

## Purpose

Let a coding agent run a **live demo** of work just completed. The agent
authors a walkthrough script (JSON). The browser overlay pops a scrolling
floating list of step cards that narrate the current state, highlight real
app elements, and advance in three ways: an auto-play timer, clicking the
highlighted app element, or waiting for an app-state condition. The user can
watch (auto-play) or walk through it manually (Next/Prev). Scripts register so
the user can replay them later from the floating indicator.

## Where it lives

A new injected JS module `walkthrough.js`, mounted **only in the chrome
frame** (`window.__devtool_frame_role === 'chrome'`) like the indicator panel.
The chrome shell persists across content-frame navigation, so a multi-step demo
survives page changes for free (the iframe can do full page nav without
breaking the script). Exposed as `window.__devtool.walkthrough.*`, mirroring the
`sketch` and `design` namespaces in `api.js`.

The floating panel is a scrolling list of step cards in a shadow root. It
auto-scrolls to the active step. Controls: play/pause, Next, Prev, restart,
close. Reuses existing overlay/shadow-root mounting patterns.

## Script format (agent-authored JSON)

```json
{
  "id": "demo-checkout",
  "title": "New checkout flow",
  "steps": [
    {
      "title": "Open the cart",
      "body": "Markdown narration describing this state.",
      "target": "#cart-btn",
      "advance": { "type": "click-target" }
    },
    {
      "title": "Review totals",
      "target": ".order-total",
      "advance": { "type": "auto", "ms": 4000 }
    },
    {
      "title": "Confirm",
      "advance": { "type": "wait", "when": "url-contains", "value": "/confirm" }
    }
  ]
}
```

- `id` (string, required) — stable key for register/replay.
- `title` (string) — shown in panel header + indicator launcher.
- `steps[]` — ordered.
  - `title` (string), `body` (markdown string) — narration card.
  - `target` (CSS selector, optional) — element in the content frame to
    highlight + scroll into view.
  - `advance` (object):
    - `{ "type": "auto", "ms": N }` — show for N ms then advance. Default ms if
      omitted: 5000.
    - `{ "type": "click-target" }` — advance when the user clicks the
      highlighted `target` element. Requires `target`.
    - `{ "type": "wait", "when": <cond>, "value": <v> }` — advance when the
      condition holds. `when` ∈ `url-contains` | `element-present` |
      `element-visible`. For element conditions, `value` is a CSS selector.

Panel Next/Prev/restart are always available regardless of per-step
`advance.type`, so the user can step manually ("click next in the script").
Play/pause gates auto + wait advancement; manual Next always works.

## Highlight + cross-frame (same-origin direct reach)

The walkthrough runs in the chrome frame; targets live in the content frame.
The proxy serves both frames, so they are **same-origin** — the chrome host
reaches the content document directly via the shell frame registry
(`window.__devtool_frames.active().win`), with a first-`iframe` fallback. No new
WebSocket message types are needed.

- **Highlight**: the host queries the target in the content document,
  `scrollIntoView({behavior:'smooth', block:'center'})`, and overlays an
  absolutely-positioned pulsing outline box (injected `.__devtool-wt-hilite`
  style) at the element's page rect.
- **click-target**: the host attaches a one-shot capture-phase click listener on
  the matched content element. On click it advances the narration **without**
  `preventDefault`, so the real click still happens and the demo follows real
  app behavior. Missing target → emits a `warning` event and falls back to
  manual stepping (never wedges).
- **wait**: the host polls the content frame (300ms) — `url-contains` reads
  `location.href`; `element-present`/`element-visible` query the selector /
  check the rect — and advances when the condition holds.

The host is authoritative for the current step index. For an unwrapped
top-level page (no shell), the host IS the content frame and reaches its own
document directly.

## MCP tool (exec-driven)

New `walkthrough` MCP tool (`internal/tools/walkthrough_tools.go`), handler
pattern from `proxy_tools.go` (Input/Output structs with jsonschema tags;
errors as `CallToolResult{IsError:true}`, never Go errors). Registered in
`cmd/agnt/serve.go` (daemon mode). All actions are driven through the existing
`proxy exec` IPC (`dt.client.ProxyExec`) — debug-exempt class, like
`responsive_audit`/`snapshot`. The exec lands in the active content frame and
calls `window.__devtool.walkthrough.<action>(...)`, which forwards to the
chrome-frame host (`window.parent.__devtool_walkthrough_host`). No daemon
protocol or proxy server state changes — all walkthrough state lives
browser-side in the chrome frame, so `status`/`list` simply return the exec
result.

Actions: `load`, `start` (inline `script` or `script_id`, `mode` auto|manual),
`stop`, `next`, `prev`, `play`, `pause`, `status`, `list`.

## Progress reporting back to the agent

New `LogTypeWalkthrough` + `WalkthroughEntry` in `internal/proxy/logger.go`,
emitted on: start, each step advance (with how it advanced: timer / click /
wait / manual), user manual Next/Prev, finish, and stop. Agent reads via
`proxylog query` (and the incident path when enabled). This is how the agent
knows where the user is and can narrate live.

## State

All state lives **browser-side in the chrome frame** (`walkthrough.js`):
- registered scripts: `{ id -> script }` (populated by `load`; drives the
  replay launcher).
- active run: `{ script, index, mode, playing, timer, poll, armed }`.

In-memory only, best-effort, consistent with sketch/design transient state.
No Go-side proxy server state.

The replay launcher is self-rendered by `walkthrough.js` in the chrome frame's
shadow root (a floating chip shown when a script is registered and none is
running) — `indicator.js` is intentionally left untouched to keep blast radius
small.

## Files

New:
- `internal/proxy/scripts/walkthrough.js`
- `internal/tools/walkthrough_tools.go` (+ `_test.go`)

Modified:
- `internal/proxy/scripts/embed.go` — add walkthrough.js to the bundle (deps
  core+frames; before indicator/api).
- `internal/proxy/scripts/api.js` — `window.__devtool.walkthrough.*` namespace.
- `internal/proxy/ws_handler.go` — handle inbound `walkthrough_event`.
- `internal/proxy/logger.go` — `WalkthroughEntry`, `LogTypeWalkthrough`,
  `LogWalkthrough()`, timestamp case.
- `internal/tools/proxy_log_tools.go` — human-readable walkthrough formatting.
- `cmd/agnt/serve.go` — register `walkthrough` tool (daemon mode).

## Testing

- Go unit tests: tool handler action routing (load/start/stop/status/list),
  `WalkthroughEntry` logging + `proxylog` retrieval, script JSON validation
  (missing id, click-target without target, unknown advance type, unknown wait
  condition).
- JS module: pure functions (step indexing, advance resolution) structured for
  testability; condition evaluation isolated.
- End-to-end: `proxy exec` against a live page to drive a script through all
  three advance types.

## Out of scope (YAGNI v1)

- TTS / voice narration (text-only).
- Agent-scripted navigation actions (the demo observes nav, does not perform
  clicks for the user beyond highlighting).
- Persisting scripts across daemon restart.
- Branching / conditional step graphs (linear steps only).
