package readiness

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessReadiness_MarkReadyBeforeWait(t *testing.T) {
	pr := NewProcessReadiness()
	pr.MarkReady("proc1")

	result := pr.Wait("proc1", 100*time.Millisecond)
	assert.Equal(t, RStateReady, result.State)
	assert.NoError(t, result.Err)
}

func TestProcessReadiness_WaitThenMarkReady(t *testing.T) {
	pr := NewProcessReadiness()

	done := make(chan ReadinessResult, 1)
	go func() {
		done <- pr.Wait("proc1", 2*time.Second)
	}()

	time.Sleep(50 * time.Millisecond)
	pr.MarkReady("proc1")

	select {
	case result := <-done:
		assert.Equal(t, RStateReady, result.State)
		assert.NoError(t, result.Err)
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not unblock after MarkReady")
	}
}

func TestProcessReadiness_MarkExitedUnblocksWaiter(t *testing.T) {
	pr := NewProcessReadiness()
	pr.MarkStarting("proc1")

	done := make(chan ReadinessResult, 1)
	go func() {
		done <- pr.Wait("proc1", 5*time.Second)
	}()

	time.Sleep(50 * time.Millisecond)
	pr.MarkExited("proc1")

	select {
	case result := <-done:
		assert.Equal(t, RStateExited, result.State)
		assert.Error(t, result.Err)
	case <-time.After(1 * time.Second):
		t.Fatal("Wait did not unblock after MarkExited — this is the core bug")
	}
}

func TestProcessReadiness_MarkFailedUnblocksWaiter(t *testing.T) {
	pr := NewProcessReadiness()
	pr.MarkStarting("proc1")

	done := make(chan ReadinessResult, 1)
	go func() {
		done <- pr.Wait("proc1", 5*time.Second)
	}()

	time.Sleep(50 * time.Millisecond)
	pr.MarkFailed("proc1", fmt.Errorf("crash: exit code 1"))

	select {
	case result := <-done:
		assert.Equal(t, RStateFailed, result.State)
		assert.Error(t, result.Err)
		assert.Contains(t, result.Err.Error(), "crash")
	case <-time.After(1 * time.Second):
		t.Fatal("Wait did not unblock after MarkFailed")
	}
}

func TestProcessReadiness_Timeout(t *testing.T) {
	pr := NewProcessReadiness()

	start := time.Now()
	result := pr.Wait("proc1", 100*time.Millisecond)
	elapsed := time.Since(start)

	assert.Equal(t, RStateUnknown, result.State)
	assert.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "timeout")
	assert.GreaterOrEqual(t, elapsed, 80*time.Millisecond)
}

func TestProcessReadiness_IdempotentResolve(t *testing.T) {
	pr := NewProcessReadiness()

	// Ready first, then failed — should stay ready.
	pr.MarkReady("proc1")
	pr.MarkFailed("proc1", fmt.Errorf("late failure"))

	result := pr.Wait("proc1", 100*time.Millisecond)
	assert.Equal(t, RStateReady, result.State)
	assert.NoError(t, result.Err)
}

func TestProcessReadiness_MarkExitedNeverTracked(t *testing.T) {
	pr := NewProcessReadiness()
	// Should not panic.
	pr.MarkExited("never-seen")
}

func TestProcessReadiness_PortProbeSuccess(t *testing.T) {
	ln, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	pr := NewProcessReadiness()
	pr.StartPortProbe("proc1", port, context.Background())

	result := pr.Wait("proc1", 3*time.Second)
	assert.Equal(t, RStateReady, result.State)
}

