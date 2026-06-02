# Browser In-Page Audit Suite Expansion — Design

Date: 2026-06-02. Status: implemented (worktrack epic `agnt/EPIC: Browser in-page audit suite expansion`).

## Context

The proxy-injected `__devtool` suite already ships six scored audits (DOM, CSS,
performance, security, SEO, accessibility) plus a responsive *audit* and a
responsive-risk scan. This epic adds three capabilities:

1. **Responsive Mode** — an interactive responsive workbench (4th indicator mode).
2. **API Efficiency Audit** — a 7th scored audit over the fetch/XHR buffer.
3. **Loading (Spinner) Audit** — an 8th scored audit over a spinner timeline.

Each feature ships independently. All ride existing plumbing rather than
introducing new subsystems.

## Feature A — Responsive Mode

Interactive, human + agent partnership: drive a live iframe at a controllable
width, detect layout shifts programmatically, dial into a break, hand off to the
agent for a fix, then re-verify.

- **Surface**: new 4th indicator mode beside sketch/design (`indicator.js`
  toolbar button + `startResponsiveMode()`), implemented in a new
  `responsive-mode.js` that **augments** the existing `window.__devtool_responsive`
  object (created by `responsive.js`) without clobbering its audit API. Public
  namespace `__devtool.responsive.{open,close,toggle,setWidth,getState}` in
  `api.js`. Registered in `embed.go` after `responsive`.
- **Panel**: fixed drawer hosting a live `<iframe src=location.href>` inside a
  positioned `frameWrap`, with a width slider + numeric input (320–1920) + preset
  chips (375/768/1440) + an edge drag handle. Every control funnels through a
  single `applyWidth()` so human-driven and agent-driven (`setWidth`) changes
  share one source of truth.
- **Shift detection** (`captureShifts`, debounced 250 ms): reuses
  `responsive.js`'s `detectLayoutIssues(win,width)` + `detectOverflowIssues`
  against the panel iframe's `contentWindow` at the current width. Findings new
  at the current width vs the previous one are flagged `isNew`. Returned via
  `getState().shifts/selectors`.
- **Overlay** (`renderOverlays`): draws severity-colored boxes + labels over the
  flagged elements; cleared and redrawn every detection (stale overlays clear on
  width change).
- **Channel handoff**: a "Send to agent" button emits a new `responsive_request`
  event `{width, shifts[], selectors[]}` through the proxy WS pipeline →
  TrafficLogger (`LogTypeResponsiveRequest`) → proxylog + overlay notifier +
  channel sink (forwarded as a channel event; added to `validEventTypes`). A
  lighter `responsive_state` `{width, shiftCount}` is logged on open/settle but
  intentionally NOT channel-forwarded (avoids per-settle spam). The agent can
  drive width back via `__devtool.responsive.setWidth(w)`.
- **Auto-sweep**: a button that runs the existing headless `responsive.js`
  multi-viewport audit (`ns.audit({raw:true})`) and lists all findings in a
  results drawer — bridges manual and automated.

## Feature B — API Efficiency Audit

New scored audit + MCP tool over the always-on `api-tracker.js` fetch/XHR buffer
(`window.__devtool_api.getCalls()`). No new recorder.

- **Module** `audit-api.js` → `window.__devtool_audit_api.auditAPIEfficiency(opts)`,
  registered in `embed.go`, joins the shared `window.__devtool.audit` registry.
- **Four detectors**: waterfall/serial-chain (B starts ≈ A ends, sum wasted vs
  parallel-possible); N+1 (URL→template normalization, ≥5 sharing a `{id}`
  template); redundant/duplicate (identical method+url within 2 s); over-fetch →
  chatty-load (call count in the first 3 s load window).
- **Fail-honest data limitation**: the call buffer has **no response-size**
  field, so over-fetch by payload size is not measurable. The audit emits an
  explicit `over-fetch-unavailable` info note instead of fabricating a size.
  Documented; a future content-length capture in `api-tracker.js` would unlock
  real payload-bloat detection.
- **Scoring**: 100 minus weighted findings → grade A–F. Output matches the
  audit-module shape (`score/grade/summary/findings/findingSelectors`), compact
  by default, full with `raw:true`.
