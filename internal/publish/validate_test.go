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
		// NOTE: addScript with `src` used to sit here as an accepted "publish-time
		// fetch input". It is now refused at publish — see
		// TestAddScriptSrcRefusedAtPublish for why.
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

// TestAddScriptSrcRefusedAtPublish pins that an addScript op carrying `src` is
// REFUSED at publish time, with an error that names why.
//
// Before this gate, `src` was a validated input with NO consumer
// (.claude/rules/publish-security-review-lessons.md §5): nothing in the daemon
// fetches Op.Src, authoredScriptCSPHashes contributes no 'sha256-' source for
// such an op, and variant-engine.js refuses it at render time — so the op
// validated, published, reported success, and then silently never ran. The
// author got no signal at all. §6a's publish-time fetch-and-inline decision
// stands as the recorded plan; until its fetch half exists, refusing at the
// source is what makes the unimplemented state loud.
func TestAddScriptSrcRefusedAtPublish(t *testing.T) {
	set := func(op string) string {
		return `{"version":"v1","id":"s","variants":[{"id":"a","ops":[` + op + `]}]}`
	}

	// src alone: refused, and the error must be actionable — it names that
	// publish-time fetching is not implemented and points at inline `code`.
	_, err := DecodeVariantSet([]byte(set(`{"op":"addScript","src":"https://cdn.example.com/demo.js"}`)))
	if err == nil {
		t.Fatal("addScript with src must be refused at publish, got nil error")
	}
	srcMsg := err.Error()
	for _, want := range []string{"src", "not implemented", "code"} {
		if !strings.Contains(srcMsg, want) {
			t.Fatalf("src rejection must mention %q, got: %s", want, srcMsg)
		}
	}

	// src+code together stays a distinct rejection (the §6a 422): the two errors
	// must not collapse into one indistinguishable message, or an author who
	// supplied both would be told the wrong thing.
	_, bothErr := DecodeVariantSet([]byte(set(`{"op":"addScript","src":"https://cdn.example.com/demo.js","code":"x=1"}`)))
	if bothErr == nil {
		t.Fatal("addScript with both src and code must be refused, got nil error")
	}
	if bothMsg := bothErr.Error(); bothMsg == srcMsg {
		t.Fatalf("src-only and src+code rejections must be distinguishable, both: %s", bothMsg)
	} else if !strings.Contains(bothMsg, "alternatives") {
		t.Fatalf("src+code rejection must name the xor contract, got: %s", bothMsg)
	}

	// The supported form is untouched: an inline `code` body still publishes,
	// and the exact bytes survive to the revision the CSP hash is pinned from
	// (internal/proxy's TestAuthoredScriptHashPinnedInScriptSrc hashes
	// Op.Code — so preserving these bytes is what keeps that hash correct).
	const code = "document.title='demo'"
	vs, err := DecodeVariantSet([]byte(set(`{"op":"addScript","code":"` + code + `"}`)))
	if err != nil {
		t.Fatalf("inline code must still publish, got: %v", err)
	}
	op := vs.Variants[0].Ops[0]
	if op.Code != code {
		t.Fatalf("inline code body altered: got %q want %q", op.Code, code)
	}
	if op.Src != "" {
		t.Fatalf("inline-code op must carry no src, got %q", op.Src)
	}
}

// publishedWithOps builds the simplest valid published walkthrough that carries
// one variant with the given ops, so a test can flip only the op under scrutiny.
func publishedWithOps(ops ...Op) *PublishedWalkthrough {
	return &PublishedWalkthrough{
		Version: SchemaV1, ID: "wt", Title: "t",
		Steps: []Step{{ID: "s1", Title: "One", Advance: Advance{Type: "auto", MS: 1000}}},
		VariantSet: &VariantSet{
			Version: SchemaV1, ID: "vs",
			Variants: []Variant{{ID: "a", Ops: ops}},
		},
	}
}

