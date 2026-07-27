package publish

import (
	"strings"
	"testing"
)

// happyVariantSet is the overfit happy-path case (karpathy principle 3): the
// simplest valid set that must pass. The malicious table then flips one thing
// at a time.
func happyVariantSet() string {
	return `{
      "version":"v1","id":"set-1","variants":[
        {"id":"a","label":"A","ops":[
          {"op":"setText","selector":".title","value":"Hello"},
          {"op":"applyStyle","selector":"#box","props":{"color":"red","font-size":"12px"}},
          {"op":"setImageSrc","selector":"img.hero","url":"https://example.com/a.png"},
          {"op":"addClass","selector":"div > .child","value":"active"},
          {"op":"setAttribute","selector":"[data-x]","name":"aria-label","value":"ok"}
        ]}
      ]}`
}

func TestDecodeVariantSet_HappyPath(t *testing.T) {
	if _, err := DecodeVariantSet([]byte(happyVariantSet())); err != nil {
		t.Fatalf("happy path must validate, got: %v", err)
	}
}

// TestMaliciousFixtures is the security heart: every row must be REJECTED with a
// non-nil error. One malicious mutation per row (karpathy: one variable at a
// time).
func TestMaliciousFixtures(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"innerHTML-unknown-field", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":".x","innerHTML":"<b>x</b>"}]}]}`},
		{"javascript-url-image", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setImageSrc","selector":"img","url":"javascript:alert(1)"}]}]}`},
		{"http-url-image", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setImageSrc","selector":"img","url":"http://ex.com/a.png"}]}]}`},
		{"data-url-image", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setImageSrc","selector":"img","url":"data:text/html,<script>"}]}]}`},
		{"file-url-image", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setImageSrc","selector":"img","url":"file:///etc/passwd"}]}]}`},
		{"blob-url-image", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setImageSrc","selector":"img","url":"blob:https://ex.com/uuid"}]}]}`},
		{"onclick-attribute", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setAttribute","selector":".x","name":"onclick","value":"alert(1)"}]}]}`},
		{"href-attribute-excluded", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setAttribute","selector":"a","name":"href","value":"https://ex.com"}]}]}`},
		{"src-attribute-excluded", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setAttribute","selector":"img","name":"src","value":"https://ex.com/a.png"}]}]}`},
		{"srcdoc-attribute-excluded", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setAttribute","selector":"iframe","name":"srcdoc","value":"<script>"}]}]}`},
		{"formaction-attribute-excluded", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setAttribute","selector":"button","name":"formaction","value":"https://ex.com"}]}]}`},
		{"has-selector", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":"div:has(.x)","value":"y"}]}]}`},
		{"is-selector", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":":is(a,b)","value":"y"}]}]}`},
		{"not-selector", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":"div:not(.x)","value":"y"}]}]}`},
		{"sibling-combinator", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":"a ~ b","value":"y"}]}]}`},
		{"adjacent-sibling", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":"a + b","value":"y"}]}]}`},
		{"pseudo-element", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":"a::before","value":"y"}]}]}`},
		{"at-rule-selector", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":"@media .x","value":"y"}]}]}`},
		{"oversize-selector", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":".` + strings.Repeat("a", 300) + `","value":"y"}]}]}`},
		{"oversize-depth", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":"a b c d e f g","value":"y"}]}]}`},
		{"duplicate-variant-ids", `{"version":"v1","id":"s","variants":[{"id":"dup","ops":[{"op":"setText","selector":".x","value":"1"}]},{"id":"dup","ops":[{"op":"setText","selector":".y","value":"2"}]}]}`},
		{"too-many-variants", tooManyVariants()},
		{"unknown-version", `{"version":"v9","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":".x","value":"y"}]}]}`},
		{"unknown-top-field", `{"version":"v1","id":"s","bogus":true,"variants":[{"id":"a","ops":[{"op":"setText","selector":".x","value":"y"}]}]}`},
		{"empty-variants", `{"version":"v1","id":"s","variants":[]}`},
		{"bad-id-chars", `{"version":"v1","id":"has space","variants":[{"id":"a","ops":[{"op":"setText","selector":".x","value":"y"}]}]}`},
		{"stray-field-on-op", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":".x","value":"y","name":"class"}]}]}`},
		{"duplicate-op", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"addClass","selector":".x","value":"on"},{"op":"addClass","selector":".x","value":"on"}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeVariantSet([]byte(tc.json)); err == nil {
				t.Fatalf("expected rejection for %s, got nil error", tc.name)
			}
		})
	}
}

// TestAcceptsLegitStyle guards against over-rejection: plain declarative values
// must pass.
func TestAcceptsLegitStyle(t *testing.T) {
	j := `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"color":"#fff","margin":"8px"}}]}]}`
	if _, err := DecodeVariantSet([]byte(j)); err != nil {
		t.Fatalf("legit style must be accepted, got: %v", err)
	}
}

// TestINV6RetirementAcceptsRawContent is the flipped half of TestMaliciousFixtures.
// Every row here was REJECTED under INV-6 and must now be ACCEPTED: INV-6 was
// retired 2026-07-27 because it guarded the wrong boundary — the author of a
// variant op is the publisher, a trusted actor (spec §0 Actors), not the
// anonymous viewer. Containment of publisher-authored CSS/HTML/JS moved to the
// wholesale-replaced CSP pinned to the authored-revision script hash
// (INV-11/INV-12). §6a forbids reintroducing any of these string scans as
// "defense in depth" — they produce false rejections and add nothing CSP does
// not already guarantee. Rows that survive INV-6's retirement (https-only URLs,
// the selector grammar, the size caps, the attr allowlist) stay in
// TestMaliciousFixtures.
func TestINV6RetirementAcceptsRawContent(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		// Was "script-tag-via-unknown-op": raw markup is now a first-class op.
		{"raw-html-with-script-tag", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setHTML","selector":".x","html":"<b>hi</b><script>alert(1)</script>"}]}]}`},
		{"raw-html-inline-handler", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setHTML","selector":".x","html":"<div onclick=\"alert(1)\">x</div>"}]}]}`},
		// Was "css-url-exfil" / "css-expression" / "css-import" — the §5 forbidden
		// CSS token list is retired.
		{"css-url", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"background":"url(https://cdn.example.com/x.png)"}}]}]}`},
		{"css-expression", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"width":"expression(alert(1))"}}]}]}`},
		{"css-import", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"color":"@import 'x'"}}]}]}`},
		// Was "css-forbidden-property" — the §5b property allowlist is retired.
		{"css-any-property", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"position":"fixed","grid-template-columns":"1fr 2fr"}}]}]}`},
		// Was "css-breakout-semicolon".
		{"css-semicolon-value", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"color":"red;position:fixed"}}]}]}`},
		// Was the substring-scan bypass rows (image-set / cross-fade / backslash
		// escape / case + whitespace tolerance): there is no substring scan left to
		// bypass.
		{"css-image-set", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"background":"image-set(\"https://cdn.example.com/x\" 1x)"}}]}]}`},
		{"css-cross-fade", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"background":"cross-fade(url(https://cdn.example.com/x), red)"}}]}]}`},
		{"css-backslash-escape", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"background":"\\75 rl(https://cdn.example.com/x)"}}]}]}`},
		{"css-url-uppercase", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"background":"URL(https://cdn.example.com/x)"}}]}]}`},
		// §6a raw-content ops proper.
		{"addStyle-raw-css", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"addStyle","css":"@import url(https://cdn.example.com/t.css); .x{position:fixed}"}]}]}`},
		{"addScript-inline-code", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"addScript","code":"document.title='demo'"}]}]}`},
		{"addScript-src-fetch-input", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"addScript","src":"https://cdn.example.com/demo.js"}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeVariantSet([]byte(tc.json)); err != nil {
				t.Fatalf("INV-6 is retired: %s must be accepted, got: %v", tc.name, err)
			}
		})
	}
}

