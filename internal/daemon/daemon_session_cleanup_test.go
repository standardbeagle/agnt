//go:build unix

package daemon

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCleanupTestDaemon constructs a started daemon configured with a tight
// cleanup grace period so deferred-cleanup tests don't drag.
func newCleanupTestDaemon(t *testing.T, grace time.Duration) *Daemon {
	t.Helper()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	d := New(DaemonConfig{
		SocketPath:         sockPath,
		MaxClients:         10,
		WriteTimeout:       5 * time.Second,
		CleanupGracePeriod: grace,
	})

	require.NoError(t, d.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})

	return d
}

// blockingAutostartFn returns an AutostartStartFunc that blocks until its
// context is cancelled. cancelled is set to 1 if the goroutine observed
// ctx.Done() before its hard timeout.
func blockingAutostartFn(cancelled *atomic.Int32) AutostartStartFunc {
	return func(ctx context.Context, _ chan<- AutostartProgress) *AutostartResult {
		select {
		case <-ctx.Done():
			cancelled.Store(1)
		case <-time.After(10 * time.Second):
			// Hard timeout — guards the test from hanging if cancellation
			// wiring is broken.
		}
		return &AutostartResult{}
	}
}

// registerSession is a small helper that puts a session into the registry
// without going through the full hub handshake.
func registerSession(t *testing.T, d *Daemon, code, projectPath string) {
	t.Helper()
	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:        code,
		ProjectPath: projectPath,
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}))
}

// TestCleanupSessionResources_CancelsAutostart_LastSession verifies that the
// running autostart context is cancelled when the last session for a project
// is cleaned up.
func TestCleanupSessionResources_CancelsAutostart_LastSession(t *testing.T) {
	d := newCleanupTestDaemon(t, 10*time.Millisecond)

	tmpDir := t.TempDir()
	registerSession(t, d, "session-A", tmpDir)

	var cancelled atomic.Int32
	handle := d.autostartManager.GetOrCreate(tmpDir, blockingAutostartFn(&cancelled))
	require.NotNil(t, handle)

	// Sanity: handle is still in flight (not done) before cleanup.
	select {
	case <-handle.Done():
		t.Fatal("handle done too early — blockingAutostartFn returned without cancellation")
	case <-time.After(20 * time.Millisecond):
	}

	d.CleanupSessionResources("session-A")

	// Wait for the cancellation to propagate and the worker to return.
	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("autostart handle did not finish after Cancel()")
	}

	assert.Equal(t, int32(1), cancelled.Load(),
		"autostart goroutine should have observed ctx.Done()")
	assert.Nil(t, d.autostartManager.Get(tmpDir),
		"handle should be removed from the manager after last-session cleanup")
}

// TestCleanupSessionResources_KeepsAutostart_OtherSessionsRemain verifies
// that disconnecting a non-last session does NOT cancel the autostart run.
func TestCleanupSessionResources_KeepsAutostart_OtherSessionsRemain(t *testing.T) {
	d := newCleanupTestDaemon(t, 10*time.Millisecond)

	tmpDir := t.TempDir()
	registerSession(t, d, "session-A", tmpDir)
	registerSession(t, d, "session-B", tmpDir)

	var cancelled atomic.Int32
	handle := d.autostartManager.GetOrCreate(tmpDir, blockingAutostartFn(&cancelled))
	require.NotNil(t, handle)

	// Disconnect only one session.
	d.CleanupSessionResources("session-A")

	// Give Cancel a chance to (incorrectly) propagate.
	select {
	case <-handle.Done():
		t.Fatal("autostart handle should NOT be done — another session remains")
	case <-time.After(150 * time.Millisecond):
	}

	assert.Equal(t, int32(0), cancelled.Load(),
		"autostart goroutine must not be cancelled while sessions remain")
	assert.NotNil(t, d.autostartManager.Get(tmpDir),
		"handle should still be tracked in the manager")

	// Cleanly tear down the second session so the test exits without leaks.
	d.CleanupSessionResources("session-B")
	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("autostart handle did not finish after final cleanup")
	}
	assert.Equal(t, int32(1), cancelled.Load(),
		"autostart goroutine should be cancelled after the last session leaves")
}

