package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
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

// TestModernCSS_DetectsHandRolledShapes drives the REAL auditCSS over a
// synthetic stylesheet under node: each rule set is one hand-rolled shape a
// newer primitive replaces, so every feature must fire exactly once.
//
// The page carries no inline styles, no !important and no z-index, so its
// only findings are advisory — which makes `score == 100` a mutation guard:
// wire the advisories into the score and this assertion fails.
//
//	AGNT_JS_RUNTIME_TESTS=1 go test ./internal/proxy/scripts/ -run TestModernCSS_DetectsHandRolledShapes
func TestModernCSS_DetectsHandRolledShapes(t *testing.T) {
	if os.Getenv("AGNT_JS_RUNTIME_TESTS") == "" {
		t.Skip("SKIPPING js-runtime tier: set AGNT_JS_RUNTIME_TESTS=1 to drive audit-css.js under node (the always-on source guard in TestModernCSS_AdvisoryContract still ran)")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("AGNT_JS_RUNTIME_TESTS is set but node is not on PATH: %v", err)
	}

	cmd := exec.Command(node, "-e", modernCSSDriver())
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node driver failed: %v\n%s", err, raw)
	}
	var got struct {
		Score         int `json:"score"`
		Opportunities []struct {
			Type     string `json:"type"`
			Advisory bool   `json:"advisory"`
			Baseline string `json:"baseline"`
			Fallback string `json:"fallback"`
			Fix      string `json:"fix"`
		} `json:"opportunities"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode node output %q: %v", raw, err)
	}

	seen := map[string]int{}
	for _, o := range got.Opportunities {
		seen[o.Type]++
		if !o.Advisory {
			t.Errorf("%s finding is not marked advisory", o.Type)
		}
		if o.Baseline == "" || o.Fallback == "" || o.Fix == "" {
			t.Errorf("%s finding is missing baseline/fallback/fix: %+v", o.Type, o)
		}
	}
	for _, feature := range modernCSSFeatures {
		if seen[feature] != 1 {
			t.Errorf("%s fired %d times over a page built to trigger it once", feature, seen[feature])
		}
	}
	if got.Score != 100 {
		t.Errorf("score is %d, want 100 — a page whose only findings are advisory must not be marked down for them", got.Score)
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

// modernCSSDriver evaluates the real audit-utils.js and audit-css.js under a
// minimal window/document stub and runs auditCSS over a synthetic stylesheet.
// Nothing is reimplemented: the shapes below go through the shipped scan.
func modernCSSDriver() string {
	var b strings.Builder
	b.WriteString(`
globalThis.window = {};
window.__devtool_utils = { isDevtoolElement: function () { return false; } };
window.getComputedStyle = function () { return { zIndex: 'auto' }; };

function rule(selectorText, decls) {
  var props = Object.keys(decls);
  var style = { length: props.length, getPropertyValue: function (p) { return decls[p] || ''; } };
  props.forEach(function (p, i) { style[i] = p; });
  return { selectorText: selectorText, style: style, cssText: selectorText + ' {}' };
}

var rules = [
  // sibling-index(): one rule per position, breaks when an item is added.
  rule('.ring li:nth-child(1)', { transform: 'rotate(0deg)' }),
  rule('.ring li:nth-child(2)', { transform: 'rotate(90deg)' }),
  rule('.ring li:nth-child(3)', { transform: 'rotate(180deg)' }),
  rule('.ring li:nth-child(4)', { transform: 'rotate(270deg)' }),
  // attr(): one rule per attribute value.
  rule('.btn[data-size="sm"]', { padding: '4px' }),
  rule('.btn[data-size="md"]', { padding: '8px' }),
  rule('.btn[data-size="lg"]', { padding: '12px' }),
  // alpha(): one base color repeated per opacity level.
  rule('.card', { color: '#0b5fff' }),
  rule('.card--muted', { color: 'rgba(11, 95, 255, 0.4)' }),
  rule('.card--ghost', { color: 'rgba(11, 95, 255, 0.8)' }),
  // progress(): manual normalisation in calc().
  rule('.meter', { opacity: 'calc((var(--v) - var(--min)) / (var(--max) - var(--min)))' }),
  // text-box-trim: half-leading compensated by hand.
  rule('.chip', { 'line-height': '1.2', 'padding-top': '10px', 'padding-bottom': '8px' }),
  // shrink-to-fit: a content-sized child a wrapper has to hug.
  rule('.tag', { width: 'fit-content' })
];

globalThis.document = {
  styleSheets: [{ cssRules: rules }],
  querySelectorAll: function () { return []; }
};
`)
	b.WriteString(auditUtilsJS)
	b.WriteString("\n")
	b.WriteString(auditCssJS)
	b.WriteString(`
var result = window.__devtool_audit_css.auditCSS();
process.stdout.write(JSON.stringify({
  score: result.score,
  opportunities: result.modernCSS.opportunities
}));
`)
	return b.String()
}
