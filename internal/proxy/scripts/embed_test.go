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
		"style-editor": styleEditorJS, "indicator": indicatorJS,
		"snapshot-helper": snapshotHelperJS, "diagnostics": diagnosticsJS,
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
	rest := shadowRootJS[cssTextIdx:]
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
