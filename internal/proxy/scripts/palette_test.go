package scripts

import (
	"strings"
	"testing"
)

// TestPaletteScriptEmbedded verifies the palette module is reachable from the
// combined script bundle and exposes the public API surface that callers
// (design.js dispatch + manual show()) depend on.
func TestPaletteScriptEmbedded(t *testing.T) {
	combined := buildCombinedScript()

	// Module load marker — confirms embed.go wiring + moduleOrder entry.
	if !strings.Contains(combined, "// palette module") {
		t.Error("palette module marker not found in combined script — embed.go moduleOrder entry missing")
	}

	// Public API surface — the four entry points called from outside the
	// module (design.js dispatch via show, manual hide, classifyElement +
	// computePosition for callers that want positioning logic).
	required := []string{
		"window.__devtool_palette",
		"show: show",
		"hide: hide",
		"classifyElement: classifyElement",
		"controlsForType: controlsForType",
		"computePosition: computePosition",
	}
	for _, want := range required {
		if !strings.Contains(combined, want) {
			t.Errorf("combined script missing palette API export %q", want)
		}
	}
}

// TestPaletteInScriptNames verifies palette.js shows up in GetScriptNames so
// the debug surface (used by `agnt mcp` script-name reporting) lists it.
func TestPaletteInScriptNames(t *testing.T) {
	names := GetScriptNames()
	for _, n := range names {
		if n == "palette.js" {
			return
		}
	}
	t.Error("palette.js not found in GetScriptNames()")
}

// TestPaletteModuleOrder verifies palette loads after its declared deps
// (core, utils) and before api (which depends on it). The shared
// TestModuleDependencyOrder also catches dep ordering, but this test
// surfaces the specific palette-related ordering invariant explicitly so
// future module re-ordering can't silently break the palette wiring.
func TestPaletteModuleOrder(t *testing.T) {
	combined := buildCombinedScript()

	paletteIdx := strings.Index(combined, "// palette module")
	coreIdx := strings.Index(combined, "// core module")
	utilsIdx := strings.Index(combined, "// utils module")
	apiIdx := strings.Index(combined, "// api module")

	if paletteIdx < 0 {
		t.Fatal("palette module marker not found")
	}
	if coreIdx < 0 || coreIdx >= paletteIdx {
		t.Errorf("core must load before palette (core=%d, palette=%d)", coreIdx, paletteIdx)
	}
	if utilsIdx < 0 || utilsIdx >= paletteIdx {
		t.Errorf("utils must load before palette (utils=%d, palette=%d)", utilsIdx, paletteIdx)
	}
	if apiIdx >= 0 && paletteIdx >= apiIdx {
		t.Errorf("palette must load before api (palette=%d, api=%d)", paletteIdx, apiIdx)
	}
}

// TestPaletteSelectionFeedFromDesign verifies the design.js → palette
// integration: design.selectElement() must dispatch a custom DOM event that
// the palette listens for. This is the load-bearing decoupling — without
// the event the palette never appears after a design selection. Two halves:
//
//  1. design.js dispatches 'devtool:palette-show' with detail.element pointing
//     at the selected element.
//  2. palette.js subscribes via document.addEventListener for the same event
//     name and reads detail.element.
//
// If either half changes the event name without updating the other, the
// palette silently stops appearing after design selection.
func TestPaletteSelectionFeedFromDesign(t *testing.T) {
	if !strings.Contains(designJS, "devtool:palette-show") {
		t.Error("design.js must dispatch 'devtool:palette-show' event after selectElement so the palette can attach")
	}
	if !strings.Contains(designJS, "new CustomEvent('devtool:palette-show'") {
		t.Error("design.js dispatch must use CustomEvent constructor with 'devtool:palette-show' name")
	}
	if !strings.Contains(designJS, "detail: { element: element") {
		t.Error("design.js CustomEvent detail must include element reference so palette can read it")
	}
	if !strings.Contains(paletteJS, "document.addEventListener('devtool:palette-show'") {
		t.Error("palette.js must subscribe to 'devtool:palette-show' event on document")
	}
	if !strings.Contains(paletteJS, "e.detail.element") {
		t.Error("palette.js event handler must read element from event.detail.element")
	}
}

// TestPaletteContextAwareControlBanks verifies each element-type bank
// matches the spec. The palette is context-aware — if a future edit
// silently drops one of the spec'd controls, the banks become useless.
//
// Spec table (from task description):
//
//	text/heading → font-size, font-weight, font-style (italic), color, line-height
//	image        → width, height, object-fit
//	button/input → padding, border-radius, background-color, color
//	block        → width, height, padding, background-color
//	any (always) → opacity, display, delete
func TestPaletteContextAwareControlBanks(t *testing.T) {
	cases := []struct {
		typeName string
		expected []string
	}{
		// Strings appear inside controlsForType's switch arms; we look for
		// the unique 'id:' tags assigned to each control so the test is not
		// fooled by labels appearing in unrelated contexts.
		{"text", []string{"'font-size'", "'font-weight-bold'", "'font-style-italic'", "'color'", "'line-height'"}},
		{"image", []string{"'width'", "'height'", "'object-fit'"}},
		{"button", []string{"'padding'", "'border-radius'", "'background-color'", "'color'"}},
		{"block", []string{"'width'", "'height'", "'padding'", "'background-color'"}},
	}
	for _, tc := range cases {
		// Each control id must appear inside controlsForType (the source
		// scope of palette.js — we don't bother slicing by case arm because
		// that's parser-fragile; the controls are named uniquely enough).
		for _, ctrl := range tc.expected {
			if !strings.Contains(paletteJS, ctrl) {
				t.Errorf("palette type %q missing control descriptor id %s", tc.typeName, ctrl)
			}
		}
	}

	// Universal bank — opacity / display / delete (action) appended to every type.
	universal := []string{"'opacity'", "'display'", "'delete'"}
	for _, ctrl := range universal {
		if !strings.Contains(paletteJS, ctrl) {
			t.Errorf("palette universal bank missing control descriptor id %s", ctrl)
		}
	}
}

