# Walkthrough: Layout Diagnostics — the modal under the sticky header

## What it is

A set of runtime CSS introspection helpers that tell you *why* a layout is
broken, not just *that* it is. They run inside the live page through
`proxy {action:"exec"}` and return the offending ancestor, the exact CSS
property to change, and the common wrong fix to avoid:

- `window.__devtool.getStacking(selector)` — resolves the real stacking root a
  z-index competes in, and the property that created it (`rootTrigger`).
- `window.__devtool.getContainer(selector)` — for `position:fixed`/`absolute`,
  the ancestor that captured it (`trappedBy`).
- `window.__devtool.diagnoseLayoutIssues()` (= `window.__devtool_layout.diagnose()`)
  — one bounded pass over four cause→symptom bug classes.
- `currentpage {action:"triage"}` — lifts recognized framework runtime warnings
  (React hydration, missing keys, Vue reactivity, etc.) out of the error stream
  and names the bug class.

Source of truth: `internal/proxy/scripts/layout.js`,
`internal/proxy/scripts/inspection.js`, `internal/proxy/scripts/utils.js`
(shared trigger detection), framework triage in
`internal/tools/currentpage_diagnostics.go`.

## Why it is unique

The hardest CSS bugs are non-textual: the decisive evidence is the *computed*
stacking / containing-block state, which never appears in the source. This is
exactly where source-only LLM reasoning reliably suggests the wrong fix — bump
the z-index, add `!important`, nudge `top`/`left`. These helpers surface the
runtime cause directly, so the fix targets the real containing block or
stacking root instead of fighting symptoms. The CSS-trigger detection is
centralized in `utils.stackingContextTriggers` / `utils.containingBlockTrap`,
so `getStacking`, `getContainer`, and `findStackingContexts` can never disagree
about what creates a context.

## Real-world scenario

A checkout modal renders *underneath* a sticky site header. The obvious fix —
raise the modal's `z-index` to `9999` — does nothing. The header still wins.
The modal markup and the header markup live in completely different parts of
the tree, and the header's ancestor has `transform: translateZ(0)` (a common
"promote to its own layer" trick). That transform silently created a stacking
context, so the modal's `z-index:9999` is being compared against siblings
*inside the modal's own root*, not against the header at all.

## Step by step

Assume a proxy is already running for the project (see
`docs/walkthroughs/frame-model-auth-breakout.md` for starting one). All exec
calls target the active content frame by default.

### 1. Diagnose broadly first

```
proxy {action:"exec", code:"return window.__devtool.diagnoseLayoutIssues()"}
```

Expected output shape (from `layout.js` `diagnose()`):

```json
{
  "findings": [
    {
      "check": "ineffective-zindex",
      "severity": "high",
      "selector": ".modal-overlay",
      "cause": "",
      "cause_property": "position:static",
      "detail": "z-index 9999 is ignored: z-index only applies to positioned elements or flex/grid items, and this element is position:static with a non-flex/grid parent.",
      "fix": "Set position:relative on the element (or make the parent display:flex/grid) so the z-index takes effect.",
      "avoid": "Do not raise the z-index value — it is being discarded entirely, not losing a comparison."
    }
  ],
  "count": 1,
  "scanned": 812,
  "capped": false,
  "by_check": {
    "containing-block-trap": 0,
    "ineffective-zindex": 1,
    "click-interception": 0,
    "clipped-descendant": 0
  }
}
```

The four `check` classes are exactly: `containing-block-trap`,
`ineffective-zindex`, `click-interception`, `clipped-descendant`. Each finding
names the offending ancestor in `cause`, the property in `cause_property`, and
carries `fix` + `avoid`. Findings are capped at 15 per check; `capped` and
`by_check` make truncation explicit.

### 2. Confirm the stacking root with getStacking

If the modal *is* positioned but still loses, the problem is which stacking
root it lives in:

```
proxy {action:"exec", code:"return window.__devtool.getStacking('.modal-overlay')"}
```

Expected output shape (from `inspection.js` `getStacking`):

```json
{
  "zIndex": "9999",
  "position": "fixed",
  "createsContext": true,
  "selfTriggers": [{ "property": "position", "value": "fixed" }],
  "stackingRoot": "div.app-root",
  "rootTrigger": { "property": "transform", "value": "matrix(1, 0, 0, 1, 0, 0)" },
  "chain": [
    { "selector": "div.app-root", "triggers": [{ "property": "transform", "value": "..." }] }
  ],
  "opacity": 1,
  "transform": "none",
  "filter": "none"
}
```

