package scripts

import (
	"strings"
	"testing"
)

// TestModuleDependencyOrder verifies that every module's declared dependencies
// appear earlier in moduleOrder, and that every embedded script variable is
// accounted for in the order.
func TestModuleDependencyOrder(t *testing.T) {
	// Build a position index: module name -> index in moduleOrder
	pos := make(map[string]int, len(moduleOrder))
	for i, m := range moduleOrder {
		if _, exists := pos[m.name]; exists {
			t.Errorf("duplicate module %q in moduleOrder", m.name)
		}
		pos[m.name] = i
	}

	// Verify each module's deps appear earlier
	for _, m := range moduleOrder {
		modPos := pos[m.name]
		for _, dep := range m.deps {
			depPos, exists := pos[dep]
			if !exists {
				t.Errorf("module %q declares dependency %q which is not in moduleOrder", m.name, dep)
				continue
			}
			if depPos >= modPos {
				t.Errorf("module %q (index %d) depends on %q (index %d), but dependency must appear earlier",
					m.name, modPos, dep, depPos)
			}
		}
	}

	// Verify all embedded variables are in moduleScript
	embeddedVars := map[string]string{
		"core": coreJS, "shadow-root": shadowRootJS,
		"framework-detector": frameworkDetectorJS,
		"api-tracker":        apiTrackerJS, "utils": utilsJS,
		"overlay": overlayJS, "inspection": inspectionJS,
		"tree": treeJS, "visual": visualJS,
		"layout": layoutJS, "interactive": interactiveJS,
		"capture": captureJS, "accessibility": accessibilityJS,
		"audit-utils": auditUtilsJS, "audit-dom": auditDomJS,
		"audit-css": auditCssJS, "audit-security": auditSecurityJS,
		"audit-performance": auditPerformanceJS, "audit-quality": auditQualityJS,
		"interaction": interactionJS, "mutation": mutationJS,
		"toast": toastJS, "voice": voiceJS,
		"sketch": sketchJS, "design": designJS,
		"palette":        paletteJS,
		"variant-engine": variantEngineJS,
		"demo-indicator": demoIndicatorJS,
		"style-editor":   styleEditorJS, "indicator": indicatorJS,
		"indicator-styles": indicatorStylesJS, "indicator-data": indicatorDataJS,
		"indicator-tabs": indicatorTabsJS, "indicator-bridge": indicatorBridgeJS,
		"walkthrough-proxy": walkthroughProxyJS,
		"snapshot-helper":   snapshotHelperJS, "diagnostics": diagnosticsJS,
		"session": sessionJS, "store": storeJS,
		"content": contentJS, "text-fragility": textFragilityJS,
		"responsive-risk": responsiveRiskJS, "wireframe": wireframeJS,
		"responsive": responsiveJS, "api": apiJS,
	}

	// Every module in moduleOrder must have a script
	for _, m := range moduleOrder {
		script, exists := moduleScript[m.name]
		if !exists {
			t.Errorf("module %q is in moduleOrder but missing from moduleScript", m.name)
			continue
		}
		if strings.TrimSpace(script) == "" {
			t.Errorf("module %q maps to an empty embedded script", m.name)
		}
	}

	// Every embedded var must be in moduleScript (no orphaned embeds)
	for name := range embeddedVars {
		if _, exists := moduleScript[name]; !exists {
			t.Errorf("embedded script %q is not in moduleScript (orphaned embed)", name)
		}
	}

	// Every moduleScript entry must be in moduleOrder
	for name := range moduleScript {
		if _, exists := pos[name]; !exists {
			t.Errorf("moduleScript entry %q is not in moduleOrder", name)
		}
	}
}

