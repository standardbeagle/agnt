package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitResult runs fn on a goroutine and returns a channel carrying its result,
// so a test can assert both "still blocked" and "returned with X".
func waitResult(fn func() error) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	return ch
}

func requireStillBlocked(t *testing.T, ch <-chan error, why string) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("%s, but the wait returned: %v", why, err)
	case <-time.After(100 * time.Millisecond):
	}
}

func requireReturns(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not return")
		return nil
	}
}

// --- ReadySignaler: failure, restart, cancellation ---

func TestReadySignaler_FailureIsNotReadiness(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()
	rs.SignalFailed("db", errors.New("exec: not found"))

	err := rs.WaitReadyCtx("db", context.Background())
	require.Error(t, err, "a failed start must not read as ready")
	assert.ErrorIs(t, err, ErrDependencyFailed)
	assert.Contains(t, err.Error(), "exec: not found")
}

func TestReadySignaler_FailureWakesBlockedWaiter(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()
	ch := waitResult(func() error { return rs.WaitReadyCtx("db", context.Background()) })
	requireStillBlocked(t, ch, "nothing has been signalled")

	rs.SignalFailed("db", errors.New("boom"))
	assert.ErrorIs(t, requireReturns(t, ch), ErrDependencyFailed)
}

// A waiter that was already blocked when the process stopped must receive the
// signal from the process's next start. Before generations existed it held the
// deleted channel and waited until daemon shutdown.
func TestReadySignaler_ResetCarriesWaitersToNextStart(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()
	ch := waitResult(func() error { return rs.WaitReadyCtx("api", context.Background()) })
	requireStillBlocked(t, ch, "nothing has been signalled")

	rs.Reset("api") // process stopped
	requireStillBlocked(t, ch, "a reset is not readiness")

	rs.SignalReady("api") // process started again
	assert.NoError(t, requireReturns(t, ch))
}

func TestReadySignaler_ResetClearsStaleOutcome(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()

	rs.SignalReady("api")
	rs.Reset("api")
	assert.Error(t, rs.WaitReady("api", 50*time.Millisecond), "ready must not survive a reset")

	rs.SignalFailed("api", errors.New("boom"))
	rs.Reset("api")
	rs.SignalReady("api")
	assert.NoError(t, rs.WaitReady("api", time.Second), "a retry that succeeds must clear the old failure")
}

func TestReadySignaler_WaitRestartSitsOutAFailure(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()
	assert.NoError(t, rs.WaitRestart("api", context.Background()), "not failed: nothing to sit out")

	rs.SignalFailed("api", errors.New("boom"))
	ch := waitResult(func() error { return rs.WaitRestart("api", context.Background()) })
	requireStillBlocked(t, ch, "the failure still stands")

	rs.Reset("api")
	assert.NoError(t, requireReturns(t, ch))
}

func TestReadySignaler_ResetPrefixIsProjectScoped(t *testing.T) {
	t.Parallel()
	rs := NewReadySignaler()
	rs.SignalReady("proj-a:web")
	rs.SignalReady("proj-b:web")

	rs.ResetPrefix("proj-a:")

	assert.Error(t, rs.WaitReady("proj-a:web", 50*time.Millisecond))
	assert.NoError(t, rs.WaitReady("proj-b:web", time.Second), "another project's readiness must survive")
}

// --- Finding 1: pending launches are cancellable ---

func requireNeverLaunched(t *testing.T, d *Daemon, processID string) {
	t.Helper()
	require.Never(t, func() bool {
		_, err := d.hub.ProcessManager().Get(processID)
		return err == nil
	}, 500*time.Millisecond, 25*time.Millisecond, "cancelled launch must never reach the ProcessManager")
}

func TestPendingLaunch_CancelPreventsLaunchAfterDepReady(t *testing.T) {
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	result := daemon.StartProcessWithDeps(context.Background(), "api",
		&config.ScriptConfig{Run: longRunningCmd()}, tmpDir, []string{"db"}, -1*time.Second)
	require.Equal(t, "pending", result.State)

	require.True(t, daemon.pendingProcs.Cancel(result.ProcessID))
	_, stillPending := daemon.pendingProcs.Get(result.ProcessID)
	assert.False(t, stillPending)

	// The readiness signal that used to launch the orphaned goroutine.
	daemon.readySignaler.SignalReady(makeProcessID(tmpDir, "db"))
	requireNeverLaunched(t, daemon, result.ProcessID)
}

