---
sidebar_position: 13
---

import ModeVideo from '@site/src/components/ModeVideo';

# Responsive Mode

An interactive responsive workbench — the fourth indicator mode, beside sketch and design. It drives a live iframe of the current page at a controllable width, detects layout shifts programmatically as you change the width, and hands a break off to the AI agent for a fix that the agent can then re-verify by driving the same width back.

Where [`responsive_audit`](/api/responsive_audit) is a headless one-shot across fixed viewports, Responsive Mode is a human + agent loop: dial into the exact width where layout breaks, see the broken elements highlighted, ship them to the agent, watch the fix land.

<ModeVideo src="/img/responsive-mode.webm" poster="/img/responsive-mode-poster.webp" label="Responsive mode — a width slider, numeric input, Mobile/Tablet/Desktop presets, Auto-sweep and Send-to-agent buttons across the top, with a live iframe of the page rendered at 414px and layout-shift findings overlaid as severity-colored boxes that update as the width changes." />

## Opening It

- **In the browser**: click the responsive button in the floating indicator toolbar.
- **From the agent**: `window.__devtool.responsive.open()`.

The panel is a fixed drawer hosting `<iframe src=location.href>` inside a positioned wrapper, with a width slider, a numeric input (320–1920), preset chips (375 / 768 / 1440), and an edge drag handle. Every control funnels through one `applyWidth()`, so human-driven and agent-driven (`setWidth`) changes share a single source of truth.

## API

```javascript
window.__devtool.responsive.open()      // open the panel (no-op return state if already open)
window.__devtool.responsive.close()     // close the panel
window.__devtool.responsive.toggle()    // toggle open/closed
window.__devtool.responsive.setWidth(w) // set the frame width (clamped to 320–1920)
window.__devtool.responsive.getState()  // read current width + detected shifts
```

All five return the current state object.

### getState()

```javascript
{
  open: true,
  width: 414,
  shifts: [
    {
      id: "shift-a1b2",
      selector: ".sidebar",
      severity: "critical",
      // ...detector fields (rect, message, isNew, ...)
    }
  ],
  selectors: [
    { id: "shift-a1b2", selector: ".sidebar" }
  ]
}
```

- `width` — the current frame width in px.
- `shifts` — layout-shift findings detected at the current width (reuses the [`responsive_audit`](/api/responsive_audit) layout + overflow detectors against the framed page).
- `selectors` — a compact `{id, selector}` projection of `shifts`, convenient for highlighting.

### setWidth(w)

```javascript
// Agent dials the frame to the reported break, re-checks, then fixes and re-verifies
window.__devtool.responsive.setWidth(414)
const state = window.__devtool.responsive.getState()
console.log(`${state.shifts.length} shifts at ${state.width}px`)
```

Width is clamped to 320–1920. Setting the width re-runs shift detection (debounced ~250ms) and redraws overlays.

## Shift Detection & Overlays

When the framed page settles — and again after any in-frame mutation — the detectors run against the iframe's `contentWindow` at the current width. Findings that are **new at the current width** (vs the previous width) are flagged `isNew`. Each finding is drawn as a severity-colored box with a label over the offending element; overlays clear and redraw on every detection, so they never go stale when you change width.

## Panel Actions

| Action | What it does |
|--------|--------------|
| **Send to agent** | Emits a `responsive_request` event `{width, shifts, selectors}` — handed to the agent via proxylog, the overlay notifier, and (when enabled) the channel sink. The agent fixes, then re-verifies with `setWidth`. |
| **Auto-sweep** | Runs the headless multi-viewport [`responsive_audit`](/api/responsive_audit) and lists every finding in a results drawer — bridges the manual loop and the automated one. |

## Events

| Event | Payload | Forwarded to |
|-------|---------|--------------|
| `responsive_request` | `{width, shifts[], selectors[]}` | proxylog, overlay notifier, channel sink (forwarded as a channel event) |
| `responsive_state` | `{width, shiftCount}` | proxylog + overlay only — **not** channel-forwarded, to avoid per-settle spam |

`responsive_state` is logged on open and whenever the width settles; `responsive_request` fires only on an explicit hand-off.

## Typical Loop

1. Open Responsive Mode, drag the width until the layout breaks.
2. The broken elements light up; click **Send to agent**.
3. The agent receives `{width, shifts, selectors}`, edits the CSS, then calls `setWidth(width)` and reads `getState()` to confirm `shifts` is empty.
4. Click **Auto-sweep** to confirm no regressions at the standard viewports.

## See Also

- [responsive_audit](/api/responsive_audit) — headless multi-viewport audit (the engine Auto-sweep runs)
- [Layout Diagnostics](/api/frontend/layout-diagnostics) — overflow and stacking-context primitives
- [Responsive Design Testing with AI](/guides/responsive-design-testing-ai) — workflow guide