// TestCleanupSessionResources_CancelsAutostart_AllSessionsLeave verifies
// the multi-session shutdown path: with two sessions, autostart survives the
// first disconnect and is cancelled when the second leaves.
func TestCleanupSessionResources_CancelsAutostart_AllSessionsLeave(t *testing.T) {
	d := newCleanupTestDaemon(t, 10*time.Millisecond)

	tmpDir := t.TempDir()
	registerSession(t, d, "session-A", tmpDir)
	registerSession(t, d, "session-B", tmpDir)

	var cancelled atomic.Int32
	handle := d.autostartManager.GetOrCreate(tmpDir, blockingAutostartFn(&cancelled))
	require.NotNil(t, handle)

	d.CleanupSessionResources("session-A")
	// Still alive after first disconnect.
	require.NotNil(t, d.autostartManager.Get(tmpDir))
	require.Equal(t, int32(0), cancelled.Load())

	d.CleanupSessionResources("session-B")
	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("autostart handle did not finish after the last session left")
	}

	assert.Equal(t, int32(1), cancelled.Load())
	assert.Nil(t, d.autostartManager.Get(tmpDir))
}

// TestCleanupSessionResourcesDeferred_ReconnectWithdrawsCancel verifies that
// a re-registration during the grace period cancels the deferred cleanup,
// and the autostart context is NOT cancelled.
func TestCleanupSessionResourcesDeferred_ReconnectWithdrawsCancel(t *testing.T) {
	const grace = 200 * time.Millisecond
	d := newCleanupTestDaemon(t, grace)

	tmpDir := t.TempDir()
	registerSession(t, d, "session-A", tmpDir)

	var cancelled atomic.Int32
	handle := d.autostartManager.GetOrCreate(tmpDir, blockingAutostartFn(&cancelled))
	require.NotNil(t, handle)

	// Connection drop: schedule deferred cleanup.
	d.CleanupSessionResourcesDeferred("session-A")

	// Reconnect well within the grace period: bump LastSeen and cancel the
	// pending timer (this is what hubHandleSessionRegister does on reconnect).
	time.Sleep(grace / 4)
	if s, ok := d.sessionRegistry.Get("session-A"); ok {
		s.LastSeen = time.Now()
	}
	d.cancelPendingCleanup("session-A")

	// Wait until well past the grace window.
	time.Sleep(grace + 200*time.Millisecond)

	select {
	case <-handle.Done():
		t.Fatal("autostart handle was cancelled — reconnect should have withdrawn the cleanup")
	default:
	}
	assert.Equal(t, int32(0), cancelled.Load(),
		"autostart goroutine must survive a reconnect within the grace period")
	assert.NotNil(t, d.autostartManager.Get(tmpDir),
		"handle should remain in the manager after a reconnect")

	// Final tear-down: explicit cleanup so the test exits cleanly.
	d.CleanupSessionResources("session-A")
	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("autostart handle did not finish after final cleanup")
	}
}

// TestCleanupSessionResources_NoAutostartHandle_NoPanic verifies that the
// cleanup path is robust when no autostart handle was ever created for the
// project (e.g., empty .agnt.kdl, programmatic registration, tests).
func TestCleanupSessionResources_NoAutostartHandle_NoPanic(t *testing.T) {
	d := newCleanupTestDaemon(t, 10*time.Millisecond)

	tmpDir := t.TempDir()
	registerSession(t, d, "session-A", tmpDir)

	// No GetOrCreate — handle never registered.
	require.NotPanics(t, func() {
		d.CleanupSessionResources("session-A")
	})
	assert.Nil(t, d.autostartManager.Get(tmpDir))
}
