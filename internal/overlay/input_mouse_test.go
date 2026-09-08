package overlay

import (
	"bytes"
	"testing"
)

// feedAll runs a whole byte sequence through the reader and returns every key
// it emitted.
func feedAll(r *EscapeSequenceReader, seq string) []string {
	var keys []string
	for i := 0; i < len(seq); i++ {
		if key, complete := r.Feed(seq[i]); complete && key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

// A terminal reports mouse motion as an escape sequence on stdin. The reader
// must consume the whole report and emit nothing: emitting Escape closes the
// panel, and the bytes left over after that go straight to the child process,
// where they appear as text in its input field.
func TestEscapeSequenceReader_SwallowsMouseReports(t *testing.T) {
	tests := []struct {
		name string
		seq  string
	}{
		{"SGR motion", "\x1b[<35;80;24M"},
		{"SGR press", "\x1b[<0;5;5M"},
		{"SGR release", "\x1b[<0;5;5m"},
		{"SGR three-digit coordinates", "\x1b[<35;180;124M"},
		{"urxvt", "\x1b[32;80;24M"},
		// X10 reports three raw bytes after \x1b[M, and those bytes can be
		// anything -- including '[' and 'M' -- so they must be consumed by
		// count, never by looking for a terminator.
		{"X10", "\x1b[M\x20\x30\x40"},
		{"X10 with sequence-like coordinates", "\x1b[M\x1b[M"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewEscapeSequenceReader()
			if keys := feedAll(r, tt.seq); len(keys) != 0 {
				t.Errorf("mouse report produced keys %q; it must be swallowed", keys)
			}
			if r.IsPending() {
				t.Error("reader still mid-sequence after a complete mouse report")
			}
			// The reader must be usable straight afterwards: a mouse report
			// that ate the next keypress would be its own bug.
			if key, complete := r.Feed('q'); !complete || key != "q" {
				t.Errorf("next key after a mouse report = %q/%v, want q/true", key, complete)
			}
		})
	}
}

// The same rule applies to any complete escape sequence the overlay has no
// binding for. Reporting Escape for it closes the panel the developer is
// looking at, and leaves the tail of the sequence to be typed into the child.
func TestEscapeSequenceReader_SwallowsUnknownCompleteSequences(t *testing.T) {
	for _, seq := range []string{
		"\x1b[200~",         // bracketed paste start
		"\x1b[201~",         // bracketed paste end
		"\x1b[1;2C",         // shift+right
		"\x1b[15;3~",        // alt+F5
		"\x1b[?1000;1006$y", // DECRQM reply
	} {
		r := NewEscapeSequenceReader()
		if keys := feedAll(r, seq); len(keys) != 0 {
			t.Errorf("%q produced keys %q; an unbound sequence must be swallowed", seq, keys)
		}
		if r.IsPending() {
			t.Errorf("%q left the reader mid-sequence", seq)
		}
	}
}

func TestEscapeSequenceReader_StillRecognizesBoundKeys(t *testing.T) {
	tests := map[string]string{
		"\x1b[A":    "Up",
		"\x1b[B":    "Down",
		"\x1b[C":    "Right",
		"\x1b[D":    "Left",
		"\x1b[H":    "Home",
		"\x1b[F":    "End",
		"\x1b[3~":   "Delete",
		"\x1b[Z":    "Shift+Tab",
		"\x1b[1;5C": "Ctrl+Right",
		"\x1b[1;5D": "Ctrl+Left",
		"\x1b[1;5A": "Ctrl+Up",
		"\x1b[1;5B": "Ctrl+Down",
		// SS3 arrows: a terminal in application cursor mode sends these.
		"\x1bOA": "Up",
		"\x1bOB": "Down",
		"\x1bOC": "Right",
		"\x1bOD": "Left",
	}
	for seq, want := range tests {
		r := NewEscapeSequenceReader()
		keys := feedAll(r, seq)
		if len(keys) != 1 || keys[0] != want {
			t.Errorf("%q = %q, want [%q]", seq, keys, want)
		}
	}
}

// End to end through the router: mouse motion over an open panel must leave
// the panel open. Closing it is what let the rest of the report reach the
// child, which is how this surfaced -- fragments of mouse reports typed into
// the agent's input field.
func TestPanelSurvivesMouseMotion(t *testing.T) {
	var pty bytes.Buffer
	o := New(&pty, 80, 24, DefaultConfig())
	o.renderer = NewRenderer(&pty, 80, 24)
	o.panelMode = true
	o.panelItems = []PanelItem{{Type: "overview", Label: "overview"}}
	o.state.Store(int32(StateMenu))
	r := NewInputRouter(&pty, o)

	for _, report := range []string{"\x1b[<35;40;12M", "\x1b[<35;41;12M", "\x1b[M\x20\x30\x40"} {
		for i := 0; i < len(report); i++ {
			r.handleOverlayInput(report[i])
		}
	}

	if got := o.State(); got != StateMenu {
		t.Errorf("overlay state = %v after mouse motion, want StateMenu (the panel closed)", got)
	}
	if !o.panelMode {
		t.Error("panel mode was left by mouse motion")
	}
}

// A sequence split across the 50ms escape window must not be read as Escape:
// that is the same panel-closing guess, reached by a slow report instead of a
// long one. Only a bare ESC is Escape.
func TestEscapeSequenceReader_TimeoutOnlyReportsABareEscape(t *testing.T) {
	bare := NewEscapeSequenceReader()
	bare.Feed(0x1b)
	if key, hadPending := bare.Timeout(); !hadPending || key != "Escape" {
		t.Errorf("bare ESC timeout = %q/%v, want Escape/true", key, hadPending)
	}

	for _, partial := range []string{"\x1b[", "\x1b[<35;80", "\x1b[M\x20"} {
		r := NewEscapeSequenceReader()
		feedAll(r, partial)
		if _, hadPending := r.Timeout(); hadPending {
			t.Errorf("timeout partway through %q reported a keypress", partial)
		}
		if r.IsPending() {
			t.Errorf("timeout left %q pending", partial)
		}
	}
}
