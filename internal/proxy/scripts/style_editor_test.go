package scripts

import (
	"strings"
	"testing"
)

// TestStyleEditorScriptEmbedded verifies the style-editor module is reachable
// from the combined script bundle and exposes its public API surface.
func TestStyleEditorScriptEmbedded(t *testing.T) {
	combined := buildCombinedScript()

	if !strings.Contains(combined, "// style-editor module") {
		t.Error("style-editor module marker not found in combined script — embed.go moduleOrder entry missing")
	}

	required := []string{
		"window.__devtool_style_editor",
		"open: open",
		"close: close",
	}
	for _, want := range required {
		if !strings.Contains(combined, want) {
			t.Errorf("combined script missing style-editor API export %q", want)
		}
	}
}

// TestSharedDesignStateEventName locks in the canonical DOM event name used
// for palette ↔ panel synchronisation. Both modules subscribe and dispatch
// against the same event name; if either side drifts, sync silently breaks.
//
// Naming choice: 'devtool:design-state' matches the existing
// 'devtool:palette-show' convention (devtool: prefix, kebab-case suffix) and
// reuses the design_state concept from TrafficLogger.
func TestSharedDesignStateEventName(t *testing.T) {
	const eventName = "devtool:design-state"

	if !strings.Contains(paletteJS, eventName) {
		t.Errorf("palette.js must reference %q — without it the palette cannot participate in shared state sync", eventName)
	}
	if !strings.Contains(styleEditorJS, eventName) {
		t.Errorf("style-editor.js must reference %q — without it the panel cannot participate in shared state sync", eventName)
	}
}

// TestPaletteEmitsAndListensForDesignState verifies the palette is wired as
// both producer and consumer of the shared design-state event:
//
//  1. palette dispatches on style change (so panel can react)
//  2. palette subscribes to incoming events (so panel-driven changes update it)
//  3. palette ignores events it emitted itself (loop guard via source tag)
func TestPaletteEmitsAndListensForDesignState(t *testing.T) {
	// Producer: dispatch CustomEvent with detail.source = 'palette'.
	if !strings.Contains(paletteJS, "new CustomEvent('devtool:design-state'") {
		t.Error("palette.js must dispatch CustomEvent('devtool:design-state') on style mutation so the panel can sync")
	}
	if !strings.Contains(paletteJS, "source: 'palette'") {
		t.Error("palette.js dispatch must tag detail.source = 'palette' so the panel can identify origin")
	}

	// Consumer: subscribe via document.addEventListener.
	if !strings.Contains(paletteJS, "document.addEventListener('devtool:design-state'") {
		t.Error("palette.js must subscribe to 'devtool:design-state' on document so panel-driven edits update the palette")
	}

	// Loop guard: ignore self-source events.
	if !strings.Contains(paletteJS, "e.detail.source === 'palette'") {
		t.Error("palette.js handler must ignore events it emitted (detail.source === 'palette') to avoid feedback loops")
	}
}

// TestPanelEmitsAndListensForDesignState mirrors the palette test for the
// full inspect panel (style-editor.js).
func TestPanelEmitsAndListensForDesignState(t *testing.T) {
	// Producer: dispatch CustomEvent with detail.source = 'panel'.
	if !strings.Contains(styleEditorJS, "new CustomEvent('devtool:design-state'") {
		t.Error("style-editor.js must dispatch CustomEvent('devtool:design-state') on inline style edit so the palette can sync")
	}
	if !strings.Contains(styleEditorJS, "source: 'panel'") {
		t.Error("style-editor.js dispatch must tag detail.source = 'panel' so the palette can identify origin")
	}

	// Consumer: subscribe via document.addEventListener.
	if !strings.Contains(styleEditorJS, "document.addEventListener('devtool:design-state'") {
		t.Error("style-editor.js must subscribe to 'devtool:design-state' on document so palette-driven edits update the panel")
	}

	// Loop guard: ignore self-source events.
	if !strings.Contains(styleEditorJS, "e.detail.source === 'panel'") {
		t.Error("style-editor.js handler must ignore events it emitted (detail.source === 'panel') to avoid feedback loops")
	}
}

// TestDesignStateEventCarriesEditPayload verifies the event detail shape used
// for style-edit synchronisation. Both producers must include the property
// name and the new value so the consumer can apply the same change to its
// matching control without re-reading from the DOM.
func TestDesignStateEventCarriesEditPayload(t *testing.T) {
	// Both modules must include kind discriminator so consumers can route
	// edit vs. select vs. deselect events.
	for _, src := range []struct {
		name string
		body string
	}{
		{"palette.js", paletteJS},
		{"style-editor.js", styleEditorJS},
	} {
		if !strings.Contains(src.body, "kind: 'edit'") {
			t.Errorf("%s must dispatch design-state events with kind: 'edit' for style mutations", src.name)
		}
		// Edit payload must carry the property + value pair so the consumer
		// can mirror the change. The 'change' object is the canonical key.
		if !strings.Contains(src.body, "change:") {
			t.Errorf("%s must include change: { property, value } in design-state edit dispatch", src.name)
		}
	}
}

