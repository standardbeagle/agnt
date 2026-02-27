# Live Style Editor — Design Document

**Date**: 2026-02-20
**Status**: Approved

## Overview

A standalone floating Style Editor panel that lets users inspect and live-edit CSS variables, inline styles, and view React props for any selected DOM element. Changes can be attached to messages sent to the AI agent as structured `style-edit` attachments with before/after screenshots.

## Key Decisions

- **UI**: Standalone floating panel (not in indicator tabs, not in design mode)
- **Activation**: Toolbar button in indicator compose tab → hover-to-select → editor opens
- **CSS variable scope**: Element-scoped + ancestors (walk DOM to find all in-scope custom properties)
- **React props**: Read-only display with "Edit via AI" handoff (no direct fiber mutation)
- **Attachment**: New `style-edit` attachment type with structured diff + before/after screenshots

## Architecture

### New File

`internal/proxy/scripts/style-editor.js` — self-contained module (`window.__devtool_style_editor`), loaded between `design.js` and `indicator.js` in embed order.

### Go Changes

- `parsePanelMessage` in `server.go`: handle `style-edit` attachment type
- `formatPanelMessage` in `overlay.go`: render `style-edit` as readable diff text
- `embed.go`: add style-editor.js to combined script

### Data Flow

```
User clicks Inspect → hover-to-select overlay → click element
    → editor panel opens (captures "before" screenshot)
    → user edits CSS vars / inline styles (live via element.style / setProperty)
    → user clicks "Attach Changes"
    → captures "after" screenshot
    → builds diff (original vs current values)
    → creates style-edit attachment
    → switches to compose tab
    → user writes message + sends
    → attachment flows through panel_message pipeline
    → agent receives structured diff + screenshots + React props
```

## Value Controls

Type-appropriate controls auto-detected from CSS values:

| Value Pattern | Control | Behavior |
|---|---|---|
| `#hex`, `rgb()`, `hsl()` | Color picker + text input | Native `<input type="color">` with manual hex entry |
| Numeric with `px`/`rem`/`em`/`%` | Slider + number input | Range based on property semantics |
| `0`–`1` (opacity, etc.) | Slider (0.01 step) | Float slider with 2 decimal places |
| `none`/`block`/`flex`/etc. | Dropdown | Known enum values per property |
| `true`/`false` | Checkbox | Toggle for React boolean props |
| Multi-value (padding, margin) | Split inputs | 4 linked number inputs with "link all" toggle |
| Everything else | Text input | Free-form with live apply on Enter/blur |

### Slider Range Heuristics

- `font-size`: 8–72px, step 1
- `border-radius`: 0–50px, step 1
- `padding`/`margin`: 0–100px, step 1
- `gap`: 0–64px, step 1
- `opacity`: 0–1, step 0.01
- `line-height`: 0.5–3, step 0.1
- `border-width`: 0–20px, step 1
- Custom properties: infer from current value (±4x range)

Unit awareness: slider works in the value's native unit. Unit selector allows switching between px/rem/em/%.

## CSS Variable Discovery

1. `getComputedStyle(element)` — iterate all properties, filter `--` prefixed
2. Walk ancestors to `documentElement` — check `element.style` for inline custom properties
3. Scan `document.styleSheets` — find rules matching element or ancestors, extract `--` properties
4. Deduplicate by name, closest scope wins

Each entry shows:
- Property name, current value with control, scope label (which element defines it)
- Usage hint: which computed properties on the element reference this variable (collapsed)

Editing: `scopeElement.style.setProperty('--var-name', newValue)` — cascades to all consumers.
Reset: `scopeElement.style.removeProperty('--var-name')` — restores stylesheet value.

## Inline Style Editing

Organized by category (collapsible):
- **Layout** — display, position, width, height, flex/grid
- **Spacing** — margin, padding (4-value split controls)
- **Typography** — font-size, font-weight, line-height, color, text-align
- **Background** — background-color, background-image
- **Border** — border-width, border-color, border-radius, border-style
- **Effects** — opacity, box-shadow, transform, transition

Filtering: compare `getComputedStyle` against browser defaults for that element type. Only show non-default properties. "Show all" toggle reveals full list.

Editing: `element.style[property] = value` — inline override. Blue dot indicator on edited values.

Box model visual at top of section — clickable regions jump to corresponding inputs.

## React Props Inspector

Appears only when element (or nearest ancestor) has a React fiber.

Detection: check for `__reactFiber$*` or `__reactInternalInstance$*`, walk up to nearest component boundary.

Display: component name, props with name/value/type. Functions show name only, objects show collapsed preview. All read-only.

"Edit via AI": captures component name, props snapshot, `fiber._debugSource` path, context HTML. Pre-fills compose textarea, attaches as part of `style-edit` payload.

## Attachment Format

