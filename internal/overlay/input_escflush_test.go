package overlay

import (
	"io"
	"testing"
	"time"
)

// A lone Esc keypress arrives as a one-byte read that is indistinguishable
// from the first byte of a split multi-byte sequence. The scanner holds it as
// a remainder; without a deadline it is held until EOF, so Esc reaches neither
// the overlay nor the wrapped TUI. These assert the byte is released.

func collect(t *testing.T, r io.Reader, flush time.Duration, want int, within time.Duration) []byte {
	t.Helper()
	got := make(chan []byte, 1)
	go func() {
		var out []byte
		for b := range scanWin32Input(r, flush) {
			out = append(out, b)
			if len(out) >= want {
				break
			}
		}
		got <- out
	}()
	select {
	case out := <-got:
		return out
	case <-time.After(within):
		t.Fatalf("scanner delivered nothing within %s; the byte is still held", within)
		return nil
	}
}

func TestScannerReleasesALoneEscape(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })

	go func() {
		pw.Write([]byte{0x1b})
		// Deliberately send nothing else and never close: this is a user
		// pressing Esc and then waiting, which is what hung before.
	}()

	out := collect(t, pr, 20*time.Millisecond, 1, 2*time.Second)
	if len(out) != 1 || out[0] != 0x1b {
		t.Fatalf("expected a lone ESC to be released, got %q", out)
	}
}

// The remainder buffer still has to do its real job: a sequence split across
// two reads must be reassembled, not torn into loose bytes.
// The tests above inject a flush value, so they exercise the mechanism but
// not the production entry point. This one drives ScanWin32Input itself, so
// removing the deadline from the shipped default fails here.
func TestScanWin32InputReleasesALoneEscapeByDefault(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })

	go func() { pw.Write([]byte{0x1b}) }()

	got := make(chan byte, 1)
	go func() {
		for b := range ScanWin32Input(pr) {
			got <- b
			return
		}
	}()

	select {
	case b := <-got:
		if b != 0x1b {
			t.Fatalf("got %q, want ESC", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ScanWin32Input held a lone ESC: Esc reaches neither the overlay nor the wrapped TUI")
	}
}

func TestScannerStillReassemblesASplitSequence(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })

	go func() {
		pw.Write([]byte{0x1b})
		time.Sleep(2 * time.Millisecond) // well inside the flush window
		pw.Write([]byte("[1;5C"))
	}()

	out := collect(t, pr, 100*time.Millisecond, 6, 2*time.Second)
	if string(out) != "\x1b[1;5C" {
		t.Fatalf("split Ctrl+Right was not reassembled, got %q", out)
	}
}

func TestScannerFlushesPendingOnEOF(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte{0x1b})
		pw.Close()
	}()
	out := collect(t, pr, time.Hour, 1, 2*time.Second)
	if len(out) != 1 || out[0] != 0x1b {
		t.Fatalf("expected EOF to flush the pending ESC, got %q", out)
	}
}

// A lone Esc must survive the whole input stack: scanner release, then the
// router's own 50ms disambiguation, ending as the "Escape" key.
func TestLoneEscapeResolvesToTheEscapeKey(t *testing.T) {
	er := NewEscapeSequenceReader()
	if key, complete := er.Feed(0x1b); complete || key != "" {
		t.Fatalf("ESC should start a pending sequence, got %q/%v", key, complete)
	}
	if !er.IsPending() {
		t.Fatal("reader should be pending after a bare ESC")
	}
	key, had := er.Timeout()
	if !had || key != "Escape" {
		t.Fatalf("timeout should yield Escape, got %q/%v", key, had)
	}
}
