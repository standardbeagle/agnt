//go:build unix

package daemon

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// Self-exec helper discriminator. When AGNT_TEST_PORTHOLDER is set on the
// test binary, we skip normal test execution and instead act as a simple
// TCP port holder: bind to the port given in the env var and block until
// killed. This keeps the containment repro hermetic (no python/nc/etc
// dependency) while still exercising a real bind/listen pair.
const portHolderEnv = "AGNT_TEST_PORTHOLDER"

func TestMain(m *testing.M) {
	if port := os.Getenv(portHolderEnv); port != "" {
		runPortHolder(port)
		return
	}
	// Note: there is NO global test-only fence for startupOrphanPGIDScan
	// here. The scan is gated via DaemonConfig.OrphanScanEnabled, which
	// defaults to false (zero value = scan disabled). Any test that uses
	// a literal DaemonConfig{} gets the safe default automatically. Only
	// the procisolation suite (daemon_orphan_pgid_test.go) explicitly
	// enables the scan, and only inside a PID namespace via
	// `make test-isolated`. Production sets OrphanScanEnabled=true in
	// cmd/agnt/daemon.go.
	//
	// See iter 15 (task 6btkGG5QUGTL) for the migration from the legacy
	// AGNT_DISABLE_ORPHAN_SCAN env var fence.
	//
	// goleak.IgnoreCurrent() captures goroutines already running at TestMain
	// start (test infrastructure, go-cli-server internals) to avoid false
	// positives. Only goroutines leaked by individual tests are flagged.
	//
	// Daemon infrastructure goroutines are context-driven but may outlive the
	// 2s stop timeout used in test teardown. These are pre-existing drain-time
	// issues rather than new leaks; suppress them here to prevent false
	// positives while still catching goroutines introduced by future changes.
	goleak.VerifyTestMain(m,
		goleak.IgnoreCurrent(),
		// Daemon infrastructure goroutines: context-driven, may outlive d.Stop(2s).
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*SchedulerStateManager).writeLoop"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*StateManager).writeLoop"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*Scheduler).run"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*URLTracker).scanLoop"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*Daemon).handleProxyEvents"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*Daemon).drainHooks"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*AutostartManager).run"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*DuplicateScanner).scanDuplicates"),
		// Test infrastructure goroutine: stress-test drain helper in alert_hub_stress_test.go.
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*stubStreamDrainer).run.func1"),
		// go-cli-server / go-sdk infrastructure.
		goleak.IgnoreAnyFunction("github.com/standardbeagle/go-cli-server/process.(*FilePIDTracker).descendantScanLoop"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/go-cli-server/client.(*ResilientConn).heartbeatLoop"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/go-cli-server/client.(*AutoStartConn).waitForHub"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/go-sdk/mcp.newIOConn.func1"),
		// Proxy background goroutines: race with test teardown when d.Stop
		// cancels the proxy context but runServer/releaseLoop haven't exited yet.
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/proxy.(*ReorderQueue).releaseLoop"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/proxy.(*ProxyServer).runServer"),
	)
}

// runPortHolder binds the given TCP port on 127.0.0.1 and blocks. Exits
// with a non-zero code on bind failure so the parent test fails fast
// rather than silently passing on a stale port. SIGTERM/SIGKILL from the
// daemon's pgid reaper will tear it down.
func runPortHolder(port string) {
	addr := "127.0.0.1:" + port
	l, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "portholder: bind %s failed: %v\n", addr, err)
		os.Exit(2)
	}
	// Signal readiness to the parent on stdout, then block on Accept.
	// We don't care about accepted connections — only that the port is
	// bound. Accept serves as a "park until killed" primitive.
	fmt.Fprintln(os.Stdout, "READY")
	_ = os.Stdout.Close()
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