// TestShadowRootBootstrapOrder verifies that shadow-root.js is registered as
// a dependency of every module that mounts UI into the mount root, so that
// window.__devtoolGetMountRoot is always available before those modules init.
func TestShadowRootBootstrapOrder(t *testing.T) {
	consumers := []string{"overlay", "indicator"}

	// Build a dep lookup
	deps := make(map[string][]string)
	for _, m := range moduleOrder {
		deps[m.name] = m.deps
	}

	for _, name := range consumers {
		modDeps, ok := deps[name]
		if !ok {
			t.Errorf("consumer module %q not found in moduleOrder", name)
			continue
		}
		found := false
		for _, d := range modDeps {
			if d == "shadow-root" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("module %q must declare %q as a dependency so the mount helper is available at init time",
				name, "shadow-root")
		}
	}

	// Verify the shadow-root.js source exports the public helpers consumers rely on.
	if !strings.Contains(shadowRootJS, "__devtoolGetMountRoot") {
		t.Errorf("shadow-root.js does not expose window.__devtoolGetMountRoot")
	}
	if !strings.Contains(shadowRootJS, "__devtoolIsShadowMount") {
		t.Errorf("shadow-root.js does not expose window.__devtoolIsShadowMount")
	}
	// Verify it uses 'open' mode (intentional — see shadow-root.js header comment).
	if !strings.Contains(shadowRootJS, "mode: 'open'") {
		t.Errorf("shadow-root.js should use attachShadow({ mode: 'open' }); closed mode breaks devtool self-inspection")
	}

	// Regression guard (DART-wV4wNZb8XWW3): the shadow host's inline cssText
	// must not set `pointer-events: none`. When an ancestor has
	// pointer-events:none, hit testing skips the element AND any descendants
	// that do not explicitly set pointer-events to a non-none value. The
	// indicator bug / panel / buttons inherit the default `auto` and therefore
	// become unclickable and undraggable when this rule is set. The host is
	// already 0x0 and position:static so it cannot intercept clicks on its own
	// — setting pointer-events:none is both unnecessary and harmful.
	//
	// We scan the host.style.cssText assignment specifically (not the whole
	// file, which contains explanatory comments referencing the forbidden
	// rule).
	cssTextIdx := strings.Index(shadowRootJS, "host.style.cssText")
	if cssTextIdx < 0 {
		t.Fatalf("shadow-root.js missing host.style.cssText assignment; test cannot guard against pointer-events regression")
	}
	// Find the end of the statement (semicolon after the closing quote).
	rest := strings.ReplaceAll(shadowRootJS[cssTextIdx:], "\r\n", "\n")
	stmtEnd := strings.Index(rest, ";\n")
	if stmtEnd < 0 {
		t.Fatalf("shadow-root.js host.style.cssText assignment not terminated with ;\\n; cannot parse")
	}
	cssTextStmt := rest[:stmtEnd]
	if strings.Contains(cssTextStmt, "pointer-events: none") ||
		strings.Contains(cssTextStmt, "pointer-events:none") {
		t.Errorf("shadow-root.js host.style.cssText must not set pointer-events:none — it blocks hit testing of shadow descendants (indicator bug/panel/buttons). Assignment:\n  %s", cssTextStmt)
	}

	// Verify the combined script emits shadow-root before overlay and indicator.
	combined := GetCombinedScript()
	shadowIdx := strings.Index(combined, "// shadow-root module")
	overlayIdx := strings.Index(combined, "// overlay module")
	indicatorIdx := strings.Index(combined, "// indicator module")
	if shadowIdx < 0 {
		t.Fatalf("combined script missing shadow-root module marker")
	}
	if overlayIdx < 0 || indicatorIdx < 0 {
		t.Fatalf("combined script missing overlay or indicator marker")
	}
	if shadowIdx >= overlayIdx {
		t.Errorf("shadow-root must be emitted before overlay (got shadowIdx=%d, overlayIdx=%d)", shadowIdx, overlayIdx)
	}
	if shadowIdx >= indicatorIdx {
		t.Errorf("shadow-root must be emitted before indicator (got shadowIdx=%d, indicatorIdx=%d)", shadowIdx, indicatorIdx)
	}
}

