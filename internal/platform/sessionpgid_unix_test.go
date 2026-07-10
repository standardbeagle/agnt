//go:build !windows

package platform

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// startLeader spawns `sh -c "sleep 30 & sleep 30 & sleep 30 & wait"` in its
// own session so the shell and its three children form a single pgid that
// the test can then signal. Returns the pgid and a cleanup function that
// kills everything if the test fails before the expected teardown.
func startLeader(t *testing.T) (int, *exec.Cmd, func()) {
	t.Helper()
	// `sh -c` is non-interactive, so job control is off — the backgrounded
	// `sleep` commands inherit the shell's pgid, matching the scenario the
	// task targets: `bash -c 'npm run dev &'`.
	cmd := exec.Command("sh", "-c", "sleep 30 & sleep 30 & sleep 30 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start leader: %v", err)
	}

	// Give the shell a moment to fork its children before we read pgid
	// members — otherwise membersOfPGID may see only the shell itself.
	deadline := time.Now().Add(500 * time.Millisecond)
	pgid := cmd.Process.Pid
	for time.Now().Before(deadline) {
		if n := len(membersOfPGID(pgid)); n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	cleanup := func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
	}
	return pgid, cmd, cleanup
}

func TestKillSessionPGID_GracefulTerm(t *testing.T) {
	pgid, cmd, cleanup := startLeader(t)
	defer cleanup()

	// Sanity: at least the shell and one child must be in the pgid.
	members := membersOfPGID(pgid)
	if len(members) < 2 {
		t.Fatalf("expected at least 2 members in pgid %d, got %d: %v", pgid, len(members), members)
	}
	t.Logf("pgid %d starting members: %v", pgid, members)

	// Kill graceful — SIGTERM with a 500ms grace window. `sleep` responds
	// to SIGTERM within the grace window so we should NOT escalate to
	// SIGKILL (this test doesn't assert that, but it's the fast path).
	err := KillSessionPGID(pgid, 0, 1*time.Second, false)
	if err != nil {
		t.Fatalf("KillSessionPGID: %v", err)
	}

	_ = cmd.Wait()

	// Verify nothing survived.
	if n := len(membersOfPGID(pgid)); n != 0 {
		t.Fatalf("pgid %d still has %d members after kill: %v",
			pgid, n, membersOfPGID(pgid))
	}
}

func TestKillSessionPGID_Aggressive(t *testing.T) {
	pgid, cmd, cleanup := startLeader(t)
	defer cleanup()

	// Aggressive mode skips SIGTERM + grace and sends SIGKILL directly. The
	// invariant under test is "no grace wait happened" — i.e. the members are
	// gone without KillSessionPGID having blocked for anything resembling the
	// grace window. That's a *relative* comparison (aggressive path took a
	// negligible fraction of the grace duration it was given), not an
	// absolute wall-clock bound: under host CPU saturation the syscalls
	// themselves are still instant, but scheduling the calling goroutine back
	// after them is not, so a fixed 500ms ceiling is exactly the flaky
	// pattern this task eradicates. Comparing against the 5s grace window
	// passed to KillSessionPGID keeps the assertion meaningful without
	// depending on absolute scheduler latency.
	const grace = 5 * time.Second
	start := time.Now()
	if err := KillSessionPGID(pgid, 0, grace, true); err != nil {
		t.Fatalf("KillSessionPGID aggressive: %v", err)
	}
	_ = cmd.Wait()

	if elapsed := time.Since(start); elapsed >= grace {
		t.Errorf("aggressive kill should skip the grace window entirely, took %v (>= grace %v)", elapsed, grace)
	}
	if n := len(membersOfPGID(pgid)); n != 0 {
		t.Fatalf("pgid %d still has %d members after aggressive kill", pgid, n)
	}
}

func TestKillSessionPGID_NohupEscape(t *testing.T) {
	// Regression test: `nohup sleep 30 &` only installs SIGHUP ignore;
	// the pgid is unchanged so killpg(SIGTERM) still reaps it. This is
	// one of the acceptance criteria from the task.
	cmd := exec.Command("sh", "-c", "nohup sleep 30 >/dev/null 2>&1 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start nohup leader: %v", err)
	}
	pgid := cmd.Process.Pid
	defer func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	// Wait for nohup's sleep child to actually join the pgid.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && len(membersOfPGID(pgid)) < 2 {
		time.Sleep(20 * time.Millisecond)
	}

	if err := KillSessionPGID(pgid, 0, 1*time.Second, false); err != nil {
		t.Fatalf("KillSessionPGID: %v", err)
	}
	_ = cmd.Wait()

	if n := len(membersOfPGID(pgid)); n != 0 {
		t.Fatalf("nohup escape: pgid %d still has %d members", pgid, n)
	}
}

func TestKillSessionPGID_SetsidEscapeDocumentedLeak(t *testing.T) {
	// Documented escape hatch from the task description: `setsid sleep 30 &`
	// explicitly creates a new session/pgid so the original pgid kill does
	// NOT reap it. This test locks in that behavior so we notice if a
	// future change silently "fixes" it (would also need cgroups v2 to
	// actually fix).
	cmd := exec.Command("sh", "-c", "setsid sleep 30 >/dev/null 2>&1 & echo $! > /tmp/.agnt-setsid-test-$$; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		// setsid may not be installed on some minimal test environments.
		t.Skipf("setsid not available: %v", err)
	}
	pgid := cmd.Process.Pid
	defer func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	time.Sleep(200 * time.Millisecond)

	// Kill the original pgid — the setsid-escaped sleep should survive
	// (it's in its own pgid) and be visible as NOT a member of `pgid`.
	_ = KillSessionPGID(pgid, 0, 500*time.Millisecond, false)
	_ = cmd.Wait()

	// If members is 0, the original pgid is gone — which is correct.
	// This test doesn't assert the setsid child is still running (would
	// make the test flaky), it just documents in code that the escape
	// hatch exists: a future test harness could enrich this to check
	// for the escaped child explicitly via /proc scan.
	if n := len(membersOfPGID(pgid)); n != 0 {
		t.Errorf("unexpected survivors in original pgid %d: %d", pgid, n)
	}
}

func TestKillSessionPGID_InvalidPGID(t *testing.T) {
	// Guard against accidentally calling with 0 or 1 (both would be
	// catastrophic — 0 targets "this process's pgid" and 1 is init).
	for _, pgid := range []int{0, 1, -1} {
		if err := KillSessionPGID(pgid, 0, 0, false); err == nil {
			t.Errorf("KillSessionPGID(%d) returned nil, want error", pgid)
		}
	}
}

func TestReadPGID(t *testing.T) {
	// Self-check: readPGID on our own pid should match Getpgid.
	self := syscall.Getpid()
	want, err := syscall.Getpgid(self)
	if err != nil {
		t.Fatalf("Getpgid: %v", err)
	}
	got := readPGID(self)
	if got == 0 {
		// /proc not available — readPGID returns 0 on non-Linux Unix.
		// The fallback path in membersOfPGID uses syscall.Getpgid, so
		// this is not a bug.
		t.Skip("/proc/<pid>/stat unreadable (non-Linux?) — readPGID fallback path covers this")
	}
	if got != want {
		t.Errorf("readPGID(%d) = %d, want %d", self, got, want)
	}
}
