package scripts

import (
	"strings"
	"testing"
)

// The CSS stacking/containing-block introspection is static-tested (the
// scripts package has no embedded JS engine). These tests pin the runtime
// contract that makes these helpers useful for agents: the FULL spec set of
// stacking-context triggers must be detected (the old four-check
// implementation silently missed will-change, isolation, contain,
// mix-blend-mode, clip-path, mask, backdrop-filter, and flex/grid children),
// and the containing-block trap detection must cover the ancestor properties
// that capture a position:fixed element. A regression — a deleted trigger, a
// renamed helper — fails the build instead of silently degrading the agent's
// diagnostic surface.

// TestStackingTriggers_FullSpecSet asserts utils.stackingContextTriggers
// detects every stacking-context trigger, not just the obvious four.
func TestStackingTriggers_FullSpecSet(t *testing.T) {
	// CSS property tokens that must appear as detected triggers.
	wantProps := []string{
		"z-index",
		"position",
		"opacity",
		"transform",
		"filter",
		"backdrop-filter",
		"perspective",
		"clip-path",
		"mask",
		"mix-blend-mode",
		"isolation",
		"will-change",
		"contain",
	}
	for _, p := range wantProps {
		if !strings.Contains(utilsJS, "'"+p+"'") {
			t.Errorf("utils.js stackingContextTriggers missing trigger property %q", p)
		}
	}
}

// TestStackingTriggers_Helpers asserts the shared helpers exist and are
// exported, so getStacking/findStackingContexts/getContainer can rely on them.
func TestStackingTriggers_Helpers(t *testing.T) {
	helpers := []string{
		"stackingContextTriggers",
		"getStackingChain",
		"containingBlockTrap",
		"isFlexOrGridItem",
	}
	for _, h := range helpers {
		if !strings.Contains(utilsJS, "function "+h) {
			t.Errorf("utils.js missing helper function %q", h)
		}
		if !strings.Contains(utilsJS, h+": "+h) {
			t.Errorf("utils.js does not export helper %q on __devtool_utils", h)
		}
	}
}

// TestGetStacking_ExposesRootCause asserts getStacking returns the
// agent-decisive fields — the stacking root and the property that created it —
// not just a boolean.
func TestGetStacking_ExposesRootCause(t *testing.T) {
	fields := []string{
		"createsContext",
		"selfTriggers",
		"stackingRoot",
		"rootTrigger",
		"chain",
	}
	for _, f := range fields {
		if !strings.Contains(inspectionJS, f+":") {
			t.Errorf("inspection.js getStacking missing field %q", f)
		}
	}
}

// TestGetContainer_ExposesFixedTrap asserts getContainer surfaces the
// distant-ancestor property that traps a position:fixed element.
func TestGetContainer_ExposesFixedTrap(t *testing.T) {
	fields := []string{
		"expectedContainingBlock",
		"actualContainingBlock",
		"trappedBy",
		"escaped",
	}
	for _, f := range fields {
		if !strings.Contains(inspectionJS, "result."+f) {
			t.Errorf("inspection.js getContainer missing field %q", f)
		}
	}
	if !strings.Contains(inspectionJS, "containingBlockTrap") {
		t.Error("inspection.js getContainer does not call utils.containingBlockTrap")
	}
}

// TestFindStackingContexts_UsesSharedTriggers asserts the document-wide scan
// uses the same canonical trigger helper (so it cannot drift from getStacking)
// and emits structured {property,value} triggers.
func TestFindStackingContexts_UsesSharedTriggers(t *testing.T) {
	if !strings.Contains(layoutJS, "stackingContextTriggers") {
		t.Error("layout.js findStackingContexts does not use utils.stackingContextTriggers")
	}
	if !strings.Contains(layoutJS, "triggers:") {
		t.Error("layout.js findStackingContexts does not emit structured triggers")
	}
}
