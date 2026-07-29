package scripts

import (
	"encoding/json"
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

// TestPlayerCapsAndClampsAreByteDenominated is the source-level guard against
// the cap-UNIT drift this player shares with variant-engine.js (closed there by
// commit 34abd80c). Every limit the player enforces is a BYTE limit on the Go
// side — internal/publish measures with len() over a UTF-8 string — while JS
// `.length` counts UTF-16 code units, which undercounts 3x for CJK and 4x for
// astral-plane characters.
//
// The two clamps are the worse shape and the reason this test exists: a
// mis-denominated CAP refuses loudly and the visitor sees it, but a
// mis-denominated TRUNCATION silently emits a value the Go validator still
// considers oversize — the player believes it clamped, Go rejects the same
// payload, and nothing in the browser explains why.
//
// Written as an exact-source assertion per site (not a bare "utf8Len appears
// somewhere") so re-introducing a code-unit comparison at ANY ONE of them fails
// while the others stay converted — the precise way this drifted the first time.
func TestPlayerCapsAndClampsAreByteDenominated(t *testing.T) {
	js := walkthroughViewerJS
	sites := []struct {
		what   string
		goRef  string
		byteEd string // the required byte-denominated form
		unitEd string // the code-unit form it replaced
	}{
		{
			what:   "selector length cap",
			goRef:  "internal/publish/selector.go:37 len(sel) > MaxSelectorLength",
			byteEd: "if (utf8Len(sel) > MAX_SELECTOR_BYTES)",
			unitEd: "sel.length > 256",
		},
		{
			what:   "title/body/wait-value clamp (TRUNCATION)",
			goRef:  "internal/publish/limits.go MaxTextBytes = 2048 bytes UTF-8",
			byteEd: "return truncateToBytes(s, MAX_TEXT_BYTES);",
			unitEd: "s.slice(0, MAX_TEXT)",
		},
		{
			what:   "gesture_label clamp (TRUNCATION)",
			goRef:  "internal/publish/validate.go:205 len(s.GestureLabel) > MaxGestureLabelLength",
			byteEd: "truncateToBytes(s.gesture_label, MAX_GESTURE_LABEL)",
			unitEd: "s.gesture_label.slice(0, MAX_GESTURE_LABEL)",
		},
	}
	for _, s := range sites {
		if !strings.Contains(js, s.byteEd) {
			t.Errorf("walkthrough-viewer.js %s must be denominated in UTF-8 bytes (%q), mirroring %s", s.what, s.byteEd, s.goRef)
		}
		if strings.Contains(js, s.unitEd) {
			t.Errorf("walkthrough-viewer.js %s uses UTF-16 code units (%q) against a byte-denominated Go limit (%s) — 3x gap for CJK, 4x for emoji", s.what, s.unitEd, s.goRef)
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

	// The unit claim must live next to the limits, because an imprecise claim is
	// exactly what let the two halves drift unnoticed.
	if !strings.Contains(js, "compared and truncated in UTF-8 BYTES via utf8Len(), matching Go's len()") {
		t.Error("walkthrough-viewer.js must state that its size limits are byte-denominated — a bare 'mirrors publish.Max…' claim with no unit is what let the mirrors drift")
	}
}

// TestPlayerByteTruncationIsClusterSafe runs the player's OWN utf8Len /
// truncateToBytes source in node against payloads that are under the limit in
// UTF-16 code units but over it in UTF-8 bytes — the exact input class the
// pre-fix `.slice(0, N)` emitted and the Go validator then rejected.
//
// Each case asserts four things, so reverting the fix fails: the result fits the
// BYTE budget, it is a prefix of the input, it contains no replacement character
// (a split surrogate pair decodes to U+FFFD), and it does not end mid-cluster
// (no dangling ZWJ / combining mark / variation selector / skin-tone modifier,
// and no odd regional indicator).
func TestPlayerByteTruncationIsClusterSafe(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("SKIPPING byte-truncation behavior tier: node is not on PATH (the source-level guard in TestPlayerCapsAndClampsAreByteDenominated still ran)")
	}

	const limit = 64
	cases := []struct {
		name  string
		input string
	}{
		{"cjk", strings.Repeat("中", 30)},                   // 30 code units, 90 bytes
		{"astral-emoji", strings.Repeat("\U0001F600", 20)}, // 40 code units, 80 bytes
		{"zwj-family", strings.Repeat("\U0001F468‍\U0001F469‍\U0001F467", 3)},
		{"regional-flag", strings.Repeat("\U0001F1EF\U0001F1F5", 10)},
		{"skin-tone", strings.Repeat("\U0001F44D\U0001F3FD", 10)},
		{"combining-mark", strings.Repeat("é", 30)},
		{"under-limit-cjk", strings.Repeat("中", 5)}, // control: 15 bytes, unchanged
	}

	script := playerTruncationDriver(t)
	in, err := json.Marshal(struct {
		Limit int      `json:"limit"`
		Cases []string `json:"cases"`
	}{Limit: limit, Cases: func() []string {
		out := make([]string, len(cases))
		for i, c := range cases {
			out[i] = c.input
		}
		return out
	}()})
	if err != nil {
		t.Fatalf("marshal cases: %v", err)
	}

	cmd := exec.Command(node, "-e", script)
	cmd.Stdin = strings.NewReader(string(in))
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node driver failed: %v\n%s", err, raw)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode node output %q: %v", raw, err)
	}
	if len(got) != len(cases) {
		t.Fatalf("node returned %d results for %d cases", len(got), len(cases))
	}

	for i, c := range cases {
		out := got[i]
		t.Run(c.name, func(t *testing.T) {
			if len(out) > limit {
				t.Errorf("truncateToBytes returned %d bytes, over the %d-byte limit: a code-unit slice of %d units would be %d bytes",
					len(out), limit, len([]rune(c.input)), len(c.input))
			}
			if !strings.HasPrefix(c.input, out) {
				t.Errorf("result %q is not a prefix of the input — truncation must not rewrite content", out)
			}
			if strings.ContainsRune(out, '�') {
				t.Errorf("result %q contains U+FFFD: a surrogate pair or multi-byte sequence was split", out)
			}
			// A complete cluster may legitimately END with an extender (👍🏽, é), so
			// the mid-cluster test is on the cut itself: the first character the
			// truncation dropped must not be one that attaches to what was kept.
			if r, _ := utf8.DecodeLastRuneInString(out); out != "" && r == zwj {
				t.Errorf("result ends with a dangling ZWJ — the cut landed inside a joined emoji sequence")
			}
			if rest := strings.TrimPrefix(c.input, out); rest != "" {
				if r, _ := utf8.DecodeRuneInString(rest); isClusterExtender(r) || r == zwj {
					t.Errorf("the cut dropped %U, which attaches to the kept prefix — the truncation landed mid-grapheme", r)
				}
			}
			if n := countRegionalIndicators(out); n%2 != 0 {
				t.Errorf("result carries %d regional indicators (odd) — a flag cluster was split in half", n)
			}
			if len(c.input) <= limit && out != c.input {
				t.Errorf("an input already inside the byte budget must pass through unchanged, got %q", out)
			}
			if len(c.input) > limit && out == c.input {
				t.Errorf("input of %d bytes was NOT truncated (%d code units is under the limit, which is the whole bug)", len(c.input), len([]rune(c.input)))
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

// playerTruncationDriver builds a node script out of the player's OWN helper
// source (extracted verbatim from walkthrough-viewer.js) plus a stdin/stdout
// harness, so the behavior tier tests the shipped code rather than a copy.
func playerTruncationDriver(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, header := range []string{
		"function utf8Len(s)",
		"function isExtender(cp)",
		"function isRegionalIndicator(cp)",
		"function truncateToBytes(s, maxBytes)",
	} {
		b.WriteString(extractJSFunc(t, walkthroughViewerJS, header))
		b.WriteString("\n")
	}
	b.WriteString(`
var input = JSON.parse(require('fs').readFileSync(0, 'utf8'));
process.stdout.write(JSON.stringify(input.cases.map(function (s) {
  return truncateToBytes(s, input.limit);
})));
`)
	return b.String()
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
