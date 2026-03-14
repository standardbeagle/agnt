package overlay

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"
)

// writeRecorder records each Write call's payload to verify batching.
type writeRecorder struct {
	mu     sync.Mutex
	writes [][]byte
}

func (w *writeRecorder) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := make([]byte, len(p))
	copy(cp, p)
	w.writes = append(w.writes, cp)
	return len(p), nil
}

func (w *writeRecorder) Read(p []byte) (int, error) {
	return 0, io.EOF
}

func (w *writeRecorder) getWrites() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	cp := make([][]byte, len(w.writes))
	copy(cp, w.writes)
	return cp
}

func (w *writeRecorder) waitForWrites(n int, timeout time.Duration) [][]byte {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		count := len(w.writes)
		w.mu.Unlock()
		if count >= n {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return w.getWrites()
}

// --- Unit tests (batching logic only) ---

// TestInputBatchingEscapeSequences verifies that multi-byte escape sequences
// written to the inputCh channel are batched into a single Write to the PTY.
func TestInputBatchingEscapeSequences(t *testing.T) {
	rec := &writeRecorder{}
	inputCh := make(chan byte, 16)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-done:
				return
			case b := <-inputCh:
				buf := []byte{b}
				if b == 0x1b {
					time.Sleep(1 * time.Millisecond)
				}
				drain := true
				for drain {
					select {
					case nb := <-inputCh:
						buf = append(buf, nb)
					default:
						drain = false
					}
				}
				rec.Write(buf)
			}
		}
	}()

	inputCh <- 0x1b
	inputCh <- '['
	inputCh <- 'A'
	time.Sleep(50 * time.Millisecond)
	close(done)

	writes := rec.getWrites()
	if len(writes) != 1 {
		t.Fatalf("expected 1 write, got %d: %v", len(writes), writes)
	}
	if !bytes.Equal(writes[0], []byte{0x1b, '[', 'A'}) {
		t.Fatalf("expected \\x1b[A, got %v", writes[0])
	}
}

// TestInputBatchingRegularBytes verifies that non-escape bytes are written
// immediately without the 1ms delay.
func TestInputBatchingRegularBytes(t *testing.T) {
	rec := &writeRecorder{}
	inputCh := make(chan byte, 16)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-done:
				return
			case b := <-inputCh:
				buf := []byte{b}
				if b == 0x1b {
					time.Sleep(1 * time.Millisecond)
				}
				drain := true
				for drain {
					select {
					case nb := <-inputCh:
						buf = append(buf, nb)
					default:
						drain = false
					}
				}
				rec.Write(buf)
			}
		}
	}()

	inputCh <- 'a'
	time.Sleep(10 * time.Millisecond)
	close(done)

	writes := rec.getWrites()
	if len(writes) != 1 {
		t.Fatalf("expected 1 write, got %d", len(writes))
	}
	if !bytes.Equal(writes[0], []byte{'a'}) {
		t.Fatalf("expected [a], got %v", writes[0])
	}
}

// TestInputBatchingMultipleEscapeSequences verifies that consecutive escape
// sequences are each batched separately.
func TestInputBatchingMultipleEscapeSequences(t *testing.T) {
	rec := &writeRecorder{}
	inputCh := make(chan byte, 16)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-done:
				return
			case b := <-inputCh:
				buf := []byte{b}
				if b == 0x1b {
					time.Sleep(1 * time.Millisecond)
				}
				drain := true
				for drain {
					select {
					case nb := <-inputCh:
						buf = append(buf, nb)
					default:
						drain = false
					}
				}
				rec.Write(buf)
			}
		}
	}()

	inputCh <- 0x1b
	inputCh <- '['
	inputCh <- 'A'
	time.Sleep(10 * time.Millisecond)

	inputCh <- 0x1b
	inputCh <- '['
	inputCh <- 'B'
	time.Sleep(50 * time.Millisecond)
	close(done)

	writes := rec.getWrites()
	if len(writes) != 2 {
		t.Fatalf("expected 2 writes, got %d: %v", len(writes), writes)
	}
	if !bytes.Equal(writes[0], []byte{0x1b, '[', 'A'}) {
		t.Fatalf("write 0: expected \\x1b[A, got %v", writes[0])
	}
	if !bytes.Equal(writes[1], []byte{0x1b, '[', 'B'}) {
		t.Fatalf("write 1: expected \\x1b[B, got %v", writes[1])
	}
}

