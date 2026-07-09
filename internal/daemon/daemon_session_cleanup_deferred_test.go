package daemon

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDeferredCleanupDaemon builds a daemon with a short cleanup grace window.
func newDeferredCleanupDaemon(t *testing.T, grace time.Duration) *Daemon {
	t.Helper()
	return NewForTest(t, DaemonConfig{
		SocketPath:         filepath.Join(t.TempDir(), "d.sock"),
		CleanupGracePeriod: grace,
	})
}

// startPGIDLeader starts a sleeper in its own process group. It returns the pgid
// and a channel that closes when the process exits.
//
// Liveness is observed by reaping the child, not by kill(-pgid, 0): a killed
// child stays a zombie until waited, and a zombie still answers signal 0 — so a
// signal probe cannot tell "alive" from "reaped".
func startPGIDLeader(t *testing.T) (int, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())

	exited := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	return cmd.Process.Pid, exited
}

func stillRunning(exited <-chan struct{}) bool {
	select {
	case <-exited:
		return false
	default:
		return true
	}
}

// A dropped connection is not an unregister. A session with no project path used
// to have its pgid reaped inline in the hub's disconnect callback, with no grace
// window — so an acp one-shot or cooked REPL that reconnected within the window
// had its process tree killed out from under it, while every project-scoped
// session was given that window.
func TestDeferredCleanup_NoProjectPath_HonorsGracePeriod(t *testing.T) {
	d := newDeferredCleanupDaemon(t, 400*time.Millisecond)
	pgid, exited := startPGIDLeader(t)

	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:        "no-project",
		ProjectPath: "", // acp one-shot / cooked REPL
		SessionPGID: pgid,
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}))

	d.CleanupSessionResourcesDeferred("no-project")

	// The kill must not have happened yet — that is the whole point of the grace.
	assert.True(t, stillRunning(exited), "pgid reaped immediately, with no grace period")
	_, stillRegistered := d.sessionRegistry.Get("no-project")
	assert.True(t, stillRegistered, "session unregistered before the grace expired")

	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("pgid never reaped after the grace expired")
	}
	assert.Eventually(t, func() bool {
		_, ok := d.sessionRegistry.Get("no-project")
		return !ok
	}, 5*time.Second, 10*time.Millisecond, "session never unregistered")
}

// Reconnect within the grace window cancels the reap.
func TestDeferredCleanup_NoProjectPath_ReconnectCancelsReap(t *testing.T) {
	d := newDeferredCleanupDaemon(t, 300*time.Millisecond)
	pgid, exited := startPGIDLeader(t)

	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:        "reconnector",
		ProjectPath: "",
		SessionPGID: pgid,
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}))

	d.CleanupSessionResourcesDeferred("reconnector")
	// The client comes back, as SessionRegister does on reconnect.
	d.cancelPendingCleanup("reconnector")

	time.Sleep(600 * time.Millisecond) // past the grace window
	assert.True(t, stillRunning(exited), "reconnect did not cancel the reap")
	_, ok := d.sessionRegistry.Get("reconnector")
	assert.True(t, ok, "session unregistered despite reconnecting")
}

// A session-host session survives client disconnect by definition. Its pgid is
// reaped only by SESSION-HOST KILL, by the child exiting, or by the startup
// orphan scan — never by a dropped connection.
func TestDeferredCleanup_SessionHostIsNeverReaped(t *testing.T) {
	d := newDeferredCleanupDaemon(t, 200*time.Millisecond)
	pgid, exited := startPGIDLeader(t)

	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:        "hosted",
		Kind:        SessionKindSessionHost,
		ProjectPath: "", // exercises the no-project branch, which bypasses doCleanup
		SessionPGID: pgid,
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}))

	d.CleanupSessionResourcesDeferred("hosted")

	time.Sleep(500 * time.Millisecond) // well past the grace window
	assert.True(t, stillRunning(exited), "session-host pgid reaped on client disconnect")
	_, ok := d.sessionRegistry.Get("hosted")
	assert.True(t, ok, "session-host session unregistered on client disconnect")
}
