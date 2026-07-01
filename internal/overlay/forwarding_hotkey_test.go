package overlay

import "testing"

// TestForwardingHotkey pins the Ctrl+Up→pause / Ctrl+Down→resume mapping and
// asserts no other sequence (including the panel Ctrl+Left/Right and plain
// arrows the wrapped tool needs) is captured.
func TestForwardingHotkey(t *testing.T) {
	cases := []struct {
		seq        string
		wantPaused bool
		wantOK     bool
	}{
		{"\x1b[1;5A", true, true},   // Ctrl+Up → pause
		{"\x1b[1;5B", false, true},  // Ctrl+Down → resume
		{"\x1b[1;5C", false, false}, // Ctrl+Right (panel nav, not ours)
		{"\x1b[1;5D", false, false}, // Ctrl+Left (panel nav, not ours)
		{"\x1b[A", false, false},    // plain Up — must pass through to the agent
		{"\x1b[B", false, false},    // plain Down — must pass through
		{"q", false, false},         // unrelated key
		{"", false, false},          // empty
	}
	for _, c := range cases {
		paused, ok := forwardingHotkey([]byte(c.seq))
		if ok != c.wantOK || paused != c.wantPaused {
			t.Errorf("forwardingHotkey(%q) = (%v,%v), want (%v,%v)",
				c.seq, paused, ok, c.wantPaused, c.wantOK)
		}
	}
}
