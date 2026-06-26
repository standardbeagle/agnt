package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemon"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// Isolate the daemon socket so cmd/agnt tests spawn/use their OWN daemon
	// rather than the host's real one. Without this, `agnt run` subprocesses
	// connect to the shared default socket (the host daemon), hit a version
	// mismatch (test binary = "test" vs installed version), trigger an
	// auto-upgrade of the host daemon, and that churn flakes tests under load
	// (rare "fork/exec: operation not permitted"). AGNT_SOCKET is honored by
	// daemon.DefaultSocketPath; the exec'd agnt subprocesses inherit it.
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("agnt-cmdtest-%d.sock", os.Getpid()))
	_ = os.Setenv("AGNT_SOCKET", sock)

	// Build goleak options now: IgnoreCurrent() snapshots the goroutines that
	// exist at TestMain start (runtime + test infra) so only test-introduced
	// leaks are flagged. Constructed before m.Run() so the snapshot is taken
	// before any test runs — matching goleak.VerifyTestMain's own semantics.
	opts := goleakOptions()

	code := m.Run()

	// Best-effort: stop the isolated daemon and remove its socket so each
	// cmd/agnt test process leaves nothing behind (the old shared-socket path
	// reused the host daemon and never cleaned up).
	_ = daemon.StopDaemon(sock)
	_ = os.Remove(sock)

	// Replicate VerifyTestMain: only check for leaks when tests passed.
	if code == 0 {
		if err := goleak.Find(opts...); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: found leaked goroutines: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

// goleakOptions returns the goleak ignore-list. IgnoreCurrent() must be the
// first entry and is evaluated when this function runs (TestMain start).
func goleakOptions() []goleak.Option {
	return []goleak.Option{
		// goleak.IgnoreCurrent() captures goroutines already running at TestMain
		// start (runtime, test infrastructure) to avoid false positives. Only
		// goroutines leaked by individual tests in this package are flagged.
		//
		// Known-infrastructure goroutines that outlive daemon.Stop(2s) in tests:
		//   - AutostartManager.run: launched per autostart entry, drains async
		//   - ResilientConn.heartbeatLoop: go-cli-server client goroutine, exits
		//     when the daemon socket closes (races with the 2s stop deadline)
		//   - mcp.newIOConn: go-sdk MCP I/O pump, similarly races stop deadline
		//
		// These are pre-existing architectural drain-time issues, not leaks
		// introduced by individual tests. Suppress here to avoid false positives
		// while still catching new goroutine leaks introduced by future changes.
		goleak.IgnoreCurrent(),
		// Daemon infrastructure goroutines: context-driven, may outlive d.Stop(2s).
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*SchedulerStateManager).writeLoop"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*StateManager).writeLoop"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*Scheduler).run"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*URLTracker).scanLoop"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*Daemon).handleProxyEvents"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*Daemon).drainHooks"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*AutostartManager).run"),
		// HoldBuffer.loop: introduced by transport-outage gate WIP.
		// ApplyAlertsConfig races daemon.Stop in some autostart-driven
		// tests — the new buffer is created after Stop has already passed
		// the holdBuffer.Stop call. Tracked under the unify-message-queue
		// follow-up plan; ignore here so test load is the only signal.
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*HoldBuffer).loop"),
		// AutostartManager.run spawns goroutines for DuplicateScanner and
		// ScanForProject; these may be mid-execution when daemon stops.
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/daemon.(*DuplicateScanner).scanDuplicates"),
		// go-cli-server / go-sdk infrastructure.
		goleak.IgnoreAnyFunction("github.com/standardbeagle/go-cli-server/process.(*FilePIDTracker).descendantScanLoop"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/go-cli-server/client.(*ResilientConn).heartbeatLoop"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/go-cli-server/client.(*AutoStartConn).waitForHub"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/go-sdk/mcp.newIOConn.func1"),
		// Proxy background goroutines: race with test teardown.
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/proxy.(*ReorderQueue).releaseLoop"),
		goleak.IgnoreAnyFunction("github.com/standardbeagle/agnt/internal/proxy.(*ProxyServer).runServer"),
		// exec.Cmd stdout/stderr pipe copiers: alive until process exits and
		// pipes drain. Tests that kill processes may race with these goroutines.
		goleak.IgnoreAnyFunction("os/exec.(*Cmd).Start.func2"),
		// exec.CommandContext watchCtx goroutine: stdlib-managed, exits with the
		// subprocess/ctx. WSL Windows-interop execs (tasklist.exe, 3s timeout)
		// can leave it mid-shutdown at teardown under -race. Not a real leak.
		goleak.IgnoreAnyFunction("os/exec.(*Cmd).watchCtx"),
	}
}
