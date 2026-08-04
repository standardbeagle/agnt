package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAutomationEvalScriptDefaultsToContentFrame pins the default that removes
// the hoop: a caller's script reaches the APP without writing the
// __devtool_content_frame hop itself. The proxy always-wraps a top-level
// navigation, so a bare evaluate would otherwise land in the chrome shell and
// return empty results that read like a broken selector.
func TestAutomationEvalScriptDefaultsToContentFrame(t *testing.T) {
	for _, frame := range []string{"", "content"} {
		got, err := automationEvalScript("document.title", frame)
		if err != nil {
			t.Fatalf("frame %q: unexpected error: %v", frame, err)
		}
		if !strings.Contains(got, "__devtool_content_frame") {
			t.Errorf("frame %q must reach through the content frame, got: %s", frame, got)
		}
		// Indirect eval through the iframe's own window runs the script in THAT
		// realm's global scope, so the caller's window/document are the app's.
		if !strings.Contains(got, "w.eval(") {
			t.Errorf("frame %q must hand the script to the content realm, got: %s", frame, got)
		}
	}
}

// TestAutomationEvalScriptContentNeverSilentlyRunsInShell pins the repair of
// the wrong-frame default: inside a wrapped shell (window.__devtool_role ===
// "chrome") a missing or not-yet-loaded app frame must WAIT and then fail loud
// — never fall back to evaluating in the shell, which is the silent failure
// that read as "my evaluate ran in the outer frame, not the page". The bare
// window fallback is reserved for a genuinely unwrapped page.
func TestAutomationEvalScriptContentNeverSilentlyRunsInShell(t *testing.T) {
	got, err := automationEvalScript("document.title", "content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The unwrapped-page fallback must be gated on the shell role marker, not
	// on whether the iframe lookup happened to hit.
	if !strings.Contains(got, "window.__devtool_role==='chrome'?null:window") {
		t.Errorf("window fallback must be gated on the shell role marker, got: %s", got)
	}
	// A wrapped shell whose app frame never becomes ready must throw, and the
	// error must name the escape hatch rather than leave the caller guessing.
	if !strings.Contains(got, "throw new Error(") ||
		!strings.Contains(got, "content frame not ready") {
		t.Errorf("timeout must fail loud, got: %s", got)
	}
	if !strings.Contains(got, `frame:\"top\"`) {
		t.Errorf("timeout error must name the frame:\"top\" escape hatch, got: %s", got)
	}
	// Readiness means the app realm, not the transient about:blank one: either
	// the injected content role, or a fully loaded non-blank document.
	if !strings.Contains(got, "__devtool_role==='content'") ||
		!strings.Contains(got, "about:blank") {
		t.Errorf("readiness gate must accept the app realm only, got: %s", got)
	}
	// Bounded wait: the wrapper polls against a deadline instead of failing on
	// first miss (an evaluate issued right after navigate is a normal call).
	if !strings.Contains(got, "deadline") || !strings.Contains(got, "setTimeout") {
		t.Errorf("wrapper must wait for the frame before giving up, got: %s", got)
	}
}

// TestAutomationEvalScriptTopIsVerbatim pins the escape hatch: inspecting the
// proxy chrome shell (overlay, indicator, panels) is a real debugging surface,
// and it must get the caller's script untouched.
func TestAutomationEvalScriptTopIsVerbatim(t *testing.T) {
	const script = "document.getElementById('__wt_panel') !== null"
	got, err := automationEvalScript(script, "top")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != script {
		t.Errorf("frame=top must not rewrite the script:\n got: %s\nwant: %s", got, script)
	}
}

// TestAutomationEvalScriptRejectsUnknownFrame keeps a typo loud instead of
// silently picking a frame the caller did not ask for — a silent wrong-frame
// evaluate is precisely the failure this change exists to remove.
func TestAutomationEvalScriptRejectsUnknownFrame(t *testing.T) {
	for _, frame := range []string{"app", "iframe", "Content", "TOP", "inner"} {
		if _, err := automationEvalScript("1", frame); err == nil {
			t.Errorf("frame %q must be rejected", frame)
		}
	}
}

// TestAutomationEvalScriptQuotesTheScript pins that the script is embedded as a
// JSON string literal. Quotes, newlines, and backslashes in a caller's script
// must not break out of the wrapper and corrupt the surrounding expression.
func TestAutomationEvalScriptQuotesTheScript(t *testing.T) {
	hostile := []string{
		`document.querySelector("a[href='/x']").textContent`,
		"var s = 'it\\'s';\nreturn s;",
		`"); alert(1); ("`,
		"line1\nline2\r\n\ttabbed",
		`back\slash`,
	}
	for _, script := range hostile {
		got, err := automationEvalScript(script, "content")
		if err != nil {
			t.Fatalf("script %q: unexpected error: %v", script, err)
		}
		lit, err := json.Marshal(script)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "w.eval("+string(lit)+")") {
			t.Errorf("script %q must be embedded as a JSON literal, got: %s", script, got)
		}
	}
}

// TestAutomationEvalScriptUnwrappedPageStillWorks documents the no-shell case:
// a direct (non-proxied) page has no content iframe AND no chrome role marker,
// and the wrapper resolves immediately to the page's own window. That is the
// same conclusion walkthrough.js's contentWin() reaches for an unwrapped page —
// and it is only reachable when the page is genuinely unwrapped: inside a shell
// the role marker routes a missing frame to the wait/fail-loud path instead.
func TestAutomationEvalScriptUnwrappedPageStillWorks(t *testing.T) {
	got, err := automationEvalScript("document.title", "content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "window.__devtool_role==='chrome'?null:window") {
		t.Errorf("wrapper must resolve to the page window when there is no shell, got: %s", got)
	}
}
