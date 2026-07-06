package scripts

import (
	"strings"
	"testing"
)

// These tests cover the audit-api.js module (B1) statically. The scripts
// package has no embedded JS engine, so the four detectors cannot be executed
// against synthetic call buffers from Go. Instead we assert the module's
// structural contract — detector presence, finding type strings, the
// fail-honest payload-size note, the data source, and the public export — so
// that any regression (a deleted detector, a renamed type, a fabricated size,
// a wrong global) fails the build. Runtime behavior is verified manually in a
// browser (the optional render-check step) on a Chrome-capable host.

// TestAuditAPI_DetectorsPresent asserts all four efficiency detectors exist.
func TestAuditAPI_DetectorsPresent(t *testing.T) {
	detectors := []string{
		"detectWaterfall",
		"detectNPlusOne",
		"detectDuplicates",
		"detectChattyLoad",
	}
	for _, d := range detectors {
		if !strings.Contains(auditApiJS, "function "+d) {
			t.Errorf("audit-api.js missing detector function %q", d)
		}
	}
}

// TestAuditAPI_FindingTypes asserts each detector emits its documented finding
// type string. These are the stable identifiers downstream tools group on.
func TestAuditAPI_FindingTypes(t *testing.T) {
	types := []string{
		"waterfall",
		"n-plus-one",
		"duplicate-call",
		"chatty-load",
	}
	for _, ty := range types {
		if !strings.Contains(auditApiJS, "'"+ty+"'") {
			t.Errorf("audit-api.js missing finding type %q", ty)
		}
	}
}

// TestAuditAPI_FailHonestNoFabricatedSize guards the project's no-fake-data
// rule: the call buffer has no response-size field, so the audit must surface
// an explicit limitation rather than inventing a payload size. The limitation
// lives in the result's `limitations` metadata, not in findings, so it never
// inflates issue counts.
func TestAuditAPI_FailHonestNoFabricatedSize(t *testing.T) {
	if !strings.Contains(auditApiJS, "over-fetch detection unavailable") {
		t.Error("audit-api.js must state that payload-size over-fetch detection is unavailable (payload size is not in the buffer; do not fabricate)")
	}
	if !strings.Contains(auditApiJS, "limitations") {
		t.Error("audit-api.js must expose the over-fetch limitation via a 'limitations' field")
	}
	if strings.Contains(auditApiJS, "over-fetch-unavailable") {
		t.Error("over-fetch-unavailable must not be a finding type — it inflated issue totals; keep it in limitations metadata")
	}
}

// TestAuditAPI_PublicExportAndDataSource asserts the module exposes the public
// entry point and reads the api-tracker buffer (not a private recorder).
func TestAuditAPI_PublicExportAndDataSource(t *testing.T) {
	if !strings.Contains(auditApiJS, "auditAPIEfficiency") {
		t.Error("audit-api.js missing auditAPIEfficiency entry point")
	}
	if !strings.Contains(auditApiJS, "window.__devtool_audit_api") {
		t.Error("audit-api.js must export window.__devtool_audit_api")
	}
	if !strings.Contains(auditApiJS, "window.__devtool_api") {
		t.Error("audit-api.js must read the api-tracker buffer via window.__devtool_api")
	}
}

// TestAuditAPI_EmbeddedAndOrdered confirms the module is embedded and loads
// after its api-tracker dependency in the combined script.
func TestAuditAPI_EmbeddedAndOrdered(t *testing.T) {
	if auditApiJS == "" {
		t.Fatal("auditApiJS embed is empty — audit-api.js not embedded")
	}
	combined := GetCombinedScript()
	apiTrackerPos := strings.Index(combined, "Intercepts fetch and XMLHttpRequest")
	auditApiPos := strings.Index(combined, "auditAPIEfficiency")
	if apiTrackerPos < 0 || auditApiPos < 0 {
		t.Fatal("combined script missing api-tracker or audit-api content")
	}
	if apiTrackerPos > auditApiPos {
		t.Error("audit-api must load after api-tracker (it reads window.__devtool_api at call time)")
	}
}