// TestPaletteClassifyElementCovers the documented element-type table. The
// classifier is pure JS, so we assert on the source's class branches: each
// branch must reference at least one canonical tag from the spec table.
func TestPaletteClassifyElementCovers(t *testing.T) {
	// classifyElement's source must check for the canonical tags from each
	// row of the spec. We assert a representative tag per row so future
	// edits that drop entire branches show up loudly.
	required := []string{
		// image branch: img must be checked
		"tag === 'img'",
		// button branch: button + input both checked
		"tag === 'button'",
		"tag === 'input'",
		// text/heading branch: at least h1 + p represented
		"h1: 1",
		"p: 1",
		// fallback returns 'block'
		"return 'block'",
	}
	for _, want := range required {
		if !strings.Contains(paletteJS, want) {
			t.Errorf("palette classifyElement missing required tag check %q", want)
		}
	}
}

// TestPalettePositioningSpec verifies the positioning rules from the spec:
//
//   - 8px gap to the right of the bbox by default
//   - falls back to below when right-of would overflow viewport
//   - clamps to viewport bounds so the palette is never off-screen
//
// The positioning math lives in computePosition; we assert on the source
// to lock down the rules. Viewport-bound clamping is asserted by checking
// for the Math.max(0, Math.min(...)) guard.
func TestPalettePositioningSpec(t *testing.T) {
	// 8px gap: GAP = 8
	if !strings.Contains(paletteJS, "var GAP = 8") {
		t.Error("palette must use 8px gap (GAP = 8) per spec")
	}
	// Right-of placement: rect.right + GAP
	if !strings.Contains(paletteJS, "rect.right + GAP") {
		t.Error("palette default placement must be rect.right + GAP")
	}
	// Below fallback: rect.bottom + GAP
	if !strings.Contains(paletteJS, "rect.bottom + GAP") {
		t.Error("palette no-room-on-right fallback must place at rect.bottom + GAP")
	}
	// Viewport clamp: Math.max(0, Math.min(x, viewportW - w))
	if !strings.Contains(paletteJS, "Math.max(0, Math.min(x, viewportW") {
		t.Error("palette must clamp x to [0, viewportW-w] so palette never escapes viewport")
	}
	if !strings.Contains(paletteJS, "Math.max(0, Math.min(y, viewportH") {
		t.Error("palette must clamp y to [0, viewportH-h] so palette never escapes viewport")
	}
}

// TestPaletteSizingSpec locks in the ~240px width cap from the spec.
// "Token-sized: max ~240px wide" — if a future change widens the palette
// it would break the spec's compactness contract.
func TestPaletteSizingSpec(t *testing.T) {
	if !strings.Contains(paletteJS, "var PALETTE_WIDTH = 240") {
		t.Error("palette width must be 240 per spec ('max ~240px wide')")
	}
	// Inline style must apply the width cap so the palette doesn't grow
	// past the spec'd width when controls overflow.
	if !strings.Contains(paletteJS, "max-width: ' + PALETTE_WIDTH") {
		t.Error("palette inline style must enforce max-width = PALETTE_WIDTH")
	}
}

// TestPaletteAutoRepositionWiring verifies the auto-reposition handlers are
// wired to scroll + resize, and that they rAF-coalesce so we don't thrash
// layout. Both the listeners and the rAF guard must be present.
func TestPaletteAutoRepositionWiring(t *testing.T) {
	if !strings.Contains(paletteJS, "window.addEventListener('scroll'") {
		t.Error("palette must listen for scroll to auto-reposition (per spec)")
	}
	if !strings.Contains(paletteJS, "window.addEventListener('resize'") {
		t.Error("palette must listen for resize to auto-reposition (per spec)")
	}
	if !strings.Contains(paletteJS, "requestAnimationFrame") {
		t.Error("palette scroll/resize handler must use rAF coalescing to avoid layout thrashing")
	}
	// The scroll listener must use capture (third arg true) so it sees
	// scroll events from any scrolling ancestor, not just window.
	if !strings.Contains(paletteJS, "'scroll', state.scrollResizeHandler, true") {
		t.Error("palette scroll listener must use capture=true to catch scrolls on ancestor scrollers, not just window")
	}
}

