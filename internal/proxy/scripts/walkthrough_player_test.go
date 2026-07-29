package scripts

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"
)

// Pure-logic (no-browser) gates for the P5 public walkthrough PLAYER
// (walkthrough-viewer.js). These pin the invariants that do NOT need a real DOM:
// the shared-normalization default, the accessibility scaffolding, the absence
// of code/markup/navigation sinks, and the public-surface shape. The DOM
// behaviors (variant-before-step-0, advancement, remount, teardown residue,
// keyboard/focus, reduced-motion, invalid-target) are covered by the chromedp
// e2e in internal/proxy/walkthrough_player_e2e_test.go.

// TestPlayerSharedNormalizationDefault asserts the player uses the P2 shared
// default auto dwell (5000ms, matching internal/publish/normalize.go
// defaultAutoMS and walkthrough.js), NOT the P4 stub's forked 4000. This proves
// the player consumes the shared normalization semantics rather than a fork.
func TestPlayerSharedNormalizationDefault(t *testing.T) {
	js := walkthroughViewerJS
	if !strings.Contains(js, "DEFAULT_AUTO_MS = 5000") {
		t.Error("player must default auto dwell to 5000ms (shared P2/walkthrough.js semantics), not the P4 stub's 4000")
	}
	if strings.Contains(js, "4000") {
		t.Error("player still references the forked 4000ms default")
	}
	// The three shared advance types must all be represented.
	for _, tok := range []string{"'auto'", "'click-target'", "'wait'"} {
		if !strings.Contains(js, tok) {
			t.Errorf("player missing shared advance type %s", tok)
		}
	}
	// The wait grammar must mirror P2 (schema.go validWaitWhen).
	for _, tok := range []string{"url-contains", "element-present", "element-visible"} {
		if !strings.Contains(js, tok) {
			t.Errorf("player missing shared wait condition %q", tok)
		}
	}
}

// TestPlayerAccessibilityScaffolding pins the a11y contract that is core
// acceptance: dialog role + modal, a polite live region, focus management, a
// keyboard map, and reduced-motion handling.
func TestPlayerAccessibilityScaffolding(t *testing.T) {
	js := walkthroughViewerJS
	wants := map[string]string{
		"'role', 'dialog'":       "card must be a dialog",
		"'aria-modal', 'true'":   "dialog must be modal",
		"'aria-live', 'polite'":  "step changes must be announced via a polite live region",
		"prefers-reduced-motion": "reduced motion must be honored",
		"'Escape'":               "Escape must close",
		"'ArrowRight'":           "arrow keys must advance",
		"'ArrowLeft'":            "arrow keys must step back",
		"'Tab'":                  "Tab must be trapped within the card",
		"card.focus":             "focus must move to the card on show",
		"lastFocused":            "focus must be restored on close",
	}
	for tok, why := range wants {
		if !strings.Contains(js, tok) {
			t.Errorf("player missing a11y feature (%s): expected token %q", why, tok)
		}
	}
}

// TestPlayerNoCodeOrNavigationSinks proves the player never introduces a
// code/markup/navigation sink from author-supplied strings: no innerHTML, no
// outerHTML write, no href/src follow, no location assignment, no el.click().
// (The forbidden-symbol scan in role_public_test.go already bans eval/WebSocket/
// __devtool at the bundle level; this narrows to the player's own surface.)
func TestPlayerNoCodeOrNavigationSinks(t *testing.T) {
	js := walkthroughViewerJS
	forbidden := []string{
		".innerHTML",
		".outerHTML =",
		"location.href =",
		"location.assign",
		"location.replace",
		"window.open",
		".click()",
		"setAttribute('href'",
		"setAttribute('src'",
		"document.write",
	}
	for _, tok := range forbidden {
		if strings.Contains(js, tok) {
			t.Errorf("player contains a navigation/code sink %q — the public plane must not execute or navigate from author strings", tok)
		}
	}
	// Narration/title/body must be written with textContent only.
	if !strings.Contains(js, "textContent") {
		t.Error("player must render narration with textContent")
	}
}

