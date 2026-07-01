package daemon

import "testing"

// TestForwardingPause exercises the per-session push-pause flag: set/clear,
// resume deletes the entry, empty session code is a no-op (never panics, never
// matches), and unrelated sessions are independent.
func TestForwardingPause(t *testing.T) {
	d := &Daemon{}

	if d.IsForwardingPaused("s1") {
		t.Fatal("fresh session must not be paused")
	}

	d.SetForwardingPaused("s1", true)
	if !d.IsForwardingPaused("s1") {
		t.Fatal("session s1 should be paused after set")
	}
	if d.IsForwardingPaused("s2") {
		t.Fatal("pausing s1 must not pause s2")
	}

	// Resume clears it.
	d.SetForwardingPaused("s1", false)
	if d.IsForwardingPaused("s1") {
		t.Fatal("session s1 should be resumed after clear")
	}

	// Empty session code: no-op both ways.
	d.SetForwardingPaused("", true)
	if d.IsForwardingPaused("") {
		t.Fatal("empty session code must never report paused")
	}
}
