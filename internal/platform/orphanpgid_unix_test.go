//go:build !windows && procisolation

// Tagged `procisolation` because the tests here exercise the system-global
// primitives ScanOrphanedPGIDs + KillSessionPGID directly against host /proc.
// Default `go test ./internal/platform/...` excludes this file; run via:
//
//     make test-isolated
//
// which places the test binary inside a PID namespace via
// `unshare --user --pid --mount --fork --mount-proc` so kill syscalls can
// not reach host processes.

package platform

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// startOrphanedLeader spawns a setsid shell that forks a child and exits
// the leader immediately, leaving the child as the sole member of a pgid
// whose leader PID is dead. Matches the Slice B target scenario: daemon
// restart loses the old session leader but background jobs survive.
//
// Returns the orphaned pgid and a cleanup that kills any survivors if the
// test panics before reaping them.
func startOrphanedLeader(t *testing.T) (int, func()) {
	t.Helper()

	// Strategy: Setsid on the shell so it becomes the session leader with
	// pgid == its pid. We spawn `sleep 30 &` (non-interactive sh has job
	// control off, so the sleep inherits the shell's pgid) and then the
	// shell exits. The sleep survives, reparents to init, but retains
	// the original pgid. Now:
	//   - `kill(pgid, 0)` returns ESRCH (leader reaped after Wait)
	//   - `membersOfPGID(pgid)` still shows the sleep
	// which is exactly what ScanOrphanedPGIDs must detect.
	cmd := exec.Command("sh", "-c", "sleep 30 </dev/null >/dev/null 2>&1 &")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start orphan leader: %v", err)
	}

	pgid := cmd.Process.Pid

	// Wait for the shell to exit (it backgrounds and returns immediately
	// because we did not `wait`). This reaps the leader zombie so
	// kill(pgid, 0) returns ESRCH.
	if err := cmd.Wait(); err != nil {
		// Non-zero exit is fine; the shell may exit with whatever status.
		_ = err
	}

	// Give the kernel a moment to actually release the leader PID and
	// for the sleep child to be fully reparented to init. This also
	// guards against the rare case where /proc has not caught up.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if !isProcessAlive(pgid) && len(membersOfPGID(pgid)) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cleanup := func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	return pgid, cleanup
}

func TestScanOrphanedPGIDs_FindsLeaderDeadPGID(t *testing.T) {
	pgid, cleanup := startOrphanedLeader(t)
	defer cleanup()

	// Sanity preconditions: leader must be dead, at least one member alive.
	if isProcessAlive(pgid) {
		t.Fatalf("precondition: leader pid %d should be dead", pgid)
	}
	if n := len(membersOfPGID(pgid)); n == 0 {
		t.Skipf("precondition: pgid %d has no members (sleep child exited early or was reparented out); test environment limitation", pgid)
	}

	uid := syscall.Getuid()
	orphans := ScanOrphanedPGIDs(uid, nil)

	// We should find our pgid in the results.
	var found *OrphanPGID
	for i := range orphans {
		if orphans[i].PGID == pgid {
			found = &orphans[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("ScanOrphanedPGIDs did not find pgid %d; got %d orphans", pgid, len(orphans))
	}
	if len(found.Members) == 0 {
		t.Fatalf("orphan pgid %d reported with zero members", pgid)
	}
	t.Logf("found orphan pgid %d with members %v", found.PGID, found.Members)

	// Kill via the Slice A primitive and verify the pgid is drained.
	if err := KillSessionPGID(pgid, 0, 1*time.Second, false); err != nil {
		t.Fatalf("KillSessionPGID: %v", err)
	}

	// Give the kernel a brief window to reap.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if len(membersOfPGID(pgid)) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(membersOfPGID(pgid)); n != 0 {
		t.Fatalf("pgid %d still has %d members after KillSessionPGID", pgid, n)
	}
}

func TestScanOrphanedPGIDs_ExcludesLiveLeader(t *testing.T) {
	// A standard setsid'd shell whose leader is STILL running must not
	// appear in the orphan list even though its children share the pgid.
	pgid, cmd, cleanup := startLeader(t)
	defer cleanup()

	if !isProcessAlive(pgid) {
		t.Fatalf("precondition: leader pid %d should be alive", pgid)
	}

	orphans := ScanOrphanedPGIDs(syscall.Getuid(), nil)
	for _, o := range orphans {
		if o.PGID == pgid {
			t.Fatalf("live-leader pgid %d incorrectly flagged as orphan", pgid)
		}
	}

	// Clean up the live session.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	_ = cmd.Wait()
}

func TestScanOrphanedPGIDs_HonorsExcludeSet(t *testing.T) {
	pgid, cleanup := startOrphanedLeader(t)
	defer cleanup()

	// Precondition: the pgid is an orphan (leader dead, members alive).
	if isProcessAlive(pgid) {
		t.Skip("leader pid still alive -- test environment race")
	}
	if len(membersOfPGID(pgid)) == 0 {
		t.Skip("pgid has no members -- test environment race")
	}

	exclude := map[int]bool{pgid: true}
	orphans := ScanOrphanedPGIDs(syscall.Getuid(), exclude)
	for _, o := range orphans {
		if o.PGID == pgid {
			t.Fatalf("pgid %d in exclude set but still returned", pgid)
		}
	}

	// Reap for hygiene.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

func TestScanOrphanedPGIDs_ForeignUIDIsFiltered(t *testing.T) {
	pgid, cleanup := startOrphanedLeader(t)
	defer cleanup()

	if isProcessAlive(pgid) {
		t.Skip("leader pid still alive -- test environment race")
	}
	if len(membersOfPGID(pgid)) == 0 {
		t.Skip("pgid has no members -- test environment race")
	}

	// Pretend some other uid owns the daemon. Our pgid members are owned
	// by the real uid, so the foreign-uid scan must NOT report us.
	fakeUID := syscall.Getuid() + 12345
	orphans := ScanOrphanedPGIDs(fakeUID, nil)
	for _, o := range orphans {
		if o.PGID == pgid {
			t.Fatalf("pgid %d returned for foreign uid %d (owned by %d)",
				pgid, fakeUID, syscall.Getuid())
		}
	}

	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

func TestReadProcUID_Self(t *testing.T) {
	uid, ok := readProcUID(syscall.Getpid())
	if !ok {
		t.Skip("/proc/self/status unreadable on this platform")
	}
	if uid != syscall.Getuid() {
		t.Errorf("readProcUID(self) = %d, want %d", uid, syscall.Getuid())
	}
}

func TestIsProcessAlive_PID1(t *testing.T) {
	// PID 1 is treated as "always alive" by our helper so the scan never
	// treats init as an orphan leader.
	if !isProcessAlive(1) {
		t.Error("isProcessAlive(1) should be true (init)")
	}
}

func TestIsProcessAlive_DeadPID(t *testing.T) {
	// Spawn, wait, then probe -- the pid is definitively dead.
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()

	if isProcessAlive(pid) {
		// Kernel may keep the pid reusable but ESRCH should be reported.
		t.Errorf("isProcessAlive(%d) should be false after Wait", pid)
	}
}
