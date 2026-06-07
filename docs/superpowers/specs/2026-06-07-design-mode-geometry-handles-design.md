# Design Mode Geometry Handles — Design Spec

**Date:** 2026-06-07
**Status:** Approved for planning
**Scope:** Direct-manipulation geometry handles on existing DOM elements, extending design mode, with edits round-tripped to the AI agent as code-change requests.

## Problem

Design mode (`internal/proxy/scripts/design.js`) lets a user pick an existing
DOM element on a proxied page and iterate on it through an AI chat/alternatives
loop. The palette (`palette.js`) and style-editor (`style-editor.js`) edit styles
through panel controls. None of these surface **direct-manipulation handles on
the real page element** — no Figma/Webflow-style corner/edge resize grips, no
drag-to-move. Sketch mode has resize handles, but only for shapes drawn on its
own canvas, never for live DOM nodes.

The user wants more resize and edit targets surfaced directly on existing
elements, and wants those edits to become real source diffs the agent writes.

## Goals

- Surface 8-direction resize handles + a move grip directly on the element
  currently selected in design mode.
- Drags snap to detected design tokens / spacing scale / sibling edges.
- Each committed edit emits a code-change request to the AI agent carrying a
  CSS selector plus the computed before→after delta.
- Live preview is immediate and non-destructive to the element's own inline
  styles.

## Non-Goals (this pass)

- Spacing (padding/margin/gap) handles, sibling reorder, and inline text/content
  editing. These are explicitly deferred to later passes that reuse the same
  three building blocks (`transform.js`, the override store, the `design_edit`
  channel). No rework expected.
- Free-pixel drag with modifier-key override. MVP ships snap-only; key override
  is a fast-follow.
- Extracting a shared handle primitive between sketch shapes and DOM elements
  (Approach B). Defer until a second consumer (spacing/reorder) actually needs
  it, so the MVP stays focused.

## Chosen Approach — A + C

**A — Transform-handles module bolted onto design mode.** A new browser script
module renders handles around the selected element and translates drags into
geometry deltas.

**C — Override-stylesheet delta store.** Edits accumulate as rules in a single
injected `<style>` element rather than mutating each element's inline `style`.
The rule set *is* the delta store: clean read-off for the round-trip payload,
trivial undo, no inline-style collisions, and a stable target attribute for
elements that lack a good selector.

## Components

### `internal/proxy/scripts/transform.js` (new)

- Subscribes to design-mode selection. Design mode already dispatches
  `devtool:palette-show` when an element is selected (`design.js:239`); this
  module listens for the same signal and reads the selected element from the
  design API.
- Renders 8 resize handles (4 corner, 4 edge) plus a move grip into the existing
  shadow-DOM overlay mount root (`window.__devtoolGetMountRoot()`, used by
  `overlay.js`). Handles track the element's bounding rect and reposition on
  scroll/resize via `requestAnimationFrame`, matching design.js's RAF-batched
  hover model (`design.js:113-145`).
- Pointer-drag on a handle computes a geometry delta (width/height for resize,
  x/y translate for move), runs it through the snap engine, and writes the
  snapped result to the override store for live preview.
- On pointer release, emits the edit through `core.send('design_edit', …)`.
- Detached-element guard: if the selected element leaves the document between
  selection and drag, abort handle render, log to console, emit nothing. No
  phantom delta. (Fail-fast — no fallback data, per project rules.)

### `internal/proxy/scripts/override-store.js` (new)

- Owns one injected `<style id="__devtool_overrides">` in the shadow root.
- Each edited element gets a generated unique attribute
  `data-devtool-oid="<id>"` and a corresponding rule
  `[data-devtool-oid="<id>"] { … }`. The attribute selector dodges specificity
  battles with existing page CSS and inline styles.
- API: `upsert(oid, props)`, `read(oid)`, `pop(oid)` (undo), `clear()`.
- The accumulated rule for an element is the canonical computed delta read into
  the round-trip payload.
- Write-failure surfaces (console error + no silent swallow); a failed upsert
  does not emit a `design_edit`.

