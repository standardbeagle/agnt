# Walkthrough: Fixing a Marketing Page That Breaks on Mobile

## What it is

A guided workflow for finding and fixing responsive-layout defects with two
complementary agnt surfaces:

- **`responsive_audit`** — a headless MCP tool that loads the page in hidden
  iframes at several viewport sizes and reports layout, overflow, and mobile
  accessibility issues.
- **Interactive responsive mode** — a browser panel (`window.__devtool_responsive`)
  that lets a human drag a resize handle, jump to preset widths, and push the
  observed breakpoint straight into the agent's context.

Both share one detector core (`internal/proxy/scripts/responsive.js`), so what
the human sees dragging the handle is the same thing the automated sweep scores.

## Why it's unique

Most responsive checks are either fully manual (resize the browser and eyeball
it) or fully automated (a CI lighthouse run you read later). agnt closes the
loop between them:

- The **automated sweep** runs in-page against your real, running dev server —
  not a screenshot, not a static render — so it catches issues that only appear
  after hydration and real CSS.
- The **interactive mode** is bidirectional: `setWidth()` from the agent and the
  human's edge-drag funnel through the same state, so an agent can reproduce
  exactly the width where the human saw the break, and the human can watch the
  agent's fix reflow live.
- Findings carry stable IDs (FNV-1a over type + selector + message, shared with
  the other audit modules), so the same overflow reported twice is the same
  finding — you can highlight it in the page and track whether a fix cleared it.

## Real-world scenario

A marketing landing page looks perfect on the designer's 27" monitor. On a
phone: a hero headline pushes a horizontal scrollbar, the "Get started" CTA is a
36px-tall tap target, and tapping the email field zooms the whole page because
its font-size is 14px. None of this is visible at desktop width. You want to
find every instance, fix it, and prove it's gone.

## Step by step

### 1. Make sure the page is proxied

The audit runs through a proxy so agnt can inject its detector scripts. If you
don't already have one:

```
proxy {action: "start", target: "http://localhost:3000"}
```

Note the returned proxy id (referred to as `dev` below).

### 2. Run the default sweep

```
responsive_audit {proxy_id: "dev"}
```

This loads the page sequentially at the three default viewports:

| Name    | Width | Height |
|---------|-------|--------|
| mobile  | 375   | 667    |
| tablet  | 768   | 1024   |
| desktop | 1440  | 900    |

Sequential (not parallel) loading is deliberate — it avoids the memory spike of
several full-page iframes at once. Expect roughly 3-6 seconds for three
viewports.

The compact report groups findings by check:

- **overflow** — horizontal scroll, clipped content, truncated text, squeezed
  images. This is where the hero headline's horizontal scrollbar shows up.
- **a11y** (mobile only) — touch targets smaller than the 44x44px Apple HIG
  minimum (the CTA), inputs whose font-size is under 16px and therefore trigger
  iOS auto-zoom (the email field), and text under 12px.
- **layout** — collapsed content, fixed elements covering the viewport,
  margin/padding squeeze.

### 3. Narrow or widen the sweep as needed

Run only the checks you care about:

```
responsive_audit {proxy_id: "dev", checks: ["overflow", "a11y"]}
```

Add a custom breakpoint (for example the narrow 320px iPhone SE) alongside or
instead of the defaults:

```
responsive_audit {proxy_id: "dev", viewports: [{name: "xs", width: 320, height: 568}]}
```

Get the full machine-readable detail (every issue, selector, and measurement)
instead of the compact text:

```
responsive_audit {proxy_id: "dev", raw: true}
```

### 4. Reproduce the exact break interactively

The sweep tells you *what* is wrong; interactive mode lets you (and the agent)
watch it. In the browser, open the responsive panel, or drive it from the agent
via proxy exec:

```
proxy {action: "exec", id: "dev", code: "window.__devtool_responsive.open()"}
proxy {action: "exec", id: "dev", code: "window.__devtool_responsive.setWidth(375)"}
```

`open()` mounts a resizable content iframe; `setWidth(375)` drives it to the
width where the sweep flagged the overflow. A human can instead grab the edge
handle and drag — both paths update the same `state` object, so the agent and
the human never disagree about the current width.

When the human finds the precise breaking width, the panel's **[Send to agent]**
button emits a `responsive_request` event (`core.send('responsive_request', …)`)
that lands in the agent's context — no copy-paste of "it breaks around 400px."

### 5. Fix the CSS

With the offending selectors from the audit and the reproduced width in hand,
apply the fixes: constrain the hero (`max-width: 100%`, wrap the headline),
raise the CTA to at least 44x44px, and bump the email input to `font-size: 16px`
to stop iOS zoom.

### 6. Verify with an auto-sweep

Interactive mode can re-run the same headless multi-viewport audit against the
live panel (`runAutoSweep`, which reuses the `responsive.js` detectors), or just
re-run the MCP tool:

```
responsive_audit {proxy_id: "dev", checks: ["overflow", "a11y"]}
```

Because findings have stable IDs, a cleared overflow simply disappears from the
report. Zero findings on the previously-failing checks is your proof.

## Gotchas

- **`proxy_id` is required.** You can pass it as `proxy_id` (preferred) or the
  `id` alias, but one of them must be set or the tool errors out.
- **Only three check types are valid:** `layout`, `overflow`, `a11y`. Anything
  else is rejected before the audit runs.
- **a11y findings are mobile-only by design.** Touch-target and iOS-zoom checks
  only fire at mobile widths — they won't appear in the desktop viewport's
  results, which is correct, not a miss.
- **The audit targets the content frame.** In agnt's always-wrap model the page
  is loaded inside a content iframe. `responsive_audit` defaults to `target:
  "inner"` (the active content frame), which is almost always what you want.
  `target: "outer"` audits the chrome shell instead — rarely useful.
- **Viewports need positive dimensions.** Zero or negative width/height is
  rejected; a viewport with no name is auto-labelled `WIDTHxHEIGHT`.
- **Sequential loading means the run isn't instant.** Budget a few seconds and
  raise `timeout` (per-viewport load budget in ms, default 10000) for a heavy
  page rather than assuming the audit hung.
