package scripts

import (
	"strings"
	"testing"
)

// TestFullPageAuditCallsAuditAll pins the fix for the 'fullAudit' toolbar
// action (label "Full Page Audit", described as a "Comprehensive quality
// audit with grade (A-F)"). It used to call w.__devtool.auditPageQuality(),
// which is the SEO/meta audit alone — the SAME function the separate 'seo'
// action already calls — and has no A-F grade at all. The comprehensive,
// eight-audit weighted grade the label promises is auditAll
// (docs/mcp-tools.md "Scored audits": DOM, CSS, performance, security, SEO,
// accessibility, API efficiency, loading).
func TestFullPageAuditCallsAuditAll(t *testing.T) {
	idx := strings.Index(indicatorJS, "id: 'fullAudit'")
	if idx < 0 {
		t.Fatal("indicator.js missing the fullAudit AUDIT_ACTIONS entry")
	}
	// The entry's body ends at the next top-level action's id.
	rest := indicatorJS[idx:]
	end := strings.Index(rest[1:], "id: '")
	if end < 0 {
		t.Fatal("could not bound the fullAudit action body")
	}
	body := rest[:end+1]

	if !strings.Contains(body, "w.__devtool.auditAll()") {
		t.Error("fullAudit must call w.__devtool.auditAll() (the comprehensive, graded audit)")
	}
	if strings.Contains(body, "w.__devtool.auditPageQuality()") {
		t.Error("fullAudit must not call auditPageQuality — that is the SEO-only audit the 'seo' action already exposes separately, and has no A-F grade")
	}
	if !strings.Contains(body, "label: 'Full Page Audit'") {
		t.Error("fullAudit label changed unexpectedly; update this test if that was intentional")
	}
}
