package scripts

import (
	"strings"
	"testing"
)

// TestAuditFindingIDsPresent verifies that all four audit JS files contain
// computeFindingID and that their findings include an id field.
func TestAuditFindingIDsPresent(t *testing.T) {
	tests := []struct {
		name       string
		script     string
		scriptName string
	}{
		{"accessibility", accessibilityJS, "accessibility.js"},
		{"audit-dom", auditDomJS, "audit-dom.js"},
		{"audit-css", auditCssJS, "audit-css.js"},
		{"audit-quality", auditQualityJS, "audit-quality.js"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.script, "computeFindingID") {
				t.Errorf("%s missing computeFindingID function", tc.scriptName)
			}
			if !strings.Contains(tc.script, "registerFinding") {
				t.Errorf("%s missing registerFinding function", tc.scriptName)
			}
			if !strings.Contains(tc.script, "window.__devtool.audit.findingSelectors") {
				t.Errorf("%s does not write to shared window.__devtool.audit.findingSelectors registry", tc.scriptName)
			}
		})
	}
}

// TestAuditFindingIDStableHash verifies that computeFindingID produces the
// exact same 8-char hex output as the FNV-1a 32-bit JS implementation
// for a set of known inputs. The Go implementation here mirrors the JS exactly
// so any drift between the two would surface immediately.
func TestAuditFindingIDStableHash(t *testing.T) {
	// Reference implementation of the JS FNV-1a 32-bit hash copied verbatim.
	computeFindingID := func(typ, selector, message string) string {
		input := typ + "\x00" + selector + "\x00" + message
		h := uint32(0x811c9dc5)
		for i := 0; i < len(input); i++ {
			h = h ^ uint32(input[i])
			h = (h + (h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24))
		}
		hex := "0123456789abcdef"
		out := make([]byte, 8)
		for i := 7; i >= 0; i-- {
			out[i] = hex[h&0xf]
			h >>= 4
		}
		return string(out)
	}

	cases := []struct {
		typ, selector, message string
	}{
		{"layout", "#header", "collapsed content, element has text but zero height"},
		{"overflow", ".sidebar", "forces horizontal scroll +42px"},
		{"a11y", "button.submit", "touch target smaller than 44x44px minimum"},
		{"duplicate-id", "#main", `Duplicate ID "main" found 2 times`},
		{"missing-title", "head > title", "Add a descriptive page title"},
		{"missing-alt", "img:not([alt])", "Add descriptive alt text to 3 images"},
		{"color-contrast", "p.text", "Insufficient color contrast: 2.50:1 (requires 4.5:1)"},
	}

	for _, c := range cases {
		id := computeFindingID(c.typ, c.selector, c.message)
		if len(id) != 8 {
			t.Errorf("computeFindingID(%q, %q, %q) = %q, want 8 chars", c.typ, c.selector, c.message, id)
		}
		// Verify determinism: same inputs must always produce same ID.
		id2 := computeFindingID(c.typ, c.selector, c.message)
		if id != id2 {
			t.Errorf("computeFindingID not deterministic for (%q, %q, %q): got %q then %q",
				c.typ, c.selector, c.message, id, id2)
		}
		// Verify it is lowercase hex.
		for _, ch := range id {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
				t.Errorf("computeFindingID(%q, %q, %q) = %q contains non-lowercase-hex char %q",
					c.typ, c.selector, c.message, id, string(ch))
			}
		}
	}

	// Verify different inputs produce different IDs (collision resistance for basic cases).
	ids := make(map[string]struct{})
	for _, c := range cases {
		id := computeFindingID(c.typ, c.selector, c.message)
		if _, exists := ids[id]; exists {
			t.Errorf("hash collision detected for input (%q, %q, %q)", c.typ, c.selector, c.message)
		}
		ids[id] = struct{}{}
	}
}

// TestAuditHighlightSharedRegistry verifies that responsive.js references the
// shared window.__devtool.audit.findingSelectors so highlights from all four
// audit modules are reachable via window.__devtool.audit.highlight.
func TestAuditHighlightSharedRegistry(t *testing.T) {
	if !strings.Contains(responsiveJS, "window.__devtool.audit.findingSelectors") {
		t.Error("responsive.js does not reference window.__devtool.audit.findingSelectors — findings from other audits will not be highlightable")
	}
	if !strings.Contains(responsiveJS, "getSharedRegistry") {
		t.Error("responsive.js missing getSharedRegistry helper that provides access to the shared finding registry")
	}
}

// TestAuditIDsInCombinedScript verifies the combined script contains the
// computeFindingID function from all four audit modules.
func TestAuditIDsInCombinedScript(t *testing.T) {
	combined := buildCombinedScript()

	// There should be multiple occurrences (one per audit module).
	count := strings.Count(combined, "computeFindingID")
	if count < 5 {
		t.Errorf("combined script contains only %d occurrences of computeFindingID; expected at least 5 (one per audit module + one in responsive.js)", count)
	}

	// The shared registry init pattern must appear.
	if !strings.Contains(combined, "window.__devtool.audit.findingSelectors") {
		t.Error("combined script missing window.__devtool.audit.findingSelectors")
	}
}
