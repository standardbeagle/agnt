package sshclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
	"golang.org/x/crypto/ssh"
)

// newRealTestClient dials a real (throwaway) *ssh.Client against a
// HardCloseHarness so tests exercising Reconnector.Run's client.Close()
// path (on a failed Attach) have something genuinely closable, rather than
// a zero-value *ssh.Client that panics on Close.
func newRealTestClient(t *testing.T, addr string) *Client {
	t.Helper()
	privPEM, _ := generateClientKey(t)
	signer, err := ssh.ParsePrivateKey(privPEM)
	if err != nil {
		t.Fatalf("parsing test client key: %v", err)
	}
	cfg := &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
	}
	sshClient, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("dialing test harness: %v", err)
	}
	return &Client{SSH: sshClient, stopKeepalive: make(chan struct{}), dead: make(chan struct{})}
}

// --- Criterion 2: backoff growth + jitter bounds, no wall-clock sleeps ---
//
// BackoffConfig.Delay is a pure function (no sleeping, no wall-clock
// reads), so every assertion below runs at microsecond scale purely by
// arithmetic — there is no load-sensitive real-time primary invariant here,
// per the flake-eradication lesson this task brief calls out.

func TestBackoffConfig_Delay_MultiplicativeGrowth(t *testing.T) {
	cfg := BackoffConfig{
		BaseDelay: time.Microsecond,
		MaxDelay:  1 * time.Second,               // far above anything this test reaches
		Jitter:    func() float64 { return 0.5 }, // fixed jitter -> factor 1.0
	}

	prev := time.Duration(0)
	for attempt := 1; attempt <= 6; attempt++ {
		got := cfg.Delay(attempt)
		want := time.Microsecond * time.Duration(1<<uint(attempt-1))
		if got != want {
			t.Fatalf("attempt %d: Delay = %s, want %s", attempt, got, want)
		}
		if attempt > 1 && got != prev*2 {
			t.Fatalf("attempt %d: expected exactly 2x previous delay (%s), got %s", attempt, prev*2, got)
		}
		prev = got
	}
}

func TestBackoffConfig_Delay_CapsAtMaxDelay(t *testing.T) {
	cfg := BackoffConfig{
		BaseDelay: time.Microsecond,
		MaxDelay:  10 * time.Microsecond,
		Jitter:    func() float64 { return 0.5 }, // factor 1.0
	}
	// 2^9 microseconds = 512us, far past the 10us cap.
	got := cfg.Delay(10)
	if got != 10*time.Microsecond {
		t.Fatalf("Delay(10) = %s, want capped at %s", got, 10*time.Microsecond)
	}
}

func TestBackoffConfig_Delay_JitterBounds(t *testing.T) {
	cfg := BackoffConfig{
		BaseDelay: 100 * time.Microsecond,
		MaxDelay:  time.Second,
	}
	base := 100 * time.Microsecond * 4 // attempt 3 -> 2^2 * base
	lower := time.Duration(float64(base) * 0.8)
	upper := time.Duration(float64(base) * 1.2)

	// Sweep the full jitter domain [0,1) densely; every value must land
	// within the documented +/-20% band, and the endpoints must hit the
	// band's edges (proves the mapping isn't accidentally narrower).
	sawLower, sawUpper := false, false
	for i := 0; i <= 1000; i++ {
		j := float64(i) / 1000
		cfg.Jitter = func() float64 { return j }
		got := cfg.Delay(3)
		if got < lower || got > upper {
			t.Fatalf("jitter=%v: Delay = %s, out of bounds [%s, %s]", j, got, lower, upper)
		}
		if got == lower {
			sawLower = true
		}
		if got == upper {
			sawUpper = true
		}
	}
	if !sawLower {
		t.Errorf("jitter sweep never reached the lower bound %s", lower)
	}
	if !sawUpper {
		t.Errorf("jitter sweep never reached the upper bound %s", upper)
	}
}

func TestBackoffConfig_Delay_NilJitterIsExact(t *testing.T) {
	cfg := BackoffConfig{BaseDelay: time.Microsecond, MaxDelay: time.Second}
	if got, want := cfg.Delay(1), time.Microsecond; got != want {
		t.Fatalf("Delay(1) with nil Jitter = %s, want exactly %s", got, want)
	}
}

// --- Criterion 4: Ctrl-C during RECONNECTING, no goroutine leak ---

func TestInputPump_ForwardsToTargetWhenSet(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	pr, pw := io.Pipe()
	defer pr.Close()

	var interrupted atomic.Bool
	pump := NewInputPump(func() { interrupted.Store(true) })

	src, srcWrite := io.Pipe()
	pump.Start(src)
	pump.SetTarget(pw)

	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 16)
		n, _ := pr.Read(buf)
		done <- buf[:n]
	}()

	if _, err := srcWrite.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case got := <-done:
		if string(got) != "hello" {
			t.Fatalf("forwarded = %q, want %q", got, "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for forwarded bytes")
	}
	if interrupted.Load() {
		t.Fatal("interrupt fired even though a target was set")
	}

	srcWrite.Close()
	pw.Close()
}

