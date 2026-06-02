package scripts

import (
	"strings"
	"testing"
)

// These tests cover the audit-loading.js module (C2) statically. The scripts
// package has no embedded JS engine, so the cascade/fragmentation detectors
// cannot be executed against synthetic spinner timelines from Go. Instead we
// assert the module's structural contract — detector presence, finding type
// strings, the data source, the empty-timeline honesty path, and the public
// export — so that a regression (deleted detector, renamed type, wrong global)
// fails the build. Runtime behavior is verified manually in a browser (the
// optional render-check step) on a Chrome-capable host. This mirrors the
// established static-only precedent of audit_ids_test.go / audit_api_test.go.

// TestAuditLoading_DetectorsPresent asserts both loading-UX detectors exist.
func TestAuditLoading_DetectorsPresent(t *testing.T) {
	detectors := []string{
		"detectCascade",
		"detectFragmentation",
	}
	for _, d := range detectors {
		if !strings.Contains(auditLoadingJS, "function "+d) {
			t.Errorf("audit-loading.js missing detector function %q", d)
		}
	}
}

// TestAuditLoading_FindingTypes asserts each detector emits its documented
// finding type string. These are the stable identifiers downstream groups on.
func TestAuditLoading_FindingTypes(t *testing.T) {
	types := []string{
		"spinner-cascade",
		"spinner-fragmentation",
	}
	for _, ty := range types {
		if !strings.Contains(auditLoadingJS, "'"+ty+"'") {
			t.Errorf("audit-loading.js missing finding type %q", ty)
		}
	}
}

// TestAuditLoading_PublicExportAndDataSource asserts the public entry point
// and that the audit reads the spinner timeline recorded by the C1 observer.
func TestAuditLoading_PublicExportAndDataSource(t *testing.T) {
	if !strings.Contains(auditLoadingJS, "auditLoading") {
		t.Error("audit-loading.js missing auditLoading entry point")
	}
	if !strings.Contains(auditLoadingJS, "window.__devtool_audit_loading") {
		t.Error("audit-loading.js must export window.__devtool_audit_loading")
	}
	if !strings.Contains(auditLoadingJS, "window.__devtool_spinners") {
		t.Error("audit-loading.js must read the spinner timeline via window.__devtool_spinners")
	}
}

// TestAuditLoading_EmptyTimelineHonest guards the no-fake-data rule: an empty
// or absent timeline must produce an honest "reload page" summary rather than
// inventing findings.
func TestAuditLoading_EmptyTimelineHonest(t *testing.T) {
	if !strings.Contains(auditLoadingJS, "reload page") {
		t.Error("audit-loading.js must surface a 'reload page then re-run' summary on an empty timeline")
	}
}

// TestAuditLoading_EmbeddedAndOrdered confirms the module is embedded and that
// audit-loading content appears in the combined script.
func TestAuditLoading_EmbeddedAndOrdered(t *testing.T) {
	if auditLoadingJS == "" {
		t.Fatal("auditLoadingJS embed is empty — audit-loading.js not embedded")
	}
	combined := GetCombinedScript()
	if !strings.Contains(combined, "auditLoading") {
		t.Error("combined script missing audit-loading content")
	}
}
