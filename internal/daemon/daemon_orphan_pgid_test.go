//go:build unix && procisolation

// This file is tagged `procisolation` because every test it contains calls
// startupOrphanPGIDScan directly. That function walks host /proc and issues
// real kill(2) syscalls against any pgid whose leader is dead. The shared
// newCleanupTestDaemon helper leaves DaemonConfig.OrphanScanEnabled at its
// zero value (false) so the rest of the test suite cannot trigger the scan
// via daemon.Start(); each test below flips that field to true on the
// returned daemon to actually exercise the scan. Run via:
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
	"golang.org/x/sys/unix"
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

// chdirToProject sets the test process's cwd to projectDir and restores
// the previous cwd on test cleanup. Under unshare, the compiled test
// binary is NOT pid 1 — the `go test` harness itself is pid 1 and the
// binary runs as its child. chdirToProject therefore only affects the
// test binary's /proc/<pid>/cwd, not pid 1's. Combined with
// prctlSetChildSubreaper below, this lets a reparented orphan stop at
// the test binary (which is itself an agnt-like ancestor for gate
// matching purposes) rather than reaching pid 1.
func chdirToProject(t *testing.T, projectDir string) {
	t.Helper()
	prev, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(projectDir))
	t.Cleanup(func() {
		_ = os.Chdir(prev)
	})
}

// prctlSetChildSubreaper makes the current process the subreaper for any
// descendant whose immediate parent has exited. Without this, when the
// spawnOrphanPGIDFixture shell dies, its sleep child reparents all the
// way up to pid 1 (the `go test` harness under unshare), which has its
// own cwd and cmdline and is not under this test's control.
//
// With subreaper set, reparenting stops at the test binary process
// (which is an immediate ancestor in the unshare PID namespace and
// whose cmdline starts with the compiled daemon.test path — matching
// the daemon-binary branch of cmdlineLooksLikeAgnt). The test can then
// chdir into a temp project directory and the ownership gate will find
// both a matching cmdline AND a matching cwd on the same ancestor.
//
// The flag is process-wide and sticky: once set, it remains set for the
// lifetime of the process. That is acceptable because procisolation
// tests run serially inside a fresh unshare namespace.
func prctlSetChildSubreaper(t *testing.T) {
	t.Helper()
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Skipf("prctl(PR_SET_CHILD_SUBREAPER) unavailable: %v", err)
	}
}

