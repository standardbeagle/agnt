package overlay

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// --- Win32 input mode parser tests ---

// buildWin32Seq creates a win32-input-mode sequence: ESC [ Vk;Sc;Uc;Kd;Cs;Rc _
func buildWin32Seq(vk, sc, uc, kd, cs, rc int) []byte {
	s := []byte{0x1b, '['}
	s = append(s, []byte(fmt.Sprintf("%d;%d;%d;%d;%d;%d", vk, sc, uc, kd, cs, rc))...)
	s = append(s, '_')
	return s
}

func TestParseWin32ArrowKeys(t *testing.T) {
	tests := []struct {
		name string
		vk   int
		want []byte
	}{
		{"up", 0x26, []byte{0x1b, '[', 'A'}},
		{"down", 0x28, []byte{0x1b, '[', 'B'}},
		{"right", 0x27, []byte{0x1b, '[', 'C'}},
		{"left", 0x25, []byte{0x1b, '[', 'D'}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq := buildWin32Seq(tt.vk, 0, 0, 1, 0, 1) // key down, no modifiers
			got, remainder := parseWin32InputModeInternal(seq)
			if remainder != nil {
				t.Fatalf("unexpected remainder: %v", remainder)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestParseWin32CtrlArrowKeys(t *testing.T) {
	tests := []struct {
		name string
		vk   int
		want []byte
	}{
		{"ctrl+right", 0x27, []byte{0x1b, '[', '1', ';', '5', 'C'}},
		{"ctrl+left", 0x25, []byte{0x1b, '[', '1', ';', '5', 'D'}},
		{"ctrl+up", 0x26, []byte{0x1b, '[', '1', ';', '5', 'A'}},
		{"ctrl+down", 0x28, []byte{0x1b, '[', '1', ';', '5', 'B'}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq := buildWin32Seq(tt.vk, 0, 0, 1, 0x08, 1) // key down, ctrl held
			got, remainder := parseWin32InputModeInternal(seq)
			if remainder != nil {
				t.Fatalf("unexpected remainder: %v", remainder)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestParseWin32HomeEnd(t *testing.T) {
	tests := []struct {
		name string
		vk   int
		want []byte
	}{
		{"home", 0x24, []byte{0x1b, '[', 'H'}},
		{"end", 0x23, []byte{0x1b, '[', 'F'}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seq := buildWin32Seq(tt.vk, 0, 0, 1, 0, 1)
			got, _ := parseWin32InputModeInternal(seq)
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestParseWin32Delete(t *testing.T) {
	seq := buildWin32Seq(0x2E, 0, 0, 1, 0, 1) // VK_DELETE
	got, _ := parseWin32InputModeInternal(seq)
	want := []byte{0x1b, '[', '3', '~'}
	if !bytes.Equal(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestParseWin32KeyUpIgnored(t *testing.T) {
	// Key-up events (kd=0) should produce no output
	seq := buildWin32Seq(0x26, 0, 0, 0, 0, 1)
	got, _ := parseWin32InputModeInternal(seq)
	if len(got) != 0 {
		t.Fatalf("expected empty output for key-up, got %v", got)
	}
}

func TestParseWin32UnicodeCharStillWorks(t *testing.T) {
	// Regular characters with uc > 0 should still work
	seq := buildWin32Seq(65, 0, 97, 1, 0, 1) // 'a' key, uc=97
	got, _ := parseWin32InputModeInternal(seq)
	if !bytes.Equal(got, []byte{'a'}) {
		t.Fatalf("expected [97], got %v", got)
	}
}

func TestParseWin32CtrlY(t *testing.T) {
	// Ctrl+Y: vk=89, uc=25 (0x19)
	seq := buildWin32Seq(89, 21, 25, 1, 8, 1)
	got, _ := parseWin32InputModeInternal(seq)
	if !bytes.Equal(got, []byte{0x19}) {
		t.Fatalf("expected [0x19], got %v", got)
	}
}

func TestParseWin32MixedSequences(t *testing.T) {
	// Multiple win32 sequences in one buffer: 'h' + arrow-up + 'i'
	var buf []byte
	buf = append(buf, buildWin32Seq(72, 0, 104, 1, 0, 1)...) // 'h'
	buf = append(buf, buildWin32Seq(0x26, 0, 0, 1, 0, 1)...) // arrow up
	buf = append(buf, buildWin32Seq(73, 0, 105, 1, 0, 1)...) // 'i'

	got, remainder := parseWin32InputModeInternal(buf)
	if remainder != nil {
		t.Fatalf("unexpected remainder: %v", remainder)
	}
	want := []byte{'h', 0x1b, '[', 'A', 'i'}
	if !bytes.Equal(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestParseWin32F5Key(t *testing.T) {
	seq := buildWin32Seq(0x74, 0, 0, 1, 0, 1) // VK_F5
	got, _ := parseWin32InputModeInternal(seq)
	want := []byte{0x1b, '[', '1', '5', '~'}
	if !bytes.Equal(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestParseWin32ShiftArrow(t *testing.T) {
	// Shift+Right: mod=2
	seq := buildWin32Seq(0x27, 0, 0, 1, 0x10, 1) // VK_RIGHT, shift
	got, _ := parseWin32InputModeInternal(seq)
	want := []byte{0x1b, '[', '1', ';', '2', 'C'}
	if !bytes.Equal(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

// --- Panel refresh tests ---

// mockOutputFetcher records calls and returns configurable output.
type mockOutputFetcher struct {
	mu        sync.Mutex
	output    string
	callCount int
	calls     []string // script names passed to GetScriptOutput
}

func (m *mockOutputFetcher) GetProcessOutput(_ string, _ int) (string, error) {
	return "", nil
}

func (m *mockOutputFetcher) GetScriptOutput(name string, _ int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	m.calls = append(m.calls, name)
	return m.output, nil
}

func (m *mockOutputFetcher) setOutput(s string) {
	m.mu.Lock()
	m.output = s
	m.mu.Unlock()
}

func (m *mockOutputFetcher) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// newPanelTestRouter creates an InputRouter with a process panel ready for refresh testing.
func newPanelTestRouter(t *testing.T) (*InputRouter, *Overlay, *mockOutputFetcher) {
	t.Helper()
	rec := &writeRecorder{}
	cfg := DefaultConfig()
	cfg.ShowIndicator = false
	ov := New(rec, 80, 24, cfg)

	router := NewInputRouter(rec, ov, 0x19)
	fetcher := &mockOutputFetcher{output: "line1\nline2"}
	router.SetOutputFetcher(fetcher)

	// Set up a process panel as the active panel
	ov.panelItems = []PanelItem{
		{Type: "overview", Label: "overview"},
		{Type: "process", ID: "dev", Label: "dev"},
	}
	ov.panelMode = true
	ov.panelIndex = 1
	ov.state.Store(int32(StateMenu))

	return router, ov, fetcher
}

func TestPanelRefreshStartsAndStops(t *testing.T) {
	router, _, fetcher := newPanelTestRouter(t)

	// Start refresh
	router.overlay.mu.Lock()
	router.startPanelRefresh("dev")
	router.overlay.mu.Unlock()

	// Wait for at least one refresh tick
	time.Sleep(panelRefreshInterval + 200*time.Millisecond)
	count := fetcher.getCallCount()
	assert.GreaterOrEqual(t, count, 1, "expected at least 1 refresh call")

	// Stop refresh
	router.stopPanelRefresh()

	// Record count after stop, wait, verify no more calls
	countAfterStop := fetcher.getCallCount()
	time.Sleep(panelRefreshInterval + 200*time.Millisecond)
	assert.Equal(t, countAfterStop, fetcher.getCallCount(), "no calls expected after stop")
}

func TestPanelRefreshSkipsUnchangedContent(t *testing.T) {
	router, ov, fetcher := newPanelTestRouter(t)

	// Pre-fill panel with same content the fetcher returns
	fetcher.setOutput("line1\nline2")
	ov.mu.Lock()
	ov.panelItems[1].SetContent("line1\nline2")
	ov.mu.Unlock()

	// Start refresh
	router.overlay.mu.Lock()
	router.startPanelRefresh("dev")
	router.overlay.mu.Unlock()

	time.Sleep(panelRefreshInterval + 200*time.Millisecond)

	// Content should remain unchanged (no redraw needed)
	ov.mu.Lock()
	content := ov.panelItems[1].Content
	ov.mu.Unlock()
	assert.Equal(t, "line1\nline2", content)

	// Now change the output
	fetcher.setOutput("line1\nline2\nline3")
	time.Sleep(panelRefreshInterval + 200*time.Millisecond)

	ov.mu.Lock()
	content = ov.panelItems[1].Content
	ov.mu.Unlock()
	assert.Equal(t, "line1\nline2\nline3", content)

	router.stopPanelRefresh()
}

func TestPanelRefreshRespectsScrollOffset(t *testing.T) {
	router, ov, fetcher := newPanelTestRouter(t)

	// User has scrolled up
	ov.mu.Lock()
	ov.panelItems[1].ScrollOffset = 5
	ov.panelItems[1].SetContent("old content")
	ov.mu.Unlock()

	fetcher.setOutput("new content with more lines")

	router.overlay.mu.Lock()
	router.startPanelRefresh("dev")
	router.overlay.mu.Unlock()

	time.Sleep(panelRefreshInterval + 200*time.Millisecond)

	ov.mu.Lock()
	offset := ov.panelItems[1].ScrollOffset
	content := ov.panelItems[1].Content
	ov.mu.Unlock()

	// Content should be updated
	assert.Equal(t, "new content with more lines", content)
	// ScrollOffset should be preserved since user was not at bottom
	assert.Equal(t, 5, offset)

	router.stopPanelRefresh()
}

func TestPanelRefreshAutoScrollsWhenAtBottom(t *testing.T) {
	router, ov, fetcher := newPanelTestRouter(t)

	// User is at bottom (ScrollOffset == 0)
	ov.mu.Lock()
	ov.panelItems[1].ScrollOffset = 0
	ov.panelItems[1].SetContent("old")
	ov.mu.Unlock()

	fetcher.setOutput("new output")

	router.overlay.mu.Lock()
	router.startPanelRefresh("dev")
	router.overlay.mu.Unlock()

	time.Sleep(panelRefreshInterval + 200*time.Millisecond)

	ov.mu.Lock()
	offset := ov.panelItems[1].ScrollOffset
	ov.mu.Unlock()

	assert.Equal(t, 0, offset, "should auto-scroll to bottom")

	router.stopPanelRefresh()
}

func TestPanelRefreshStopsOnPanelSwitch(t *testing.T) {
	router, ov, fetcher := newPanelTestRouter(t)

	// Start refresh on process panel
	ov.mu.Lock()
	router.startPanelRefresh("dev")
	ov.mu.Unlock()

	time.Sleep(panelRefreshInterval + 200*time.Millisecond)
	require.GreaterOrEqual(t, fetcher.getCallCount(), 1)

	// Navigate away to overview panel
	ov.mu.Lock()
	ov.panelIndex = 0 // overview
	router.stopPanelRefresh()
	ov.mu.Unlock()

	countAfterStop := fetcher.getCallCount()
	time.Sleep(panelRefreshInterval + 200*time.Millisecond)
	assert.Equal(t, countAfterStop, fetcher.getCallCount())
}

func TestPanelRefreshIgnoresWrongPanel(t *testing.T) {
	router, ov, fetcher := newPanelTestRouter(t)

	fetcher.setOutput("updated output")
	ov.mu.Lock()
	ov.panelItems[1].SetContent("original")
	ov.mu.Unlock()

	// Start refresh for "dev" but switch active panel to overview
	router.overlay.mu.Lock()
	router.startPanelRefresh("dev")
	router.overlay.mu.Unlock()

	// Switch to overview between ticks
	ov.mu.Lock()
	ov.panelIndex = 0
	ov.mu.Unlock()

	time.Sleep(panelRefreshInterval + 200*time.Millisecond)

	// The refresh should have called GetScriptOutput but skipped the update
	// because the active panel is no longer "dev"
	ov.mu.Lock()
	content := ov.panelItems[1].Content
	ov.mu.Unlock()
	assert.Equal(t, "original", content, "should not update non-active panel")

	router.stopPanelRefresh()
}

func TestEagerRefreshOnUnfreeze(t *testing.T) {
	router, ov, fetcher := newPanelTestRouter(t)

	// Set up gate so hideMenu can freeze/unfreeze
	gate := NewOutputGate(&writeRecorder{})
	ov.SetGate(gate)

	// Pre-fill panel with stale content
	ov.mu.Lock()
	ov.panelItems[1].SetContent("stale content")
	ov.mu.Unlock()

	// Update daemon output (simulates new output arriving while frozen)
	fetcher.setOutput("fresh content from daemon")

	// Open and close the overlay to trigger hideMenu -> before-unfreeze callback
	ov.mu.Lock()
	ov.activateOverlay(true)
	ov.panelMode = true
	ov.panelIndex = 1
	ov.mu.Unlock()

	ov.mu.Lock()
	router.exitPanelMode()
	ov.mu.Unlock()

	// Verify the panel content was refreshed from the daemon
	ov.mu.Lock()
	content := ov.panelItems[1].Content
	ov.mu.Unlock()
	assert.Equal(t, "fresh content from daemon", content)

	// Verify the fetcher was called for the correct panel
	fetcher.mu.Lock()
	calls := fetcher.calls
	fetcher.mu.Unlock()
	found := false
	for _, c := range calls {
		if c == "dev" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected eager refresh call for 'dev' panel")
}

func TestEagerRefreshSkippedWhenNoProcessPanel(t *testing.T) {
	router, ov, fetcher := newPanelTestRouter(t)

	gate := NewOutputGate(&writeRecorder{})
	ov.SetGate(gate)

	// Set active panel to overview (not a process panel)
	ov.mu.Lock()
	ov.activateOverlay(true)
	ov.panelMode = true
	ov.panelIndex = 0 // overview
	ov.mu.Unlock()

	initialCount := fetcher.getCallCount()

	ov.mu.Lock()
	router.exitPanelMode()
	ov.mu.Unlock()

	// No daemon fetch should have happened for the overview panel
	assert.Equal(t, initialCount, fetcher.getCallCount())
}

func TestEagerRefreshSkippedWithoutFetcher(t *testing.T) {
	rec := &writeRecorder{}
	cfg := DefaultConfig()
	cfg.ShowIndicator = false
	ov := New(rec, 80, 24, cfg)
	router := NewInputRouter(rec, ov, 0x19)
	// No output fetcher set

	gate := NewOutputGate(&writeRecorder{})
	ov.SetGate(gate)

	ov.panelItems = []PanelItem{
		{Type: "overview", Label: "overview"},
		{Type: "process", ID: "dev", Label: "dev"},
	}

	ov.mu.Lock()
	ov.activateOverlay(true)
	ov.panelMode = true
	ov.panelIndex = 1
	ov.mu.Unlock()

	// Should not panic or fail without a fetcher
	ov.mu.Lock()
	router.exitPanelMode()
	ov.mu.Unlock()
}

func TestPanelRefreshDoubleStartReplacesOld(t *testing.T) {
	router, _, fetcher := newPanelTestRouter(t)

	router.overlay.mu.Lock()
	router.startPanelRefresh("dev")
	// Starting again should stop the old one
	router.startPanelRefresh("dev")
	router.overlay.mu.Unlock()

	time.Sleep(panelRefreshInterval + 200*time.Millisecond)
	count := fetcher.getCallCount()
	router.stopPanelRefresh()

	// Should have had at most ~2 calls (one from each goroutine's first tick
	// at most, since the old one is stopped almost immediately)
	time.Sleep(panelRefreshInterval + 200*time.Millisecond)
	finalCount := fetcher.getCallCount()
	assert.Equal(t, count, finalCount, "old goroutine should be stopped")
}
