package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

type captureWriter struct {
	mu    sync.Mutex
	buf   []byte
	wrote chan struct{}
}

func newCaptureWriter() *captureWriter {
	return &captureWriter{wrote: make(chan struct{}, 1)}
}

func (c *captureWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.buf = append(c.buf, p...)
	c.mu.Unlock()
	select {
	case c.wrote <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (c *captureWriter) string() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}

// TestInjectAfterFirstOutputThenQuiet: the message is written once the agent
// has produced output and then gone quiet for the settle window.
func TestInjectAfterFirstOutputThenQuiet(t *testing.T) {
	pulse := make(chan struct{}, 1)
	cw := newCaptureWriter()
	ctx := context.Background()
	go injectInitialStdin(ctx, cw, []byte("ctx-msg"), pulse, 30*time.Millisecond)

	pulse <- struct{}{} // first sign of life
	select {
	case <-cw.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("never injected after output went quiet")
	}
	if cw.string() != "ctx-msg" {
		t.Fatalf("got %q", cw.string())
	}
}

// TestInjectDefersWhileOutputContinues: continuing output keeps resetting the
// quiet window, so nothing is injected mid-render.
func TestInjectDefersWhileOutputContinues(t *testing.T) {
	pulse := make(chan struct{}, 1)
	cw := newCaptureWriter()
	ctx := context.Background()
	go injectInitialStdin(ctx, cw, []byte("x"), pulse, 60*time.Millisecond)

	// Keep the agent "rendering" for ~150ms (longer than settle).
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case pulse <- struct{}{}:
		default:
		}
		if cw.string() != "" {
			t.Fatal("injected while output was still flowing")
		}
		time.Sleep(15 * time.Millisecond)
	}
	// Now quiet — it should inject.
	select {
	case <-cw.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("never injected after output stopped")
	}
}

// TestInjectSilentAgentFallback: with no output at all, the ceiling delivers
// the message best-effort.
func TestInjectSilentAgentFallback(t *testing.T) {
	old := maxStdinReadyWait
	maxStdinReadyWait = 40 * time.Millisecond
	defer func() { maxStdinReadyWait = old }()

	pulse := make(chan struct{}, 1) // never pulsed
	cw := newCaptureWriter()
	go injectInitialStdin(context.Background(), cw, []byte("silent"), pulse, time.Second)

	select {
	case <-cw.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("silent-agent fallback never injected")
	}
	if cw.string() != "silent" {
		t.Fatalf("got %q", cw.string())
	}
}

// TestInjectCancelledContextNoWrite: a cancelled context suppresses injection.
func TestInjectCancelledContextNoWrite(t *testing.T) {
	pulse := make(chan struct{}, 1)
	cw := newCaptureWriter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	injectInitialStdin(ctx, cw, []byte("nope"), pulse, 10*time.Millisecond)
	if cw.string() != "" {
		t.Fatalf("wrote despite cancelled context: %q", cw.string())
	}
}

// TestActivityPulseNonBlocking: the pulse writer never blocks even when the
// channel is full, and reports the full byte count.
func TestActivityPulseNonBlocking(t *testing.T) {
	ch := make(chan struct{}, 1)
	p := newActivityPulse(ch)
	for i := 0; i < 100; i++ {
		n, err := p.Write([]byte("data"))
		if err != nil || n != 4 {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	// Empty writes do not pulse.
	if n, _ := p.Write(nil); n != 0 {
		t.Fatalf("empty Write should report 0, got %d", n)
	}
}