// TestPanelShortcutDelta verifies Ctrl+Arrow detection.
func TestPanelShortcutDelta(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  int
	}{
		{"Ctrl+Right", []byte("\x1b[1;5C"), 1},
		{"Ctrl+Left", []byte("\x1b[1;5D"), -1},
		{"plain Right", []byte("\x1b[C"), 0},
		{"plain Left", []byte("\x1b[D"), 0},
		{"regular text", []byte("hello"), 0},
		{"Ctrl+Up", []byte("\x1b[1;5A"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := panelShortcutDelta(tt.input)
			if got != tt.want {
				t.Errorf("panelShortcutDelta(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// TestCtrlArrowInterceptedFromIndicator verifies that Ctrl+Right from the
// indicator state does NOT pass through to the PTY (it opens panel mode).
func TestCtrlArrowInterceptedFromIndicator(t *testing.T) {
	rec := &writeRecorder{}
	cfg := DefaultConfig()
	cfg.ShowIndicator = true
	ov := New(rec, 80, 24, cfg)
	ov.state.Store(int32(StateIndicator))

	pr, pw := io.Pipe()
	router := NewInputRouter(rec, ov, 0x19)
	router.input = pr

	go router.Run()
	defer router.Stop()

	// Send Ctrl+Right — should be intercepted, not written to PTY
	pw.Write([]byte("\x1b[1;5C"))
	time.Sleep(50 * time.Millisecond)

	writes := rec.getWrites()
	for _, w := range writes {
		if bytes.Equal(w, []byte("\x1b[1;5C")) {
			t.Fatal("Ctrl+Right was passed through to PTY; should have been intercepted")
		}
	}
}

// --- Integration tests (full InputRouter with pipe I/O) ---

// newTestRouter creates an InputRouter wired to a pipe input and a write recorder.
// The overlay is inactive by default, so bytes pass through to the PTY recorder.
func newTestRouter(t *testing.T) (*InputRouter, *io.PipeWriter, *writeRecorder) {
	t.Helper()
	rec := &writeRecorder{}
	cfg := DefaultConfig()
	cfg.ShowIndicator = false // keep overlay inactive
	ov := New(rec, 80, 24, cfg)
	ov.state.Store(int32(StateHidden)) // ensure overlay is not capturing input

	pr, pw := io.Pipe()
	router := NewInputRouter(rec, ov, 0x01) // Ctrl-A as hotkey (unlikely in test data)
	router.input = pr
	return router, pw, rec
}

// TestInputRouterArrowKeysIntegration exercises the full path:
// pipe write → ScanWin32Input → inputCh → batched Write → PTY recorder
func TestInputRouterArrowKeysIntegration(t *testing.T) {
	router, pw, rec := newTestRouter(t)

	go router.Run()
	defer router.Stop()

	// Write arrow-up as a single chunk (as a real terminal would)
	pw.Write([]byte{0x1b, '[', 'A'})

	writes := rec.waitForWrites(1, 200*time.Millisecond)
	if len(writes) < 1 {
		t.Fatal("expected at least 1 write to PTY, got 0")
	}
	if !bytes.Equal(writes[0], []byte{0x1b, '[', 'A'}) {
		t.Fatalf("expected arrow-up (\\x1b[A), got %v", writes[0])
	}
}

// TestInputRouterAllArrowKeysIntegration sends all four arrow keys and
// verifies each arrives as a single batched write.
func TestInputRouterAllArrowKeysIntegration(t *testing.T) {
	router, pw, rec := newTestRouter(t)

	go router.Run()
	defer router.Stop()

	arrows := []struct {
		name string
		seq  []byte
	}{
		{"up", []byte{0x1b, '[', 'A'}},
		{"down", []byte{0x1b, '[', 'B'}},
		{"right", []byte{0x1b, '[', 'C'}},
		{"left", []byte{0x1b, '[', 'D'}},
	}

	for i, a := range arrows {
		pw.Write(a.seq)
		time.Sleep(20 * time.Millisecond) // gap between sequences

		writes := rec.waitForWrites(i+1, 200*time.Millisecond)
		if len(writes) < i+1 {
			t.Fatalf("after %s: expected %d writes, got %d", a.name, i+1, len(writes))
		}
		if !bytes.Equal(writes[i], a.seq) {
			t.Fatalf("arrow %s: expected %v, got %v", a.name, a.seq, writes[i])
		}
	}
}

// TestInputRouterMixedInputIntegration sends regular text interspersed with
// escape sequences and verifies correct batching.
func TestInputRouterMixedInputIntegration(t *testing.T) {
	router, pw, rec := newTestRouter(t)

	go router.Run()
	defer router.Stop()

	// Type "a", then arrow-up, then "b"
	pw.Write([]byte{'a'})
	time.Sleep(20 * time.Millisecond)

	pw.Write([]byte{0x1b, '[', 'A'})
	time.Sleep(20 * time.Millisecond)

	pw.Write([]byte{'b'})
	time.Sleep(20 * time.Millisecond)

	writes := rec.waitForWrites(3, 200*time.Millisecond)
	if len(writes) < 3 {
		t.Fatalf("expected 3 writes, got %d: %v", len(writes), writes)
	}

	if !bytes.Equal(writes[0], []byte{'a'}) {
		t.Fatalf("write 0: expected [a], got %v", writes[0])
	}
	if !bytes.Equal(writes[1], []byte{0x1b, '[', 'A'}) {
		t.Fatalf("write 1: expected \\x1b[A, got %v", writes[1])
	}
	if !bytes.Equal(writes[2], []byte{'b'}) {
		t.Fatalf("write 2: expected [b], got %v", writes[2])
	}
}

// TestInputRouterRapidArrowKeysIntegration sends multiple arrow key sequences
// in a single write (as a terminal might when keys are pressed quickly) and
// verifies each is still written as a complete sequence.
func TestInputRouterRapidArrowKeysIntegration(t *testing.T) {
	router, pw, rec := newTestRouter(t)

	go router.Run()
	defer router.Stop()

	// Write two arrow keys in one chunk
	pw.Write([]byte{0x1b, '[', 'A', 0x1b, '[', 'B'})

	writes := rec.waitForWrites(1, 200*time.Millisecond)
	if len(writes) < 1 {
		t.Fatal("expected at least 1 write, got 0")
	}

	// They may arrive as 1 or 2 writes depending on timing, but each write
	// must contain complete escape sequences (no split ESC).
	var all []byte
	for _, w := range writes {
		all = append(all, w...)
	}
	expected := []byte{0x1b, '[', 'A', 0x1b, '[', 'B'}
	if !bytes.Equal(all, expected) {
		t.Fatalf("combined writes: expected %v, got %v", expected, all)
	}

	// Verify no write starts with '[' or ends with bare 0x1b (split sequence)
	for i, w := range writes {
		if len(w) > 0 && w[0] == '[' {
			t.Fatalf("write %d starts with '[' — escape sequence was split: %v", i, w)
		}
		if len(w) > 0 && w[len(w)-1] == 0x1b {
			t.Fatalf("write %d ends with bare ESC — escape sequence was split: %v", i, w)
		}
	}
}

// TestInputRouterExtendedEscapeSequenceIntegration tests longer CSI sequences
// like F1-F12 keys (e.g. \x1b[15~ for F5).
func TestInputRouterExtendedEscapeSequenceIntegration(t *testing.T) {
	router, pw, rec := newTestRouter(t)

	go router.Run()
	defer router.Stop()

	// F5 key: \x1b[15~
	f5 := []byte{0x1b, '[', '1', '5', '~'}
	pw.Write(f5)

	writes := rec.waitForWrites(1, 200*time.Millisecond)
	if len(writes) < 1 {
		t.Fatal("expected at least 1 write, got 0")
	}
	if !bytes.Equal(writes[0], f5) {
		t.Fatalf("expected F5 sequence %v, got %v", f5, writes[0])
	}
}