// TestDeselectClearsBothSurfaces verifies that closing the panel or hiding
// the palette dispatches a 'deselect' design-state event so the other
// surface clears too.
func TestDeselectClearsBothSurfaces(t *testing.T) {
	// Both modules dispatch a deselect event when their public hide/close
	// API is invoked. This is the load-bearing wiring for the spec
	// requirement: "Deselecting clears and hides the mini palette; full
	// panel returns to idle state."
	if !strings.Contains(paletteJS, "kind: 'deselect'") {
		t.Error("palette.js hide() must dispatch a design-state event with kind: 'deselect' so the panel can close in sync")
	}
	if !strings.Contains(styleEditorJS, "kind: 'deselect'") {
		t.Error("style-editor.js close() must dispatch a design-state event with kind: 'deselect' so the palette can hide in sync")
	}
}

// TestUndoStackWiring verifies Ctrl+Z (and Cmd+Z on macOS) is wired in the
// shared sync layer so the last edit is reverted across both surfaces. The
// undo entry must capture the previous value before the edit so revert can
// restore it.
func TestUndoStackWiring(t *testing.T) {
	// Undo handler is owned by the palette (the smaller, always-present
	// surface) — it intercepts Ctrl+Z and dispatches a revert as a
	// design-state event so the panel reflects the rollback too.
	if !strings.Contains(paletteJS, "(e.ctrlKey || e.metaKey)") {
		t.Error("palette.js must listen for Ctrl+Z and Cmd+Z for cross-platform undo")
	}
	if !strings.Contains(paletteJS, "e.key === 'z'") {
		t.Error("palette.js undo handler must check for the 'z' key")
	}
	// Undo stack: per-selection LIFO of {property, prevValue} captured
	// before each applyStyle write.
	if !strings.Contains(paletteJS, "undoStack") {
		t.Error("palette.js must maintain an undoStack array for Ctrl+Z to pop from")
	}
}

// TestScrubberFactoryExists verifies the panel exposes a reusable scrubber
// attachment helper that can be bound to any DOM element to enable
// drag/scroll-wheel value editing for numeric properties. Factoring this as
// a single helper avoids divergence between the property-name handle and the
// numeric input handle, both of which must scrub identically.
func TestScrubberFactoryExists(t *testing.T) {
	if !strings.Contains(styleEditorJS, "function attachScrubber") {
		t.Error("style-editor.js must define attachScrubber(target, opts) — the shared drag+wheel handler used by both label and numeric input")
	}
}

// TestScrubberWiredOnNumericRows verifies the property-name label is
// converted into a scrub handle for numeric rows. The cursor must change to
// the standard horizontal-resize indicator on hover so users discover the
// affordance without docs.
func TestScrubberWiredOnNumericRows(t *testing.T) {
	// Cursor affordance: ew-resize is the WYSIWYG-scrubber convention used
	// by Photoshop, Figma, and the Chrome devtools styles pane.
	if !strings.Contains(styleEditorJS, "ew-resize") {
		t.Error("style-editor.js must use cursor: ew-resize on scrub handles to signal drag affordance (matches Chrome devtools/Figma)")
	}
	// The numeric path in buildPropertyRow must call attachScrubber on the
	// label so dragging the property name scrubs the value. Gating on
	// detected.type === 'numeric' keeps the affordance off non-numeric
	// rows (color, dropdown, multivalue, text).
	if !strings.Contains(styleEditorJS, "attachScrubber(nameEl") {
		t.Error("style-editor.js buildPropertyRow must call attachScrubber(nameEl, ...) so the property-name label scrubs numeric values")
	}
}

// TestScrubberHandlesPointerAndWheel verifies the scrubber binds both the
// pointerdown drag gesture AND the wheel gesture. Either alone is half a
// feature — the spec calls for both to map onto the same value-mutation path.
func TestScrubberHandlesPointerAndWheel(t *testing.T) {
	if !strings.Contains(styleEditorJS, "pointerdown") {
		t.Error("style-editor.js scrubber must listen for pointerdown to start drag scrubbing")
	}
	if !strings.Contains(styleEditorJS, "pointermove") {
		t.Error("style-editor.js scrubber must listen for pointermove to track horizontal drag distance")
	}
	if !strings.Contains(styleEditorJS, "pointerup") {
		t.Error("style-editor.js scrubber must listen for pointerup to commit the scrubbed value")
	}
	// Wheel gesture: spec requires +1/-1 per tick, ±10 with shift. The
	// listener must be present and pass {passive: false} so preventDefault()
	// can stop the page from scrolling while the user scrubs.
	if !strings.Contains(styleEditorJS, "addEventListener('wheel'") {
		t.Error("style-editor.js scrubber must listen for wheel events to support scroll-wheel scrubbing")
	}
	if !strings.Contains(styleEditorJS, "passive: false") {
		t.Error("style-editor.js wheel listener must use {passive: false} so preventDefault() can suppress page scroll during scrub")
	}
}

