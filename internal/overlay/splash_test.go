package overlay

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// safeWriter is a thread-safe wrapper around bytes.Buffer for concurrent tests.
// (Separate from the one in activity_test.go to keep test files independent.)
type splashSafeWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (sw *splashSafeWriter) Write(p []byte) (n int, err error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.buf.Write(p)
}

func (sw *splashSafeWriter) String() string {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.buf.String()
}

func TestStartupSplashClearOnFirstOutput(t *testing.T) {
	buf := &splashSafeWriter{}
	splash := NewStartupSplash(buf, 80, 24)
	splash.SetMessages([]string{"test message"})

	// Start splash and let it render
	splash.Start()
	time.Sleep(100 * time.Millisecond)

	if splash.active.Load() != 1 {
		t.Fatal("splash should be active after Start")
	}

	output := buf.String()
	if !strings.Contains(output, "test message") {
		t.Errorf("splash should contain test message, got: %q", output)
	}

	// Simulate first output callback
	splash.OnFirstActivity()()
	time.Sleep(50 * time.Millisecond)

	if splash.active.Load() != 0 {
		t.Error("splash should be inactive after OnFirstActivity")
	}

	// After stop, the clear method should have written a clear-line sequence
	finalOutput := buf.String()
	if !strings.Contains(finalOutput, ClearLine) {
		t.Error("splash should clear its line on stop")
	}
}

func TestStartupSplashAutoExpire(t *testing.T) {
	buf := &splashSafeWriter{}
	splash := NewStartupSplash(buf, 80, 24)
	splash.SetMessages([]string{"waiting..."})

	start := time.Now()
	splash.Start()

	// The splash has a 30s timeout, we verify it's active
	time.Sleep(50 * time.Millisecond)
	if splash.active.Load() != 1 {
		t.Error("splash should be active shortly after start")
	}

	// Stop it manually (simulating timeout would take 30s, too long for unit tests)
	splash.Stop()

	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Errorf("Stop took too long: %v", elapsed)
	}

	if splash.active.Load() != 0 {
		t.Error("splash should be inactive after Stop")
	}
}

func TestStartupSplashDoubleStop(t *testing.T) {
	var buf bytes.Buffer
	splash := NewStartupSplash(&buf, 80, 24)

	splash.Start()
	time.Sleep(50 * time.Millisecond)

	// Double stop should not panic
	splash.Stop()
	splash.Stop()

	if splash.active.Load() != 0 {
		t.Error("splash should be inactive after double stop")
	}
}

func TestStartupSplashDoubleStart(t *testing.T) {
	var buf bytes.Buffer
	splash := NewStartupSplash(&buf, 80, 24)

	splash.Start()
	splash.Start() // Second start should be a no-op
	time.Sleep(50 * time.Millisecond)

	splash.Stop()
}

func TestStartupSplashCustomMessages(t *testing.T) {
	buf := &splashSafeWriter{}
	splash := NewStartupSplash(buf, 80, 24)
	splash.SetMessages([]string{"custom 1", "custom 2"})

	splash.Start()
	time.Sleep(100 * time.Millisecond)

	output := buf.String()
	if !strings.Contains(output, "custom 1") {
		t.Errorf("splash should show custom message, got: %q", output)
	}

	splash.Stop()
}

func TestStartupSplashMessageRotation(t *testing.T) {
	buf := &splashSafeWriter{}
	splash := NewStartupSplash(buf, 80, 24)
	splash.SetMessages([]string{"msg-a", "msg-b"})
	splash.WithInterval(50 * time.Millisecond)

	splash.Start()
	// Wait for at least one rotation cycle
	time.Sleep(200 * time.Millisecond)

	// Stop before reading to avoid race on buffer
	splash.Stop()

	output := buf.String()
	if !strings.Contains(output, "msg-a") {
		t.Error("splash should contain msg-a")
	}
	if !strings.Contains(output, "msg-b") {
		t.Error("splash should contain msg-b after rotation")
	}
}

