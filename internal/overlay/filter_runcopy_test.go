package overlay

import (
	"bytes"
	"math/rand"
	"testing"
)

// writeByteAtATime is the filter's per-byte path alone, the reference the
// whole-run copy in Write must match.
func writeByteAtATime(pw *ProtectedWriter, data []byte) {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	for _, c := range data {
		out := &pw.filtered
		out.Reset()
		pw.processStateByte(out, c)
		if out.Len() > 0 {
			_ = pw.sendToHost(out.Bytes())
		}
	}
	pw.publishCursor()
}

// The filter's whole-run copy of ground-state text must send the host the
// same bytes, and leave the same cursor, as the per-byte path deciding every
// byte, for any split of the stream.
func TestProtectedWriterRunCopyMatchesByteAtATime(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	extra := []string{"\xc3", "\xa9", "\xff", "\xe2\x94", "\xc0\xaf", "\xed\xa0\x80", "\x1b[5;10r", "\x1b[2J", "\x1b[?1c", "\x1bc", "\x1b[10;3H"}
	for iter := 0; iter < 200; iter++ {
		data := randomOutput(rng)
		for k := 0; k < 40; k++ {
			at := rng.Intn(len(data))
			data = append(data[:at], append([]byte(extra[rng.Intn(len(extra))]), data[at:]...)...)
		}
		var got, want bytes.Buffer
		a := NewProtectedWriter(&got, 80, 24, FilterConfig{ProtectBottomRows: 1})
		b := NewProtectedWriter(&want, 80, 24, FilterConfig{ProtectBottomRows: 1})
		for rest := data; len(rest) > 0; {
			n := 1 + rng.Intn(min(len(rest), 700))
			a.Write(rest[:n])
			rest = rest[n:]
		}
		writeByteAtATime(b, data)
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("iter %d: host bytes differ (%d vs %d)", iter, got.Len(), want.Len())
		}
		ar, ac, av := a.ChildCursor()
		br, bc, bv := b.ChildCursor()
		if ar != br || ac != bc || av != bv || a.InAltScreen() != b.InAltScreen() {
			t.Fatalf("iter %d: cursor %d,%d,%v vs %d,%d,%v", iter, ar, ac, av, br, bc, bv)
		}
	}
}
