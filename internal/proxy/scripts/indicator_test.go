package scripts

import (
	"strings"
	"testing"
)

// TestTabRefreshGatedOnTextSelection verifies the floating-indicator panel's
// 1s tab-content refresh is suppressed while the user has an active text
// selection inside the tab content area.
//
// Background: The panel uses VanJS reactive state (store.X.val) for tab
// content. Each refresh tick (refreshOverviewData / refreshErrorsData / ...)
// writes to those stores, which causes VanJS to re-run the derived state
// function and produce new DOM nodes that replace the existing ones via
// Element.replaceWith. When the user is mid-drag selecting text inside the
// tab content, the swap collapses the selection before mouseup fires, so
// the user cannot copy the very content the panel exists to expose
// (errors, network calls, perf metrics).
//
// The gate lives in startTabUpdates' interval body and short-circuits
// before updateActiveTabContent. Tab-badge updates outside the content
// container still run so badge counts stay live.
//
// This test asserts the gate is wired and points at the right helper, the
// right element id, and the right interval body. If a future refactor
// moves the refresh to a different scheduler the test fails loudly so the
// gate has to be re-applied to the new path.
func TestTabRefreshGatedOnTextSelection(t *testing.T) {
	const helperName = "selectionWithinTabContent"
	if !strings.Contains(indicatorJS, "function "+helperName) {
		t.Fatalf("indicator.js must define %s — the helper that detects an active selection inside the panel content area", helperName)
	}

	// The helper must scope to the tab content container. Selections outside
	// it (e.g. user selecting page text) must NOT block the panel refresh,
	// otherwise the panel goes stale every time the user works with the
	// host page.
	if !strings.Contains(indicatorJS, "getInMount('__devtool-tab-content')") {
		t.Error("selectionWithinTabContent must scope its check to the __devtool-tab-content container so host-page selections do not freeze the panel")
	}

	// The helper must consult the document selection. It must also try the
	// shadow-root selection path when the panel lives inside a shadow root,
	// because Chromium returns shadow-internal selections from
	// ShadowRoot.getSelection rather than document.getSelection.
	if !strings.Contains(indicatorJS, "document.getSelection") {
		t.Error("selectionWithinTabContent must call document.getSelection so light-DOM/fallback mount selections are detected")
	}
	if !strings.Contains(indicatorJS, "root.getSelection") {
		t.Error("selectionWithinTabContent must call ShadowRoot.getSelection (root.getSelection) so Chromium shadow-internal selections are detected")
	}

	// The helper must inspect both anchor and focus nodes — a selection
	// can start inside the panel and end outside (or vice versa), and
	// either case must still suppress the refresh that would collapse it.
	if !strings.Contains(indicatorJS, "sel.anchorNode") || !strings.Contains(indicatorJS, "sel.focusNode") {
		t.Error("selectionWithinTabContent must check both anchorNode and focusNode so selections that straddle the panel boundary are still detected")
	}

	// Empty/collapsed selections must not block updates — without this
	// the panel would freeze whenever the user clicks a focusable element
	// (which leaves a collapsed caret selection) anywhere on the page.
	if !strings.Contains(indicatorJS, "sel.isCollapsed") {
		t.Error("selectionWithinTabContent must skip collapsed selections so a stray caret position does not freeze panel updates")
	}

	// The interval body in startTabUpdates must call the gate before
	// updateActiveTabContent. updateTabBadges runs unconditionally above
	// the gate so badge counts stay live even while the user is selecting.
	startIdx := strings.Index(indicatorJS, "function startTabUpdates")
	if startIdx < 0 {
		t.Fatal("startTabUpdates not found in indicator.js")
	}
	// Locate the function body's closing brace by scanning for the first
	// "\n  }\n" pattern after startTabUpdates — matches the indent style
	// used throughout indicator.js.
	rest := indicatorJS[startIdx:]
	endIdx := strings.Index(rest, "\n  }\n")
	if endIdx < 0 {
		t.Fatal("could not locate end of startTabUpdates function body")
	}
	body := rest[:endIdx]

	gateCall := helperName + "()"
	gateIdx := strings.Index(body, gateCall)
	updateIdx := strings.Index(body, "updateActiveTabContent()")
	if gateIdx < 0 {
		t.Fatalf("startTabUpdates must call %s to gate the periodic refresh", gateCall)
	}
	if updateIdx < 0 {
		t.Fatal("startTabUpdates must still call updateActiveTabContent — the gate is supposed to short-circuit it, not remove it")
	}
	if gateIdx >= updateIdx {
		t.Errorf("%s must be called BEFORE updateActiveTabContent in startTabUpdates so the gate can short-circuit the refresh; gate@%d updateActiveTabContent@%d", gateCall, gateIdx, updateIdx)
	}

	// updateTabBadges must remain unconditional (above the gate) so badge
	// counts continue to update during selection. Otherwise the panel
	// would look frozen even when no selection is active in the content
	// area but the user is selecting elsewhere on the page.
	badgeIdx := strings.Index(body, "updateTabBadges()")
	if badgeIdx < 0 {
		t.Fatal("startTabUpdates must call updateTabBadges so badge counts stay live")
	}
	if badgeIdx >= gateIdx {
		t.Errorf("updateTabBadges must be called BEFORE %s in startTabUpdates so badges stay live even when content refresh is gated; updateTabBadges@%d gate@%d", gateCall, badgeIdx, gateIdx)
	}
}
