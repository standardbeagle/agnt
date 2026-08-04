package scripts

import (
	"strings"
	"testing"
)

// These tests cover the audit-animations.js module statically. The scripts
// package has no embedded JS engine, so the detectors cannot be executed
// against synthetic animation registries from Go. Instead we assert the
// module's structural contract — detector presence, finding type strings, the
// data source, the unavailable-API honesty path, and the public export — so
// that a regression (deleted detector, renamed type, wrong global) fails the
// build. Runtime behavior is verified manually in a browser. This mirrors the
// established static-only precedent of audit_loading_test.go /
// audit_api_test.go / audit_ids_test.go.

// TestAuditAnimations_DetectorsPresent asserts the two scan passes exist.
func TestAuditAnimations_DetectorsPresent(t *testing.T) {
	detectors := []string{
		"scanAnimations",
		"scanAmplifiers",
		"sampleFrames",
	}
	for _, d := range detectors {
		if !strings.Contains(auditAnimationsJS, "function "+d) {
			t.Errorf("audit-animations.js missing detector function %q", d)
		}
	}
}

// TestAuditAnimations_FindingTypes asserts each documented finding type string
// is emitted. These are the stable identifiers downstream groups on.
func TestAuditAnimations_FindingTypes(t *testing.T) {
	types := []string{
		"infinite-animation",
		"layout-property-animation",
		"viewport-overlay-amplifier",
		"backdrop-filter-amplifier",
		"will-change-overuse",
	}
	for _, ty := range types {
		if !strings.Contains(auditAnimationsJS, "'"+ty+"'") {
			t.Errorf("audit-animations.js missing finding type %q", ty)
		}
	}
}

// TestAuditAnimations_PublicExportAndDataSource asserts the public entry point
// and that the audit reads the declarative animation registry — the one signal
// MutationObserver and visual-state snapshots cannot provide.
func TestAuditAnimations_PublicExportAndDataSource(t *testing.T) {
	if !strings.Contains(auditAnimationsJS, "auditAnimations") {
		t.Error("audit-animations.js missing auditAnimations entry point")
	}
	if !strings.Contains(auditAnimationsJS, "window.__devtool_audit_animations") {
		t.Error("audit-animations.js must export window.__devtool_audit_animations")
	}
	if !strings.Contains(auditAnimationsJS, "document.getAnimations()") {
		t.Error("audit-animations.js must read document.getAnimations()")
	}
}

// TestAuditAnimations_UnavailableAPIHonest guards the no-fake-data rule: a
// browser without document.getAnimations must produce a notApplicable report,
// never a passing grade the audit could not have measured.
func TestAuditAnimations_UnavailableAPIHonest(t *testing.T) {
	if !strings.Contains(auditAnimationsJS, "notApplicable: true") {
		t.Error("audit-animations.js must report notApplicable when getAnimations is unavailable")
	}
	if !strings.Contains(auditAnimationsJS, "not measurable") {
		t.Error("audit-animations.js must say compositor load is not measurable without the registry")
	}
}

// TestAuditAnimations_EmbeddedAndOrdered confirms the module is embedded and
// present in the combined script.
func TestAuditAnimations_EmbeddedAndOrdered(t *testing.T) {
	if auditAnimationsJS == "" {
		t.Fatal("auditAnimationsJS embed is empty — audit-animations.js not embedded")
	}
	combined := GetCombinedScript()
	if !strings.Contains(combined, "auditAnimations") {
		t.Error("combined script missing audit-animations content")
	}
}

// TestAuditAnimations_RAFCaveatDocumented pins the honesty caveat: rAF frame
// sampling is one signal, not an oracle — compositor-only animations can
// commit without firing page rAF, and GPU-process CPU lives outside page APIs.
func TestAuditAnimations_RAFCaveatDocumented(t *testing.T) {
	if !strings.Contains(auditAnimationsJS, "not an oracle") {
		t.Error("audit-animations.js must document the rAF-sampling caveat")
	}
}

// TestAuditAnimations_FrameSampleBounded guards the liveness-probe rule
// (.claude/rules/lessons-liveness-probes.md): a backgrounded/occluded tab
// throttles or stops rAF entirely, so the sample must carry a setTimeout
// backstop that resolves with rafStarved rather than hanging forever — and
// the summary must call the starved sample inconclusive, never "page idles".
func TestAuditAnimations_FrameSampleBounded(t *testing.T) {
	if !strings.Contains(auditAnimationsJS, "setTimeout") {
		t.Error("audit-animations.js sampleFrames must bound the rAF wait with a setTimeout backstop")
	}
	if !strings.Contains(auditAnimationsJS, "rafStarved") {
		t.Error("audit-animations.js must flag a starved sample via rafStarved")
	}
	if !strings.Contains(auditAnimationsJS, "inconclusive") {
		t.Error("audit-animations.js summary must report a starved sample as inconclusive")
	}
}