// TestStartupOrphanPGIDScan_ReapsOwnedSession exercises the ownership
// gate's positive path end-to-end. Setup:
//
//  1. Chdir the test process (= pid 1 under unshare) into a temp project
//     directory. This is the ancestor cwd the gate will find when walking
//     from a reparented orphan's live member back up to init.
//  2. Spawn the orphan fixture (setsid shell that forks a sleep and
//     exits). The sleep reparents to pid 1 (this test binary).
//  3. Call startupOrphanPGIDScan with the same project path. Internally
//     the gate:
//     - builds knownProjects = [projectDir]
//     - resolves daemonBinary = this test binary path via os.Executable
//     - walks sleep's parent chain → finds pid 1 (= test binary)
//     - cmdline of pid 1 starts with the test binary path → matches
//     cmdlineLooksLikeAgnt via the daemonBinary prefix branch
//     - cwd of pid 1 is projectDir → matches cwdIsInsideKnownProject
//     Both evidence items hold → pgid is classified as owned and reaped.
//
// If any of the above wiring breaks, this test fails — it is the
// end-to-end assertion that the ownership gate does not leave crash-
// recovery dead in the water for the case we DO care about (this
// daemon's own crashed sessions).
func TestStartupOrphanPGIDScan_ReapsOwnedSession(t *testing.T) {
	// The shared helper leaves OrphanScanEnabled=false; procisolation tests
	// re-enable the scan on the live daemon so this test actually exercises
	// the /proc walk path. Safe here because we run in a PID namespace.

	projectDir := t.TempDir()
	// Become the subreaper BEFORE chdir + fixture spawn so the sleep
	// reparents to us instead of pid 1 (= go test harness).
	prctlSetChildSubreaper(t)
	chdirToProject(t, projectDir)

	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	// Re-open the scan gate that newCleanupTestDaemon closed. Safe here
	// because this file is `procisolation`-tagged and only runs inside a
	// PID namespace (see `make test-isolated`).
	d.config.OrphanScanEnabled = true

	pgid, cleanup := spawnOrphanPGIDFixture(t)
	defer cleanup()

	if err := syscall.Kill(pgid, 0); err == nil {
		t.Skip("fixture leader still alive -- test environment race")
	}
	if len(platform.MembersOfPGID(pgid)) == 0 {
		t.Skip("fixture pgid has no members -- test environment race")
	}

	killed := d.startupOrphanPGIDScan(projectDir)
	if killed < 1 {
		// Dump diagnostic evidence to help debug any ancestor-chain
		// miss on a weird namespace setup.
		members := platform.MembersOfPGID(pgid)
		var chain []platform.AncestorInfo
		if len(members) > 0 {
			chain = platform.WalkParents(members[0])
		}
		t.Fatalf(
			"startupOrphanPGIDScan killed %d orphan(s), want >= 1\n"+
				"  projectDir=%q\n  pgid=%d members=%v\n  ancestor chain=%+v",
			killed, projectDir, pgid, members, chain,
		)
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

// TestStartupOrphanPGIDScan_SkipsUnownedSession is the end-to-end fix
// for task wJimXk0hzYAC: when the scanning daemon has NO known project
// paths (the default daemon.Start() call site), the scan must NOT reap
// any candidate pgid even if ScanOrphanedPGIDs classifies it as an
// orphan. The skip must be visible in startupErrorStore as a
// startup_orphan_pgid_skipped_unowned entry — no silent failures.
//
// This is the multi-daemon safety guarantee: a fresh test daemon with
// an empty known-projects set cannot reach any host pgid regardless of
// whether its leader is dead.
func TestStartupOrphanPGIDScan_SkipsUnownedSession(t *testing.T) {
	// The shared helper leaves OrphanScanEnabled=false; procisolation tests
	// re-enable the scan on the live daemon so this test actually exercises
	// the /proc walk path. Safe here because we run in a PID namespace.

	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	// Re-open the scan gate that newCleanupTestDaemon closed. Safe here
	// because this file is `procisolation`-tagged and only runs inside a
	// PID namespace (see `make test-isolated`).
	d.config.OrphanScanEnabled = true

	pgid, cleanup := spawnOrphanPGIDFixture(t)
	defer cleanup()

	if err := syscall.Kill(pgid, 0); err == nil {
		t.Skip("fixture leader still alive -- test environment race")
	}
	before := len(platform.MembersOfPGID(pgid))
	if before == 0 {
		t.Skip("fixture pgid has no members -- test environment race")
	}

	// Empty projectPath + empty session registry = empty known projects.
	killed := d.startupOrphanPGIDScan("")
	if killed != 0 {
		t.Fatalf("startupOrphanPGIDScan killed %d, want 0 for unowned pgid", killed)
	}

	// The orphan must still be alive (we did not reap it).
	if got := len(platform.MembersOfPGID(pgid)); got == 0 {
		t.Fatalf("pgid %d was drained despite unowned classification (had %d members, now %d)",
			pgid, before, got)
	}

	// Skip decision must appear in the structured log. This is the
	// "no silent failures" assertion from daemon-architecture.md.
	entries := d.startupErrorStore.Query(StartupLogFilter{
		Level: "info",
		Limit: 100,
	})
	var foundSkip bool
	var skipMsg string
	for _, e := range entries {
		if e.EventType == "startup_orphan_pgid_skipped_unowned" {
			foundSkip = true
			skipMsg = e.Message
			break
		}
	}
	if !foundSkip {
		t.Errorf("expected startup_orphan_pgid_skipped_unowned log entry")
	}
	if foundSkip && skipMsg == "" {
		t.Errorf("skip log entry has empty message")
	}

	// Cleanup the surviving orphan so it does not leak to later tests.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// TestStartupOrphanPGIDScan_ConfigGateDisables verifies that the
// `session.orphan-pgid-scan` config flag, when set to false, prevents
// the scan from running. The orphan is left intact and the decision is
// recorded in the startup error store (not silent).
func TestStartupOrphanPGIDScan_ConfigGateDisables(t *testing.T) {
	// The shared helper leaves OrphanScanEnabled=false; procisolation tests
	// re-enable the scan on the live daemon so this test actually exercises
	// the /proc walk path. Safe here because we run in a PID namespace.
	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	// Re-open the scan gate that newCleanupTestDaemon closed. Safe here
	// because this file is `procisolation`-tagged and only runs inside a
	// PID namespace (see `make test-isolated`).
	d.config.OrphanScanEnabled = true
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
	// The shared helper leaves OrphanScanEnabled=false; procisolation tests
	// re-enable the scan on the live daemon so this test actually exercises
	// the /proc walk path. Safe here because we run in a PID namespace.
	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	// Re-open the scan gate that newCleanupTestDaemon closed. Safe here
	// because this file is `procisolation`-tagged and only runs inside a
	// PID namespace (see `make test-isolated`).
	d.config.OrphanScanEnabled = true

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
