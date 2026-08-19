package daemon

import (
	"os"
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

func exitedOwnerPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	require.NoError(t, cmd.Wait())
	return pid
}

// A dropped connection is not an unregister. A session with no project path used
// to have its pgid reaped inline in the hub's disconnect callback, with no grace
// window — so an acp one-shot or cooked REPL that reconnected within the window
// had its process tree killed out from under it, while every project-scoped
// session was given that window.
func TestDeferredCleanup_NoProjectPath_HonorsGracePeriod(t *testing.T) {
	// This is the only test in this file whose reap actually fires (the grace
	// expires and the pgid is killed), so it writes a "reaped session" record.
	// Redirect selflog into a test-scoped file — the suite must never append a
	// fabricated cdsp reap record to the user's real ~/.cache/agnt/errors.log.
	readSelflog := selflogSink(t)
	d := newDeferredCleanupDaemon(t, 400*time.Millisecond)
	pgid, exited := startPGIDLeader(t)

	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:        "no-project",
		ProjectPath: "", // acp one-shot / cooked REPL
		SessionPGID: pgid,
		OwnerPID:    exitedOwnerPID(t),
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

	// Unregister completes strictly after the reap records, so the record is
	// present by now — in the test-scoped sink, proving the redirect caught it.
	entries := readSelflog()
	require.Len(t, entries, 1, "the deferred reap of a live pgid must leave one selflog record")
	require.Contains(t, entries[0].Message, "no-project", "record must name the reaped session")
}

// A socket/control-plane outage is not evidence that agnt run exited. While
// the registered wrapper PID is alive, repeated grace periods must never reap
// the agent process group.
func TestDeferredCleanup_LiveOwnerPreventsReap(t *testing.T) {
	d := newDeferredCleanupDaemon(t, 100*time.Millisecond)
	pgid, exited := startPGIDLeader(t)

	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:        "live-owner",
		ProjectPath: t.TempDir(),
		SessionPGID: pgid,
		OwnerPID:    os.Getpid(),
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}))

	d.CleanupSessionResourcesDeferred("live-owner")
	time.Sleep(450 * time.Millisecond) // cross several ownership poll windows

	assert.True(t, stillRunning(exited), "live wrapper owner did not prevent agent pgid reap")
	_, ok := d.sessionRegistry.Get("live-owner")
	assert.True(t, ok, "live wrapper session was unregistered after control-plane loss")
}

// Legacy/unknown ownership fails safe. It may leak until explicit cleanup or
// startup orphan reconciliation, but it must not kill a possibly-live agent.
func TestDeferredCleanup_UnknownOwnerPreventsReap(t *testing.T) {
	d := newDeferredCleanupDaemon(t, 100*time.Millisecond)
	pgid, exited := startPGIDLeader(t)

	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:        "unknown-owner",
		ProjectPath: t.TempDir(),
		SessionPGID: pgid,
		OwnerPID:    0,
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}))

	d.CleanupSessionResourcesDeferred("unknown-owner")
	time.Sleep(350 * time.Millisecond)

	assert.True(t, stillRunning(exited), "unknown owner was treated as proof of wrapper exit")
	_, ok := d.sessionRegistry.Get("unknown-owner")
	assert.True(t, ok)
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
		ProjectPath: "", // exercises the no-project branch, which bypasses doCleanupExact
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