func TestProcStop_CancelsPendingLaunch(t *testing.T) {
	daemon, client, tmpDir := newHubProcTestDaemon(t)

	result := daemon.StartProcessWithDeps(context.Background(), "api",
		&config.ScriptConfig{Run: longRunningCmd()}, tmpDir, []string{"db"}, -1*time.Second)
	require.Equal(t, "pending", result.State)

	resp, err := client.ProcStop(result.ProcessID, false)
	require.NoError(t, err, "PROC STOP must find a process that is still waiting on dependencies")
	assert.Equal(t, "cancelled", resp["state"])

	_, stillPending := daemon.pendingProcs.Get(result.ProcessID)
	assert.False(t, stillPending)

	daemon.readySignaler.SignalReady(makeProcessID(tmpDir, "db"))
	requireNeverLaunched(t, daemon, result.ProcessID)
}

func TestCleanupSessionResources_CancelsPendingLaunches(t *testing.T) {
	d, _, tmpDir := newHubProcTestDaemon(t)
	registerSession(t, d, "session-A", tmpDir)
	projectPath := normalizePath(tmpDir)

	result := d.StartProcessWithDeps(context.Background(), "api",
		&config.ScriptConfig{Run: longRunningCmd()}, projectPath, []string{"db"}, -1*time.Second)
	require.Equal(t, "pending", result.State)

	d.CleanupSessionResources("session-A")

	_, stillPending := d.pendingProcs.Get(result.ProcessID)
	assert.False(t, stillPending)

	// A readiness signal arriving after the session ended must launch nothing.
	d.readySignaler.SignalReady(makeProcessID(projectPath, "db"))
	requireNeverLaunched(t, d, result.ProcessID)
}

// A second PROC RUN under the same ID supersedes the first: exactly one
// goroutine may launch, and the superseded one must not retire its successor.
func TestPendingTracker_ReRegisterCancelsReplacedLaunch(t *testing.T) {
	t.Parallel()
	tracker := NewPendingProcessTracker()

	firstCtx, firstCancel := context.WithCancel(context.Background())
	first := tracker.RegisterLaunch(PendingProcess{ProcessID: "api"}, []string{"db"}, firstCancel)
	second := tracker.RegisterLaunch(PendingProcess{ProcessID: "api"}, []string{"redis"}, func() {})

	assert.Error(t, firstCtx.Err(), "the replaced launch must be cancelled")

	tracker.RemoveExact(first)
	got, ok := tracker.Get("api")
	require.True(t, ok, "a superseded launch must not remove its replacement")
	assert.Equal(t, []string{"redis"}, got.WaitingFor)

	tracker.RemoveExact(second)
	_, ok = tracker.Get("api")
	assert.False(t, ok)
}

// --- Findings 3 and 5: explicit starts own readiness, failure included ---

// A prerequisite started through the explicit path that prints no URL and
// binds no known port used to leave dependents waiting out their timeout.
func TestStartScriptExplicit_ArmsReadinessWithoutURL(t *testing.T) {
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	require.NoError(t, daemon.StartScriptExplicit(context.Background(), "db",
		&config.ScriptConfig{Run: longRunningCmd()}, tmpDir, nil))

	assert.NoError(t, daemon.readySignaler.WaitReady(makeProcessID(tmpDir, "db"), 5*time.Second),
		"an explicit start with no expected port must signal ready")
}

func TestStartScriptExplicit_FailedStartIsReportedAsFailure(t *testing.T) {
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	err := daemon.StartScriptExplicit(context.Background(), "db",
		&config.ScriptConfig{Command: "agnt-test-no-such-binary-7f3a"}, tmpDir, nil)
	require.Error(t, err)

	waitErr := daemon.readySignaler.WaitReady(makeProcessID(tmpDir, "db"), 5*time.Second)
	assert.ErrorIs(t, waitErr, ErrDependencyFailed, "a failed start must reach dependents as a failure")
}

func TestProcRunDeps_FailedDependencyBlocksDependent(t *testing.T) {
	daemon, _, tmpDir := newHubProcTestDaemon(t)
	daemon.readySignaler.SignalFailed(makeProcessID(tmpDir, "db"), errors.New("boom"))

	result := daemon.StartProcessWithDeps(context.Background(), "api",
		&config.ScriptConfig{Run: longRunningCmd()}, tmpDir, []string{"db"}, -1*time.Second)
	require.Equal(t, "pending", result.State)

	require.Eventually(t, func() bool {
		p, ok := daemon.pendingProcs.Get(result.ProcessID)
		return ok && p.State == PendingFailed
	}, 5*time.Second, 20*time.Millisecond)
	p, _ := daemon.pendingProcs.Get(result.ProcessID)
	assert.Equal(t, "dependency_failed:db", p.FailureReason)
	requireNeverLaunched(t, daemon, result.ProcessID)

	// The failure cascades: something waiting on api must not hang either.
	assert.ErrorIs(t, daemon.readySignaler.WaitReady(result.ProcessID, time.Second), ErrDependencyFailed)
	t.Cleanup(func() { daemon.pendingProcs.Cancel(result.ProcessID) })
}