// TestScreenshotOverlayBlockHandler guards against a recurring regression in
// startScreenshotMode's `block` helper: if it calls preventDefault on pointer*
// events (pointerdown / pointermove / pointerup), per the Pointer Events spec
// the browser suppresses the compatibility mousedown / mousemove / mouseup
// that the drag selection handlers depend on, and the rectangle never draws.
// Two prior commits already attempted to fix this regression
// (commit 4cceb2e introduced the bug, commit aaf0c36 made it less broken by
// removing stopImmediatePropagation but left preventDefault for pointer events
// in place). The current fix routes preventDefault past pointer events.
func TestScreenshotOverlayBlockHandler(t *testing.T) {
	// Locate the block helper inside startScreenshotMode.
	const marker = "function block(e)"
	idx := strings.Index(indicatorJS, marker)
	if idx < 0 {
		t.Fatalf("indicator.js missing %q; cannot guard screenshot drag regression", marker)
	}
	end := strings.Index(indicatorJS[idx:], "}")
	if end < 0 {
		t.Fatalf("block() helper in indicator.js not terminated with }; cannot parse")
	}
	body := indicatorJS[idx : idx+end+1]

	// Must not unconditionally preventDefault — that re-introduces the
	// pointer-events compatibility-mouse suppression bug. Verify each
	// preventDefault call sits inside an if-block whose condition tests the
	// event type for "pointer". A nearby preceding `if (` line referencing
	// `pointer` is treated as a valid gate.
	lines := strings.Split(body, "\n")
	preventDefaultLines := []int{}
	for i, line := range lines {
		if strings.Contains(line, "e.preventDefault()") {
			preventDefaultLines = append(preventDefaultLines, i)
		}
	}
	if len(preventDefaultLines) == 0 {
		t.Errorf("block() in indicator.js no longer calls preventDefault — non-pointer mouse/click/contextmenu events need it to suppress text selection and the right-click menu.\nblock body:\n%s", body)
	}
	for _, lineNum := range preventDefaultLines {
		guarded := false
		for j := lineNum - 1; j >= 0 && j >= lineNum-3; j-- {
			if strings.Contains(lines[j], "if (") && strings.Contains(lines[j], "pointer") {
				guarded = true
				break
			}
		}
		if !guarded {
			t.Errorf("block() in indicator.js calls e.preventDefault() at body line %d without an enclosing pointer-event gate — "+
				"this suppresses compatibility mouse events and breaks screenshot drag selection.\nblock body:\n%s", lineNum, body)
		}
	}

	// Must call stopPropagation (otherwise click pass-through to the page
	// underneath comes back).
	if !strings.Contains(body, "e.stopPropagation()") {
		t.Errorf("block() in indicator.js missing e.stopPropagation() — clicks will pass through to the page beneath the overlay")
	}

	// Must NOT use stopImmediatePropagation — that blocks sibling handlers on
	// the same overlay from firing (the original bug from commit 4cceb2e).
	if strings.Contains(body, "stopImmediatePropagation") {
		t.Errorf("block() in indicator.js uses stopImmediatePropagation — this blocks the drag handlers registered as siblings on the same overlay element")
	}
}

// TestAuditMegaMenuVisibilityNotInlinedForPopover guards against a regression
// where STYLES.megaMenu's inline opacity:0 / transform / pointer-events:none
// values beat the stylesheet rules from injectAuditMenuStyles() that toggle
// those properties on :popover-open. Inline styles always win over stylesheet
// rules (without !important), so the popover opens but renders invisible.
//
// The fix in createAuditButton clears those three inline properties when on
// the modern (Popover API) path, leaving the stylesheet rules in control.
// This test parses the createAuditButton body and verifies the cleanup is
// still present and gated on supportsPopover.
func TestAuditMegaMenuVisibilityNotInlinedForPopover(t *testing.T) {
	// Find the menu-creation block.
	const idMarker = "menu.id = '__devtool-audit-menu';"
	idx := strings.Index(indicatorJS, idMarker)
	if idx < 0 {
		t.Fatalf("indicator.js missing audit menu id assignment %q; cannot guard regression", idMarker)
	}
	// Look at the next ~25 lines after the id assignment for the cleanup.
	tail := indicatorJS[idx:]
	if len(tail) > 1500 {
		tail = tail[:1500]
	}

	// Cleanup must be gated on supportsPopover so the legacy path keeps its
	// inline visibility defaults.
	if !strings.Contains(tail, "if (supportsPopover)") {
		t.Errorf("createAuditButton must clear inline visibility properties under `if (supportsPopover)` after creating the menu so :popover-open stylesheet rules can take effect. Block:\n%s", tail)
	}

	// All three properties must be cleared.
	required := []string{
		"menu.style.opacity = ''",
		"menu.style.transform = ''",
		"menu.style.pointerEvents = ''",
	}
	for _, expr := range required {
		if !strings.Contains(tail, expr) {
			t.Errorf("createAuditButton missing %q after menu creation — inline visibility will beat the :popover-open stylesheet rules and the audit menu will render invisible", expr)
		}
	}
}