// TestPlayerPublicSurface pins the player's public API shape and the teardown
// observability hooks the e2e residue test relies on.
func TestPlayerPublicSurface(t *testing.T) {
	js := walkthroughViewerJS
	for _, tok := range []string{
		"window.__walkthroughViewer",
		"trackedTimerCount",
		"trackedListenerCount",
		"activeVariant",
		"function start",
		"function destroy",
	} {
		if !strings.Contains(js, tok) {
			t.Errorf("player missing public-surface member %q", tok)
		}
	}
	// The module MUST keep the leading-space IIFE so wrapModule leaves it
	// isolated in the combined bundle (same trick as variant-engine).
	if !strings.HasPrefix(strings.TrimSpace(headerless(js)), "(function () {") {
		t.Error("player must keep the leading-space IIFE '(function () {' so wrapModule does not merge it into the shared scope")
	}
}

// TestPlayerGestureAffordances pins the gesture affordance contract: the closed
// vocabulary, the dedicated affordance element, WAAPI motion (no style element
// or code-eval sink), reduced-motion degradation, and auto-dismiss with the
// highlight.
func TestPlayerGestureAffordances(t *testing.T) {
	js := walkthroughViewerJS
	for _, tok := range []string{"'hover'", "'click'", "'scroll'", "'drag'"} {
		if !strings.Contains(js, tok) {
			t.Errorf("player missing gesture %s", tok)
		}
	}
	if !strings.Contains(js, "__wt_player_gesture") {
		t.Error("player must render the gesture affordance in a dedicated __wt_player_gesture element")
	}
	if !strings.Contains(js, ".animate(") {
		t.Error("gesture motion must use the Web Animations API (CSP-safe: no style element, no code-eval sink)")
	}
	if !strings.Contains(js, "function removeGesture") {
		t.Error("gesture affordance must be removed (auto-dismissed) with the highlight")
	}
	if !strings.Contains(js, "gestureAnims[i].cancel()") {
		t.Error("gesture animations must be cancelled on teardown (no orphaned WAAPI animations)")
	}
}

// TestPlayerGestureLabel pins the author-supplied affordance label: the player
// reads gesture_label, clamps it (degrade, never throw — the public plane must
// not crash a visitor's page), writes it with textContent only, and keys the
// affordance cache on the label so two consecutive steps sharing a gesture do
// not reuse the earlier step's label. Both label caps must agree with the Go
// validator (publish.MaxGestureLabelLength).
func TestPlayerGestureLabel(t *testing.T) {
	js := walkthroughViewerJS
	if !strings.Contains(js, "s.gesture_label") {
		t.Error("player must read the step's gesture_label")
	}
	if !strings.Contains(js, "MAX_GESTURE_LABEL = 64") {
		t.Error("player must clamp gesture_label at 64 chars (mirrors publish.MaxGestureLabelLength)")
	}
	if !strings.Contains(js, "truncateToBytes(s.gesture_label, MAX_GESTURE_LABEL)") {
		t.Error("gesture_label must be clamped (in UTF-8 bytes), not rejected: the public player degrades instead of throwing")
	}
	if !strings.Contains(js, "lbl.textContent = gestureLabel") {
		t.Error("gesture_label must be written with textContent (never parsed as markup)")
	}
	if !strings.Contains(js, "renderedGesture !== key") {
		t.Error("affordance cache must key on gesture+label, else a same-gesture step keeps the stale label")
	}
	// The live walkthrough must agree on both the cap and the fallback phrases,
	// so a script authored against one plane reads identically on the other.
	live := walkthroughJS
	if !strings.Contains(live, "MAX_GESTURE_LABEL = 64") {
		t.Error("live walkthrough must use the same 64-char gesture_label cap")
	}
	for _, phrase := range []string{"'Hover here'", "'Click here'", "'Scroll this area'", "'Drag to move'"} {
		if !strings.Contains(js, phrase) {
			t.Errorf("player missing fallback label %s", phrase)
		}
		if !strings.Contains(live, phrase) {
			t.Errorf("live walkthrough missing fallback label %s", phrase)
		}
	}
}

