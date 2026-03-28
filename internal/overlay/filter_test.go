package overlay

import (
	"bytes"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestProtectedWriter_PassthroughNormalText(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	input := "Hello, World!\n"
	n, err := pw.Write([]byte(input))

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != len(input) {
		t.Errorf("expected n=%d, got %d", len(input), n)
	}
	if buf.String() != input {
		t.Errorf("expected output %q, got %q", input, buf.String())
	}
}

func TestProtectedWriter_ScrollRegionReset(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Send a scroll region reset: ESC [ r
	// Should be converted to ESC [ 1 ; 23 r (protecting row 24)
	input := "\x1b[r"
	pw.Write([]byte(input))

	expected := "\x1b[1;23r"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}
}

func TestProtectedWriter_ScrollRegionWithParams(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Send scroll region 1-24, should be clamped to 1-23
	input := "\x1b[1;24r"
	pw.Write([]byte(input))

	expected := "\x1b[1;23r"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}
}

func TestProtectedWriter_ScrollRegionAlreadyValid(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Send scroll region 1-20, should pass through as-is (within bounds)
	input := "\x1b[1;20r"
	pw.Write([]byte(input))

	expected := "\x1b[1;20r"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}
}

func TestProtectedWriter_CursorMoveToProtectedRow(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Move cursor to row 24 — passes through (scroll region is the safety net)
	input := "\x1b[24;1H"
	pw.Write([]byte(input))

	if buf.String() != input {
		t.Errorf("expected passthrough %q, got %q", input, buf.String())
	}
	// Cursor position should be tracked
	if row := int(pw.cursorRow.Load()); row != 24 {
		t.Errorf("expected cursor row 24, got %d", row)
	}
}

func TestProtectedWriter_CursorMoveToValidRow(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Move cursor to row 10, should pass through
	input := "\x1b[10;5H"
	pw.Write([]byte(input))

	expected := "\x1b[10;5H"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}
}

func TestProtectedWriter_VPA_VerticalPositionAbsolute(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// VPA to row 24 — passes through (scroll region is the safety net)
	input := "\x1b[24d"
	pw.Write([]byte(input))

	if buf.String() != input {
		t.Errorf("expected passthrough %q, got %q", input, buf.String())
	}
	if row := int(pw.cursorRow.Load()); row != 24 {
		t.Errorf("expected cursor row 24, got %d", row)
	}
}

func TestProtectedWriter_ClearScreenTriggersRedraw(t *testing.T) {
	var buf bytes.Buffer
	var redrawCount int32
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{
		ProtectBottomRows: 1,
		OnRedraw: func() {
			atomic.AddInt32(&redrawCount, 1)
		},
	})
	defer pw.Stop()

	// Clear screen
	input := "\x1b[2J"
	pw.Write([]byte(input))

	// Should have marked redraw as needed
	if !pw.redrawNeeded.Load() {
		t.Error("expected redrawNeeded to be true after clear screen")
	}
}

func TestProtectedWriter_AltScreenPassthroughAndTracked(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Enter alt screen - should pass through and InAltScreen tracked
	pw.Write([]byte("\x1b[?1049h"))
	if buf.String() != "\x1b[?1049h" {
		t.Errorf("expected alt screen enter to pass through, got %q", buf.String())
	}
	if !pw.InAltScreen() {
		t.Error("expected InAltScreen() to return true after entering alt screen")
	}

	buf.Reset()

	// Exit alt screen - should pass through and InAltScreen tracked
	pw.Write([]byte("\x1b[?1049l"))
	if buf.String() != "\x1b[?1049l" {
		t.Errorf("expected alt screen exit to pass through, got %q", buf.String())
	}
	if pw.InAltScreen() {
		t.Error("expected InAltScreen() to return false after exiting alt screen")
	}

	buf.Reset()

	// Older alt screen sequences should also pass through and be tracked
	pw.Write([]byte("\x1b[?47h"))
	if buf.String() != "\x1b[?47h" {
		t.Errorf("expected ?47h to pass through, got %q", buf.String())
	}
	if !pw.InAltScreen() {
		t.Error("expected InAltScreen() to return true after ?47h")
	}

	buf.Reset()
	pw.Write([]byte("\x1b[?1047h"))
	if buf.String() != "\x1b[?1047h" {
		t.Errorf("expected ?1047h to pass through, got %q", buf.String())
	}
}

