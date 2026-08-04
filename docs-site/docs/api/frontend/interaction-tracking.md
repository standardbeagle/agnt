---
sidebar_position: 13
---

# Interaction & Mutation Tracking

Two continuously-recording histories the injected instrumentation keeps on every proxied page: what the user did (`window.__devtool.interactions`) and what the DOM did in response (`window.__devtool.mutations`). Together they answer the question no amount of source reading can — "when the user typed/clicked/tabbed, what actually happened?" — which is the decisive evidence for input-binding bugs, re-render storms, and dead click handlers.

Both record passively from page load; there is nothing to start. Records also flow to the proxy traffic log (`proxylog` types `interaction` and `mutation`), so history survives navigation for after-the-fact queries.

## Interactions — `window.__devtool.interactions`

Captures `click`, `dblclick`, `keydown`, `input`, `focus`, `blur`, `scroll`, `submit`, and `contextmenu` events document-wide (capture phase), plus a rolling mouse-movement buffer. Events on agnt's own UI are excluded.

| Function | Returns |
|----------|---------|
| `getHistory(count?)` | Last `count` interaction records (default 50), oldest first |
| `getLastClick()` | Most recent click record |
| `getClicksOn(selector)` | Clicks whose target matches a selector |
| `getMouseTrail(timestamp, trailMs?)` | Mouse positions leading up to a moment — how the pointer arrived |
| `getMouseBuffer()` | The raw recent mouse-movement buffer |
| `getLastClickContext()` | The last click bundled with its mouse trail — "what did the user click, and how" |

Record shape:

```json
{
  "event_type": "input",
  "target": {
    "selector": "input#email.form-field",
    "tag": "input",
    "id": "email",
    "classes": ["form-field"],
    "attributes": {"name": "email", "type": "text"}
  },
  "timestamp": 1754340000000
}
```

Keyboard events add a `key` object; mouse events add position. Example — did the user's clicks land on the element the handler is bound to?

```json
proxy {action: "exec", id: "app", code: `
  window.__devtool.interactions.getClicksOn('button.submit')
`}
```

An empty result while the user swears they clicked usually means the clicks landed on an overlaying element instead — cross-check with [click interception diagnostics](/api/frontend/layout-diagnostics).

## Mutations — `window.__devtool.mutations`

A MutationObserver over `document.body` (childList + subtree, plus attribute and character-data tracking per config) recording structural and attribute changes:

| Function | Returns |
|----------|---------|
| `getHistory(count?)` | Last `count` mutation records (default 50) |
| `getAdded(since)` | Node-addition records after a timestamp |
| `getRemoved(since)` | Node-removal records after a timestamp |
| `getModified(since)` | Attribute-change records after a timestamp |
| `highlightRecent(since?)` | Visually flashes recently-mutated elements on the page |

Record shape (addition; removals carry `removed`, attribute changes carry `attribute: {name, old_value, new_value}`):

```json
{
  "mutation_type": "added",
  "target": {"selector": "div.search-results", "tag": "div"},
  "added": [{"selector": "li.result-row", "tag": "li", "html": "<li class=\"result-row\">..."}],
  "timestamp": 1754340000123,
  "triggered_by": {"type": "input", "target": "input#search", "latency": 42, "timestamp": 1754340000081}
}
```

`triggered_by` is the built-in correlation: each mutation names the most recent user interaction within a 500ms window that plausibly provoked it (`null` when the page mutated on its own), so "the subtree re-mounted *because of the blur*" is a field read, not an inference.

Example — is anything churning on an idle page?

```json
proxy {action: "exec", id: "app", code: `
  (function() {
    const before = Date.now();
    return new Promise(r => setTimeout(r, 2000)).then(() =>
      window.__devtool.mutations.getHistory(200).filter(m => m.timestamp > before).length);
  })()
`}
```

A visually idle page returning hundreds of mutations in two seconds is a re-render storm, localized by grouping the records on `target.selector` — the full workflow is in [Debugging React Re-renders](/guides/react-rerender-debugging-ai).

## Known Limits

- **Property writes are invisible.** MutationObserver sees attributes, not properties. A controlled input's typed text updates the `value` *property*, so per-keystroke value changes produce no mutation records — read the element's live `value` directly when that is the evidence you need (see [Form & Input Binding](/guides/form-input-binding-debugging-ai)).
- **Bounded buffers.** Histories are rolling; on a busy page, pull promptly or query the proxy log (`proxylog {types: ["interaction"]}` / `["mutation"]`) for retained entries.
- **Capture-phase listeners.** A handler that calls `stopPropagation` in the capture phase before the document listener can suppress recording for that event; this is rare outside instrumentation-hostile pages.

## See Also

- [Form & Input Binding Debugging](/guides/form-input-binding-debugging-ai) — the correlation workflow end to end
- [React Re-render Debugging](/guides/react-rerender-debugging-ai) — mutation churn as a render-storm meter
- [Layout Diagnostics](/api/frontend/layout-diagnostics) — click-interception analysis for dead-handler bugs
- [proxylog](/api/proxylog) — the retained `interaction` / `mutation` log entries