// TestPlayerReadThroughReveal pins the narration read-through contract: a
// tracked interval reveals the body character-by-character, reduced motion
// shows full text instantly, and the interval self-removes on completion so
// trackedTimerCount stays exact.
func TestPlayerReadThroughReveal(t *testing.T) {
	js := walkthroughViewerJS
	if !strings.Contains(js, "function revealBody") {
		t.Error("player must reveal the narration body with a read-through animation (revealBody)")
	}
	if !strings.Contains(js, "intervals.push(id)") {
		t.Error("the reveal interval must be tracked so destroy()/disarm() kill it (teardown gate)")
	}
	if !strings.Contains(js, "intervals.splice(k, 1)") {
		t.Error("the reveal interval must self-remove on completion so trackedTimerCount stays exact")
	}
}

// TestPlayerCapsAndClampsAreByteDenominated is the source-level guard on the
// player's rendering ceilings. Two ways to get one wrong, and this file has held
// both:
//
//	too LOOSE  — comparing JS `.length` (UTF-16 code units) against a limit Go
//	             expresses in bytes admits up to 4x the intended bound;
//	too TIGHT  — a byte ceiling borrowed from a DIFFERENT field's Go limit cuts
//	             text the validator accepted (a Go-accepted 3000-byte CJK body is
//	             only 1000 code units), i.e. silent visitor-facing content loss.
//
// So each site is pinned twice: byte-denominated, and bound to the constant
// carrying ITS OWN field's Go limit. The player is a CONSUMER of an
// already-validated payload — it submits nothing, so no ceiling here can trigger
// a Go rejection; the requirement is only that it never alter what Go accepted.
//
// Written as an exact-source assertion per site (not a bare "utf8Len appears
// somewhere") so re-introducing a code-unit comparison, or re-pointing one field
// at another field's limit, fails at that ONE site while the others stay correct.
func TestPlayerCapsAndClampsAreByteDenominated(t *testing.T) {
	js := walkthroughViewerJS
	sites := []struct {
		what   string
		goRef  string
		byteEd string // the required byte-denominated, per-field form
		unitEd string // a code-unit or wrong-limit form that must NOT be present
	}{
		{
			what:   "target selector cap",
			goRef:  "internal/publish/selector.go:37 len(sel) > MaxSelectorLength (256)",
			byteEd: "if (utf8Len(sel) > MAX_SELECTOR_BYTES)",
			unitEd: "sel.length > 256",
		},
		{
			what:   "title clamp",
			goRef:  "internal/publish/validate.go:181 len(s.Title) > MaxTitleLength (256)",
			byteEd: "title: clampText(s.title || '', MAX_TITLE_BYTES)",
			unitEd: "title: clampText(s.title || '')",
		},
		{
			what:   "body clamp",
			goRef:  "internal/publish/validate.go:184 len(s.Body) > MaxBodyLength (4096)",
			byteEd: "MAX_BODY_BYTES)",
			unitEd: "MAX_TEXT_BYTES",
		},
		{
			what:   "advance.value clamp",
			goRef:  "internal/publish/validate.go:159 len(a.Value) > MaxURLLength (2048)",
			byteEd: "value: clampText(adv.value || '', MAX_WAIT_VALUE_BYTES)",
			unitEd: "value: clampText(adv.value || '')",
		},
		{
			what:   "gesture_label clamp",
			goRef:  "internal/publish/validate.go:205 len(s.GestureLabel) > MaxGestureLabelLength (64)",
			byteEd: "truncateToBytes(s.gesture_label, MAX_GESTURE_LABEL)",
			unitEd: "s.gesture_label.slice(0, MAX_GESTURE_LABEL)",
		},
	}
	for _, s := range sites {
		if !strings.Contains(js, s.byteEd) {
			t.Errorf("walkthrough-viewer.js %s must be byte-denominated and bound to its own field limit (%q), matching what %s ACCEPTS", s.what, s.byteEd, s.goRef)
		}
		if strings.Contains(js, s.unitEd) {
			t.Errorf("walkthrough-viewer.js %s still carries %q — either UTF-16 code units or another field's limit; %s is the limit that applies here", s.what, s.unitEd, s.goRef)
		}
	}

	// Each field's ceiling constant must carry the Go value for THAT field. A
	// single shared text constant is the regression this pins against.
	for _, want := range []string{
		"var MAX_SELECTOR_BYTES = 256;",
		"var MAX_TITLE_BYTES = 256;",
		"var MAX_BODY_BYTES = 4096;",
		"var MAX_WAIT_VALUE_BYTES = 2048;",
		"var MAX_GESTURE_LABEL = 64;",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("walkthrough-viewer.js must declare %q — one ceiling per field, each at its own publish limit", want)
		}
	}

	// The helper must be the SAME spelling variant-engine.js uses, so the two
	// mirrors cannot drift into two different notions of "length".
	for _, tok := range []string{
		"function utf8Len(s)",
		"new TextEncoder().encode(s).length",
		"function truncateToBytes(s, maxBytes)",
	} {
		if !strings.Contains(js, tok) {
			t.Errorf("walkthrough-viewer.js must carry %q (the variant-engine.js spelling of the byte-length/byte-truncation helpers)", tok)
		}
	}

	// A truncation that splits a character is not a fix: pin the boundary-safe
	// iteration so a future edit cannot quietly go back to slicing code units.
	for _, tok := range []string{"codePointAt", "fromCodePoint"} {
		if !strings.Contains(js, tok) {
			t.Errorf("truncateToBytes must walk whole code points (%q) so it never splits a surrogate pair", tok)
		}
	}

	// The unit claim must live next to the ceilings, because an imprecise claim is
	// exactly what let these drift unnoticed.
	if !strings.Contains(js, "compared/truncated in UTF-8 BYTES via utf8Len()") {
		t.Error("walkthrough-viewer.js must state that its ceilings are byte-denominated — a bare 'mirrors publish.Max…' claim with no unit is what let them drift")
	}
	// ...and it must not claim a round-trip this file does not have: the player
	// consumes an already-validated payload and submits nothing, so it can never
	// cause a Go rejection. (Same correction as commit 35291eae, which replaced a
	// false "byte-for-byte mirror" claim with the real scope.)
	if !strings.Contains(js, "This file is a CONSUMER") {
		t.Error("the ceilings block must state the player is a consumer of an already-validated payload, so the ceilings are a render bound rather than a mirror of a rejection this code can trigger")
	}
	for _, falseClaim := range []string{
		"rejected in Go's",
		"Go still\n    // considers oversize",
		"validator rejects, and the two clamps",
	} {
		if strings.Contains(js, falseClaim) {
			t.Errorf("walkthrough-viewer.js claims %q — this player never submits to the Go validator, so it cannot cause a rejection; state the consumer direction instead", falseClaim)
		}
	}
}

