package finding

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestStableIDMatchesAuditReportHash pins StableID to the exact 8-char
// FNV-1a 32-bit output produced by audit-utils.js auditComputeFindingID
// (ASCII reference vectors from internal/proxy/scripts/audit_ids_test.go,
// non-ASCII vectors produced by the JS implementation run under node).
func TestStableIDMatchesAuditReportHash(t *testing.T) {
	cases := []struct {
		kind, selector, message, want string
	}{
		{"layout", "#header", "collapsed content, element has text but zero height", "a695afde"},
		{"overflow", ".sidebar", "forces horizontal scroll +42px", "792e5d2b"},
		{"a11y", "button.submit", "touch target smaller than 44x44px minimum", "f63f2c6c"},
		{"duplicate-id", "#main", `Duplicate ID "main" found 2 times`, "2a0d3289"},
		{"missing-title", "head > title", "Add a descriptive page title", "5533505d"},
		{"missing-alt", "img:not([alt])", "Add descriptive alt text to 3 images", "cfafa2e0"},
		{"color-contrast", "p.text", "Insufficient color contrast: 2.50:1 (requires 4.5:1)", "d404f06d"},
		// Non-ASCII vectors: the JS implementation hashes UTF-16 code units
		// (charCodeAt), not UTF-8 bytes. Expected ids produced by
		// audit-utils.js auditComputeFindingID run under node.
		{"a11y", "button.日本語", "Insufficient contrast: 3.0:1", "f36f22d5"},
		{"layout", ".emoji-🎉", "zero height — ünïcödé", "4d20dea7"},
	}
	for _, c := range cases {
		got := StableID(c.kind, c.selector, c.message)
		if got != c.want {
			t.Errorf("StableID(%q, %q, %q) = %q, want %q", c.kind, c.selector, c.message, got, c.want)
		}
	}
}

// TestStableIDKindPrefixDistinguishesIncidentFromAudit verifies the kind
// prefix separates incident fingerprints from audit IDs for the same
// selector+message.
func TestStableIDKindPrefixDistinguishesIncidentFromAudit(t *testing.T) {
	inc := StableID("incident", "proxy/1", "HTTP 500")
	aud := StableID("audit", "proxy/1", "HTTP 500")
	if inc == aud {
		t.Errorf("kind prefix did not distinguish IDs: both %q", inc)
	}
	if inc != "e6cd297e" || aud != "2f95bd93" {
		t.Errorf("got incident=%q audit=%q, want e6cd297e / 2f95bd93", inc, aud)
	}
}

// TestParseProfileRejectsUnknown pins the closed profile set.
func TestParseProfileRejectsUnknown(t *testing.T) {
	for _, s := range []string{"bug", "changed", "release", "full"} {
		p, err := ParseProfile(s)
		if err != nil {
			t.Errorf("ParseProfile(%q) error: %v", s, err)
		}
		if p != Profile(s) {
			t.Errorf("ParseProfile(%q) = %q", s, p)
		}
	}
	for _, s := range []string{"", "BUG", "all", "quick", "bug "} {
		if _, err := ParseProfile(s); err == nil {
			t.Errorf("ParseProfile(%q) = nil error, want rejection naming the value", s)
		} else if got := err.Error(); !contains(got, s) && s != "" {
			t.Errorf("ParseProfile(%q) error %q does not name the value", s, got)
		}
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestNextActionJSONShapeMatchesToolSuggestion pins the JSON shape of
// NextAction to {tool, args(omitempty), rationale} — identical to
// incident.ToolSuggestion in internal/incident/remediation.go, which aliases
// this type.
func TestNextActionJSONShapeMatchesToolSuggestion(t *testing.T) {
	full := NextAction{Tool: "proxy", Args: map[string]any{"action": "exec"}, Rationale: "why"}
	b, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"args", "rationale", "tool"} // keys() returns sorted
	if !reflect.DeepEqual(keys(m), wantKeys) {
		t.Errorf("full NextAction JSON keys = %v, want %v (%s)", keys(m), wantKeys, b)
	}

	noArgs := NextAction{Tool: "proxy", Rationale: "why"}
	b, err = json.Marshal(noArgs)
	if err != nil {
		t.Fatal(err)
	}
	m = map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, hasArgs := m["args"]; hasArgs {
		t.Errorf("empty Args must be omitted, got %s", b)
	}
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// deterministic order
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
