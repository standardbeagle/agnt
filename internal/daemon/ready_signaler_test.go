package daemon

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadySignaler_SignalBeforeWait(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()
	rs.SignalReady("proc1")

	err := rs.WaitReady("proc1", 100*time.Millisecond)
	assert.NoError(t, err, "WaitReady should return nil when already signaled")
}

func TestReadySignaler_WaitThenSignal(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()

	done := make(chan error, 1)
	go func() {
		done <- rs.WaitReady("proc1", 2*time.Second)
	}()

	// Give the goroutine time to enter WaitReady.
	time.Sleep(50 * time.Millisecond)
	rs.SignalReady("proc1")

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("WaitReady did not unblock after SignalReady")
	}
}

func TestReadySignaler_Timeout(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()

	start := time.Now()
	err := rs.WaitReady("proc1", 100*time.Millisecond)
	elapsed := time.Since(start)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
	assert.GreaterOrEqual(t, elapsed, 80*time.Millisecond, "should wait at least close to timeout")
}

func TestReadySignaler_IdempotentSignal(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()
	rs.GetOrCreate("proc1")

	// Signal multiple times -- should not panic.
	rs.SignalReady("proc1")
	rs.SignalReady("proc1")
	rs.SignalReady("proc1")

	err := rs.WaitReady("proc1", 100*time.Millisecond)
	assert.NoError(t, err)
}

func TestReadySignaler_GetOrCreateReturnsSameChannel(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()
	ch1 := rs.GetOrCreate("proc1")
	ch2 := rs.GetOrCreate("proc1")
	assert.Equal(t, ch1, ch2, "GetOrCreate should return same channel for same ID")
}

func TestReadySignaler_PortProbeSuccess(t *testing.T) {
	t.Parallel()
	// Start a TCP listener.
	ln, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	rs := NewReadySignaler()
	ctx := context.Background()
	rs.StartPortProbe("proc1", port, ctx)

	err = rs.WaitReady("proc1", 3*time.Second)
	assert.NoError(t, err, "port probe should detect listening port and signal ready")
}

func TestReadySignaler_PortProbeCancel(t *testing.T) {
	t.Parallel()
	// Keep the listener open so the port is definitely reachable.
	// Cancel the context BEFORE starting the probe so signalFromProbe's
	// probeCtx.Err() check blocks the signal regardless of dial timing.
	// (Using a closed port and cancelling mid-flight was racy: net.DialTimeout
	// ignores context, so if the OS reused the ephemeral port before the 100ms
	// cancel the probe would fire and the test would spuriously pass.)
	ln, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	rs := NewReadySignaler()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before probe starts — probe must not signal.

	rs.StartPortProbe("proc1", port, ctx)

	// WaitReady should timeout since probe was cancelled before success.
	err = rs.WaitReady("proc1", 200*time.Millisecond)
	assert.Error(t, err, "should timeout because probe was cancelled")
}

func TestReadySignaler_Cleanup(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()
	ch1 := rs.GetOrCreate("proc1")
	rs.Cleanup("proc1")

	// After cleanup, GetOrCreate should return a new channel.
	ch2 := rs.GetOrCreate("proc1")
	assert.NotEqual(t, fmt.Sprintf("%p", ch1), fmt.Sprintf("%p", ch2),
		"Cleanup should remove old channel so a new one is created")
}

func TestReadySignaler_CleanupStopsProbe(t *testing.T) {
	t.Parallel()
	// Use a port that nothing is listening on.
	ln, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	rs := NewReadySignaler()
	ctx := context.Background()
	rs.StartPortProbe("proc1", port, ctx)

	// Cleanup should cancel the probe.
	rs.Cleanup("proc1")

	// Now start a listener on that port -- probe should NOT signal.
	ln2, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	require.NoError(t, err)
	defer ln2.Close()

	// WaitReady should timeout since probe was cleaned up.
	err = rs.WaitReady("proc1", 800*time.Millisecond)
	assert.Error(t, err, "should timeout because probe was cleaned up before port became available")
}

func TestReadySignaler_CleanupNonexistent(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()
	// Should not panic.
	rs.Cleanup("nonexistent")
}

func TestReadySignaler_PortProbeImmediateDetection(t *testing.T) {
	t.Parallel()
	// Start a TCP listener BEFORE starting the probe.
	ln, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	rs := NewReadySignaler()
	ctx := context.Background()

	start := time.Now()
	rs.StartPortProbe("proc1", port, ctx)

	err = rs.WaitReady("proc1", 3*time.Second)
	elapsed := time.Since(start)

	assert.NoError(t, err, "probe should detect already-bound port immediately")
	// The immediate check plus dial timeout should resolve well under 2s.
	assert.Less(t, elapsed, 2*time.Second, "should resolve quickly via immediate check")
}

func TestReadySignaler_PortProbeReplacesExisting(t *testing.T) {
	t.Parallel()
	// Start a listener.
	ln, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	rs := NewReadySignaler()
	ctx := context.Background()

	// Start probe on a closed port first.
	rs.StartPortProbe("proc1", 1, ctx) // port 1 won't connect

	// Replace with probe on open port.
	rs.StartPortProbe("proc1", port, ctx)

	err = rs.WaitReady("proc1", 3*time.Second)
	assert.NoError(t, err, "replacement probe should detect the open port")
}