func TestProtectedWriter_PeriodicRedraw(t *testing.T) {
	var buf bytes.Buffer
	var redrawCount atomic.Int32

	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{
		ProtectBottomRows: 1,
		RedrawInterval:    50 * time.Millisecond,
		OnRedraw: func() {
			redrawCount.Add(1)
		},
	})

	// Mark redraw as needed
	pw.RequestRedraw()

	// Wait for periodic redraw
	time.Sleep(100 * time.Millisecond)

	pw.Stop()

	if redrawCount.Load() < 1 {
		t.Errorf("expected at least 1 redraw, got %d", redrawCount.Load())
	}
}

func TestProtectedWriter_SetSize(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Change size
	pw.SetSize(100, 30)

	// Verify scroll region was updated (protectedRow = 30, scrollBottom = 29)
	buf.Reset()
	pw.Write([]byte("\x1b[r"))

	expected := "\x1b[1;29r"
	if buf.String() != expected {
		t.Errorf("expected %q after resize, got %q", expected, buf.String())
	}

	// CUP to row 30 passes through (tracked, not clamped)
	buf.Reset()
	pw.Write([]byte("\x1b[30;1H"))
	if buf.String() != "\x1b[30;1H" {
		t.Errorf("expected passthrough, got %q", buf.String())
	}
	if row := int(pw.cursorRow.Load()); row != 30 {
		t.Errorf("expected cursor row 30, got %d", row)
	}
}

func TestProtectedWriter_EnforceScrollRegion(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	pw.EnforceScrollRegion()

	// DECSC + DECSTBM + DECRC: save cursor, set scroll region, restore cursor
	expected := "\x1b7\x1b[1;23r\x1b8"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}
}

func TestProtectedWriter_MixedContent(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Mix of text and escape sequences
	input := "Hello\x1b[24;1HWorld\x1b[rDone"
	pw.Write([]byte(input))

	// CUP passes through, scroll region reset is still modified
	expected := "Hello\x1b[24;1HWorld\x1b[1;23rDone"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}
}

func TestProtectedWriter_OSCSequence(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// OSC sequence (set window title) - should pass through
	input := "\x1b]0;My Title\x07"
	pw.Write([]byte(input))

	if buf.String() != input {
		t.Errorf("expected OSC to pass through, got %q", buf.String())
	}
}

func TestProtectedWriter_OSCWithST(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// OSC sequence with ST terminator (ESC \)
	input := "\x1b]0;My Title\x1b\\"
	pw.Write([]byte(input))

	if buf.String() != input {
		t.Errorf("expected OSC to pass through, got %q", buf.String())
	}
}

func TestProtectedWriter_SGRSequence(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// SGR (Set Graphics Rendition) - should pass through
	input := "\x1b[1;31mRed Bold\x1b[0m"
	pw.Write([]byte(input))

	if buf.String() != input {
		t.Errorf("expected SGR to pass through, got %q", buf.String())
	}
}

func TestProtectedWriter_CursorDown_TrackedPastProtected(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Set cursor position to row 22 first
	pw.Write([]byte("\x1b[22;1H"))
	buf.Reset()

	// Move down 5 rows — passes through, cursor tracked at 27
	pw.Write([]byte("\x1b[5B"))

	if buf.String() != "\x1b[5B" {
		t.Errorf("expected passthrough \\x1b[5B, got %q", buf.String())
	}
	if row := int(pw.cursorRow.Load()); row != 27 {
		t.Errorf("expected cursor row 27, got %d", row)
	}
}