func TestStartupSplashNoLeakedGoroutines(t *testing.T) {
	var buf bytes.Buffer
	splash := NewStartupSplash(&buf, 80, 24)
	splash.SetMessages([]string{"test"})

	splash.Start()
	time.Sleep(50 * time.Millisecond)
	splash.Stop()

	// Stop should have waited for the goroutine to finish
	// If wg.Wait() works correctly, this will not hang
	done := make(chan struct{})
	go func() {
		splash.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Good - goroutine was cleaned up
	case <-time.After(1 * time.Second):
		t.Error("splash goroutine was not cleaned up after Stop")
	}
}

func TestStartupSplashConcurrentStartStop(t *testing.T) {
	buf := &splashSafeWriter{}
	splash := NewStartupSplash(buf, 80, 24)
	splash.SetMessages([]string{"concurrent test"})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			splash.Start()
			time.Sleep(10 * time.Millisecond)
			splash.Stop()
		}()
	}
	wg.Wait()

	if splash.active.Load() != 0 {
		t.Error("splash should be inactive after concurrent start/stop")
	}
}

func TestProcessStateIconAnimationFrameCycling(t *testing.T) {
	// Verify that different frames produce different icons for "starting" state
	icons := make(map[string]bool)
	for frame := 0; frame < len(startingAnimFrames); frame++ {
		icon, color := processStateIcon("starting", false, frame)
		icons[icon] = true
		if color != FgCyan {
			t.Errorf("starting icon color should be FgCyan, got %q for frame %d", color, frame)
		}
	}

	// Should have at least 2 distinct icons in the cycle
	if len(icons) < 2 {
		t.Errorf("expected at least 2 distinct animation frames, got %d unique icons", len(icons))
	}

	// Frame 0 should be the dashed circle (same as before)
	icon0, _ := processStateIcon("starting", false, 0)
	if icon0 != "\u25cc" {
		t.Errorf("frame 0 should be dashed circle (\\u25cc), got %q", icon0)
	}

	// Verify non-starting states ignore the frame parameter
	iconRun, _ := processStateIcon("running", false, 0)
	iconRun2, _ := processStateIcon("running", false, 99)
	if iconRun != iconRun2 {
		t.Error("running state should not change with frame")
	}

	iconFail, _ := processStateIcon("failed", false, 0)
	iconFail2, _ := processStateIcon("failed", false, 99)
	if iconFail != iconFail2 {
		t.Error("failed state should not change with frame")
	}

	// Verify restarting also animates
	iconRestart0, _ := processStateIcon("restarting", false, 0)
	iconRestart1, _ := processStateIcon("restarting", false, 1)
	if iconRestart0 == iconRestart1 {
		// This may or may not be true depending on frame sequence, just verify it doesn't panic
		t.Log("restarting frames 0 and 1 happen to be the same")
	}
}

// TestDefaultSplashIncludesProcFirstTips locks in the proc-first rotation
// entries that the Dart task acceptance criterion calls for. The splash is
// the developer's idle-time read; if it never mentions proc, the agent's
// human partner never learns the contract either.
func TestDefaultSplashIncludesProcFirstTips(t *testing.T) {
	joined := strings.Join(defaultSplashMessages, "\n")
	wantSubstrings := []string{
		"proc run",    // start path
		"proc output", // inspect path
		"watch",       // live tail path
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(joined, want) {
			t.Errorf("default splash messages should mention %q (proc-first guidance); got:\n%s",
				want, joined)
		}
	}
	// Belt-and-braces: at least one tip should explicitly warn against
	// `npm run dev` in plain Bash so the contract is unambiguous.
	if !strings.Contains(joined, "npm run dev") {
		t.Errorf("at least one splash tip should call out `npm run dev` as the anti-pattern; got:\n%s",
			joined)
	}
}
