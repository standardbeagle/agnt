//go:build unix

package daemon

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/stretchr/testify/require"
)

// TestCleanupSessionResources_KillsSessionPGID is the integration-level
// guarantee for the task: when a session with a recorded SessionPGID is
// cleaned up, every process that inherited that pgid — including children
// the "coding agent" spawned via non-interactive bash — must be reaped
// before doCleanupExact returns.
//
// The fixture starts a real `sh -c 'sleep 30 & sleep 30 & wait'` in its
// own setsid session (mirroring what creack/pty does for the real PTY
// child), registers it on a fresh daemon as a session, triggers
// CleanupSessionResources, and asserts the pgid has zero surviving members.
func TestCleanupSessionResources_KillsSessionPGID(t *testing.T) {
	// No t.Parallel(): spawns a real setsid sh + sleep group and pgid-kills it; PID/pgid reuse under high concurrency reaps unrelated processes.
	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	tmpDir := t.TempDir()

	// Start a "PTY child stand-in": a sh session with two backgrounded
	// sleepers, all sharing the same pgid. This is the structural
	// equivalent of `agnt run claude` where claude spawns `npm run dev &`
	// via its Bash tool.
	leader := exec.Command("sh", "-c", "sleep 30 & sleep 30 & wait")
	leader.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	require.NoError(t, leader.Start())
	t.Cleanup(func() {
		_ = syscall.Kill(-leader.Process.Pid, syscall.SIGKILL)
		_, _ = leader.Process.Wait()
	})

	pgid := leader.Process.Pid

	// Wait for the shell's children to actually join the pgid. Without
	// this the test can race and see only the shell itself.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		members := platform.MembersOfPGID(pgid)
		if len(members) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.GreaterOrEqual(t, len(platform.MembersOfPGID(pgid)), 2,
		"fixture didn't stand up — pgid has fewer than 2 members")

	// Register the session with the daemon, pointing at the live pgid.
	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:        "test-pgid",
		ProjectPath: tmpDir,
		SessionPGID: pgid,
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}))

	// Trigger cleanup. doCleanupExact's first action is killSessionPGID,
	// which should reap everything.
	d.CleanupSessionResources("test-pgid")

	// Wait for the leader process to be reaped. doCleanupExact uses a 2s
	// graceful timeout; we give a bit more slack here for CI noise.
	waitDeadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(waitDeadline) {
		if len(platform.MembersOfPGID(pgid)) == 0 {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}

	survivors := platform.MembersOfPGID(pgid)
	t.Fatalf("session pgid %d still has %d members after cleanup: %v",
		pgid, len(survivors), survivors)
}

// TestSessionRegister_CarriesSessionPGID is an end-to-end guarantee that
// the SessionPGID field survives the client → protocol → hub handler →
// registry round trip. If the wire format ever regresses, this test
// catches it before the integration test above (which depends on the
// field being honored by the daemon).
func TestSessionRegister_CarriesSessionPGID(t *testing.T) {
	t.Parallel()
	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	tmpDir := t.TempDir()

	client := NewClientWithPath(d.config.SocketPath)
	defer client.Close()

	const wantPGID = 424242
	_, err := client.SessionRegisterWithPGID("pgid-wire", "/tmp/overlay.sock", tmpDir, "test", nil, wantPGID)
	require.NoError(t, err)

	sess, ok := d.sessionRegistry.Get("pgid-wire")
	require.True(t, ok, "session not registered")
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if sess.SessionPGID != wantPGID {
		t.Fatalf("session_pgid round-trip failed: got %d, want %d",
			sess.SessionPGID, wantPGID)
	}
}

// TestCleanupSessionResources_NoPGIDIsNoOp verifies that a session with
// SessionPGID == 0 (e.g. a Windows client, or one registered before
// the field existed) does not panic, error, or affect other processes.
func TestCleanupSessionResources_NoPGIDIsNoOp(t *testing.T) {
	t.Parallel()
	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	tmpDir := t.TempDir()

	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:        "no-pgid",
		ProjectPath: tmpDir,
		SessionPGID: 0, // explicit: no pgid reported
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}))

	// Should return cleanly.
	d.CleanupSessionResources("no-pgid")

	// Session should be unregistered.
	if _, ok := d.sessionRegistry.Get("no-pgid"); ok {
		t.Fatal("session not unregistered after cleanup")
	}
}

// TestSessionRegister_CarriesSessionJobHandle is the wire round-trip
// guarantee for Slice C: the SessionJobHandle field must survive
// client → protocol → hub handler → registry. On Unix (where this
// test runs) the daemon-side killSessionJobObject path no-ops via the
// platform stub, so this test asserts only the wire format — the
// end-to-end kill behavior is exercised by the Windows-tagged
// integration test in internal/platform/sessionjob_windows_test.go.
func TestSessionRegister_CarriesSessionJobHandle(t *testing.T) {
	t.Parallel()
	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	tmpDir := t.TempDir()

	client := NewClientWithPath(d.config.SocketPath)
	defer client.Close()

	const wantHandle uint64 = 0xDEADBEEFCAFEBABE
	_, err := client.SessionRegisterWithContainment(
		"job-wire", "/tmp/overlay.sock", tmpDir, "test", nil, 0, wantHandle,
	)
	require.NoError(t, err)

	sess, ok := d.sessionRegistry.Get("job-wire")
	require.True(t, ok, "session not registered")
	sess.mu.RLock()
	defer sess.mu.RUnlock()
	if sess.SessionJobHandle != wantHandle {
		t.Fatalf("session_job_handle round-trip failed: got 0x%x, want 0x%x",
			sess.SessionJobHandle, wantHandle)
	}
}

// TestCleanupSessionResources_NoJobHandleIsNoOp verifies that
// cleanup does not panic when SessionJobHandle is zero — the Unix
// path where the client never reports a job handle. This is the
// default state for every non-Windows session and must remain a
// no-op.
func TestCleanupSessionResources_NoJobHandleIsNoOp(t *testing.T) {
	t.Parallel()
	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	tmpDir := t.TempDir()

	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:             "no-job",
		ProjectPath:      tmpDir,
		SessionJobHandle: 0, // explicit
		StartedAt:        time.Now(),
		Status:           SessionStatusActive,
		LastSeen:         time.Now(),
	}))

	d.CleanupSessionResources("no-job")

	if _, ok := d.sessionRegistry.Get("no-job"); ok {
		t.Fatal("session not unregistered after cleanup")
	}
}

// TestCleanupSessionResources_StaleJobHandleIsNoOpOnUnix verifies
// that a stale or non-zero SessionJobHandle on Unix does not cause
// cleanup to fail. The platform stub in sessionjob_other.go returns
// nil unconditionally so even a garbage handle value must be safe.
// This guards against accidentally calling Windows-specific APIs
// from Unix code paths.
func TestCleanupSessionResources_StaleJobHandleIsNoOpOnUnix(t *testing.T) {
	t.Parallel()
	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	tmpDir := t.TempDir()

	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:             "stale-job",
		ProjectPath:      tmpDir,
		SessionJobHandle: 0xDEADBEEF, // garbage — stub should ignore
		StartedAt:        time.Now(),
		Status:           SessionStatusActive,
		LastSeen:         time.Now(),
	}))

	// Must not panic or error out.
	d.CleanupSessionResources("stale-job")

	if _, ok := d.sessionRegistry.Get("stale-job"); ok {
		t.Fatal("session not unregistered after cleanup")
	}
}