// pickFreePort asks the kernel for an ephemeral port, closes the listener,
// and returns the port number. Classic racy pattern, but the window is
// small and the subsequent bind happens within milliseconds.
func pickFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// spawnPortHolderInSession starts the test binary in portholder mode,
// backgrounded inside a fresh setsid session. The returned pgid is the
// shell leader's PID and doubles as the session pgid — exactly the
// structure `agnt run` creates for its PTY child.
//
// The shell uses `exec ... &` so the holder becomes a pgid member and
// the shell returns immediately without waiting, mirroring how a
// coding agent's non-interactive bash spawns background dev servers.
func spawnPortHolderInSession(t *testing.T, port int) int {
	t.Helper()

	self, err := os.Executable()
	require.NoError(t, err)

	// Shell command: background the port holder, sleep long enough to
	// stay in the pgid, and exit the shell without wait so members are
	// truly orphaned from the shell leader's perspective (but still
	// share the pgid). The `exec` on the portholder replaces a subshell
	// so the process tree is flat.
	shellCmd := fmt.Sprintf(
		"(%s >/tmp/.agnt-portholder-%d.out 2>&1 &) ; sleep 30",
		self, port,
	)

	cmd := exec.Command("sh", "-c", shellCmd)
	cmd.Env = append(os.Environ(), portHolderEnv+"="+strconv.Itoa(port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	require.NoError(t, cmd.Start())

	pgid := cmd.Process.Pid

	// Safety net: if the test fails mid-flight, nuke the whole pgid so
	// stray port holders don't linger and break later tests.
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
		_ = os.Remove(fmt.Sprintf("/tmp/.agnt-portholder-%d.out", port))
	})

	// Wait until the port is actually bound. This is the only reliable
	// way to know the holder has finished its Listen() call.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, derr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if derr == nil {
			_ = c.Close()
			return pgid
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("portholder never bound 127.0.0.1:%d", port)
	return 0
}

// portIsFree attempts a bind-and-release on 127.0.0.1:port. Returns true
// iff the bind succeeds, meaning no process is currently holding the port.
func portIsFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// TestSessionContainment_PortReuseAfterCleanup is the end-to-end repro of
// the parent task's failure story:
//
//  1. Session A spawns a backgrounded dev server via non-interactive bash.
//     The dev server inherits the PTY child's session pgid.
//  2. Session A ends (the human Ctrl-C's `agnt run`).
//  3. Session B starts and needs the same port.
//
// Before Slice A, step 3 failed because the dev server was still holding
// the port. After Slice A, CleanupSessionResources reaps the entire
// session pgid — including the backgrounded dev server — before returning.
// This test asserts the port is free within a 3s window and session B
// can bind it.
func TestSessionContainment_PortReuseAfterCleanup(t *testing.T) {
	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	tmpDir := t.TempDir()

	// Step 1: session A starts a backgrounded port holder in its session pgid.
	port := pickFreePort(t)
	pgid := spawnPortHolderInSession(t, port)

	require.False(t, portIsFree(port),
		"fixture didn't take the port — test setup is broken")
	require.GreaterOrEqual(t, len(platform.MembersOfPGID(pgid)), 1,
		"fixture didn't populate the pgid")

	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:        "session-A",
		ProjectPath: tmpDir,
		SessionPGID: pgid,
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}))

	// Step 2: session A disconnects. doCleanup must reap the pgid.
	d.CleanupSessionResources("session-A")

	// Step 3: the port must be free within the pgid kill grace window
	// (2s SIGTERM wait + SIGKILL + kernel socket release). Give 3s of
	// slack for CI noise.
	freedDeadline := time.Now().Add(3 * time.Second)
	var gotFree bool
	for time.Now().Before(freedDeadline) {
		if portIsFree(port) {
			gotFree = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !gotFree {
		survivors := platform.MembersOfPGID(pgid)
		t.Fatalf("port %d still held after session cleanup (pgid=%d survivors=%v)",
			port, pgid, survivors)
	}

	// Session B takes over. Re-bind on the same port must succeed —
	// this is the exact assertion the parent task was chasing.
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err, "session B could not reclaim port %d after session A cleanup", port)
	_ = l.Close()

	// And the pgid itself must be empty, otherwise the kill was partial.
	require.Empty(t, platform.MembersOfPGID(pgid),
		"session pgid still has members after cleanup")
}

