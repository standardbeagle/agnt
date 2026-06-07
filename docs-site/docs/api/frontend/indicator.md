---
sidebar_position: 14
---

# Floating Indicator

A draggable on-page widget injected by the proxy. It shows connection status, hosts a text input for the developer to message the AI agent, and is the launch point for screenshot capture, element selection, and the sketch / design / responsive modes.

The indicator is **visible by default** and remembers its position across reloads.

<video autoPlay loop muted playsInline controls width="100%" aria-label="The floating indicator with its message panel open — tabs for Overview/Errors/Network/Perf, a message box, and quick-action chips for Screenshot, Element, Sketch, Design, Inspect, Responsive, and Audit.">
  <source src="/img/indicator.webm" type="video/webm" />
</video>

## API

```javascript
window.__devtool.indicator.show()        // show the indicator
window.__devtool.indicator.hide()        // hide it
window.__devtool.indicator.toggle()      // toggle visibility
window.__devtool.indicator.togglePanel() // open/close the message panel
window.__devtool.indicator.destroy()     // remove it from the page entirely
```

## Messaging the Agent

The indicator's panel holds a text input. When the developer types a message and sends it, the proxy logs a `panel_message` event that the agent receives — through the channel sink (channel mode), the PTY stdin injection (`agnt run`), or the proxy log (pull).

```json
proxylog {proxy_id: "dev", types: ["panel_message"]}
```

```javascript
{
  type: "panel_message",
  message: "the submit button does nothing on mobile",
  page: "http://localhost:3000/checkout",
  timestamp: 1699999999999
}
```

This is the human → agent back-channel that pairs with [`channel_reply`](/api/channel_reply) (agent → human).

## Quick Actions

From the indicator the developer can:

- **Capture a screenshot** of the page (or a selected element)
- **Select an element** to attach to a message (feeds `getLastClickContext`)
- **Open sketch mode** — see [Sketch Mode](/api/frontend/sketch-mode)
- **Open design mode** — see [Design Mode](/api/frontend/design-mode)
- **Open responsive mode** — see [Responsive Mode](/api/frontend/responsive-mode)

## Connection Status

The indicator reflects the proxy WebSocket connection state, so the developer can see at a glance whether diagnostics are live. Programmatically:

```javascript
window.__devtool.isConnected()  // true when the metrics socket is up
```

## See Also

- [Sketch Mode](/api/frontend/sketch-mode) — wireframing overlay
- [Design Mode](/api/frontend/design-mode) — AI UI iteration
- [channel_reply](/api/channel_reply) — agent's reply path to the overlay
