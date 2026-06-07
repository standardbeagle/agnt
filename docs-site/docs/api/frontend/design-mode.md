---
sidebar_position: 16
---

import ModeVideo from '@site/src/components/ModeVideo';

# Design Mode

AI-assisted UI iteration on an existing element. The developer picks an element on the live page; the agent generates several design alternatives; the developer navigates them and refines via chat. Where [Sketch Mode](/api/frontend/sketch-mode) draws from scratch, design mode reworks what's already there.

<ModeVideo src="/img/design-mode.webm" poster="/img/design-mode-poster.webp" label="Design mode active — a prompt at the bottom reads Click an element to start design iteration, ESC to cancel." />

## API

```javascript
window.__devtool.design.start()             // enter design mode
window.__devtool.design.stop()              // exit
window.__devtool.design.selectElement()     // pick the element to iterate on
window.__devtool.design.next()              // next alternative
window.__devtool.design.previous()          // previous alternative
window.__devtool.design.addAlternative(d)   // add an agent-generated alternative
window.__devtool.design.applyAlternative()  // apply the current alternative to the page
window.__devtool.design.chat(message)       // refine via natural-language chat
window.__devtool.design.getState()          // read current element + alternatives + index
```

## Workflow

1. **Select** — `design.start()` then `design.selectElement()`; the developer hovers and clicks the element to redesign.
2. **Context to agent** — selecting emits a `design_state` event with the element's markup, computed styles, and surrounding context.
3. **Generate** — the agent produces 3–5 alternatives and feeds them back with `design.addAlternative()`. A request for more is a `design_request` event.
4. **Navigate** — `design.next()` / `design.previous()` cycle the alternatives; `design.applyAlternative()` swaps the chosen one into the live DOM.
5. **Refine** — `design.chat("make the CTA larger and use the brand green")` emits a `design_chat` event; the agent revises.

## Events

| Event | Emitted when | Payload |
|-------|--------------|---------|
| `design_state` | An element is selected for iteration | element markup, computed styles, context |
| `design_request` | A request for new alternatives | current element + constraints |
| `design_chat` | A chat refinement message | message + current element |

Read them from the proxy log:

```json
proxylog {proxy_id: "dev", types: ["design_state", "design_request", "design_chat"]}
```

## Inspecting State

```javascript
const s = window.__devtool.design.getState()
// { element, alternatives: [...], index, ... }
```

## See Also

- [Sketch Mode](/api/frontend/sketch-mode) — wireframe a new layout from scratch
- [Floating Indicator](/api/frontend/indicator) — where design mode is launched
- [Element Inspection](/api/frontend/element-inspection) — the primitives design mode builds on
