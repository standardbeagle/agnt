package overlay

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderSafeWriter is a thread-safe writer for collecting output in tests.
type renderSafeWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *renderSafeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *renderSafeWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *renderSafeWriter) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Reset()
}

func TestDrawIndicator_ProducesOutput(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)
	status := Status{
		DaemonConnected: ConnectionConnected,
	}

	r.DrawIndicator(status)

	output := buf.String()
	assert.NotEmpty(t, output, "DrawIndicator should produce output")
	assert.Contains(t, output, CursorSave, "should contain cursor save")
	assert.Contains(t, output, CursorRestore, "should contain cursor restore")
}

func TestDrawIndicator_WritesOutsideLock(t *testing.T) {
	// Verify that the lock is NOT held during the write to r.out.
	// We do this by using a writer that tries to call SetSize (which needs the lock).
	// If the lock were held during write, this would deadlock.
	lockProber := &lockProbeWriter{}
	r := NewRenderer(lockProber, 80, 24)
	lockProber.renderer = r

	status := Status{DaemonConnected: ConnectionConnected}

	done := make(chan struct{})
	go func() {
		r.DrawIndicator(status)
		close(done)
	}()

	select {
	case <-done:
		// Success: no deadlock
		assert.True(t, lockProber.lockFree, "write to r.out should happen outside r.mu lock")
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: lock held during terminal write")
	}
}

// lockProbeWriter verifies the renderer's lock is not held when Write is called.
type lockProbeWriter struct {
	renderer *Renderer
	lockFree bool
}

func (w *lockProbeWriter) Write(p []byte) (int, error) {
	// Try to acquire the lock; if it's held this would block (deadlock in test).
	// Use TryLock to detect without blocking.
	if w.renderer.mu.TryLock() {
		w.lockFree = true
		w.renderer.mu.Unlock()
	}
	return len(p), nil
}

func TestDrawIndicator_IdenticalOutput(t *testing.T) {
	// Verify the buffered approach produces the same output as would a direct approach.
	// We compare two renders with the same input.
	var buf1, buf2 bytes.Buffer
	r1 := NewRenderer(&buf1, 80, 24)
	r2 := NewRenderer(&buf2, 80, 24)

	// Reset animation frame counters to same value
	r1.animFrame.Store(0)
	r2.animFrame.Store(0)

	status := Status{
		DaemonConnected: ConnectionConnected,
		Scripts: []ScriptInfo{
			{Name: "dev", State: "running", Command: "npm run dev"},
		},
		Proxies: []ProxyInfo{
			{ID: "dev", ListenAddr: "[::]:12345"},
		},
	}

	r1.DrawIndicator(status)
	r2.DrawIndicator(status)

	assert.Equal(t, buf1.String(), buf2.String(),
		"two renderers with same input should produce identical output")
}

func TestClearIndicator_ProducesOutput(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)

	r.ClearIndicator()

	output := buf.String()
	assert.NotEmpty(t, output)
	assert.Contains(t, output, ClearLine)
}

func TestClearScreen_ProducesOutput(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)

	r.ClearScreen()

	output := buf.String()
	assert.Contains(t, output, ClearScreen)
	assert.Contains(t, output, CursorHome)
	assert.Contains(t, output, ResetScroll)
}

func TestDrawStatusBarMessage_ProducesOutput(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)

	r.DrawStatusBarMessage("Loading...")

	output := buf.String()
	assert.NotEmpty(t, output)
	assert.Contains(t, output, "Loading...")
}

func TestClearStatusBarMessage_ProducesOutput(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)

	r.ClearStatusBarMessage()

	output := buf.String()
	assert.NotEmpty(t, output)
	assert.Contains(t, output, ClearLine)
}

func TestDrawProcessOutput_ProducesOutput(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)

	r.DrawProcessOutput("test-proc", "npm start", "running", "Server started\nListening on :3000")

	output := buf.String()
	assert.NotEmpty(t, output)
	assert.Contains(t, output, "test-proc")
}

func TestConcurrentDrawAndResize(t *testing.T) {
	// Verify no deadlock when Draw and SetSize happen concurrently.
	w := &renderSafeWriter{}
	r := NewRenderer(w, 80, 24)

	status := Status{
		DaemonConnected: ConnectionConnected,
		Scripts: []ScriptInfo{
			{Name: "dev", State: "running", Command: "npm run dev"},
		},
	}

	var wg sync.WaitGroup
	const iterations = 100

	// Goroutine 1: draw indicator repeatedly
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			r.DrawIndicator(status)
		}
	}()

	// Goroutine 2: resize repeatedly
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			r.SetSize(80+i%10, 24+i%5)
		}
	}()

	// Goroutine 3: draw other things
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			r.ClearIndicator()
			r.DrawStatusBarMessage("test")
			r.ClearStatusBarMessage()
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock detected in concurrent draw + resize")
	}

	// Verify some output was produced
	require.NotEmpty(t, w.String())
}

func TestConcurrentDrawMethods(t *testing.T) {
	// Run multiple different Draw methods concurrently.
	// The race detector will catch any data races.
	w := &renderSafeWriter{}
	r := NewRenderer(w, 80, 24)

	status := Status{
		DaemonConnected: ConnectionConnected,
		Scripts: []ScriptInfo{
			{Name: "dev", State: "running", Command: "npm run dev"},
		},
	}
	panels := []PanelItem{{Type: "overview", Label: "overview"}}

	var wg sync.WaitGroup
	const iterations = 50

	draw := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				fn()
			}
		}()
	}

	draw(func() { r.DrawIndicator(status) })
	draw(func() { r.ClearIndicator() })
	draw(func() { r.DrawPanelView(panels, 0, status, 0, false, "", 0, false, OverviewActions{}) })
	draw(func() { r.DrawStatusBarMessage("msg") })
	draw(func() { r.ClearStatusBarMessage() })
	draw(func() { r.SetSize(80, 24) })

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock detected in concurrent draw methods")
	}
}

func TestBufferNotLeakedOnWrite(t *testing.T) {
	// After a Draw call, r.buf should be nil (buffer consumed).
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)

	r.DrawIndicator(Status{})
	assert.Nil(t, r.buf, "buffer should be nil after draw completes")

	r.ClearIndicator()
	assert.Nil(t, r.buf, "buffer should be nil after clear completes")

	r.DrawStatusBarMessage("test")
	assert.Nil(t, r.buf, "buffer should be nil after status bar draw")
}

func TestDrawIndicator_ContainsExpectedContent(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 120, 24)

	status := Status{
		DaemonConnected: ConnectionConnected,
		Scripts: []ScriptInfo{
			{Name: "dev", State: "running", Command: "npm run dev"},
			{Name: "test", State: "failed", Command: "npm test"},
		},
		Proxies: []ProxyInfo{
			{ID: "dev", ListenAddr: "[::]:54321"},
		},
		RecentErrors: []ErrorInfo{
			{Source: "proxy", Message: "500 error", Timestamp: time.Now()},
		},
	}

	r.DrawIndicator(status)

	output := buf.String()
	// Should contain the proxy icon
	assert.True(t, strings.Contains(output, IconProxy),
		"should contain proxy icon")
}

func TestEnterExitAltScreen_ProducesOutput(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf, 80, 24)

	r.EnterAltScreen()
	output := buf.String()
	assert.Contains(t, output, EnterAltScreen)

	buf.Reset()
	r.ExitAltScreen()
	output = buf.String()
	assert.Contains(t, output, ExitAltScreen)
}