```json
{
  "type": "style-edit",
  "selector": ".submit-btn",
  "changes": [
    {
      "property": "--primary-color",
      "scope": ":root",
      "original": "#6366f1",
      "current": "#ef4444"
    },
    {
      "property": "border-radius",
      "scope": "inline",
      "original": "8px",
      "current": "12px"
    }
  ],
  "reactProps": {
    "component": "Button",
    "props": {"variant": "primary", "disabled": false},
    "source": "src/components/Button.tsx:42"
  },
  "screenshots": {"before": "ctx_abc", "after": "ctx_def"}
}
```

Chip in compose: `[paint icon] .submit-btn: 3 changes [×]`
Hover preview: diff summary with original → new values.

Message text:
```
- Style edit `.submit-btn`: 3 CSS changes, React Button props attached
  --primary-color: #6366f1 → #ef4444 (:root)
  border-radius: 8px → 12px (inline)
  opacity: 1 → 0.85 (inline)
  Before/after screenshots: `ctx_abc`, `ctx_def`
```

## Floating Panel

- Position: right side of viewport, vertically centered, draggable
- Size: 360px wide, max 70vh tall, scrollable
- Persists position in localStorage
- Pin button: keeps open on outside click (default: unpinned)
- Re-select button: re-enters hover mode without closing
- Z-index: 2147483645 (below indicator at 2147483646, above page content)
- Section headers show property count + "(N changed)" badge

## Tasks

### Phase 1: Core Infrastructure

1.1. **Style editor module scaffold** — Create `style-editor.js` with module pattern, register in `embed.go` load order. Empty `window.__devtool_style_editor` with `open()`, `close()`, `getState()`, `attachChanges()` stubs.

1.2. **Element selection integration** — Reuse design mode's selection overlay (hover highlight, click to select). Extract shared selection logic or call `__devtool_design` internals. Return selected element + generated selector.

1.3. **Floating panel shell** — Draggable panel with title bar, pin/close buttons, scrollable content area. localStorage position persistence. Z-index management.

### Phase 2: CSS Variable Editing

2.1. **CSS variable discovery** — Walk computed styles + ancestors + stylesheets to find all `--` custom properties in scope. Deduplicate, track scope element for each.

2.2. **Variable usage analysis** — For each CSS variable, find which computed properties on the selected element reference it (search for `var(--name)` in computed values).

2.3. **Variable editing** — `setProperty` on scope element, capture original for diff, per-property reset.

### Phase 3: Value Controls

3.1. **Color picker control** — `<input type="color">` + hex text input. Parse/emit hex, rgb(), hsl() formats. Debounced live apply.

3.2. **Numeric slider control** — Slider + number input + unit selector (px/rem/em/%). Range heuristics per property name. Step size based on unit/property.

3.3. **Multi-value control** — 4 linked number inputs for margin/padding. "Link all" toggle for uniform values. Individual edge editing.

3.4. **Dropdown control** — Enum values for display, position, text-align, etc. Known value lists per property.

3.5. **Text input control** — Fallback for unrecognized values. Apply on Enter or blur.

3.6. **Type detection engine** — Given a property name + current value, return which control to render. Centralized mapping logic.

### Phase 4: Inline Style Editing

4.1. **Computed style extraction** — Get non-default computed styles grouped by category. Compare against browser defaults for the element's tag.

4.2. **Category sections UI** — Collapsible sections (Layout, Spacing, Typography, Background, Border, Effects). Property count + changed badge in headers.

4.3. **Inline style editing** — Apply via `element.style`, capture originals, blue dot indicator on changed values.

4.4. **Box model visual** — Interactive box model diagram at top. Clickable margin/border/padding/content regions. Inline value display on each edge.

### Phase 5: React Props Inspector

5.1. **React fiber detection** — Find `__reactFiber$*` on element or nearest ancestor. Extract component name, props, debug source path.

5.2. **Props display UI** — Read-only prop list with name, value preview, type badge. Expand objects/arrays inline. Copy on click for primitives.

5.3. **Edit via AI button** — Capture props snapshot + source path, pre-fill compose textarea, attach as `reactProps` field on `style-edit` attachment.

### Phase 6: Attachment & Agent Integration

6.1. **Before/after screenshots** — Capture "before" on editor open, "after" on attach. Use existing `sendCaptureBinary` pipeline.

6.2. **style-edit attachment type** — Build structured diff payload. Add to `store.attachments` as new type. Chip rendering in compose tab with hover preview.

6.3. **Message text generation** — Add `style-edit` handling to "Context from page" markdown builder and `handleSend` serialization.

6.4. **Go: parsePanelMessage** — Handle `style-edit` type in `server.go`, extract changes array and react props into `PanelAttachment.Data`.

6.5. **Go: formatPanelMessage** — Render `style-edit` as readable diff in overlay `[Attachments]` section.

### Phase 7: Indicator Integration

7.1. **Toolbar button** — Add "Inspect" button to compose tab toolbar (alongside Screenshot, Element, Sketch). Icon + label.

7.2. **API surface** — Expose `__devtool.styleEditor.open()`, `.close()`, `.getState()`, `.attachChanges()` in `api.js`.

7.3. **Keyboard shortcut** — Optional: `Escape` to close editor (consistent with sketch/design mode).
