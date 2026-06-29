package proxy

import (
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy/scripts"
)

// TestCancelExecution: a hub-side timeout must reclaim the pending exec so the
// map/channel do not leak when the browser never replies (the chronic
// page-reload-mid-exec case). Asserts removal, channel close, and idempotence.
func TestCancelExecution(t *testing.T) {
	ps := &ProxyServer{ID: "px1"}
	ch := make(chan *ExecutionResult, 1)
	ps.pendingExecs.Store("exec-1", ch)

	ps.CancelExecution("exec-1")

	if _, ok := ps.pendingExecs.Load("exec-1"); ok {
		t.Fatal("pending entry must be removed after cancel")
	}
	if _, open := <-ch; open {
		t.Fatal("result channel must be closed after cancel")
	}
	// Idempotent + safe on an unknown id (the deliver-already-won race).
	ps.CancelExecution("exec-1")
	ps.CancelExecution("never-existed")
}

// TestActiveFrame_RoundTrip: SetActiveFrame records the reported active content
// frame; ActiveFrame reads it back; empty reports are ignored.
func TestActiveFrame_RoundTrip(t *testing.T) {
	ps := &ProxyServer{ID: "px1"}

	if ps.ActiveFrame() != "" {
		t.Errorf("ActiveFrame must start empty")
	}
	ps.SetActiveFrame("frameA")
	if ps.ActiveFrame() != "frameA" {
		t.Errorf("ActiveFrame = %q, want frameA", ps.ActiveFrame())
	}
	// Last report wins (most-recently-active frame).
	ps.SetActiveFrame("frameB")
	if ps.ActiveFrame() != "frameB" {
		t.Errorf("ActiveFrame = %q, want frameB", ps.ActiveFrame())
	}
	// Empty report is ignored — does not clear the active frame.
	ps.SetActiveFrame("")
	if ps.ActiveFrame() != "frameB" {
		t.Errorf("empty report must not clear active frame, got %q", ps.ActiveFrame())
	}
}

// TestExecuteJavaScript_TargetsActiveFrameWhenUnspecified: with no clients the
// call errors, but the active-frame default resolution is still exercised — the
// point is that an explicit frame id is not required (no panic / no crash) and
// an execID is returned.
func TestExecuteJavaScript_NoClients(t *testing.T) {
	ps := &ProxyServer{ID: "px1"}
	ps.SetActiveFrame("frameA")
	execID, ch, err := ps.ExecuteJavaScript("1+1")
	if err == nil {
		t.Errorf("expected 'no connected clients' error")
	}
	if execID == "" {
		t.Errorf("execID should still be assigned")
	}
	if ch != nil {
		t.Errorf("result channel must be nil when no clients")
	}
}

// TestBundleExecFrameTargeting: the bundle carries the browser-side exec
// frame-target guard and the content frame's active-report helper.
// TestResolveExecTarget: outer/inner/explicit selectors map to the right wire
// token. Outer addresses the shell by role token; inner/active/empty collapse to
// the active content frame; an explicit id passes through.
func TestResolveExecTarget(t *testing.T) {
	ps := &ProxyServer{ID: "px1"}
	ps.SetActiveFrame("frameA")
	cases := map[string]string{
		"outer":   "@chrome",
		"shell":   "@chrome",
		"chrome":  "@chrome",
		"@chrome": "@chrome",
		"inner":   "frameA",
		"active":  "frameA",
		"content": "frameA",
		"":        "frameA",
		"frameZ":  "frameZ",
	}
	for in, want := range cases {
		if got := ps.resolveExecTarget(in); got != want {
			t.Errorf("resolveExecTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBundleExecFrameTargeting: the bundle carries the browser-side exec
// frame-target guard, the active-report helper, and the new @chrome role token +
// shell resize helper.
func TestBundleExecFrameTargeting(t *testing.T) {
	bundle := scripts.GetCombinedScript()
	for _, want := range []string{
		"message.frame_id",         // exec guard: run only if untargeted or addressed to this frame
		"frame_active",             // content frame reports active to the proxy
		"reportActive",             // the reporter helper
		"@chrome",                  // outer-shell role token
		"__devtool_resize_content", // shell-side resize helper
	} {
		if !strings.Contains(bundle, want) {
			t.Errorf("bundle missing exec-targeting marker %q", want)
		}
	}
}