`stackingRoot` is the nearest ancestor stacking context. z-index is only
resolved against siblings *inside that same root* — so if the header lives in a
different root that paints later, the modal's `9999` can never cover it.
`rootTrigger` is the exact property (`transform`) to remove or relocate, or the
signal that you must portal the modal to `<body>` to escape that root.

### 3. If the element is fixed and mispositioned, use getContainer

For a `position:fixed` element that "scrolls away" or lands in the wrong place:

```
proxy {action:"exec", code:"return window.__devtool.getContainer('.modal-overlay')"}
```

Expected output shape (from `inspection.js` `getContainer`):

```json
{
  "type": "normal",
  "name": null,
  "contain": "none",
  "position": "fixed",
  "expectedContainingBlock": "viewport",
  "actualContainingBlock": "div.app-root",
  "trappedBy": { "selector": "div.app-root", "property": "transform", "value": "matrix(...)" },
  "escaped": false
}
```

`trappedBy` non-null means a distant ancestor's `transform`/`filter`/
`will-change`/`contain` captured the fixed element, so it positions relative to
that ancestor instead of the viewport. `escaped: true` (and `trappedBy: null`)
means the fixed element is correctly viewport-relative. The fix is to portal
the modal out of the transformed ancestor, or remove that ancestor's transform
— never to tweak the offsets.

### 4. Framework runtime triage (React hydration example)

The stacking bug may be downstream of a React hydration mismatch that swapped
the tree at runtime. `currentpage` triage lifts recognized framework warnings
out of the raw error stream:

```
currentpage {action:"triage"}
```

(`triage` is the default action; it auto-selects the open page.) Relevant slice
of the output (from `currentpage_triage.go` / `currentpage_diagnostics.go`):

```json
{
  "framework_diagnostics": [
    {
      "category": "hydration-mismatch",
      "framework": "react",
      "count": 1,
      "sample": "Warning: Text content did not match. Server: \"...\" Client: \"...\"",
      "fix": "Server and client rendered different text. Find non-deterministic render input (Date.now, Math.random, window/localStorage, locale/timezone) and defer it to the client (useEffect) or make it deterministic.",
      "avoid": "Do not silence with suppressHydrationWarning — that hides the divergence instead of fixing it."
    }
  ],
  "hint": "Recognized framework runtime diagnostics — see framework_diagnostics for the bug class, the correct fix, and the common wrong fix to avoid before editing source."
}
```

Recognized categories include `hydration-mismatch`, `infinite-render-loop`,
`hooks-order`, `controlled-uncontrolled`, `missing-key`, `cross-render-update`
(React), plus `vue-prop-mutation`/`vue-reactivity`, `svelte-unknown-prop`, and
`solid-root-scope`. Each carries `fix` and `avoid`.

## Gotchas

- **These helpers are read through `proxy {action:"exec"}`.** There is no
  dedicated `getStacking` MCP tool. `diagnoseLayoutIssues()` is also reachable
  directly through `currentpage {action:"layout"}` (returns `PageLayoutOutput`),
  which is the ergonomic path when you just want the layout pass.
- **`exec` returns the value you `return`.** Write `return window.__devtool...`
  — an expression with no `return` yields `undefined`.
- **Vue/Svelte/Solid warnings are `console.warn`, not thrown errors.** They only
  reach triage once the browser forwards signature-matched warnings (the
  allowlist in `core.js`). React errors land in either `message` or `error`, so
  both are matched.
- **`diagnose()` is bounded.** It scans at most 4000 elements and reports 15
  findings per check. On a huge DOM, check the `capped` and `by_check` fields —
  a `capped: true` means there may be more of the same class.
- **`ineffective-zindex` vs a lost comparison are different bugs.** If
  `diagnoseLayoutIssues()` reports `ineffective-zindex`, the z-index is being
  *discarded* (static positioning). If `getStacking` shows a `stackingRoot`
  mismatch, the z-index is *applied but compared in the wrong root*. The fixes
  are different; do not conflate them.
- **`getContainer.trappedBy` only fires for `position:fixed`.** For
  `position:absolute` a positioned ancestor is the normal containing block, so
  `trappedBy` stays `null`; inspect `actualContainingBlock` instead.