func TestProcessReadiness_PortProbeCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // Not listening.

	// Guard against ephemeral-port reuse: on a busy host the just-freed port can
	// already be held by an unrelated listener, in which case the probe would
	// legitimately connect and this "refused probe" scenario can't be exercised.
	if c, e := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 100*time.Millisecond); e == nil {
		c.Close()
		t.Skipf("port %d was reused by another listener; cannot test the refused-probe path", port)
	}

	pr := NewProcessReadiness()
	ctx, cancel := context.WithCancel(context.Background())
	pr.StartPortProbe("proc1", port, ctx)

	time.Sleep(100 * time.Millisecond)
	cancel()

	result := pr.Wait("proc1", 200*time.Millisecond)
	assert.Equal(t, RStateUnknown, result.State) // timed out
}

func TestProcessReadiness_PortProbeSkipsIfAlreadyTerminal(t *testing.T) {
	pr := NewProcessReadiness()
	pr.MarkReady("proc1")

	// Should not start a probe — already resolved.
	pr.StartPortProbe("proc1", 1, context.Background())

	result := pr.Wait("proc1", 100*time.Millisecond)
	assert.Equal(t, RStateReady, result.State)
}

func TestProcessReadiness_Cleanup(t *testing.T) {
	pr := NewProcessReadiness()
	pr.MarkStarting("proc1")
	pr.Cleanup("proc1")

	// After cleanup, a new Wait creates a fresh entry.
	result := pr.Wait("proc1", 50*time.Millisecond)
	assert.Equal(t, RStateUnknown, result.State) // times out on fresh entry
}

func TestProcessReadiness_CleanupNonexistent(t *testing.T) {
	pr := NewProcessReadiness()
	pr.Cleanup("nonexistent") // should not panic
}

func TestProcessReadiness_MultipleWaiters(t *testing.T) {
	pr := NewProcessReadiness()

	const n = 5
	results := make(chan ReadinessResult, n)
	for i := 0; i < n; i++ {
		go func() {
			results <- pr.Wait("proc1", 2*time.Second)
		}()
	}

	time.Sleep(50 * time.Millisecond)
	pr.MarkReady("proc1")

	for i := 0; i < n; i++ {
		select {
		case r := <-results:
			assert.Equal(t, RStateReady, r.State)
		case <-time.After(2 * time.Second):
			t.Fatalf("Waiter %d did not unblock", i)
		}
	}
}

func TestProcessReadiness_ExitedProcessUnblocksDependent_RegressionTest(t *testing.T) {
	// This is the exact scenario from the bug:
	// - "dev-backend" runs `echo backend` and exits immediately
	// - "dev-frontend" depends on "dev-backend"
	// - frontend's Wait must unblock when backend exits, NOT after 120s timeout
	pr := NewProcessReadiness()

	backendID := "test-project:dev-backend"
	frontendWait := make(chan ReadinessResult, 1)

	// Frontend starts waiting for backend.
	go func() {
		frontendWait <- pr.Wait(backendID, 120*time.Second)
	}()

	time.Sleep(50 * time.Millisecond)

	// Backend starts and exits almost immediately (like `echo backend`).
	pr.MarkStarting(backendID)
	time.Sleep(10 * time.Millisecond)
	pr.MarkExited(backendID)

	// Frontend MUST unblock within 1s, not 120s.
	select {
	case result := <-frontendWait:
		assert.Equal(t, RStateExited, result.State)
		assert.Error(t, result.Err) // exited = error for dependency
	case <-time.After(1 * time.Second):
		t.Fatal("REGRESSION: Wait blocked for 120s on exited dependency — the bug is back")
	}
}

func TestReadinessState_IsTerminal(t *testing.T) {
	assert.False(t, RStateUnknown.IsTerminal())
	assert.False(t, RStateStarting.IsTerminal())
	assert.True(t, RStateReady.IsTerminal())
	assert.True(t, RStateFailed.IsTerminal())
	assert.True(t, RStateExited.IsTerminal())
}

func TestReadinessState_String(t *testing.T) {
	assert.Equal(t, "unknown", RStateUnknown.String())
	assert.Equal(t, "ready", RStateReady.String())
	assert.Equal(t, "failed", RStateFailed.String())
	assert.Equal(t, "exited", RStateExited.String())
}
