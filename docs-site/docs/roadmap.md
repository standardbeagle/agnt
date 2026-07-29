---
sidebar_position: 9
title: Roadmap
description: What is shipping next in agnt — live CSS sync across devices, component-level inspection and editing, commit-to-code, and continuous audits.
---

# Roadmap

Where agnt is going: **"Storybook in production context, on all your devices, with
a commit button."** Today agnt is a debugging bridge between your AI and the
browser. The work below turns it into a place where you edit components and
styles against real data on real devices, and push those edits back into code.

Nothing on this page has shipped. For what works today, start at
[Getting Started](/getting-started).

## Recently shipped

Landed since the last docs pass, in case you missed them:

- **[Walkthrough mode + public sharing](/features/walkthroughs)** — the AI demos
  what it built in your live app, and you can publish that demo behind a
  token-gated public URL with anonymous feedback.
- **[Detachable & remote sessions](/features/remote-sessions)** — daemon-owned
  PTYs that survive disconnects, plus `agnt ssh` with forwarded proxy ports and
  `agnt push`.
- **[Replay testing](/features/replay-testing)** — record API traffic, replay it
  from an in-page worker, and fuzz the recorded responses (Pro).
- **[API efficiency](/api/api_audit)** and **[loading UX](/api/loading_audit)
  audits** — N+1 detection, request waterfalls, spinner cascades.
- **Voice input** — speak annotations instead of typing them.

## Near-term

### QR code mobile sharing

A QR code in the floating indicator so a phone joins the instrumented session in
one scan. Auto-detects the right address — local IP, tunnel URL, or proxy URL —
and offers one-tap tunnel creation if none is running.

### Session recording sync

Recordings move from browser storage to the daemon, so a flow captured on your
desktop can be replayed on every connected device at once, with relative timing
and per-device viewport normalization.

```javascript
proxy {action: "record", id: "app", mode: "sync"}
proxy {action: "replay", id: "app", targets: "all"}
```

## Live CSS sync

### CSS patch broadcasting

Apply a CSS change once and see it on every connected browser instantly — desktop,
phone, tablet — with an undo/redo stack per session and an export path to a real
diff.

```javascript
proxy {action: "css", id: "app", mode: "start"}
proxy {action: "css", id: "app", patch: ".card { border-radius: 12px }"}
proxy {action: "css", id: "app", mode: "commit"}
```

### In-browser CSS editor

A click-to-edit panel with sliders and color pickers, a computed-vs-authored
toggle, and sourcemap awareness so it knows which file a rule actually came from.
Changes broadcast to all devices.

### DevTools change detection

Pick up edits you make in Chrome DevTools and broadcast them like any other patch
— first by polling stylesheets (~500 ms latency, no install), later via a browser
extension using the Chrome DevTools Protocol for zero-latency, full-fidelity
capture.

## Component integration

### Framework detection

Auto-detect React, Vue, Angular, Svelte, and Solid from their devtools hooks — the
foundation everything below builds on.

### Element → component mapping

Given a DOM element, name the component that owns it, the file and line it came
from, and its current props, state, and children. React first, then Vue.

```javascript
__devtool.component.fromElement('.product-card')
→ {framework: "react", name: "ProductCard",
   file: "src/components/ProductCard.tsx:24",
   props: {id: 123, title: "Widget", price: 29.99},
   state: {isHovered: false, quantity: 1}}
```

### Props and state editing

Change props or state and trigger a real re-render — in the live app, with live
data, broadcast to every connected device.

### Component inspector panel

The full editing surface: live previews at desktop/phone/tablet side by side,
typed prop controls, state controls, style controls, and buttons for
**Copy JSX**, **Commit to Code**, and **Save as Story**.

## Code integration

### Commit to code

Turn live edits into a reviewable diff. Prop changes become a JSX diff, style
changes become a CSS/SCSS/module diff, both resolved through sourcemaps to the
actual files. Preview before applying; commit directly or open a PR.

### Save as story

Generate a Storybook story from whatever you just built by hand in the live app —
args, viewport, and context included.

### AI-suggested edge cases

While you edit a component, the AI proposes the cases you did not think to try
(empty title, price 0, price 9999.99, out of stock) and loads any of them across
every device on click.

## Continuous audits

Run the audit suite on **every** CSS or prop change instead of on demand:
accessibility, responsive overflow, layout thrash, and design-system consistency,
aggregated per device with AI suggestions.

```json
{
  "type": "css_audit",
  "change": ".card { border-radius: 12px }",
  "issues": [{"severity": "warning", "message": "Other cards use 8px radius"}],
  "devices": {"desktop": {"status": "ok"},
              "mobile": {"status": "warning", "issue": "Cards overflow at 320px"}}
}
```

## Explicit non-goals

- **Svelte/Solid component editing** — they compile away; revisiting with
  community input.
- **Visual regression in CI** — Playwright and Chromatic already do this well;
  agnt's [snapshot](/api/snapshot) tool is for local runs.
- **Replacing Storybook** — agnt complements it.
- **Production monitoring** — agnt is a development tool.
