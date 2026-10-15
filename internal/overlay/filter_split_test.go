package overlay

import (
	"bytes"
	"testing"
	"unicode/utf8"
)

// The overlay draws on the same terminal between two ProtectedWriter writes
// (the gate serializes whole writes, nothing finer). Whatever a write emits
// must therefore end on a character boundary: a lead byte left at the end of
// one write and its continuation bytes at the start of the next have the
// status-bar draw land in the middle of the character.
func TestProtectedWriter_HoldsSplitUTF8UntilComplete(t *testing.T) {
	var out bytes.Buffer
	pw := NewProtectedWriter(&out, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	e := []byte("é") // C3 A9
	pw.Write([]byte{'a', e[0]})
	if !utf8.Valid(out.Bytes()) {
		t.Fatalf("write ended mid-character: % x", out.Bytes())
	}
	out.WriteString("<draw>") // an overlay draw between the two reads
	pw.Write([]byte{e[1], 'b'})
	if got, want := out.String(), "a<draw>éb"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if row, col, _ := pw.ChildCursor(); row != 1 || col != 4 {
		t.Fatalf("cursor = %d,%d, want 1,4", row, col)
	}
}

func TestProtectedWriter_InvalidUTF8StillPassesThrough(t *testing.T) {
	var out bytes.Buffer
	pw := NewProtectedWriter(&out, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// A lead byte followed by ASCII, and one followed by ESC: the bytes are
	// the child's, so they reach the terminal unchanged and in order.
	in := []byte{0xC3, 'x', 0xE4, 0xB8, 0x1b, '[', 'm', 'y'}
	pw.Write(in)
	if !bytes.Equal(out.Bytes(), in) {
		t.Fatalf("got % x, want % x", out.Bytes(), in)
	}
}
