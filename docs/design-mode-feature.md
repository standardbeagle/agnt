# Design Doc: Fast, On-Scheme Design Mode

| | |
|---|---|
| **Status** | Draft |
| **Author** | agnt |
| **Created** | 2026-06-20 |
| **Reviewers** | — |
| **Related** | `internal/proxy/scripts/design.js`, `cmd/agnt/overlay_format.go`, `internal/proxy/logger.go` |

> **Note:** This is the *engineering design doc* for the Design-mode feature. It
> is distinct from **DESIGN.md** — a reserved, per-project file in the
> [google-labs-code/design.md](https://github.com/google-labs-code/design.md)
> format that describes a single app's visual identity. Design mode *generates*
> a DESIGN.md at each proxied project's root (§6.6); this doc describes how. It
> was renamed off `design.md` to free that reserved name.

---

## 1. Context / Background

agnt bridges an AI coding agent and a browser. The reverse proxy
(`internal/proxy/`) injects JavaScript into proxied HTML responses, exposing a
floating **indicator** with action buttons. One of those buttons is **Design**.

### How the Design button works today

1. **Browser** — `indicator.js:3175` wires the Design button to
   `startDesignMode()` (`indicator.js:4978`), which calls
   `window.__devtool_design.start()` (`design.js`).
2. **Selection** — `design.js` overlays a crosshair; the user clicks an element.
   `selectElement()` (`design.js:197`) captures `selector`, `xpath`,
   `originalHTML`, parent `contextHTML`, and element `metadata`, then
   `sendDesignState()` emits a `design_state` WebSocket message via `core.js`.
3. **Proxy** — `ws_handler.go:542` parses the message
   (`ws_parse.go:381 parseDesignState`), logs it (`logger.go LogDesignState`,
   type `DesignState`), and notifies the overlay socket (`overlay.go`).
4. **Daemon → Agent** — `overlay_format.go:368 formatDesignStateText` renders a
   human-readable prompt injected into the agent's stdin/MCP channel. The prompt
   instructs the agent to act as a UX designer and produce **3–5 alternatives**,
   each pushed back via
   `proxy {action:'exec', code:"__devtool_design.addAlternative('<html>')"}`.
5. **Browser** — `addAlternative()` appends HTML strings to
   `state.alternatives[]`; Prev/Next navigation applies
   `selectedElement.innerHTML = alternatives[i]`. A chat box emits
   `design_chat` / `design_request` for refinement.

### Source-of-truth data structures (`internal/proxy/logger.go`)

- `DesignState` (356–386) — selection snapshot.
- `DesignRequest` (388–401) — "give me more alternatives".
- `DesignChat` (420–433) — free-text refinement.
- `DesignElementMetadata` (356–367) — tag/id/classes/attrs/text/rect.

There is **no design-scheme schema**: alternatives are opaque HTML strings, so
each variation re-invents colors, type, and spacing with no guarantee it matches
the proxied app's look. Generation is **single-agent and sequential** — all
3–5 alternatives come from one model call, so nothing appears until the whole
batch is authored (10–60 s of dead time).

## 2. Problem

Two concrete pain points:

- **Slow first paint.** The user sees nothing until the single agent finishes
  the entire batch. There is no "something now" feedback.
- **Off-scheme output.** Variations drift from the proxied app's design language
  because the agent is given only the selected element's HTML, not the app's
  tokens (palette, type scale, spacing, component conventions).

## 3. Goals

- **G1** — First alternative ("fast draft") visible in the browser **as soon as
  possible**, well before the polished set is ready.
- **G2** — Polished variations are **on-scheme**: constrained by design tokens
  extracted live from the proxied target app.
- **G3** — Variation generation runs **concurrently** (one subagent per
  variation) instead of one serial model call.
- **G4** — Each alternative is **labeled** (draft vs on-scheme variation) so the
  user understands fidelity while navigating.
- **G5** — Preview is **non-invasive**: never write generated HTML into the
  framework-owned target subtree. Render in an isolated panel (side-by-side and
  overlay-on-top), so React reconciliation / HMR cannot wipe it and component
  state is never corrupted. Survive HMR recompiles without losing work.

## 4. Non-Goals

- Not redesigning the selection UX or the chat-refinement loop.
- Not persisting design schemes to disk or across sessions.
- Not auto-applying any alternative; the user still chooses.
- Not changing `DesignRequest` / `DesignChat` flows beyond passing the scheme
  through.

## 5. Overview

```
Select element
      │
      ▼
design.js extracts DesignScheme (palette, type, spacing, component CSS)
      │  design_state { ...existing..., scheme }
      ▼
overlay_format.go renders a prompt that tells the agent to:
      │
      ├─►  dispatch 1 FAST-DRAFT subagent (cheap/low-effort)
      │        → addAlternative(html, {label:'draft'})   ◄── appears first
      │
      └─►  dispatch N VARIATION subagents CONCURRENTLY, each given the
               extracted scheme + design.md guidance
               → addAlternative(html, {label:'variation', note})  ◄── stream in
```

The orchestrating agent (the model behind `agnt run`) owns the fan-out using its
own subagent mechanism; the daemon's job is to (a) hand it the extracted scheme
and (b) prompt the fast-draft-first + concurrent-variations pattern explicitly.

## 6. Detailed Design

### 6.1 Design-scheme extraction (browser)

New `design.js` function `extractScheme(selectedElement)` gathers, from the live
DOM, a compact token set:

- **Palette** — frequency-ranked `color` / `background-color` / `border-color`
  from the selected element, its ancestors, and a sample of document elements.
- **Typography** — `font-family` set, distinct `font-size` ladder, weights,
  `line-height`.
- **Spacing** — observed `margin` / `padding` / `gap` step values.
- **Radius / shadow / borders** — `border-radius`, `box-shadow` samples.
- **CSS custom properties** — `:root` and ancestor `--*` variables (the app's
  declared tokens, when present — highest-signal source).
- **Sibling component HTML** — already captured as `contextHTML`; reused so
  variations mirror real component structure.

Output is a JSON `scheme` object included in the `design_state` payload (and
`design_request`). Extraction is synchronous computed-style reads over a bounded
element sample (cap ~400 nodes) to stay off the critical path.

### 6.2 Schema (`internal/proxy/logger.go`)

```go
type DesignScheme struct {
    Palette     []string          `json:"palette,omitempty"`      // ranked hex/rgb
    FontFamilies []string         `json:"font_families,omitempty"`
    FontSizes   []string          `json:"font_sizes,omitempty"`
    FontWeights []string          `json:"font_weights,omitempty"`
    Spacing     []string          `json:"spacing,omitempty"`
    Radius      []string          `json:"radius,omitempty"`
    Shadows     []string          `json:"shadows,omitempty"`
    CSSVars     map[string]string `json:"css_vars,omitempty"`
}
```

`DesignState` and `DesignRequest` gain `Scheme *DesignScheme json:"scheme,omitempty"`.
`ws_parse.go` parses it (nil-safe — old clients omit it).

### 6.3 Prompt rewrite (`cmd/agnt/overlay_format.go`)

`formatDesignStateText` is rewritten to:

1. Render the extracted scheme as a compact **"App design scheme"** block
   (palette swatches, type ladder, spacing steps, CSS vars).
2. Instruct the agent to **first** produce a fast draft via a cheap/low-effort
   subagent and `addAlternative(..., {label:'draft'})` so it shows immediately.
3. Instruct the agent to **then dispatch N variation subagents concurrently**
   (single message, multiple dispatches), each constrained to the scheme + the
   design.md design principles, each calling `addAlternative(..., {label, note})`.
4. Point at `design.md` for the design language and at `agnt`'s exec contract.

When `scheme` is nil (legacy client), fall back to the current prompt so nothing
regresses.

### 6.4 Browser rendering (`design.js`)

`addAlternative(html, meta)` gains an optional `meta` arg
(`{label, note}`); the panel shows a badge ("Fast draft" / "On-scheme 1/4") and
the optional note. Backward compatible: `meta` omitted → labeled `variation`.

### 6.5 Non-invasive preview panel (G5)

**Problem found in testing:** writing `selectedElement.innerHTML = alt` fights a
React/Turbopack dev server — reconciliation + HMR wipe the injected DOM, the
stored `nth-of-type` selector goes stale (`innerHTML … not an object`), and each
recompile reset the in-memory alternatives list (count bounced 4→3→1→4).

Resolution — the browser never mutates the target subtree:

- **OID-primary locator.** `selectElement` stamps a stable `data-devtool-oid`
  via `__devtool_override_store.ensureOID`. `resolveTarget()` resolves by oid
  first, selector/xpath only as fallback. `nth-of-type` is never authoritative.
- **Decouple store from preview.** `addAlternative` stores + persists first and
  renders best-effort; it no longer requires a live node, so a mid-recompile
  absent node can't throw away the alternative. `addAlternatives([...])` adds the
  whole concurrent set in one round-trip (no per-add recompile race).
- **Shadow-root panel.** Previews render into a Shadow-DOM panel
  (`attachShadow`, own style reset) appended at `document.body` — untouchable by
  app reconciliation. Two modes, toggleable:
  - **Side-by-side** — `Original | Alternative N` columns.
  - **Overlay-on-top** — alternative floated over the target's `getBoundingClientRect`
    in a second shadow host, repositioned on scroll/resize.
- **Persist across HMR.** Alternatives + meta + index are mirrored to
  `sessionStorage` (keyed by oid+path); `restorePersisted()` re-hydrates on
  re-select so a recompile no longer thrashes the list. Mode choice persists too.

Deferred (separate work): OID→source JSX map so a chosen alternative writes the
real component change; geometry edits mapped to framework idioms (Tailwind
`max-w-*`/`h-auto`) instead of baked `px`.

### 6.6 Per-project DESIGN.md + notification (the missing surface)

**Problem found in testing:** the feature never produced a DESIGN.md and gave no
sign anything was happening. DESIGN.md is per-project (it describes one app's
visual identity), so there is no single canonical file — each proxied project
gets its own at its root.

- **Notification (deterministic).** `design.js` toasts via `__devtool_toast` on
  `start()` ("Active — click an element…") and after selection ("Scheme captured
  — preparing DESIGN.md & on-scheme alternatives…"). The developer always sees
  activity even though generation is async on the agent side.
- **DESIGN.md authoring (agent, STEP 0).** The injected prompt's STEP 0 makes the
  agent, before any alternative: check the project root for DESIGN.md; if absent,
  create it in the
  [google-labs-code](https://github.com/google-labs-code/design.md) format from
  the extracted scheme (palette→`colors`, fonts→`typography`, radius→`rounded`,
  spacing→`spacing`, element/siblings→`components`) and announce it; if present,
  read it and treat it as the authoritative design system (overrides the
  extracted scheme on conflict).
- **Source of truth.** DESIGN.md, once written, is the durable on-scheme
  constraint; `extractScheme` only bootstraps it when absent.

## 7. Alternatives Considered

- **Client-side fast draft (no model).** Generate the draft purely in JS (e.g.
  apply a token-swapped restyle). Rejected: too low-value/generic; a cheap
  low-effort subagent yields a real draft for marginal latency.
- **Daemon-side fan-out.** Have the daemon spawn the subagents. Rejected: the
  daemon has no model access; the agent already owns subagent dispatch and
  context. Keeps the daemon a formatter/transport.
- **Stream partial HTML per variation.** Progressive skeleton→refine streaming.
  Deferred: larger protocol change; fast-draft-first already covers first-paint.
- **Invasive in-place preview (`innerHTML` into target).** The original design.
  Rejected after testing: React/HMR wipes it, stale selectors throw, work is
  lost on recompile (§6.5).
- **Iframe snapshot for isolation.** Full style fidelity but heavier, snapshot
  goes stale, more plumbing. Rejected in favor of a Shadow-root panel — light,
  no reload, immune to reconciliation; acceptable that the app's CSS cascade
  doesn't fully inherit into the shadow render.

## 8. Testing

- **Go** — extend `overlay_format_test.go`: scheme block renders; fast-draft +
  concurrent-variation instructions present; nil-scheme falls back to legacy
  prompt. `ws_parse` round-trips `DesignScheme`; nil-safe on absent field.
- **JS** — `extractScheme` returns bounded, well-formed tokens; `addAlternative`
  with/without `meta` both render (covered by existing `design.js` harness if
  present, else manual via proxy exec).
- **Manual** — proxy a sample app, click Design, confirm draft appears first and
  on-scheme variations stream in with badges.

## 9. Rollout

Single change set; no config flag (pure additive enhancement, legacy-safe
fallbacks). Version bump via `scripts/release.sh`. No migration.