### Snap engine (local to `transform.js`)

- Token source: scan `:root` CSS custom properties for numeric `px`/`rem`
  values → snap set. This reuses the design-token notion the codebase already
  leans on.
- Fallback grid: 4px / 8px.
- Edge guides: sibling and parent box edges.
- Snap threshold ≈ 4px; nearest candidate wins.
- Pure function at its core: `(rawValue, snapSet) → snappedValue`. Isolated for
  standalone unit testing.

### `internal/proxy/scripts/embed.go`

- Register `override-store.js` and `transform.js` in the module load order after
  `overlay.js` and `design.js` (`embed.go:166-228`), declaring the dependency on
  both.

### Go round-trip wiring (`internal/proxy/logger.go`)

Mirrors the existing `DesignRequest` path exactly (`logger.go:365-378`,
`599-619`, `825-833`):

- `LogTypeDesignEdit LogEntryType = "design_edit"`.
- New struct:
  ```go
  type DesignEdit struct {
      ID             string                `json:"id"`
      Timestamp      time.Time             `json:"timestamp"`
      Selector       string                `json:"selector"`
      XPath          string                `json:"xpath"`
      OID            string                `json:"oid"`             // data-devtool-oid value
      Deltas         map[string]string     `json:"deltas"`          // property → final value
      ComputedBefore map[string]string     `json:"computed_before"`
      ComputedAfter  map[string]string     `json:"computed_after"`
      Metadata       DesignElementMetadata `json:"metadata"`
      URL            string                `json:"url"`
  }
  ```
- `LogDesignEdit(entry DesignEdit)` constructor + `DesignEdit *DesignEdit` field
  on the log entry struct + timestamp-extraction case.

## Data Flow

```
design.js selects element
  → dispatches devtool:palette-show
  → transform.js renders handles around element's rect
  → user drags handle
      → snap engine snaps delta
      → override-store.upsert(oid, props)   (live preview, non-destructive)
  → user releases
      → core.send('design_edit', {selector, xpath, oid, deltas, before, after, metadata, url})
      → TrafficLogger.LogDesignEdit
      → design event channel → AI agent
      → agent writes real CSS/JSX source diff
```

The payload contract is **selector + computed delta** (plus `oid` so the agent
can locate an element whose selector is weak). The agent owns the
source-finding; the browser never resolves source files.

## Error Handling

- No current selection when handles would render → no-op, nothing drawn.
- Element detached mid-interaction → abort, console log, no emit.
- Override rule write failure → surface error, suppress the `design_edit`.
- Element with no stable CSS selector → still addressable via the `oid`
  attribute carried in the payload.

All failures are visible (console + absence of a phantom event), never silent
fallback data.

## Testing

- **Go (`internal/proxy/logger_test.go`)**: `LogDesignEdit` round-trips through
  the circular buffer; entry is retrievable; timestamp extraction returns the
  edit's timestamp. Pure, no mocks — exercises the same buffer path as the
  existing design log-type tests.
- **JS snap engine**: the core `(rawValue, snapSet) → snappedValue` function is
  pure and unit-testable standalone (token snap, grid fallback, edge guide,
  threshold boundary).

## Files Touched

| File | Change |
|------|--------|
| `internal/proxy/scripts/transform.js` | new — handle overlay + snap engine + emit |
| `internal/proxy/scripts/override-store.js` | new — injected stylesheet delta store |
| `internal/proxy/scripts/embed.go` | register new modules in load order |
| `internal/proxy/logger.go` | `design_edit` log type, `DesignEdit` struct, `LogDesignEdit`, timestamp case |
| `internal/proxy/logger_test.go` | `LogDesignEdit` round-trip test |

## Forward Compatibility

Spacing handles, sibling reorder, and inline text editing each reuse
`transform.js` (handle rendering + drag), the override store (delta capture +
undo), and the `design_edit` channel (round-trip). When the second consumer
lands and the handle-rendering duplication with sketch shapes actually bites,
extract the shared primitive (Approach B) then — not before.