// TestAuditMenuAnchorPositioningProgressiveEnhancement guards the progressive
// enhancement for audit menu placement (DART-UfSx4Exbzsuc).
//
// The modern path uses the CSS Anchor Positioning API to place the popover
// relative to the audit button without any JS layout reads. When supported,
// the browser:
//   - Places the menu directly below the button via anchor(bottom) / anchor(left)
//   - Flips block/inline direction automatically via position-try-fallbacks
//   - Re-anchors on resize without a JS resize listener
//
// The legacy JS fallback (older browsers without anchor positioning) keeps
// the existing requestAnimationFrame + getBoundingClientRect dance. Both the
// anchor-positioning CSS rules AND the repositionMenu JS fallback must be
// present for this regression test to pass.
func TestAuditMenuAnchorPositioningProgressiveEnhancement(t *testing.T) {
	// 1. The injected stylesheet must declare the anchor-name/position-anchor
	//    rules, gated on CSS.supports so legacy browsers don't see them.
	//
	// Find the injectAuditMenuStyles function body.
	const marker = "function injectAuditMenuStyles()"
	idx := strings.Index(indicatorJS, marker)
	if idx < 0 {
		t.Fatalf("indicator.js missing %q; cannot guard anchor positioning regression", marker)
	}
	// Scan a generous window for the function body.
	window := indicatorJS[idx:]
	if len(window) > 3000 {
		window = window[:3000]
	}

	// Feature-detect gate: CSS.supports('anchor-name: --x') must be tested.
	// Accept either the two-argument or one-argument form of CSS.supports.
	if !strings.Contains(window, "CSS.supports") {
		t.Errorf("injectAuditMenuStyles must feature-detect anchor positioning via CSS.supports so legacy browsers skip the anchor rules. Body:\n%s", window)
	}
	if !strings.Contains(window, "anchor-name") {
		t.Errorf("injectAuditMenuStyles must feature-detect 'anchor-name' so the modern path can skip JS positioning. Body:\n%s", window)
	}

	// The anchor positioning CSS rules themselves must be emitted when the
	// feature is supported. The audit button needs anchor-name, the menu
	// needs position-anchor + anchor() values + position-try-fallbacks.
	rules := []string{
		"anchor-name: --devtool-audit-anchor",
		"position-anchor: --devtool-audit-anchor",
		"anchor(",                // at least one anchor() function (top/bottom/left/right)
		"position-try-fallbacks", // flip-block / flip-inline
	}
	for _, rule := range rules {
		if !strings.Contains(window, rule) {
			t.Errorf("injectAuditMenuStyles missing anchor positioning rule %q. Body:\n%s", rule, window)
		}
	}

	// 2. The audit button must have an id so the #__devtool-audit-btn selector
	//    in the stylesheet rule can target it. Without an id, anchor-name
	//    won't apply to it.
	if !strings.Contains(indicatorJS, "btn.id = '__devtool-audit-btn'") {
		t.Errorf("createAuditButton must assign btn.id = '__devtool-audit-btn' so the #__devtool-audit-btn anchor-name rule can target it")
	}

	// 3. The JS fallback function repositionMenu must still be defined so
	//    pre-anchor-positioning browsers (Firefox stable, older Safari) keep
	//    working. This is the "legacy path still intact" regression guard.
	if !strings.Contains(indicatorJS, "function repositionMenu()") {
		t.Errorf("repositionMenu JS fallback must still be defined for browsers without CSS anchor positioning support")
	}

	// 4. The JS fallback must use DOUBLE requestAnimationFrame for the
	//    beforetoggle-triggered reposition. A single rAF fires before the
	//    popover's top-layer layout is fully committed in some engines; the
	//    second rAF runs after the first paint, at which point offsetWidth/
	//    offsetHeight are guaranteed to be non-zero.
	//
	// Find the beforetoggle handler in createAuditButton.
	btIdx := strings.Index(indicatorJS, "menu.addEventListener('beforetoggle'")
	if btIdx < 0 {
		t.Fatalf("createAuditButton missing beforetoggle listener; cannot guard rAF race")
	}
	btTail := indicatorJS[btIdx:]
	if len(btTail) > 2500 {
		btTail = btTail[:2500]
	}
	// Look for double rAF: a requestAnimationFrame whose callback calls
	// requestAnimationFrame again before calling repositionMenu. The exact
	// syntax varies but both calls must appear within the open-state branch.
	rafCount := strings.Count(btTail, "requestAnimationFrame")
	if rafCount < 2 {
		t.Errorf("beforetoggle open-state branch must use double requestAnimationFrame to avoid the upper-left-corner race (got %d rAF calls in the handler tail). The first rAF runs before popover layout is committed in some engines; the second rAF runs after paint with valid offsetWidth/offsetHeight. Body:\n%s", rafCount, btTail)
	}
}

