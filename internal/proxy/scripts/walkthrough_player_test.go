package scripts

import (
	"strings"
	"testing"
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
	if !strings.Contains(js, "slice(0, MAX_GESTURE_LABEL)") {
		t.Error("gesture_label must be clamped, not rejected: the public player degrades instead of throwing")
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
