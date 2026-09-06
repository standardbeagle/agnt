package scripts

import (
	"os"
	"strings"
	"testing"
)

// modernCSSFeatures is the set of newer CSS primitives audit-css.js knows how
// to recommend, each paired with the hand-rolled shape it replaces.
var modernCSSFeatures = []string{
	"alpha-shorthand",
	"progress-function",
	"typed-attr",
	"sibling-index",
	"text-box-trim",
	"shrink-to-fit",
}

// TestModernCSS_AdvisoryContract is the always-on source guard for the
// modern-CSS opportunity scan: every feature is declared, every finding
// carries its own support reality, and none of it touches the score.
//
// The support fields are the load-bearing part. These primitives shipped at
// different times and one of them (max-content-sizing) is not interoperable
// at all, so a recommendation without `baseline`/`fallback` would push a
// caller into writing a declaration an engine silently drops.
//
// Detection behaviour itself is asserted against a real document by the
// vitest + jsdom tier (`make test-js`, internal/proxy/scripts/jstest) — this
// guard runs with no node install and covers the contract, not the matching.
func TestModernCSS_AdvisoryContract(t *testing.T) {
	if !strings.Contains(auditCssJS, "'modern-css-opportunities'") {
		t.Error("audit-css.js must list modern-css-opportunities in checksRun")
	}
	for _, feature := range modernCSSFeatures {
		if !strings.Contains(auditCssJS, "'"+feature+"'") {
			t.Errorf("audit-css.js does not declare the %q opportunity", feature)
		}
	}
	// Every MODERN_CSS entry must carry both support fields. Checked inside
	// each entry's own braces rather than by counting occurrences file-wide,
	// which a copy in the finding builder would satisfy on its own.
	for _, feature := range modernCSSFeatures {
		entry := modernCSSEntry(t, feature)
		if !strings.Contains(entry, "baseline:") {
			t.Errorf("the %q entry has no baseline — a caller cannot tell 'ship it' from 'ship it behind @supports'", feature)
		}
		if !strings.Contains(entry, "fallback:") {
			t.Errorf("the %q entry has no fallback — an engine without the primitive would render nothing", feature)
		}
	}
	if !strings.Contains(auditCssJS, "advisory: true") {
		t.Error("modern-CSS findings must be marked advisory — they are opportunities, not defects")
	}
	// The scan walks every accessible rule; an unbounded walk on a large sheet
	// would stall the page the audit is inspecting.
	if !strings.Contains(auditCssJS, "MODERN_RULE_CAP") || !strings.Contains(auditCssJS, "modern.truncated") {
		t.Error("the modern-CSS rule walk must be capped and must report truncation instead of silently under-reporting")
	}
}

// TestModernCSS_EveryFeatureHasDOMCoverage keeps the two tiers from drifting.
// The Go guard here cannot run the detection; the vitest suite can, and a new
// entry in MODERN_CSS is worthless until something actually exercises it
// against a document. Adding a feature therefore fails this test until its
// case exists.
func TestModernCSS_EveryFeatureHasDOMCoverage(t *testing.T) {
	const suite = "jstest/audit-css-modern.test.js"
	body, err := os.ReadFile(suite)
	if err != nil {
		t.Fatalf("the DOM tier for this scan is missing (%s): %v", suite, err)
	}
	for _, feature := range modernCSSFeatures {
		if !strings.Contains(string(body), "'"+feature+"'") {
			t.Errorf("%s has no case for %q — the feature ships with no behavioural coverage", suite, feature)
		}
	}
}

// modernCSSEntry returns the source of one MODERN_CSS entry: everything from
// its key up to the closing brace of its object literal.
func modernCSSEntry(t *testing.T, feature string) string {
	t.Helper()
	start := strings.Index(auditCssJS, "'"+feature+"': {")
	if start == -1 {
		t.Fatalf("MODERN_CSS has no %q entry", feature)
	}
	end := strings.Index(auditCssJS[start:], "}")
	if end == -1 {
		t.Fatalf("the %q entry is unterminated", feature)
	}
	return auditCssJS[start : start+end]
}
