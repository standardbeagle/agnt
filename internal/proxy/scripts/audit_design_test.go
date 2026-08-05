package scripts

import (
	"strings"
	"testing"
)

// Static contract tests for audit-design.js and the vendored Impeccable
// browser detector, mirroring the audit_loading/audit_animations precedent
// (the scripts package has no embedded JS engine).

// TestAuditDesign_DelayLoadContract pins the lazy-load shape: the detector
// bundle must never be in any injected bundle; audit-design injects it from
// the dedicated endpoint with auto-scan disabled first.
func TestAuditDesign_DelayLoadContract(t *testing.T) {
	if !strings.Contains(auditDesignJS, "/__devtool_impeccable") {
		t.Error("audit-design.js must load the detector from /__devtool_impeccable")
	}
	if !strings.Contains(auditDesignJS, "autoScan = false") {
		t.Error("audit-design.js must disable the detector's auto-scan before injection")
	}
	combined := GetCombinedScript()
	if strings.Contains(combined, "impeccableScan") {
		t.Error("the Impeccable detector bundle must NOT be inlined into the injected instrumentation — it is delay-loaded")
	}
	if !strings.Contains(combined, "auditDesign") {
		t.Error("combined script missing the audit-design wrapper module")
	}
}

// TestAuditDesign_VendoredBundleIntact confirms the vendored detector is
// embedded, keeps its license header, and exposes the browser entry point.
func TestAuditDesign_VendoredBundleIntact(t *testing.T) {
	if impeccableDetectJS == "" {
		t.Fatal("impeccableDetectJS embed is empty")
	}
	if !strings.Contains(impeccableDetectJS, "SPDX-License-Identifier: Apache-2.0") {
		t.Error("vendored Impeccable bundle must retain its Apache-2.0 SPDX header")
	}
	if !strings.Contains(impeccableDetectJS, "window.impeccableDetect = detect") {
		t.Error("vendored bundle must expose window.impeccableDetect — audit-design.js binds to it")
	}
	if GetImpeccableDetect() == "" {
		t.Error("GetImpeccableDetect must serve the embedded bundle")
	}
}

// TestAuditDesign_PublicExportAndAdvisoryHonesty pins the audit surface and
// the advisory rule: advisory findings surface as info and never score.
func TestAuditDesign_PublicExportAndAdvisoryHonesty(t *testing.T) {
	if !strings.Contains(auditDesignJS, "window.__devtool_audit_design") {
		t.Error("audit-design.js must export window.__devtool_audit_design")
	}
	if !strings.Contains(auditDesignJS, "window.__devtool.audit.auditDesign") {
		t.Error("audit-design.js must register __devtool.audit.auditDesign")
	}
	if !strings.Contains(auditDesignJS, "f.advisory ? 'info'") {
		t.Error("audit-design.js must downgrade advisory findings to info instead of scoring them")
	}
}
