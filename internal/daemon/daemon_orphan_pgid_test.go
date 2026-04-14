//go:build unix && procisolation

// This file is tagged `procisolation` because every test it contains calls
// startupOrphanPGIDScan directly. That function walks host /proc and issues
// real kill(2) syscalls against any pgid whose leader is dead. The daemon
// test package's TestMain sets AGNT_DISABLE_ORPHAN_SCAN=1 so the other tests
// in this package cannot accidentally trigger the scan via daemon.Start();
// each test below must clear that env var via t.Setenv to actually exercise
// the scan. Run via:
//
//     make test-isolated
//
// which uses `unshare --user --pid --mount --fork --mount-proc` to put the
// test binary in its own PID namespace, so kill syscalls cannot reach host
// processes owned by the same uid.

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/stretchr/testify/require"
)

// spawnOrphanPGIDFixture starts a setsid shell that immediately backgrounds
// a `sleep 30` child and exits. After Wait() returns, the shell leader is
// dead but the child still runs, keeping the pgid alive. Structurally
// equivalent to the Slice B target scenario: daemon crashed mid-session,
// PTY child leader got reaped, but `npm run dev &` is still running.
//
// Returns the orphaned pgid and a cleanup func that force-kills any
// survivors on test teardown.
func spawnOrphanPGIDFixture(t *testing.T) (int, func()) {
	t.Helper()

	cmd := exec.Command("sh", "-c", "sleep 30 </dev/null >/dev/null 2>&1 &")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	require.NoError(t, cmd.Start())

	pgid := cmd.Process.Pid

	// The shell returns immediately because of the `&` and no `wait`.
	// Reap it so kill(pgid, 0) will return ESRCH.
	_ = cmd.Wait()

	// Wait for the kernel to actually release the leader PID and the
	// child to be reparented to init. Kernel bookkeeping is usually
	// instant but CI noise can make it flaky otherwise.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if errProbe := syscall.Kill(pgid, 0); errProbe != nil {
			// Leader gone. Need at least one member alive.
			if len(platform.MembersOfPGID(pgid)) > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	cleanup := func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	return pgid, cleanup
}

// TestStartupOrphanPGIDScan_ReapsLeakedSession is the end-to-end
// guarantee for Slice B: a pgid whose leader is dead but whose members
// are still running is found by startupOrphanPGIDScan and drained.
func TestStartupOrphanPGIDScan_ReapsLeakedSession(t *testing.T) {
	t.Setenv("AGNT_DISABLE_ORPHAN_SCAN", "")
	d := newCleanupTestDaemon(t, 10*time.Millisecond)

	pgid, cleanup := spawnOrphanPGIDFixture(t)
	defer cleanup()

	if err := syscall.Kill(pgid, 0); err == nil {
		t.Skip("fixture leader still alive -- test environment race")
	}
	if len(platform.MembersOfPGID(pgid)) == 0 {
		t.Skip("fixture pgid has no members -- test environment race")
	}

	// Scan with an empty project path so the function uses default config
	// (enabled=true). This is exactly what daemon.Start() does.
	killed := d.startupOrphanPGIDScan("")
	if killed < 1 {
		t.Fatalf("startupOrphanPGIDScan killed %d orphan(s), want >= 1", killed)
	}

	// Allow up to 3s for the graceful SIGTERM + optional SIGKILL to reap
	// the sleep. Normally instant because sleep responds to SIGTERM.
	waitDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(waitDeadline) {
		if len(platform.MembersOfPGID(pgid)) == 0 {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}

	survivors := platform.MembersOfPGID(pgid)
	t.Fatalf("orphan pgid %d still has %d members after scan: %v",
		pgid, len(survivors), survivors)
}

// TestStartupOrphanPGIDScan_ConfigGateDisables verifies that the
// `session.orphan-pgid-scan` config flag, when set to false, prevents
// the scan from running. The orphan is left intact and the decision is
// recorded in the startup error store (not silent).
func TestStartupOrphanPGIDScan_ConfigGateDisables(t *testing.T) {
	t.Setenv("AGNT_DISABLE_ORPHAN_SCAN", "")
	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	projectDir := t.TempDir()

	// Write a minimal .agnt.kdl with the scan disabled.
	kdlPath := filepath.Join(projectDir, ".agnt.kdl")
	kdlContent := `session {
    orphan-pgid-scan false
}
`
	require.NoError(t, os.WriteFile(kdlPath, []byte(kdlContent), 0o644))

	pgid, cleanup := spawnOrphanPGIDFixture(t)
	defer cleanup()

	if err := syscall.Kill(pgid, 0); err == nil {
		t.Skip("fixture leader still alive -- test environment race")
	}
	before := len(platform.MembersOfPGID(pgid))
	if before == 0 {
		t.Skip("fixture pgid has no members -- test environment race")
	}

	killed := d.startupOrphanPGIDScan(projectDir)
	if killed != 0 {
		t.Fatalf("startupOrphanPGIDScan with gate=false killed %d, want 0", killed)
	}

	// The orphan must still be alive.
	after := len(platform.MembersOfPGID(pgid))
	if after == 0 {
		t.Fatalf("orphan pgid %d drained despite config gate (had %d members, now %d)",
			pgid, before, after)
	}

	// The decision must have been logged (no silent failures rule).
	entries := d.startupErrorStore.Query(StartupLogFilter{
		Level: "info",
		Limit: 50,
	})
	var foundSkip bool
	for _, e := range entries {
		if e.EventType == "startup_orphan_pgid_skipped" {
			foundSkip = true
			break
		}
	}
	if !foundSkip {
		t.Errorf("expected a startup_orphan_pgid_skipped log entry when scan is gated off")
	}
}

// TestStartupOrphanPGIDScan_NoOrphans verifies the happy-path "nothing to
// do" case: the scan runs, finds nothing, and logs an info entry so the
// decision is visible to operators.
func TestStartupOrphanPGIDScan_NoOrphans(t *testing.T) {
	t.Setenv("AGNT_DISABLE_ORPHAN_SCAN", "")
	d := newCleanupTestDaemon(t, 10*time.Millisecond)

	// No fixture -- just ensure the scan runs cleanly and emits an info
	// log when there is nothing to reap. Other tests running in parallel
	// may produce orphans, so we only assert killed >= 0 and the scan log.
	killed := d.startupOrphanPGIDScan("")
	if killed < 0 {
		t.Fatalf("startupOrphanPGIDScan returned negative count %d", killed)
	}

	entries := d.startupErrorStore.Query(StartupLogFilter{
		Level: "info",
		Limit: 50,
	})
	var scanLogged bool
	for _, e := range entries {
		if e.EventType == "startup_orphan_pgid_scan" || e.EventType == "startup_orphan_pgid_killed" {
			scanLogged = true
			break
		}
	}
	if !scanLogged {
		t.Errorf("expected startup_orphan_pgid_scan log entry after scan run")
	}
}
