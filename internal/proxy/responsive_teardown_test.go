package proxy

import (
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy/scripts"
)

// These tests assert Slice 7's lifecycle guarantees structurally against the
// served bundle. The instrumentation is browser JS with no Go-side runtime, so
// (as with the other Bundle* tests in this package) the guarantees are pinned by
// asserting the load-bearing source constructs are present in the bundle.

// TestResponsive_NoShellInShell: neither the interactive preview (responsive-
// mode.js) nor the off-screen sweep iframes (responsive.js) may load
// window.location.href verbatim — under always-wrap that loads another chrome
// shell. Both must build a marker-bearing content URL so the page loads
// unwrapped and registers as its own content frame.
func TestResponsive_NoShellInShell(t *testing.T) {
	bundle := scripts.GetCombinedScript()

	if strings.Contains(bundle, "iframe.src = window.location.href") {
		t.Errorf("no responsive iframe may load window.location.href verbatim (shell-in-shell)")
	}
	for _, want := range []string{
		"responsiveContentSrc", // responsive-mode.js preview helper
		"iframe.src = responsiveContentSrc()",
		"markedContentSrc", // responsive.js sweep helper
		"iframe.src = markedContentSrc()",
	} {
		if !strings.Contains(bundle, want) {
			t.Errorf("bundle missing recursion-fix marker %q", want)
		}
	}
	// Both helpers append the content-frame marker via the shared param.
	if c := strings.Count(bundle, "searchParams.set(param"); c < 2 {
		t.Errorf("both responsive iframe helpers must append the frame marker, found %d", c)
	}
}

// TestResponsive_CleanRevertAndIdempotentReopen: close() must fully tear down —
// remove the panel from the DOM, clear the shift timer, end any drag, and reset
// state.open — and open() must guard on state.open so open→close→open is
// idempotent (no duplicate panel / leaked listeners).
func TestResponsive_CleanRevertAndIdempotentReopen(t *testing.T) {
	bundle := scripts.GetCombinedScript()
	for _, want := range []string{
		"if (state.open) { return getState(); }", // open() idempotency guard
		"parentNode.removeChild(state.panel)",    // panel removed from DOM on close
		"clearTimeout(state.shiftTimer)",         // shift timer cleared on close
		"state.open = false",                     // open flag reset on close
		"if (!state.open) { return; }",           // scheduleCapture short-circuits when closed
	} {
		if !strings.Contains(bundle, want) {
			t.Errorf("responsive lifecycle missing construct %q", want)
		}
	}
}

// TestContentFrame_DeregistersOnUnload: a content frame must remove itself from
// the shell's frame registry when it unloads (pagehide), so a torn-down
// responsive preview / navigated frame does not leave a stale registry entry or
// remain the active target.
func TestContentFrame_DeregistersOnUnload(t *testing.T) {
	bundle := scripts.GetCombinedScript()
	if !strings.Contains(bundle, "pagehide") {
		t.Errorf("content frame must listen for pagehide to deregister")
	}
	if !strings.Contains(bundle, "deregister(id)") {
		t.Errorf("content frame must call shell registry deregister on unload")
	}
	// The registry's deregister must also clear the active pointer when the
	// active frame goes away (Slice 5 active-target lifecycle).
	if !strings.Contains(bundle, "activeId = keys.length ? keys[0] : null") {
		t.Errorf("deregister must reassign/clear the active pointer when the active frame is removed")
	}
}