// TestPlayerByteTruncationIsClusterSafe drives the player's OWN normalizeStep
// source under node — ceilings, helpers and all — so every case goes through the
// real field-to-ceiling wiring. It covers what source text cannot: the per-field
// ceiling VALUES, that a payload Go ACCEPTS renders unaltered, and that an
// oversize one is cut on a grapheme boundary.
//
// Executing shipped JS from a Go test has no in-repo precedent (the other tests in
// this package assert on source text; the established home for running this script
// is the chromee2e tier in internal/proxy/walkthrough_player_e2e_test.go), so this
// tier is OFF by default and never lets the default suite's greenness depend on a
// node install:
//
//	AGNT_JS_RUNTIME_TESTS=1 go test ./internal/proxy/scripts/ -run TestPlayerByteTruncation
//
// A build tag would match the chromee2e precedent more closely, but a tag applies
// to a whole file and this task's declared scope is two files, so the gate is an
// explicit opt-in env var instead; see the scope note returned with this change.
//
// Per case it asserts: the result fits THAT FIELD's byte ceiling, is a prefix of
// the input, carries no U+FFFD (a split surrogate pair decodes to one), does not
// end mid-cluster (no dangling ZWJ, no odd regional indicator, and the first
// dropped character never attaches to the kept prefix), is unchanged when the
// input already fits, and is cut when it does not.
func TestPlayerByteTruncationIsClusterSafe(t *testing.T) {
	if os.Getenv("AGNT_JS_RUNTIME_TESTS") == "" {
		t.Skip("SKIPPING js-runtime tier: set AGNT_JS_RUNTIME_TESTS=1 to run the player's own ceiling/truncation source under node (the always-on source guard in TestPlayerCapsAndClampsAreByteDenominated still ran)")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("AGNT_JS_RUNTIME_TESTS is set but node is not on PATH: %v", err)
	}

	// field is the step field fed to normalizeStep; limit is the ceiling constant
	// the player must apply to it.
	const (
		titleLim = "MAX_TITLE_BYTES"
		bodyLim  = "MAX_BODY_BYTES"
		waitLim  = "MAX_WAIT_VALUE_BYTES"
		labelLim = "MAX_GESTURE_LABEL"
	)
	limitOf := map[string]string{
		"title":         titleLim,
		"body":          bodyLim,
		"value":         waitLim,
		"gesture_label": labelLim,
	}
	cases := []struct {
		name  string
		field string
		input string
	}{
		// The regression this tier exists for: a body Go ACCEPTS (3000 bytes, under
		// MaxBodyLength 4096) must render in full. It is only 1000 code units, so the
		// old code-unit ceiling passed it too; clamping it at the setText limit (2048)
		// instead of the body limit would silently cut a third of it.
		{"body-cjk-3000-bytes-go-accepts", "body", strings.Repeat("中", 1000)},
		{"body-cjk-over-its-own-limit", "body", strings.Repeat("中", 2000)},
		{"title-cjk-over-256-bytes", "title", strings.Repeat("中", 100)},
		{"title-cjk-inside-256-bytes", "title", strings.Repeat("中", 80)},
		{"wait-value-cjk-over-2048-bytes", "value", strings.Repeat("中", 700)},
		{"wait-value-cjk-inside-2048-bytes", "value", strings.Repeat("中", 600)},
		// Cluster safety, all at the 64-byte label ceiling: every input here is inside
		// the ceiling in code units and over it in bytes.
		{"label-cjk", "gesture_label", strings.Repeat("中", 30)},                   // 30 units, 90 bytes
		{"label-astral-emoji", "gesture_label", strings.Repeat("\U0001F600", 20)}, // 40 units, 80 bytes
		{"label-zwj-family", "gesture_label", strings.Repeat("\U0001F468‍\U0001F469‍\U0001F467", 3)},
		{"label-regional-flag", "gesture_label", strings.Repeat("\U0001F1EF\U0001F1F5", 10)},
		{"label-skin-tone", "gesture_label", strings.Repeat("\U0001F44D\U0001F3FD", 10)},
		{"label-combining-mark", "gesture_label", strings.Repeat("é", 30)},
		{"label-inside-ceiling", "gesture_label", strings.Repeat("中", 5)}, // control: 15 bytes
	}

	type driverCase struct {
		S     string `json:"s"`
		Field string `json:"field"`
	}
	payload := make([]driverCase, len(cases))
	for i, c := range cases {
		payload[i] = driverCase{S: c.input, Field: c.field}
	}
	in, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal cases: %v", err)
	}

	cmd := exec.Command(node, "-e", playerTruncationDriver(t))
	cmd.Stdin = strings.NewReader(string(in))
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node driver failed: %v\n%s", err, raw)
	}
	var got struct {
		Limits  map[string]int `json:"limits"`
		Results []string       `json:"results"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode node output %q: %v", raw, err)
	}
	if len(got.Results) != len(cases) {
		t.Fatalf("node returned %d results for %d cases", len(got.Results), len(cases))
	}

	// Each ceiling must carry the value publish applies to THAT field: one shared
	// constant, or a field pointed at another field's limit, is the tight-ceiling
	// bug that cuts content Go accepted.
	for name, want := range map[string]int{
		titleLim: 256,  // validate.go:181 MaxTitleLength
		bodyLim:  4096, // validate.go:184 MaxBodyLength
		waitLim:  2048, // validate.go:159 MaxURLLength
		labelLim: 64,   // validate.go:205 MaxGestureLabelLength
	} {
		if got.Limits[name] != want {
			t.Errorf("%s is %d, want %d — the ceiling must be the Go limit for that field, else a payload Go accepted is silently cut", name, got.Limits[name], want)
		}
	}

	for i, c := range cases {
		out := got.Results[i]
		limitName := limitOf[c.field]
		ceiling := got.Limits[limitName]
		t.Run(c.name, func(t *testing.T) {
			if len(out) > ceiling {
				t.Errorf("normalizeStep emitted %d bytes, over the %d-byte %s ceiling (input: %d units / %d bytes)",
					len(out), ceiling, limitName, len([]rune(c.input)), len(c.input))
			}
			if !strings.HasPrefix(c.input, out) {
				t.Error("result is not a prefix of the input — a clamp must not rewrite content")
			}
			if strings.ContainsRune(out, utf8.RuneError) {
				t.Error("result contains U+FFFD: a surrogate pair or multi-byte sequence was split")
			}
			// A complete cluster may legitimately END with an extender (a skin-tone
			// thumb, a combining accent), so the mid-cluster test is on the cut
			// itself: the first dropped character must not attach to what was kept.
			if r, _ := utf8.DecodeLastRuneInString(out); out != "" && r == zwj {
				t.Error("result ends with a dangling ZWJ — the cut landed inside a joined emoji sequence")
			}
			if rest := strings.TrimPrefix(c.input, out); rest != "" {
				if r, _ := utf8.DecodeRuneInString(rest); isClusterExtender(r) || r == zwj {
					t.Errorf("the cut dropped %U, which attaches to the kept prefix — the truncation landed mid-grapheme", r)
				}
			}
			if n := countRegionalIndicators(out); n%2 != 0 {
				t.Errorf("result carries %d regional indicators (odd) — a flag cluster was split in half", n)
			}
			if len(c.input) <= ceiling && out != c.input {
				t.Errorf("input of %d bytes is inside the %d-byte %s ceiling, so Go accepted it and it must render UNALTERED — got %d bytes",
					len(c.input), ceiling, limitName, len(out))
			}
			if len(c.input) > ceiling && out == c.input {
				t.Errorf("input of %d bytes exceeds the %d-byte %s ceiling but was not clamped (%d code units is inside it, which is the code-unit bug)",
					len(c.input), ceiling, limitName, len([]rune(c.input)))
			}
		})
	}
}

// zwj is the zero-width joiner: it binds the code points on both sides of it
// into one grapheme cluster, so a cut on either side of it is a mid-cluster cut.
const zwj = rune(0x200D)

// isClusterExtender mirrors the JS isExtender set: characters that must never be
// separated from the base character they attach to.
func isClusterExtender(r rune) bool {
	switch {
	case r == 0xFE0E, r == 0xFE0F:
		return true
	case r >= 0x0300 && r <= 0x036F:
		return true
	case r >= 0x1F3FB && r <= 0x1F3FF:
		return true
	}
	return false
}

func countRegionalIndicators(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x1F1E6 && r <= 0x1F1FF {
			n++
		}
	}
	return n
}

// playerTruncationDriver builds a node script out of the player's OWN source —
// the ceiling declarations, the byte helpers, clampText, isSafeSelector and
// normalizeStep, all extracted verbatim from walkthrough-viewer.js — plus a
// stdin/stdout harness. Driving normalizeStep rather than clampText directly is
// deliberate: the field-to-ceiling WIRING is half of what can regress here, and a
// helper-only driver would pass with body pointed at the wrong field's limit.
func playerTruncationDriver(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	// The declarations come from the shipped source too, so the values under
	// assertion are the ones the player actually renders with.
	for _, decl := range []string{
		"var DEFAULT_AUTO_MS",
		"var MAX_AUTO_MS",
		"var MAX_SELECTOR_BYTES",
		"var MAX_TITLE_BYTES",
		"var MAX_BODY_BYTES",
		"var MAX_WAIT_VALUE_BYTES",
		"var MAX_GESTURE_LABEL",
		"var SAFE_SELECTOR_RE",
	} {
		b.WriteString(extractJSLine(t, walkthroughViewerJS, decl))
		b.WriteString("\n")
	}
	for _, header := range []string{
		"function utf8Len(s)",
		"function isExtender(cp)",
		"function isRegionalIndicator(cp)",
		"function truncateToBytes(s, maxBytes)",
		"function clampText(s, maxBytes)",
		"function isSafeSelector(sel)",
		"function normalizeStep(s)",
	} {
		b.WriteString(extractJSFunc(t, walkthroughViewerJS, header))
		b.WriteString("\n")
	}
	// `window` stands in for the browser global isSafeSelector probes for the
	// shared variant-engine grammar; absent it, the local regex path runs.
	// Naming each constant in LIMITS means a rename or removal is a loud node
	// ReferenceError, not a silently-skipped assertion.
	b.WriteString(`
var window = {};
var LIMITS = {
  MAX_TITLE_BYTES: MAX_TITLE_BYTES,
  MAX_BODY_BYTES: MAX_BODY_BYTES,
  MAX_WAIT_VALUE_BYTES: MAX_WAIT_VALUE_BYTES,
  MAX_GESTURE_LABEL: MAX_GESTURE_LABEL
};
// Each field is fed to normalizeStep in a step shaped so that field survives
// normalization, and the corresponding normalized field is read back.
function renderField(field, s) {
  switch (field) {
    case 'title':
      return normalizeStep({ title: s, body: 'x' }).title;
    case 'body':
      return normalizeStep({ title: 't', body: s }).body;
    case 'value':
      return normalizeStep({ title: 't', advance: { type: 'wait', when: 'url-contains', value: s } }).advance.value;
    case 'gesture_label':
      return normalizeStep({ title: 't', target: '#a', gesture: 'click', gesture_label: s }).gestureLabel;
  }
  throw new Error('unknown field ' + field);
}
var cases = JSON.parse(require('fs').readFileSync(0, 'utf8'));
process.stdout.write(JSON.stringify({
  limits: LIMITS,
  results: cases.map(function (c) { return renderField(c.field, c.s); })
}));
`)
	return b.String()
}

// extractJSLine pulls the single source line declaring a constant, so the driver
// runs with the shipped VALUE rather than one restated in the test.
func extractJSLine(t *testing.T, js, prefix string) string {
	t.Helper()
	for _, ln := range strings.Split(js, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), prefix) {
			return strings.TrimSpace(ln)
		}
	}
	t.Fatalf("walkthrough-viewer.js is missing a %q declaration — every clamped field needs its own ceiling constant", prefix)
	return ""
}

// extractJSFunc pulls one function's source out of a JS file by brace matching.
// A missing function is a hard failure: the helpers it names ARE the fix.
func extractJSFunc(t *testing.T, js, header string) string {
	t.Helper()
	start := strings.Index(js, header)
	if start < 0 {
		t.Fatalf("walkthrough-viewer.js is missing %q — the byte-denominated truncation helpers are the fix for the code-unit drift", header)
	}
	depth := 0
	for i := start; i < len(js); i++ {
		switch js[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return js[start : i+1]
			}
		}
	}
	t.Fatalf("unbalanced braces extracting %q", header)
	return ""
}

// headerless strips the leading line-comment header so the IIFE-prefix check
// inspects the actual code start.
func headerless(js string) string {
	lines := strings.Split(js, "\n")
	var b strings.Builder
	skipping := true
	for _, ln := range lines {
		if skipping && (strings.HasPrefix(strings.TrimSpace(ln), "//") || strings.TrimSpace(ln) == "") {
			continue
		}
		skipping = false
		b.WriteString(ln)
		b.WriteString("\n")
	}
	return b.String()
}