// TestSessionContainment_SetsidEscapes is the negative guardrail: a child
// that explicitly calls `setsid` before binding a port gets its own
// session + pgid, so the daemon's pgid reaper cannot (and must not)
// reach it. This documents the accepted escape hatch — if this test
// starts failing, someone has made containment too aggressive and is
// reaping legitimately detached processes.
//
// We don't expect session B to reclaim the port here; the port holder is
// intentionally surviving session A's cleanup. The test only asserts
// that (a) the daemon completes cleanup without error, and (b) the
// escaped process is still alive afterwards.
func TestSessionContainment_SetsidEscapes(t *testing.T) {
	d := newCleanupTestDaemon(t, 10*time.Millisecond)
	tmpDir := t.TempDir()

	// Parent shell gets its own session (containerized A). The child
	// then calls `setsid` again, which creates a *second* session with
	// a fresh pgid that is NOT a descendant of session A's pgid table.
	// This is the classic double-setsid daemonization pattern.
	self, err := os.Executable()
	require.NoError(t, err)

	port := pickFreePort(t)

	// Outer: session A's pgid. Inner (after `setsid`): its own fresh pgid.
	// `exec setsid` replaces the shell subprocess so the escaped PID is
	// easy to read by polling the outfile.
	pidFile := fmt.Sprintf("/tmp/.agnt-setsid-escape-%d.pid", port)
	t.Cleanup(func() { _ = os.Remove(pidFile) })

	shellCmd := fmt.Sprintf(
		"(setsid %s >/tmp/.agnt-setsid-%d.out 2>&1 </dev/null & echo $! >%s) ; sleep 30",
		self, port, pidFile,
	)

	cmd := exec.Command("sh", "-c", shellCmd)
	cmd.Env = append(os.Environ(), portHolderEnv+"="+strconv.Itoa(port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	require.NoError(t, cmd.Start())

	sessionAPGID := cmd.Process.Pid

	// Backstop: always try to reap the escaped process on test exit,
	// even if assertions fail. We read the pidfile to find it.
	t.Cleanup(func() {
		_ = syscall.Kill(-sessionAPGID, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
		if data, readErr := os.ReadFile(pidFile); readErr == nil {
			if escaped, convErr := strconv.Atoi(string(bytesTrim(data))); convErr == nil && escaped > 1 {
				_ = syscall.Kill(escaped, syscall.SIGKILL)
			}
		}
		_ = os.Remove(fmt.Sprintf("/tmp/.agnt-setsid-%d.out", port))
	})

	// Wait for both the escape to happen (pidfile written) AND the port
	// to be bound by the escaped process.
	var escapedPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, readErr := os.ReadFile(pidFile); readErr == nil {
			if pid, convErr := strconv.Atoi(string(bytesTrim(data))); convErr == nil && pid > 1 {
				escapedPID = pid
				if !portIsFree(port) {
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Greater(t, escapedPID, 1, "setsid child never reported its pid")
	require.False(t, portIsFree(port), "setsid child never bound the port")

	// Sanity: the escaped PID is NOT in session A's pgid. If it were,
	// the `setsid` call silently failed and this test is testing nothing.
	inSessionA := false
	for _, pid := range platform.MembersOfPGID(sessionAPGID) {
		if pid == escapedPID {
			inSessionA = true
			break
		}
	}
	require.False(t, inSessionA,
		"escaped PID %d is still in session A pgid %d — setsid didn't take",
		escapedPID, sessionAPGID)

	// Register session A with the *outer* pgid and run cleanup.
	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:        "session-A-escape",
		ProjectPath: tmpDir,
		SessionPGID: sessionAPGID,
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}))
	d.CleanupSessionResources("session-A-escape")

	// Give the kill grace window time to execute. The port holder must
	// still be alive — setsid is on the "accepted escape hatches" list.
	time.Sleep(500 * time.Millisecond)

	if err := syscall.Kill(escapedPID, 0); err != nil {
		t.Fatalf("setsid-escaped PID %d was reaped by session cleanup; containment is too aggressive: %v",
			escapedPID, err)
	}
	if portIsFree(port) {
		t.Fatalf("port %d is free after session cleanup but setsid child should have kept it",
			port)
	}
}

// bytesTrim is a tiny helper to strip trailing whitespace/newlines from
// a pidfile read without pulling in strings/bytes just for one call site.
func bytesTrim(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}