// TestRawOpGuards pins the guards that SURVIVE on the raw-content ops (§6a):
// size caps, the selector rules, and addScript's src-xor-code contract. These
// are parse/abuse bounds, not injection controls, so INV-6's retirement leaves
// them standing.
func TestRawOpGuards(t *testing.T) {
	set := func(op string) string {
		return `{"version":"v1","id":"s","variants":[{"id":"a","ops":[` + op + `]}]}`
	}
	reject := []struct {
		name string
		json string
	}{
		{"setHTML-oversize", set(`{"op":"setHTML","selector":".x","html":"` + strings.Repeat("a", MaxRawHTMLBytes+1) + `"}`)},
		{"setHTML-requires-selector", set(`{"op":"setHTML","html":"<b>x</b>"}`)},
		{"setHTML-bad-selector", set(`{"op":"setHTML","selector":"div:has(.x)","html":"<b>x</b>"}`)},
		{"setHTML-empty", set(`{"op":"setHTML","selector":".x","html":""}`)},
		{"addStyle-oversize", set(`{"op":"addStyle","css":"` + strings.Repeat("a", MaxStylePatchBytes+1) + `"}`)},
		{"addStyle-rejects-selector", set(`{"op":"addStyle","selector":".x","css":".a{color:red}"}`)},
		{"addStyle-empty", set(`{"op":"addStyle","css":""}`)},
		{"addScript-oversize", set(`{"op":"addScript","code":"` + strings.Repeat("a", MaxRawScriptBytes+1) + `"}`)},
		{"addScript-src-and-code", set(`{"op":"addScript","src":"https://cdn.example.com/a.js","code":"x=1"}`)},
		{"addScript-neither", set(`{"op":"addScript"}`)},
		{"addScript-rejects-selector", set(`{"op":"addScript","selector":".x","code":"x=1"}`)},
		// src is a publish-time fetch input under §4a/INV-13: https only.
		{"addScript-http-src", set(`{"op":"addScript","src":"http://cdn.example.com/a.js"}`)},
		{"addScript-data-src", set(`{"op":"addScript","src":"data:text/javascript,alert(1)"}`)},
		{"addScript-javascript-src", set(`{"op":"addScript","src":"javascript:alert(1)"}`)},
		// Raw content does not smuggle in stray declarative fields.
		{"setHTML-stray-value", set(`{"op":"setHTML","selector":".x","html":"<b/>","value":"y"}`)},
		{"setText-stray-html", set(`{"op":"setText","selector":".x","value":"y","html":"<b/>"}`)},
		// §5 caps the raw-script budget per authored revision, not per op: two
		// under-cap bodies that together exceed it are rejected.
		{"addScript-revision-budget", `{"version":"v1","id":"s","variants":[` +
			`{"id":"a","ops":[{"op":"addScript","code":"` + strings.Repeat("a", MaxRawScriptBytes-10) + `"}]},` +
			`{"id":"b","ops":[{"op":"addScript","code":"` + strings.Repeat("b", 20) + `"}]}]}`},
		// §5 caps the style patch per VARIANT: applyStyle + addStyle share it.
		{"style-variant-budget", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[` +
			`{"op":"addStyle","css":"` + strings.Repeat("a", MaxStylePatchBytes-10) + `"},` +
			`{"op":"applyStyle","selector":".x","props":{"color":"` + strings.Repeat("b", 20) + `"}}]}]}`},
	}
	for _, tc := range reject {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeVariantSet([]byte(tc.json)); err == nil {
				t.Fatalf("expected rejection for %s, got nil error", tc.name)
			}
		})
	}
}

// TestUpstreamValidation pins the publisher-named live origin (§4a): optional,
// nil-safe, and https-only via the one existing ValidateURL. The §4a resolved-
// address deny-list (INV-13) needs a resolver and is not this slice's job — the
// scheme gate is.
func TestUpstreamValidation(t *testing.T) {
	base := func(u *UpstreamConfig) *PublishedWalkthrough {
		return &PublishedWalkthrough{
			Version: SchemaV1, ID: "wt", Title: "t", Upstream: u,
			Steps: []Step{{ID: "s1", Title: "One", Advance: Advance{Type: "auto", MS: 1000}}},
		}
	}
	// Absent upstream is legal and must not panic.
	if err := base(nil).Validate(); err != nil {
		t.Fatalf("nil upstream must be accepted, got: %v", err)
	}
	if err := base(&UpstreamConfig{URL: "https://demo.example.com/app"}).Validate(); err != nil {
		t.Fatalf("https upstream must be accepted, got: %v", err)
	}
	for _, bad := range []string{
		"", "http://demo.example.com", "data:text/html,<script>",
		"file:///etc/passwd", "blob:https://x/y", "javascript:alert(1)",
		"ftp://demo.example.com",
	} {
		if err := base(&UpstreamConfig{URL: bad}).Validate(); err == nil {
			t.Errorf("upstream %q must be rejected (https only)", bad)
		}
	}
}

func tooManyVariants() string {
	var b strings.Builder
	b.WriteString(`{"version":"v1","id":"s","variants":[`)
	for i := 0; i < 13; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":"v`)
		b.WriteByte(byte('a' + i))
		b.WriteString(`","ops":[{"op":"setText","selector":".x","value":"y"}]}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

func TestOversizeText(t *testing.T) {
	big := strings.Repeat("x", MaxTextBytes+1)
	j := `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":".x","value":"` + big + `"}]}]}`
	if _, err := DecodeVariantSet([]byte(j)); err == nil {
		t.Fatal("oversize setText value must be rejected")
	}
}

func TestOversizeStylePatch(t *testing.T) {
	// One allowlisted property whose value blows the 4096B patch cap.
	big := strings.Repeat("a", MaxStylePatchBytes+10)
	j := `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"font-family":"` + big + `"}}]}]}`
	if _, err := DecodeVariantSet([]byte(j)); err == nil {
		t.Fatal("oversize style patch must be rejected")
	}
}

// TestStepGestureValidation pins the closed gesture vocabulary: the four
// affordances are accepted, empty means none, anything else is rejected.
func TestStepGestureValidation(t *testing.T) {
	step := func(g string) Step {
		return Step{ID: "s1", Title: "t", Target: "#el", Gesture: g, Advance: Advance{Type: "auto"}}
	}
	for _, g := range []string{"", "hover", "click", "scroll", "drag"} {
		s := step(g)
		if err := s.Validate(); err != nil {
			t.Errorf("gesture %q must be accepted, got: %v", g, err)
		}
	}
	for _, g := range []string{"swipe", "tap", "CLICK", "hover "} {
		s := step(g)
		if err := s.Validate(); err == nil {
			t.Errorf("gesture %q must be rejected", g)
		}
	}
	// A gesture without a target has nothing to anchor to.
	s := Step{ID: "s1", Title: "t", Gesture: "click", Advance: Advance{Type: "auto"}}
	if err := s.Validate(); err == nil {
		t.Error("gesture without a target must be rejected")
	}
}

// TestStepGestureLabelValidation pins the author-supplied affordance label: it
// is optional, bounded, control-char-free, and meaningless without a gesture to
// label. Length is capped because the label renders as one nowrap pill.
func TestStepGestureLabelValidation(t *testing.T) {
	step := func(label string) Step {
		return Step{ID: "s1", Title: "t", Target: "#el", Gesture: "click", GestureLabel: label, Advance: Advance{Type: "auto"}}
	}
	for _, l := range []string{"", "Click to open your cart", strings.Repeat("x", MaxGestureLabelLength)} {
		s := step(l)
		if err := s.Validate(); err != nil {
			t.Errorf("gesture_label %q must be accepted, got: %v", l, err)
		}
	}
	long := step(strings.Repeat("x", MaxGestureLabelLength+1))
	if err := long.Validate(); err == nil {
		t.Errorf("gesture_label over %d bytes must be rejected", MaxGestureLabelLength)
	}
	for _, l := range []string{"Click\nhere", "Click\x00here", "Click\there"} {
		s := step(l)
		if err := s.Validate(); err == nil {
			t.Errorf("gesture_label %q with a control character must be rejected", l)
		}
	}
	// A label with no gesture has no affordance to attach to.
	s := Step{ID: "s1", Title: "t", Target: "#el", GestureLabel: "Click here", Advance: Advance{Type: "auto"}}
	if err := s.Validate(); err == nil {
		t.Error("gesture_label without a gesture must be rejected")
	}
}

// TestValidSelectors / TestInvalidSelectors pin the §5a grammar directly.
func TestValidSelectors(t *testing.T) {
	ok := []string{
		"div", "*", ".cls", "#id", "div.cls#id",
		"[data-x]", `[data-x="v"]`, `[data-x^="v"]`, `[data-x$="v"]`, `[data-x*="v"]`,
		"ul li", "ul > li", "div > .a .b",
		"li:first-child", "li:last-child", "li:nth-child(3)", "li:nth-child(-1)",
		"a b c d e f", // 6 compounds == MaxSelectorDepth
	}
	for _, s := range ok {
		if err := ValidateSelector(s); err != nil {
			t.Errorf("expected %q valid, got %v", s, err)
		}
	}
}

func TestInvalidSelectors(t *testing.T) {
	bad := []string{
		"", "a ~ b", "a + b", "a::before", "div:has(.x)", ":is(a)", "div:not(.x)",
		"a|b", "a b c d e f g", ">div", "div>", "a >> b", "/* c */div",
		"@media a", "div:nth-child(2n)", `[data-x=unquoted]`, "div:hover",
		".", "#", "div..a", "[bad<>]",
	}
	for _, s := range bad {
		if err := ValidateSelector(s); err == nil {
			t.Errorf("expected %q invalid, got nil", s)
		}
	}
}