func TestInputPump_CtrlCFiresInterruptOnlyWithNoTarget(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	interruptCh := make(chan struct{}, 1)
	pump := NewInputPump(func() {
		select {
		case interruptCh <- struct{}{}:
		default:
		}
	})

	src, srcWrite := io.Pipe()
	pump.Start(src) // no target set -> Ctrl-C path is armed

	if _, err := srcWrite.Write([]byte{0x03}); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-interruptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("interrupt callback never fired for Ctrl-C byte with no target")
	}

	srcWrite.Close() // unblock the pump's Read so the goroutine exits (goleak)
}

// TestInputPump_InterruptCancelsReconnectContext exercises the exact wiring
// cmd/agnt/ssh.go uses: InputPump owns stdin exclusively (so it, not a
// second reader, is what notices Ctrl-C during RECONNECTING), and its
// interrupt callback cancels the context guarding Reconnector.Run — proving
// the two pieces compose without a second stdin owner and without leaking
// the pump goroutine (goleak).
func TestInputPump_InterruptCancelsReconnectContext(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	ctx, cancel := context.WithCancel(context.Background())
	pump := NewInputPump(cancel)

	src, srcWrite := io.Pipe()
	pump.Start(src)
	pump.SetTarget(nil) // RECONNECTING: no live session to forward to

	srcWrite.Write([]byte{0x03})

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Ctrl-C byte never cancelled the reconnect context")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("ctx.Err() = %v, want context.Canceled", ctx.Err())
	}
	srcWrite.Close()
}

// --- Reconnector.Run: dial/attach orchestration ---

func TestReconnector_Run_RetriesOnDialErrorThenSucceeds(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	harness := NewHardCloseHarness(t)
	// Explicit Stop (not just the harness's own t.Cleanup registration):
	// t.Cleanup callbacks run after every deferred func in this test body,
	// but goleak.VerifyNone must observe the harness's accept-loop
	// goroutine already gone — deferring Stop here (after the goleak
	// defer, so it unwinds first, LIFO) achieves that ordering.
	defer harness.Stop()

	var dialAttempts atomic.Int32
	fakeSession := &PTYSession{}

	r := &Reconnector{
		Backoff: BackoffConfig{BaseDelay: time.Microsecond, MaxDelay: time.Millisecond},
		Dial: func(ctx context.Context) (*Client, error) {
			n := dialAttempts.Add(1)
			if n < 3 {
				return nil, fmt.Errorf("dial failed (attempt %d)", n)
			}
			return newRealTestClient(t, harness.Addr()), nil
		},
		Attach: func(ctx context.Context, c *Client) (*PTYSession, error) {
			return fakeSession, nil
		},
	}

	client, session, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	defer client.Close()
	if session != fakeSession {
		t.Fatal("Run did not return the successfully attached session")
	}
	if got := dialAttempts.Load(); got != 3 {
		t.Fatalf("dial attempts = %d, want 3", got)
	}
}

func TestReconnector_Run_SessionMissingIsFatalNoRetry(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	harness := NewHardCloseHarness(t)
	// Explicit Stop (not just the harness's own t.Cleanup registration):
	// t.Cleanup callbacks run after every deferred func in this test body,
	// but goleak.VerifyNone must observe the harness's accept-loop
	// goroutine already gone — deferring Stop here (after the goleak
	// defer, so it unwinds first, LIFO) achieves that ordering.
	defer harness.Stop()

	var attachAttempts atomic.Int32
	r := &Reconnector{
		Backoff: BackoffConfig{BaseDelay: time.Microsecond, MaxDelay: time.Millisecond},
		Dial: func(ctx context.Context) (*Client, error) {
			return newRealTestClient(t, harness.Addr()), nil
		},
		Attach: func(ctx context.Context, c *Client) (*PTYSession, error) {
			attachAttempts.Add(1)
			return nil, fmt.Errorf("session gone: %w", ErrSessionMissing)
		},
	}

	_, _, err := r.Run(context.Background())
	if !errors.Is(err, ErrSessionMissing) {
		t.Fatalf("Run error = %v, want wrapping ErrSessionMissing", err)
	}
	if got := attachAttempts.Load(); got != 1 {
		t.Fatalf("attach attempts = %d, want exactly 1 (no retry after a fatal missing-session error)", got)
	}
}

func TestReconnector_Run_MaxAttemptsExhausted(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var dialAttempts atomic.Int32
	r := &Reconnector{
		Backoff:     BackoffConfig{BaseDelay: time.Microsecond, MaxDelay: time.Millisecond},
		MaxAttempts: 3,
		Dial: func(ctx context.Context) (*Client, error) {
			dialAttempts.Add(1)
			return nil, fmt.Errorf("always fails")
		},
	}

	_, _, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected an error once MaxAttempts is exhausted")
	}
	if got := dialAttempts.Load(); got != 3 {
		t.Fatalf("dial attempts = %d, want 3", got)
	}
}

func TestReconnector_Run_CtxCancelDuringBackoff(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	ctx, cancel := context.WithCancel(context.Background())
	r := &Reconnector{
		// Large base delay relative to the cancel below, so the test
		// deterministically observes the ctx.Done() path in the select
		// rather than racing a real dial.
		Backoff: BackoffConfig{BaseDelay: time.Hour, MaxDelay: time.Hour},
		Dial: func(ctx context.Context) (*Client, error) {
			t.Fatal("Dial should not be called before the backoff timer fires")
			return nil, nil
		},
	}

	go cancel()
	_, _, err := r.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}
