package overlay

import (
	"bytes"
	"testing"
)

// A terminal ends an OSC or DCS string at any ESC, not only at ST: the ESC
// begins the next sequence. Waiting for ST instead held every later byte —
// one stray "ESC ]" (an aborted title update, cat of a binary) blanked all
// of the child's output for the rest of the session and grew the buffer
// without bound.
func TestProtectedWriter_EscEndsAnUnterminatedString(t *testing.T) {
	for _, intro := range []string{"\x1b]0;title", "\x1bPq#0;2;0;0;0"} {
		var out bytes.Buffer
		pw := NewProtectedWriter(&out, 80, 24, FilterConfig{ProtectBottomRows: 1})

		pw.Write([]byte(intro))
		pw.Write([]byte("\x1b[31mhello\r\n"))
		if got, want := out.String(), intro+"\x1b[31mhello\r\n"; got != want {
			t.Errorf("%q: got %q, want %q", intro, got, want)
		}
		if row, _, _ := pw.ChildCursor(); row != 2 {
			t.Errorf("%q: cursor row = %d, want 2 (the CSI and text after the string were not parsed)", intro, row)
		}
		pw.Stop()
	}
}

func TestProtectedWriter_STStillTerminatesStrings(t *testing.T) {
	var out bytes.Buffer
	pw := NewProtectedWriter(&out, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	in := "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\\x1bPq\x1b\\z"
	pw.Write([]byte(in))
	if out.String() != in {
		t.Fatalf("got %q, want %q", out.String(), in)
	}
}