func TestProtectedWriter_MultipleProtectedRows(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 3})
	defer pw.Stop()

	// With 3 protected rows, scroll region should be 1-21
	input := "\x1b[r"
	pw.Write([]byte(input))

	expected := "\x1b[1;21r"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}

	// CUP to row 22 passes through (tracked, not clamped)
	buf.Reset()
	pw.Write([]byte("\x1b[22;1H"))

	if buf.String() != "\x1b[22;1H" {
		t.Errorf("expected passthrough, got %q", buf.String())
	}
	if row := int(pw.cursorRow.Load()); row != 22 {
		t.Errorf("expected cursor row 22, got %d", row)
	}
}

func TestProtectedWriter_PrivateModePassthrough(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Private mode sequences (like cursor visibility) should pass through
	tests := []string{
		"\x1b[?25h", // Show cursor
		"\x1b[?25l", // Hide cursor
		"\x1b[?7h",  // Enable auto-wrap
		"\x1b[?7l",  // Disable auto-wrap
	}

	for _, input := range tests {
		buf.Reset()
		pw.Write([]byte(input))
		if buf.String() != input {
			t.Errorf("expected %q to pass through, got %q", input, buf.String())
		}
	}
}

func TestProtectedWriter_ConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Write from multiple goroutines
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(n int) {
			for j := 0; j < 100; j++ {
				pw.Write([]byte(fmt.Sprintf("goroutine %d write %d\n", n, j)))
			}
			done <- struct{}{}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Just verify no panic/deadlock - actual content verification is complex
	if buf.Len() == 0 {
		t.Error("expected some output")
	}
}

func TestProtectedWriter_IncrementalEscapeSequence(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Send escape sequence in pieces (simulating network chunking)
	pw.Write([]byte("\x1b"))
	pw.Write([]byte("["))
	pw.Write([]byte("24"))
	pw.Write([]byte(";"))
	pw.Write([]byte("1"))
	pw.Write([]byte("H"))

	// CUP passes through as-is, cursor tracked at row 24
	expected := "\x1b[24;1H"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}
	if row := int(pw.cursorRow.Load()); row != 24 {
		t.Errorf("expected cursor row 24, got %d", row)
	}
}

func TestProtectedWriter_DA1ResponseSuppressed(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// DA1 response (CSI ? Ps c) should be suppressed — it's a terminal-to-app
	// message that can leak when the PTY echoes before the child disables echo.
	da1 := "\x1b[?61;4;6;7;14;21;22;23;24;28;32;42;52c"
	pw.Write([]byte(da1))
	if buf.String() != "" {
		t.Errorf("DA1 response should be suppressed, got %q", buf.String())
	}

	// DA1 with simpler params
	buf.Reset()
	pw.Write([]byte("\x1b[?1;2c"))
	if buf.String() != "" {
		t.Errorf("Simple DA1 response should be suppressed, got %q", buf.String())
	}

	// Non-private CSI c (DA query, no ?) should pass through
	buf.Reset()
	csiC := "\x1b[c"
	pw.Write([]byte(csiC))
	if buf.String() != csiC {
		t.Errorf("DA query should pass through, expected %q got %q", csiC, buf.String())
	}

	// ESC c (RIS - reset) should still pass through (it's not a CSI sequence)
	buf.Reset()
	ris := "\x1b" + "c"
	pw.Write([]byte(ris))
	if buf.String() != ris {
		t.Errorf("RIS should pass through, expected %q got %q", ris, buf.String())
	}
}

func TestProtectedWriter_ResetSequence(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// RIS (Reset to Initial State) - should pass through and trigger redraw
	input := "\x1b" + "c"
	pw.Write([]byte(input))

	if buf.String() != input {
		t.Errorf("expected reset to pass through, got %q", buf.String())
	}
	if !pw.redrawNeeded.Load() {
		t.Error("expected redrawNeeded to be true after reset")
	}
}