// TestAddScriptRefusedOnPublishedWalkthrough pins the INV-14 operator decision of
// 2026-08-18 (task 01M09KYHZ0CFAX2NVGMAQJ1WFW, option (a)): a published
// walkthrough is served on the public plane AS the artifact document, always
// carrying the mandatory agnt disclosure indicator (§9b). An authored addScript
// body runs in the same realm as that indicator and — proven in real Chrome
// (task 01KYQFAZRH: id-squat, re-assert-budget exhaustion, appendChild
// monkeypatch) — can defeat the disclosure from first paint. Same-realm script
// beats same-realm script, so the module cannot be hardened against it; INV-14's
// "no publisher-reachable input can remove, hide, or blank the indicator" is
// preserved instead by refusing addScript at publish on exactly the shares that
// carry the mandatory disclosure — every published walkthrough.
//
// The refusal lives at the PublishedWalkthrough boundary, not the op or
// variant-set validator: a bare variant set is never served on the public plane,
// so DecodeVariantSet stays permissive (see the bare-set assertion below and
// TestINV6RetirementAcceptsRawContent). Deleting the guard makes the first
// assertion fail — addScript{code} publishes again — so this test has teeth.
func TestAddScriptRefusedOnPublishedWalkthrough(t *testing.T) {
	// code form: refused, and the error is actionable — it names addScript, the
	// invariant it protects, and a remedy.
	err := publishedWithOps(Op{Op: OpAddScript, Code: "document.title='x'"}).Validate()
	if err == nil {
		t.Fatal("addScript{code} must be refused on a published walkthrough (INV-14), got nil")
	}
	for _, want := range []string{"addScript", "INV-14"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("code refusal must mention %q, got: %s", want, err.Error())
		}
	}

	// src form: refused on the same badge-mandatory boundary — BOTH op forms are
	// refused, not just the inline code.
	if err := publishedWithOps(Op{Op: OpAddScript, Src: "https://cdn.example.com/a.js"}).Validate(); err == nil {
		t.Fatal("addScript{src} must be refused on a published walkthrough, got nil")
	} else if !strings.Contains(err.Error(), "addScript") {
		t.Fatalf("src refusal must name addScript, got: %s", err.Error())
	}

	// Do NOT over-reject: the CSS/DOM-removal ops on a badge-mandatory share still
	// publish. Those classes are contained by the wholesale-replaced CSP and the
	// closed shadow root (shipped e2e), not by this gate — narrowing them here
	// would be scope creep that breaks the documented raw-content contract.
	for _, op := range []Op{
		{Op: OpAddStyle, CSS: "#agnt-demo-indicator{display:none!important}"},
		{Op: OpSetText, Selector: ".x", Value: "hi"},
		{Op: OpAddClass, Selector: ".x", Value: "promo"},
		{Op: OpSetHTML, Selector: ".x", HTML: "<b>hi</b>"},
	} {
		if err := publishedWithOps(op).Validate(); err != nil {
			t.Fatalf("%s must still publish on a published walkthrough, got: %v", op.Op, err)
		}
	}

	// A published walkthrough with no variant set at all is untouched.
	noVS := &PublishedWalkthrough{
		Version: SchemaV1, ID: "wt", Title: "t",
		Steps: []Step{{ID: "s1", Title: "One", Advance: Advance{Type: "auto", MS: 1000}}},
	}
	if err := noVS.Validate(); err != nil {
		t.Fatalf("variant-set-free walkthrough must publish, got: %v", err)
	}

	// The variant-set validator itself stays permissive — the badge-mandatory
	// boundary is the PUBLISHED walkthrough, not a bare variant set (a bare set is
	// never served on the public plane). Over-rejecting here would break the
	// documented raw-content contract (TestINV6RetirementAcceptsRawContent).
	bare := `{"version":"v1","id":"s","variants":[{"id":"a","ops":[{"op":"addScript","code":"document.title='x'"}]}]}`
	if _, err := DecodeVariantSet([]byte(bare)); err != nil {
		t.Fatalf("bare variant-set addScript{code} must stay accepted, got: %v", err)
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