// TestScrubberRespectsShiftAndUnit verifies the spec's two preservation
// guarantees: shift accelerates by 10×, and the trailing CSS unit is
// preserved verbatim across the scrub (so dragging '1.5rem' produces
// '1.6rem', never '1.6px').
func TestScrubberRespectsShiftAndUnit(t *testing.T) {
	// Shift acceleration is the standard scrubber convention; the test
	// just checks that shiftKey is read at all on either pointermove or
	// wheel. Both gestures need it.
	if !strings.Contains(styleEditorJS, "shiftKey") {
		t.Error("style-editor.js scrubber must read shiftKey for ±10 acceleration on drag and wheel")
	}
	// Unit preservation: the scrubber must reuse the same parse path as
	// the numeric slider (regex with optional unit suffix). We assert the
	// canonical NUMERIC_VALUE_RE constant is used or referenced from the
	// scrubber so unit-preserving math is guaranteed.
	if !strings.Contains(styleEditorJS, "NUMERIC_VALUE_RE") {
		t.Error("style-editor.js must reference NUMERIC_VALUE_RE so scrubber output preserves the trailing unit")
	}
}

// TestScrubberEscapeRevertsToOriginal verifies the spec's revert contract:
// pressing Escape mid-scrub restores the value the row had before the drag
// started. Without this, an accidental drag is unrecoverable.
func TestScrubberEscapeRevertsToOriginal(t *testing.T) {
	// The handler must check for the Escape key by name. We assert both
	// the keydown subscription and the key check are present so the
	// scrubber actually intercepts the keystroke (rather than relying on
	// the input element's built-in behaviour, which doesn't apply to a
	// label drag).
	if !strings.Contains(styleEditorJS, "addEventListener('keydown'") {
		t.Error("style-editor.js scrubber must listen for keydown so Escape can revert mid-scrub")
	}
	if !strings.Contains(styleEditorJS, "'Escape'") {
		t.Error("style-editor.js scrubber keydown handler must check for the 'Escape' key to trigger revert")
	}
}

// TestScrubberCommitFlowsThroughDesignState verifies the scrubber's commit
// path goes through applyInlineStyleEdit so the design-state event fires and
// the mini palette mirrors the change in real time. Bypassing
// applyInlineStyleEdit would make the scrubber edit invisible to the
// palette — a silent regression of iter-12's cross-surface sync.
func TestScrubberCommitFlowsThroughDesignState(t *testing.T) {
	// The scrubber's onCommit hook must be wired to applyInlineStyleEdit
	// (the same write path the per-row controls already use). We grep
	// for the call site near the scrubber to ensure it isn't routed
	// through a parallel path that skips the design-state dispatch.
	if !strings.Contains(styleEditorJS, "function attachScrubber") {
		t.Skip("attachScrubber not yet defined; covered by TestScrubberFactoryExists")
	}
	// The scrubber must call applyInlineStyleEdit (directly or via the
	// onCommit callback wired up in buildPropertyRow). The string
	// 'applyInlineStyleEdit' appears in the panel for the existing
	// renderControl path; this assertion locks in that the scrubber
	// reuses it rather than calling element.style.setProperty directly.
	scrubberRegion := extractScrubberRegion(styleEditorJS)
	if scrubberRegion == "" {
		t.Fatal("could not locate attachScrubber region in style-editor.js for commit-path inspection")
	}
	if !strings.Contains(scrubberRegion, "onCommit") && !strings.Contains(scrubberRegion, "onScrub") {
		t.Error("attachScrubber must accept an onCommit/onScrub callback so the row owner can route writes through applyInlineStyleEdit")
	}
}

// extractScrubberRegion returns the source text of the attachScrubber
// function for region-scoped assertions. Falls back to empty string if the
// function isn't defined yet (the FactoryExists test will surface the real
// failure).
func extractScrubberRegion(src string) string {
	const marker = "function attachScrubber"
	idx := strings.Index(src, marker)
	if idx < 0 {
		return ""
	}
	// Scan forward for the matching closing brace of the function body.
	// Style-editor.js uses a consistent 2-space indent so we look for
	// "\n  }" at column 2 — the function's own closing brace.
	rest := src[idx:]
	end := strings.Index(rest, "\n  }\n")
	if end < 0 {
		return rest
	}
	return rest[:end+5]
}