// TestPaletteDismissAndReshow locks in the Escape-dismiss + outside-click
// dismiss + re-show on next selection invariants from the spec.
func TestPaletteDismissAndReshow(t *testing.T) {
	// Escape dismiss
	if !strings.Contains(paletteJS, "e.key === 'Escape'") {
		t.Error("palette must dismiss on Escape key per spec")
	}
	// Outside-click dismiss — listener on mousedown so click-on-page-element
	// dismisses before that element's click handlers fire.
	if !strings.Contains(paletteJS, "document.addEventListener('mousedown', state.outsideClickHandler)") {
		t.Error("palette must register outside-click dismiss handler on document mousedown")
	}
	// Outside-click guard: clicks INSIDE the palette must not dismiss.
	if !strings.Contains(paletteJS, "state.paletteEl.contains(e.target)") {
		t.Error("palette outside-click handler must keep palette open when clicking inside it")
	}
	// Re-show on next selection: show() tears down previous palette via
	// hide() before mounting a fresh one (so clicking another element
	// re-shows for the new element with new context-aware controls).
	if !strings.Contains(paletteJS, "function show(element)") {
		t.Fatal("palette show() function not found")
	}
	// The first call inside show() (after the element guard) must be hide().
	showStart := strings.Index(paletteJS, "function show(element)")
	if showStart < 0 {
		t.Fatal("show() function start index not found")
	}
	showBody := paletteJS[showStart : showStart+800]
	if !strings.Contains(showBody, "hide();") {
		t.Errorf("show() must call hide() before mounting so re-selecting another element re-shows fresh controls. show() body head:\n%s", showBody)
	}
}

// TestPaletteAdditiveNotReplacement verifies the palette is additive —
// it must NOT touch the inspect panel (style-editor) or design controls.
// The spec is explicit: "Keeps the full inspect panel intact — this is an
// additional surface, not a replacement."
//
// We assert by checking palette.js does not reference the style-editor or
// design module's internal IDs (which would indicate it's trying to take
// over their UI surfaces).
func TestPaletteAdditiveNotReplacement(t *testing.T) {
	forbidden := []string{
		"__devtool-style-overlay",   // style-editor selection overlay id
		"__devtool-style-highlight", // style-editor highlight id
		"__devtool-design-overlay",  // design selection overlay id
		"__devtool-design-controls", // design controls id
		"window.__devtool_style",    // style-editor public API namespace
	}
	for _, bad := range forbidden {
		if strings.Contains(paletteJS, bad) {
			t.Errorf("palette must not reference %q — palette is additive, not a replacement for inspect/design panels", bad)
		}
	}

	// And the palette's own DOM id must not collide with existing modules.
	if !strings.Contains(paletteJS, "__devtool-palette") {
		t.Error("palette must use its own '__devtool-palette' DOM id namespace")
	}
}

// TestPaletteLiveMutationViaInlineStyle verifies controls write through
// element.style[prop] = value (inline style) so changes "reflect live" per
// spec. We check the central applyStyle helper is the single mutation
// seam — every control kind goes through it (or applyAction for delete).
func TestPaletteLiveMutationViaInlineStyle(t *testing.T) {
	if !strings.Contains(paletteJS, "state.selectedElement.style[prop] = value") {
		t.Error("palette must mutate via element.style[prop] = value so changes reflect live in DOM")
	}
	if !strings.Contains(paletteJS, "function applyStyle(prop, value)") {
		t.Error("palette must funnel mutations through applyStyle() — single seam for live DOM writes")
	}
	// Delete action goes through applyAction, not applyStyle.
	if !strings.Contains(paletteJS, "function applyAction(action)") {
		t.Error("palette delete control must route through applyAction()")
	}
	if !strings.Contains(paletteJS, "parent.removeChild(state.selectedElement)") {
		t.Error("palette delete action must call parent.removeChild(selectedElement)")
	}
}

// TestPaletteDraggable verifies the drag handle is wired and the dragged
// position survives auto-reposition (userMoved gate). Without the gate,
// scroll/resize would yank the palette back to the auto position after
// the user drags it.
func TestPaletteDraggable(t *testing.T) {
	// Drag handle must exist with grab cursor.
	if !strings.Contains(paletteJS, "cursor: grab") {
		t.Error("palette drag handle must use cursor:grab to signal draggability")
	}
	// Drag wires mousedown on handle + mousemove/mouseup on document.
	if !strings.Contains(paletteJS, "handle.addEventListener('mousedown'") {
		t.Error("palette drag must wire mousedown on handle")
	}
	if !strings.Contains(paletteJS, "document.addEventListener('mousemove', onMove)") {
		t.Error("palette drag must wire mousemove on document so dragging works outside the handle")
	}
	if !strings.Contains(paletteJS, "document.addEventListener('mouseup', onUp)") {
		t.Error("palette drag must wire mouseup on document to end drag")
	}
	// userMoved gate: repositionToElement returns early when state.userMoved is set.
	if !strings.Contains(paletteJS, "if (state.userMoved && state.userPos)") {
		t.Error("palette repositionToElement must respect userMoved flag so scroll/resize doesn't undo a user drag")
	}
}
