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
		{"script-tag-via-unknown-op", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setHTML","selector":".x","value":"<script>alert(1)</script>"}]}]}`},
		{"innerHTML-unknown-field", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setText","selector":".x","innerHTML":"<b>x</b>"}]}]}`},
		{"javascript-url-image", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setImageSrc","selector":"img","url":"javascript:alert(1)"}]}]}`},
		{"http-url-image", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setImageSrc","selector":"img","url":"http://ex.com/a.png"}]}]}`},
		{"data-url-image", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setImageSrc","selector":"img","url":"data:text/html,<script>"}]}]}`},
		{"file-url-image", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setImageSrc","selector":"img","url":"file:///etc/passwd"}]}]}`},
		{"blob-url-image", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"setImageSrc","selector":"img","url":"blob:https://ex.com/uuid"}]}]}`},
		{"css-url-exfil", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"background":"url(https://evil/x)"}}]}]}`},
		{"css-expression", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"width":"expression(alert(1))"}}]}]}`},
		{"css-import", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"color":"@import 'evil'"}}]}]}`},
		{"css-forbidden-property", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"position":"fixed"}}]}]}`},
		{"css-breakout-semicolon", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"color":"red;position:fixed"}}]}]}`},
		// §5b/§6 substring-scan bypasses — URL-bearing CSS function forms not named
		// `url(` that still fetch a URL when the renderer assigns el.style[prop]=val.
		{"css-image-set-string", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"background":"image-set(\"https://evil/x\" 1x)"}}]}]}`},
		{"css-webkit-image-set", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"background":"-webkit-image-set(url(\"https://evil/x\") 1x)"}}]}]}`},
		{"css-cross-fade", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"background":"cross-fade(url(https://evil/x), red)"}}]}]}`},
		// §5b/§6 CSS backslash escape — `\75` decodes to `u`, reconstituting url(…)
		// in the CSSOM after a raw-byte substring check has already passed it.
		{"css-backslash-escape", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"background":"\\75 rl(https://evil/x)"}}]}]}`},
		// §5b/§6 case + whitespace tolerance on the existing url() check.
		{"css-url-uppercase", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"background":"URL(https://evil/x)"}}]}]}`},
		{"css-url-inner-whitespace", `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"background":"url (https://evil/x)"}}]}]}`},
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
// with no forbidden token / escape must still pass §5b.
func TestAcceptsLegitStyle(t *testing.T) {
	j := `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"applyStyle","selector":".x","props":{"color":"#fff","margin":"8px"}}]}]}`
	if _, err := DecodeVariantSet([]byte(j)); err != nil {
		t.Fatalf("legit style must be accepted, got: %v", err)
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