// Autostart used to SignalReady a script that failed to start, so the
// application launched against a database that never came up.
func TestStartOneScript_FailedDependencyIsNotStarted(t *testing.T) {
	daemon, _, tmpDir := newHubProcTestDaemon(t)
	daemon.readySignaler.SignalFailed(makeProcessID(tmpDir, "db"), errors.New("boom"))

	result := &AutostartResult{}
	var mu sync.Mutex
	failed := map[string]bool{}
	webCfg := &config.ScriptConfig{
		Run:       longRunningCmd(),
		DependsOn: config.DependsOnList{{Name: "db"}},
	}

	daemon.startOneScript(context.Background(), "web", webCfg, tmpDir, 1, nil, result, &mu, failed, nil)

	webID := makeProcessID(tmpDir, "web")
	assert.True(t, failed["web"], "a script whose dependency failed is itself failed")
	assert.Empty(t, result.Scripts)
	require.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0], `"db"`)
	_, err := daemon.hub.ProcessManager().Get(webID)
	assert.Error(t, err, "web must not be launched after its dependency failed")
	assert.NotNil(t, findEntry(depWaitEntries(t, daemon, tmpDir), "dependency_failed"),
		"the skipped start must be visible in the startup log")
	assert.ErrorIs(t, daemon.readySignaler.WaitReady(webID, time.Second), ErrDependencyFailed,
		"the failure must propagate to web's own dependents")
}

func TestStartOneScript_FailedStartFailsItsDependents(t *testing.T) {
	daemon, _, tmpDir := newHubProcTestDaemon(t)

	result := &AutostartResult{}
	var mu sync.Mutex
	failed := map[string]bool{}
	daemon.startOneScript(context.Background(), "db",
		&config.ScriptConfig{Command: "agnt-test-no-such-binary-7f3a"}, tmpDir, 0, nil, result, &mu, failed, nil)
	require.True(t, failed["db"])

	assert.ErrorIs(t, daemon.readySignaler.WaitReady(makeProcessID(tmpDir, "db"), time.Second), ErrDependencyFailed)
}

// --- Finding 6: lifecycle hooks ---

func hookWriteMarkerCmd() string {
	if runtime.GOOS == "windows" {
		return `Set-Content -LiteralPath marker.txt -Value ran -Encoding ASCII`
	}
	return `printf ran > marker.txt`
}

func TestRunLifecycleHook_RunsInScriptWorkingDir(t *testing.T) {
	// No t.Parallel(): starts a real shell process.
	d := depWaitDaemon()
	projectPath := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(projectPath, "svc"), 0o755))

	scriptCfg := hookTestScriptConfig(nil)
	scriptCfg.Cwd = "svc"
	// Generous deadline for the same reason as TestRunLifecycleHook_SetsEnvVars:
	// this asserts where the hook ran, not how fast.
	dir := resolveWorkingDir(projectPath, scriptCfg.Cwd)
	require.NoError(t, runLifecycleHookWithTimeout(30*time.Second, hookWriteMarkerCmd(), "svc", "start", scriptCfg, dir, 0))

	_, err := os.Stat(filepath.Join(projectPath, "svc", "marker.txt"))
	assert.NoError(t, err, "a relative hook command must resolve against the script's working directory")
	assert.Nil(t, findEntry(depWaitEntries(t, d, projectPath), "lifecycle_hook_failed"))
}

func TestRunLifecycleHook_FailureIsVisibleInStartupLog(t *testing.T) {
	// No t.Parallel(): starts a real shell process.
	d := depWaitDaemon()
	projectPath := t.TempDir()

	d.runLifecycleHook("exit 3", "svc", "stop", hookTestScriptConfig(nil), projectPath, 0)

	got := findEntry(depWaitEntries(t, d, projectPath), "lifecycle_hook_failed")
	require.NotNil(t, got, "a failing hook must reach the project's startup log, not only the debug file")
	assert.Equal(t, "warning", got.Level)
	assert.Contains(t, got.Message, "on-stop")
}