// TestAuditMenuAnchorClearsInsetLonghands guards against an inline-cascade
// regression where STYLES.megaMenu's `inset: unset` shorthand expands into
// four inline longhand declarations (top/right/bottom/left = auto). On the
// anchor-positioning path, those inline longhands would beat the stylesheet
// rules `top: anchor(bottom); left: anchor(left);` because inline styles
// always win over selector rules without `!important`. Result: the menu
// would render at the top-layer containing-block origin (0,0) — the exact
// upper-left-corner bug we're fixing.
//
// The fix: after assigning `menu.style.cssText = STYLES.megaMenu;`, clear
// the four inline longhand positioning properties under the
// supportsAnchorPositioning gate so the stylesheet cascade can apply the
// anchor() values unopposed.
func TestAuditMenuAnchorClearsInsetLonghands(t *testing.T) {
	// Find the createAuditButton block that runs after menu creation.
	const idMarker = "menu.id = '__devtool-audit-menu';"
	idx := strings.Index(indicatorJS, idMarker)
	if idx < 0 {
		t.Fatalf("indicator.js missing audit menu id assignment %q", idMarker)
	}
	tail := indicatorJS[idx:]
	if len(tail) > 2500 {
		tail = tail[:2500]
	}

	// The longhand cleanup must be gated on supportsAnchorPositioning so
	// the legacy (non-anchor) popover path keeps setting top/left inline
	// from repositionMenu() without having them cleared out.
	if !strings.Contains(tail, "if (supportsAnchorPositioning)") {
		t.Errorf("createAuditButton must clear inset longhands under `if (supportsAnchorPositioning)` so the stylesheet anchor() rules take effect. Block:\n%s", tail)
	}

	// All four inset longhands must be cleared. Clearing the `inset`
	// shorthand alone does NOT remove the already-expanded inline
	// longhands from the inline style map, so each must be cleared
	// individually.
	required := []string{
		"menu.style.top = ''",
		"menu.style.right = ''",
		"menu.style.bottom = ''",
		"menu.style.left = ''",
	}
	for _, expr := range required {
		if !strings.Contains(tail, expr) {
			t.Errorf("createAuditButton missing %q inside the supportsAnchorPositioning cleanup block — the inline `inset: unset` shorthand expansion will override the stylesheet's anchor() positioning rules", expr)
		}
	}
}

// TestAuditMenuAnchorPositionedSkipsJSFallback verifies the short-circuit:
// when CSS anchor positioning is supported, createAuditButton must NOT wire
// up the resize listener or the rAF-based repositionMenu beforetoggle path,
// because the browser handles placement + flip fallbacks natively.
//
// The guard here is that the beforetoggle handler's open branch is gated
// on supportsAnchorPositioning being false (or equivalently, the anchor-
// positioning path returns early / skips the JS wiring). We verify this by
// ensuring a supportsAnchorPositioning variable is declared and consulted
// in the modern path.
func TestAuditMenuAnchorPositionedSkipsJSFallback(t *testing.T) {
	// Must declare a local variable feature-detecting anchor positioning
	// inside createAuditButton so the two internal branches can diverge.
	if !strings.Contains(indicatorJS, "supportsAnchorPositioning") {
		t.Errorf("createAuditButton must declare a supportsAnchorPositioning feature-detect variable so the modern path can skip the rAF reposition dance")
	}
	// And must actually branch on it — the variable must be read, not just
	// declared. Grep for any usage beyond the declaration (at least 2
	// occurrences: the assignment and one conditional).
	occurrences := strings.Count(indicatorJS, "supportsAnchorPositioning")
	if occurrences < 2 {
		t.Errorf("supportsAnchorPositioning is declared but never read (%d occurrences). The modern path must branch on it to skip JS positioning.", occurrences)
	}
}
