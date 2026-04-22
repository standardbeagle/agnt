package overlay

import (
	"bytes"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// safeWriter is a thread-safe wrapper around bytes.Buffer for concurrent tests.
type safeWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sw *safeWriter) Write(p []byte) (n int, err error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.buf.Write(p)
}

func (sw *safeWriter) String() string {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.buf.String()
}

// TestActivityMonitorStateTransitions tests that activity state transitions correctly.
func TestActivityMonitorStateTransitions(t *testing.T) {
	var buf bytes.Buffer
	var stateChanges []ActivityState
	var mu sync.Mutex

	cfg := ActivityMonitorConfig{
		IdleTimeout:       100 * time.Millisecond, // Short timeout for tests
		IdleCheckInterval: 10 * time.Millisecond,
		MinActiveBytes:    5,
		OnStateChange: func(state ActivityState) {
			mu.Lock()
			stateChanges = append(stateChanges, state)
			mu.Unlock()
		},
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	// Initially idle
	if am.State() != ActivityIdle {
		t.Errorf("initial state = %v, want ActivityIdle", am.State())
	}

	// Small write should not trigger active (below MinActiveBytes)
	am.Write([]byte("abc"))
	time.Sleep(10 * time.Millisecond)
	if am.State() != ActivityIdle {
		t.Errorf("state after small write = %v, want ActivityIdle", am.State())
	}

	// Larger write should trigger active
	am.Write([]byte("defgh")) // Now at 8 bytes total, > 5
	time.Sleep(10 * time.Millisecond)
	if am.State() != ActivityActive {
		t.Errorf("state after large write = %v, want ActivityActive", am.State())
	}

	// Wait for idle timeout (idle check now runs every 10ms)
	require.Eventually(t, func() bool { return am.State() == ActivityIdle }, 500*time.Millisecond, 5*time.Millisecond)
	if am.State() != ActivityIdle {
		t.Errorf("state after timeout = %v, want ActivityIdle", am.State())
	}

	// Verify callbacks were invoked
	mu.Lock()
	if len(stateChanges) != 2 {
		t.Errorf("state changes = %d, want 2", len(stateChanges))
	} else {
		if stateChanges[0] != ActivityActive {
			t.Errorf("first state change = %v, want ActivityActive", stateChanges[0])
		}
		if stateChanges[1] != ActivityIdle {
			t.Errorf("second state change = %v, want ActivityIdle", stateChanges[1])
		}
	}
	mu.Unlock()
}

// TestActivityMonitorOutputPreview tests that output preview capture works correctly.
func TestActivityMonitorOutputPreview(t *testing.T) {
	var buf bytes.Buffer
	var previewLines [][]string
	var mu sync.Mutex

	cfg := ActivityMonitorConfig{
		IdleTimeout:     100 * time.Millisecond,
		MinActiveBytes:  1, // Low threshold for testing
		PreviewMaxLines: 3,
		PreviewDebounce: 50 * time.Millisecond,
		OnOutputPreview: func(lines []string) {
			mu.Lock()
			copied := make([]string, len(lines))
			copy(copied, lines)
			previewLines = append(previewLines, copied)
			mu.Unlock()
		},
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	// Write multiple lines
	am.Write([]byte("line 1\n"))
	am.Write([]byte("line 2\n"))
	am.Write([]byte("line 3\n"))
	am.Write([]byte("line 4\n"))
	am.Write([]byte("line 5\n"))

	// Wait for debounce
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if len(previewLines) == 0 {
		t.Error("expected at least one preview callback")
	} else {
		// Check the last preview has at most 3 lines
		lastPreview := previewLines[len(previewLines)-1]
		if len(lastPreview) > 3 {
			t.Errorf("last preview has %d lines, want <= 3", len(lastPreview))
		}
		// Should contain the most recent lines
		if len(lastPreview) >= 1 && lastPreview[len(lastPreview)-1] != "line 5" {
			t.Errorf("last line = %q, want %q", lastPreview[len(lastPreview)-1], "line 5")
		}
	}
	mu.Unlock()
}

// TestActivityMonitorANSIStripping tests that ANSI escape codes are stripped from preview.
func TestActivityMonitorANSIStripping(t *testing.T) {
	var buf bytes.Buffer
	var previewLines []string
	var mu sync.Mutex

	cfg := ActivityMonitorConfig{
		IdleTimeout:     100 * time.Millisecond,
		MinActiveBytes:  1,
		PreviewMaxLines: 5,
		PreviewDebounce: 10 * time.Millisecond,
		OnOutputPreview: func(lines []string) {
			mu.Lock()
			previewLines = make([]string, len(lines))
			copy(previewLines, lines)
			mu.Unlock()
		},
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	// Write line with ANSI escape codes
	am.Write([]byte("\x1b[32mGreen text\x1b[0m\n"))
	am.Write([]byte("\x1b[1;31mBold red\x1b[0m text\n"))
	am.Write([]byte("Normal line\n"))

	// Wait for debounce
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(previewLines) < 3 {
		t.Fatalf("expected 3 preview lines, got %d: %v", len(previewLines), previewLines)
	}

	// Check ANSI codes are stripped
	if previewLines[0] != "Green text" {
		t.Errorf("line 0 = %q, want %q", previewLines[0], "Green text")
	}
	if previewLines[1] != "Bold red text" {
		t.Errorf("line 1 = %q, want %q", previewLines[1], "Bold red text")
	}
	if previewLines[2] != "Normal line" {
		t.Errorf("line 2 = %q, want %q", previewLines[2], "Normal line")
	}
}

// TestActivityMonitorDebounce tests that output preview is debounced.
func TestActivityMonitorDebounce(t *testing.T) {
	var buf bytes.Buffer
	var callCount atomic.Int32

	cfg := ActivityMonitorConfig{
		IdleTimeout:     100 * time.Millisecond,
		MinActiveBytes:  1,
		PreviewMaxLines: 5,
		PreviewDebounce: 100 * time.Millisecond,
		OnOutputPreview: func(lines []string) {
			callCount.Add(1)
		},
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	// Write multiple lines quickly
	for i := 0; i < 20; i++ {
		am.Write([]byte("line\n"))
		time.Sleep(5 * time.Millisecond)
	}

	// Wait for final debounce
	time.Sleep(150 * time.Millisecond)

	// Should have been debounced - not 20 calls
	calls := callCount.Load()
	if calls >= 10 {
		t.Errorf("callback called %d times, expected debouncing to reduce this", calls)
	}
}

// TestActivityMonitorConcurrentWrites tests thread safety of writes.
func TestActivityMonitorConcurrentWrites(t *testing.T) {
	// Use thread-safe writer for concurrent test
	var buf safeWriter
	var stateChanges atomic.Int32

	cfg := ActivityMonitorConfig{
		IdleTimeout:    500 * time.Millisecond,
		MinActiveBytes: 10,
		OnStateChange: func(state ActivityState) {
			stateChanges.Add(1)
		},
		OnOutputPreview: func(lines []string) {
			// Just verify no panic
		},
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				am.Write([]byte("data from goroutine\n"))
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	wg.Wait()

	// Verify no panics and activity was detected
	if am.State() != ActivityActive && am.State() != ActivityIdle {
		t.Errorf("unexpected state: %v", am.State())
	}
}

// TestActivityMonitorWritePassthrough tests that data is passed through to underlying writer.
func TestActivityMonitorWritePassthrough(t *testing.T) {
	var buf bytes.Buffer

	cfg := ActivityMonitorConfig{
		IdleTimeout:    100 * time.Millisecond,
		MinActiveBytes: 1,
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	testData := []byte("Hello, World!\nLine 2\nLine 3\n")
	n, err := am.Write(testData)

	if err != nil {
		t.Errorf("Write error: %v", err)
	}
	if n != len(testData) {
		t.Errorf("Write returned %d, want %d", n, len(testData))
	}
	if buf.String() != string(testData) {
		t.Errorf("Buffer = %q, want %q", buf.String(), string(testData))
	}
}

// TestActivityMonitorLineTruncation tests that long lines are truncated.
func TestActivityMonitorLineTruncation(t *testing.T) {
	var buf bytes.Buffer
	var previewLines []string
	var mu sync.Mutex

	cfg := ActivityMonitorConfig{
		IdleTimeout:     100 * time.Millisecond,
		MinActiveBytes:  1,
		PreviewMaxLines: 5,
		PreviewDebounce: 10 * time.Millisecond,
		OnOutputPreview: func(lines []string) {
			mu.Lock()
			previewLines = make([]string, len(lines))
			copy(previewLines, lines)
			mu.Unlock()
		},
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	// Create a long line (200 chars)
	longLine := make([]byte, 200)
	for i := range longLine {
		longLine[i] = 'x'
	}
	am.Write(append(longLine, '\n'))

	// Wait for debounce
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(previewLines) != 1 {
		t.Fatalf("expected 1 preview line, got %d", len(previewLines))
	}

	// Should be truncated to 120 chars + "..."
	if len(previewLines[0]) != 120 {
		t.Errorf("line length = %d, want 120", len(previewLines[0]))
	}
	if previewLines[0][117:120] != "..." {
		t.Errorf("line should end with '...', got %q", previewLines[0][117:120])
	}
}

// TestActivityMonitorStop tests that Stop properly cleans up.
func TestActivityMonitorStop(t *testing.T) {
	var buf bytes.Buffer

	cfg := ActivityMonitorConfig{
		IdleTimeout:    100 * time.Millisecond,
		MinActiveBytes: 1,
	}

	am := NewActivityMonitor(&buf, cfg)

	// Trigger active state
	am.Write([]byte("some data here\n"))
	time.Sleep(10 * time.Millisecond)

	// Stop should return quickly
	done := make(chan struct{})
	go func() {
		am.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Good
	case <-time.After(time.Second):
		t.Error("Stop did not return within 1 second")
	}

	// Double stop should be safe
	am.Stop()
}

// TestActivityMonitorNilCallbacks tests that nil callbacks don't cause panics.
func TestActivityMonitorNilCallbacks(t *testing.T) {
	var buf bytes.Buffer

	cfg := ActivityMonitorConfig{
		IdleTimeout:     50 * time.Millisecond,
		MinActiveBytes:  1,
		OnStateChange:   nil, // Explicitly nil
		OnOutputPreview: nil,
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	// Should not panic
	am.Write([]byte("test data\n"))
	am.Write([]byte("more data here that is long enough\n"))
	time.Sleep(100 * time.Millisecond) // Wait for idle timeout
}

// TestActivityMonitorAnimationDetection tests that carriage returns are detected as animations.
func TestActivityMonitorAnimationDetection(t *testing.T) {
	var buf bytes.Buffer
	var previewLines []string
	var mu sync.Mutex

	cfg := ActivityMonitorConfig{
		IdleTimeout:       500 * time.Millisecond,
		MinActiveBytes:    1,
		PreviewMaxLines:   5,
		PreviewDebounce:   10 * time.Millisecond,
		AnimationDebounce: 50 * time.Millisecond,
		ShowDoneMessage:   false, // Disable for this test
		OnOutputPreview: func(lines []string) {
			mu.Lock()
			previewLines = make([]string, len(lines))
			copy(previewLines, lines)
			mu.Unlock()
		},
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	// Simulate Ink-style animation: multiple updates to same line via \r
	am.Write([]byte("Loading."))
	am.Write([]byte("\rLoading.."))
	am.Write([]byte("\rLoading..."))
	am.Write([]byte("\rLoading...."))
	am.Write([]byte("\rDone!"))

	// Wait for animation debounce to flush
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Should only have one line - the final state
	if len(previewLines) != 1 {
		t.Errorf("expected 1 preview line (final animation state), got %d: %v", len(previewLines), previewLines)
		return
	}

	if previewLines[0] != "Done!" {
		t.Errorf("expected final line to be %q, got %q", "Done!", previewLines[0])
	}
}

// TestActivityMonitorAnimationWithNewline tests that newline commits animating line.
func TestActivityMonitorAnimationWithNewline(t *testing.T) {
	var buf bytes.Buffer
	var previewLines []string
	var mu sync.Mutex

	cfg := ActivityMonitorConfig{
		IdleTimeout:       500 * time.Millisecond,
		MinActiveBytes:    1,
		PreviewMaxLines:   5,
		PreviewDebounce:   10 * time.Millisecond,
		AnimationDebounce: 100 * time.Millisecond,
		ShowDoneMessage:   false,
		OnOutputPreview: func(lines []string) {
			mu.Lock()
			previewLines = make([]string, len(lines))
			copy(previewLines, lines)
			mu.Unlock()
		},
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	// Animation followed by newline - should commit immediately
	am.Write([]byte("Processing"))
	am.Write([]byte("\rProcessing."))
	am.Write([]byte("\rProcessing.."))
	am.Write([]byte("\rComplete!\n")) // Newline commits the line

	// Wait for debounce
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(previewLines) != 1 {
		t.Errorf("expected 1 preview line, got %d: %v", len(previewLines), previewLines)
		return
	}

	if previewLines[0] != "Complete!" {
		t.Errorf("expected %q, got %q", "Complete!", previewLines[0])
	}
}

// TestActivityMonitorMixedAnimationAndLines tests mixed animated and regular output.
func TestActivityMonitorMixedAnimationAndLines(t *testing.T) {
	var buf bytes.Buffer
	var previewLines []string
	var mu sync.Mutex

	cfg := ActivityMonitorConfig{
		IdleTimeout:       500 * time.Millisecond,
		MinActiveBytes:    1,
		PreviewMaxLines:   10,
		PreviewDebounce:   10 * time.Millisecond,
		AnimationDebounce: 30 * time.Millisecond,
		ShowDoneMessage:   false,
		OnOutputPreview: func(lines []string) {
			mu.Lock()
			previewLines = make([]string, len(lines))
			copy(previewLines, lines)
			mu.Unlock()
		},
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	// Regular line
	am.Write([]byte("Starting task\n"))

	// Animated progress
	am.Write([]byte("Progress: 0%"))
	am.Write([]byte("\rProgress: 50%"))
	am.Write([]byte("\rProgress: 100%\n")) // Commit with newline

	// Another regular line
	am.Write([]byte("Task complete\n"))

	// Wait for debounce
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if len(previewLines) != 3 {
		t.Errorf("expected 3 preview lines, got %d: %v", len(previewLines), previewLines)
		return
	}

	expected := []string{"Starting task", "Progress: 100%", "Task complete"}
	for i, exp := range expected {
		if previewLines[i] != exp {
			t.Errorf("line %d: expected %q, got %q", i, exp, previewLines[i])
		}
	}
}

// TestActivityMonitorDoneMessage tests that done message is added when idle.
func TestActivityMonitorDoneMessage(t *testing.T) {
	var buf bytes.Buffer
	var previewLines []string
	var mu sync.Mutex

	cfg := ActivityMonitorConfig{
		IdleTimeout:       100 * time.Millisecond,
		IdleCheckInterval: 10 * time.Millisecond,
		MinActiveBytes:    1,
		PreviewMaxLines:   5,
		PreviewDebounce:   10 * time.Millisecond,
		AnimationDebounce: 20 * time.Millisecond,
		ShowDoneMessage:   true,
		DoneMessage:       "✓ Done",
		OnOutputPreview: func(lines []string) {
			mu.Lock()
			previewLines = make([]string, len(lines))
			copy(previewLines, lines)
			mu.Unlock()
		},
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	// Write some output
	am.Write([]byte("Working on task\n"))

	// Wait for idle timeout (idle check now runs every 10ms)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(previewLines) >= 2
	}, 500*time.Millisecond, 5*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Should have the line plus done message
	if len(previewLines) < 2 {
		t.Errorf("expected at least 2 preview lines (content + done), got %d: %v", len(previewLines), previewLines)
		return
	}

	lastLine := previewLines[len(previewLines)-1]
	if lastLine != "✓ Done" {
		t.Errorf("expected last line to be %q, got %q", "✓ Done", lastLine)
	}
}

// TestActivityMonitorDoneMessageDisabled tests that done message can be disabled.
func TestActivityMonitorDoneMessageDisabled(t *testing.T) {
	var buf bytes.Buffer
	var previewLines []string
	var mu sync.Mutex

	cfg := ActivityMonitorConfig{
		IdleTimeout:       100 * time.Millisecond,
		IdleCheckInterval: 10 * time.Millisecond,
		MinActiveBytes:    1,
		PreviewMaxLines:   5,
		PreviewDebounce:   10 * time.Millisecond,
		ShowDoneMessage:   false, // Disabled
		OnOutputPreview: func(lines []string) {
			mu.Lock()
			previewLines = make([]string, len(lines))
			copy(previewLines, lines)
			mu.Unlock()
		},
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	// Write some output
	am.Write([]byte("Working\n"))

	// Wait for idle (idle check now runs every 10ms), then a bit more for preview callback
	require.Eventually(t, func() bool { return am.State() == ActivityIdle }, 500*time.Millisecond, 5*time.Millisecond)
	time.Sleep(30 * time.Millisecond) // Allow preview callback to fire

	mu.Lock()
	defer mu.Unlock()

	// Should only have the content line, no done message
	if len(previewLines) != 1 {
		t.Errorf("expected 1 preview line, got %d: %v", len(previewLines), previewLines)
		return
	}

	if previewLines[0] == "✓ Done" {
		t.Error("done message should not appear when disabled")
	}
}

// TestActivityMonitorRapidAnimationDebounce tests that rapid animations are properly debounced.
func TestActivityMonitorRapidAnimationDebounce(t *testing.T) {
	var buf bytes.Buffer
	var callCount atomic.Int32
	var lastLines []string
	var mu sync.Mutex

	cfg := ActivityMonitorConfig{
		IdleTimeout:       500 * time.Millisecond,
		MinActiveBytes:    1,
		PreviewMaxLines:   5,
		PreviewDebounce:   50 * time.Millisecond,
		AnimationDebounce: 100 * time.Millisecond,
		ShowDoneMessage:   false,
		OnOutputPreview: func(lines []string) {
			callCount.Add(1)
			mu.Lock()
			lastLines = make([]string, len(lines))
			copy(lastLines, lines)
			mu.Unlock()
		},
	}

	am := NewActivityMonitor(&buf, cfg)
	defer am.Stop()

	// Simulate spinner - many rapid updates
	frames := []string{"|", "/", "-", "\\", "|", "/", "-", "\\"}
	for _, frame := range frames {
		am.Write([]byte("\r" + frame + " Loading"))
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for animation debounce
	time.Sleep(150 * time.Millisecond)

	// Should not have called callback for every frame
	calls := callCount.Load()
	if calls >= int32(len(frames)) {
		t.Errorf("callback called %d times for %d frames, expected debouncing", calls, len(frames))
	}

	// Should have the final state
	mu.Lock()
	defer mu.Unlock()
	if len(lastLines) != 1 {
		t.Errorf("expected 1 line, got %d: %v", len(lastLines), lastLines)
		return
	}
	if lastLines[0] != "\\ Loading" {
		t.Errorf("expected final frame %q, got %q", "\\ Loading", lastLines[0])
	}
}

// TestActivityMonitorOnOutputLine verifies that the OnOutputLine callback is
// invoked with each complete, cleaned line of output.
func TestActivityMonitorOnOutputLine(t *testing.T) {
	buf := &safeWriter{}
	var lines []string
	var linesMu sync.Mutex

	cfg := DefaultActivityMonitorConfig()
	cfg.OnOutputPreview = func([]string) {} // Required to enable captureForPreview
	cfg.OnOutputLine = func(line string) {
		linesMu.Lock()
		lines = append(lines, line)
		linesMu.Unlock()
	}

	am := NewActivityMonitor(buf, cfg)
	defer am.Stop()

	// Write lines with newlines
	am.Write([]byte("first line\nsecond line\nthird\n"))

	// Allow goroutines to run
	time.Sleep(100 * time.Millisecond)

	linesMu.Lock()
	defer linesMu.Unlock()
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}
	sort.Strings(lines)
	expected := []string{"first line", "second line", "third"}
	sort.Strings(expected)
	for i, exp := range expected {
		if lines[i] != exp {
			t.Errorf("missing expected line %q; got sorted lines: %v", exp, lines)
		}
	}
}

// TestActivityMonitorOnOutputLineStripsANSI verifies ANSI codes are stripped from callback lines.
func TestActivityMonitorOnOutputLineStripsANSI(t *testing.T) {
	buf := &safeWriter{}
	var lines []string
	var linesMu sync.Mutex

	cfg := DefaultActivityMonitorConfig()
	cfg.OnOutputPreview = func([]string) {}
	cfg.OnOutputLine = func(line string) {
		linesMu.Lock()
		lines = append(lines, line)
		linesMu.Unlock()
	}

	am := NewActivityMonitor(buf, cfg)
	defer am.Stop()

	// Write line with ANSI color codes
	am.Write([]byte("\x1b[31mERROR:\x1b[0m something failed\n"))

	time.Sleep(100 * time.Millisecond)

	linesMu.Lock()
	defer linesMu.Unlock()
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %v", len(lines), lines)
	}
	if lines[0] != "ERROR: something failed" {
		t.Errorf("expected 'ERROR: something failed', got %q", lines[0])
	}
}
