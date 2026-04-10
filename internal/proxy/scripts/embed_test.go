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
		"core": coreJS, "framework-detector": frameworkDetectorJS,
		"api-tracker": apiTrackerJS, "utils": utilsJS,
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