- **MCP tool** `api_audit {proxy_id, raw?}` (`internal/tools/api_audit.go`),
  mirrors `responsive_audit.go`: dual daemon/legacy backend, proxy-exec the audit
  JS, errors as `CallToolResult{IsError}`. Registered in both `serve.go` blocks.
  Empty buffer → "reload page then re-run".

## Feature C — Loading (Spinner) Audit

New scored audit + MCP tool detecting two loading-UX failure modes.

- **Recorder** (C1, `mutation.js`): a self-contained spinner observer (separate
  from the existing mutation pipeline) exposing `window.__devtool_spinners`
  (`getTimeline/start/stop/clear/isActive`). Detects loading indicators via
  `aria-busy`, `role=progressbar|status`, `<progress>`, class/id/aria match
  `spin|load|skeleton|shimmer|pending|placeholder`, and spin-like
  `animationName`. Records `{id, selector, ancestorPath, appearedAt,
  disappearedAt, pendingAPI}` per element (WeakMap pairing of appear/disappear);
  correlates each spinner's active window with overlapping api-tracker calls.
- **Module** `audit-loading.js` → `window.__devtool_audit_loading.auditLoading(opts)`,
  registered in `embed.go`. Two detectors:
  - **Cascade** (`spinner-cascade`): B appears after A disappears (gap ≤400 ms),
    B in A's region (ancestorPath overlap), A had a resolved API → serial chain
    depth ≥2; reports serial span vs parallel-possible.
  - **Fragmentation** (`spinner-fragmentation`): ≥3 spinners active
    simultaneously under one common ancestor → "consolidate to one master
    loader".
- **Scoring**: 100 minus cascade (depth-weighted, +critical) and fragmentation
  deductions → grade. Same output shape; empty timeline → 100/A + "reload page".
- **MCP tool** `loading_audit {proxy_id, raw?}` (`internal/tools/loading_audit.go`),
  mirrors `api_audit.go`. Registered in both `serve.go` blocks.

## Cross-cutting (Feature D)

- **auditAll** (`audit-quality.js`) grows 6 → 8 scored audits: `api` and
  `loading` wired into both the compact and raw paths (scores map, weighted
  overall, priority order, audits object, addIssues, fullResults). Final weight
  table: security 1.5, accessibility 1.3, performance 1.2, api 1.1, loading 1.1,
  seo 1.0, dom 0.8, css 0.7. Both new audits are guarded — `auditAll` degrades
  cleanly when a module is absent.
- **Registration**: each new script is registered in `embed.go` by its own slice
  (responsive-mode after responsive; audit-api after audit-performance;
  audit-loading after audit-api), and the new audit ids are covered by
  `audit_ids_test.go`. `TestModuleDependencyOrder` enforces load order.
- **Tests**: `audit_api_test.go` + `audit_loading_test.go` assert the JS modules'
  structural contracts statically. The repo has no embedded JS engine, so
  detectors are not executed from Go — this matches the existing static-only
  precedent (`audit_ids_test.go`) and avoids adding a JS-runtime dependency
  (over-engineering). Runtime behavior is verified manually in a browser.

## Environment note

The optional `browser-render-check` step of each slice was skipped throughout:
this WSL environment has no `google-chrome` binary, so live in-browser
verification of the panels/overlays/audits is deferred to a Chrome-capable host.
All Go build + race tests (scoped to changed packages) passed.

## Key files

| Area | Files |
|------|-------|
| Responsive mode | `internal/proxy/scripts/responsive-mode.js`, `indicator.js`, `api.js`, `embed.go`; `logger.go`, `ws_parse.go`, `ws_handler.go`, `overlay.go`, `internal/tools/channel_sink.go`, `internal/config/agnt.go` |
| API audit | `internal/proxy/scripts/audit-api.js`, `audit-quality.js`, `embed.go`; `internal/tools/api_audit.go`, `cmd/agnt/serve.go`; `audit_api_test.go` |
| Loading audit | `internal/proxy/scripts/mutation.js`, `audit-loading.js`, `audit-quality.js`, `embed.go`; `internal/tools/loading_audit.go`, `cmd/agnt/serve.go`; `audit_loading_test.go` |
