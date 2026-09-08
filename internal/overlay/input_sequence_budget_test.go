package overlay

import "testing"

// A terminal answers some queries on stdin with a string sequence: OSC for a
// colour or clipboard reply, DCS for a terminfo or DECRQSS reply, and APC/PM
// for private protocols. Each is introduced by ESC and one byte, and runs to
// its own string terminator.
//
// These are the same shape as the mouse report: longer than any bound key, and
// carrying a payload that must never be read as keypresses. Reporting the
// introducer as Escape closes the panel, and the rest of the reply is then
// typed into the wrapped agent.
func TestEscapeSequenceReader_SwallowsStringSequences(t *testing.T) {
	tests := []struct {
		name string
		seq  string
	}{
		{"OSC colour reply, ST terminated", "\x1b]11;rgb:1c1c/1c1c/1c1c\x1b\\"},
		{"OSC colour reply, BEL terminated", "\x1b]10;rgb:ffff/ffff/ffff\x07"},
		{"OSC clipboard reply", "\x1b]52;c;aGVsbG8=\x1b\\"},
		{"DCS DECRQSS reply", "\x1bP1$r0;1m\x1b\\"},
		{"APC", "\x1b_Gi=1,a=q;OK\x1b\\"},
		{"PM", "\x1b^somethingprivate\x1b\\"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewEscapeSequenceReader()
			if keys := feedAll(r, tt.seq); len(keys) != 0 {
				t.Errorf("%s produced keys %q; it must be swallowed", tt.name, keys)
			}
			if r.IsPending() {
				t.Error("reader still mid-sequence after a complete string sequence")
			}
			if key, complete := r.Feed('q'); !complete || key != "q" {
				t.Errorf("next key after the sequence = %q/%v, want q/true", key, complete)
			}
		})
	}
}

// Alt+key is a real binding and keeps its meaning: ESC followed by a byte that
// starts no longer sequence is Alt+that key.
func TestEscapeSequenceReader_AltKeyStillReported(t *testing.T) {
	for _, tt := range []struct{ seq, want string }{
		{"\x1bb", "Escape+b"},
		{"\x1bf", "Escape+f"},
	} {
		r := NewEscapeSequenceReader()
		keys := feedAll(r, tt.seq)
		if len(keys) != 1 || keys[0] != tt.want {
			t.Errorf("%q = %q, want [%q]", tt.seq, keys, tt.want)
		}
	}
}

// A CSI longer than the reader's cap is a corrupt or unsupported sequence. The
// cap must discard the rest of that sequence, not return to ground in the
// middle of it: the bytes still to come are parameters and a final byte, and
// read as keys they are panel-browser commands -- a trailing 'q' or 'x' closes
// the panel and a digit jumps to another one.
func TestEscapeSequenceReader_OverlongCSIIsDiscardedWhole(t *testing.T) {
	// A kitty keyboard-protocol sequence with a long text field, ending in a
	// final byte the overlay binds nothing to.
	long := "\x1b[1;2;3;4;5;6;7;8;9;10;11;12;13;14;15;16;17;18;19;20u"
	if len(long) <= csiMaxLen {
		t.Fatalf("test sequence is %d bytes, not longer than the %d-byte cap", len(long), csiMaxLen)
	}
	r := NewEscapeSequenceReader()
	if keys := feedAll(r, long); len(keys) != 0 {
		t.Errorf("an over-long CSI produced keys %q; the whole sequence must be discarded", keys)
	}
	if r.IsPending() {
		t.Error("reader still mid-sequence after an over-long CSI ended")
	}
	if key, complete := r.Feed('q'); !complete || key != "q" {
		t.Errorf("next key after an over-long CSI = %q/%v, want q/true", key, complete)
	}
}

// The same cap must not let a runaway stream wedge the reader: a sequence that
// never reaches a final byte has to be abandoned so later keys still arrive.
func TestEscapeSequenceReader_RunawaySequenceIsAbandoned(t *testing.T) {
	r := NewEscapeSequenceReader()
	runaway := "\x1b["
	for i := 0; i < 4*csiMaxLen; i++ {
		runaway += "1"
	}
	feedAll(r, runaway)
	if _, hadPending := r.Timeout(); hadPending {
		t.Error("a runaway sequence reported a keypress on timeout")
	}
	if key, complete := r.Feed('q'); !complete || key != "q" {
		t.Errorf("reader wedged after a runaway sequence: %q/%v, want q/true", key, complete)
	}
}
