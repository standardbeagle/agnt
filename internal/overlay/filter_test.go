package overlay

import (
	"bytes"
	"fmt"
	"strings"
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

	// Move cursor to row 24 (protected), should be clamped to row 23
	input := "\x1b[24;1H"
	pw.Write([]byte(input))

	expected := "\x1b[23;1H"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
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

	// VPA to row 24 (protected), should be clamped
	input := "\x1b[24d"
	pw.Write([]byte(input))

	expected := "\x1b[23d"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
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

func TestProtectedWriter_AltScreenBlocked(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Enter alt screen - should be blocked (not passed through)
	pw.Write([]byte("\x1b[?1049h"))
	if buf.String() != "" {
		t.Errorf("expected alt screen enter to be blocked, got %q", buf.String())
	}

	buf.Reset()

	// Exit alt screen - should also be blocked
	pw.Write([]byte("\x1b[?1049l"))
	if buf.String() != "" {
		t.Errorf("expected alt screen exit to be blocked, got %q", buf.String())
	}

	buf.Reset()

	// Older alt screen sequences should also be blocked
	pw.Write([]byte("\x1b[?47h"))
	pw.Write([]byte("\x1b[?1047h"))
	if buf.String() != "" {
		t.Errorf("expected older alt screen sequences to be blocked, got %q", buf.String())
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

	// Now row 30 is protected, 29 is max valid
	buf.Reset()
	pw.Write([]byte("\x1b[30;1H"))

	expected := "\x1b[29;1H"
	if buf.String() != expected {
		t.Errorf("expected %q after resize, got %q", expected, buf.String())
	}
}

func TestProtectedWriter_EnforceScrollRegion(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	pw.EnforceScrollRegion()

	expected := "\x1b[1;23r"
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

	// Row 24 should be clamped to 23, scroll region reset should be modified
	expected := "Hello\x1b[23;1HWorld\x1b[1;23rDone"
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

func TestProtectedWriter_CursorDown_ClampedAtProtected(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 1})
	defer pw.Stop()

	// Set cursor position to row 22 first
	pw.Write([]byte("\x1b[22;1H"))
	buf.Reset()

	// Now move down 5 rows - should be clamped at row 23
	pw.Write([]byte("\x1b[5B"))

	// Should output cursor position to row 23 instead of down command
	if !strings.Contains(buf.String(), "\x1b[23;") {
		t.Errorf("expected cursor to be clamped to row 23, got %q", buf.String())
	}
}

func TestProtectedWriter_MultipleProtectedRows(t *testing.T) {
	var buf bytes.Buffer
	pw := NewProtectedWriter(&buf, 80, 24, FilterConfig{ProtectBottomRows: 3})
	defer pw.Stop()

	// With 3 protected rows, rows 22-24 are protected, max valid is 21
	input := "\x1b[r"
	pw.Write([]byte(input))

	expected := "\x1b[1;21r"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
	}

	buf.Reset()
	pw.Write([]byte("\x1b[22;1H"))

	expected = "\x1b[21;1H"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
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

	expected := "\x1b[23;1H"
	if buf.String() != expected {
		t.Errorf("expected %q, got %q", expected, buf.String())
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
