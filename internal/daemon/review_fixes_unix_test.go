//go:build unix

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon/health"
	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/standardbeagle/go-cli-server/script"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bootFastStartDaemon boots a daemon whose startup-failure watch is short, so
// tests that restart a process do not sit out the 3s production window.
func bootFastStartDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	sockPath := shortSockPath(t)
	d := newBootedDaemonWithConfig(t, DaemonConfig{
		SocketPath:            sockPath,
		StartupMonitorTimeout: 150 * time.Millisecond,
	})
	return d, sockPath
}

func connectClient(t *testing.T, sockPath string) *daemonclient.Client {
	t.Helper()
	c := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// registerWithOwner sends SESSION REGISTER with an explicit owner_pid. The
// typed client always sends its own pid, which cannot model two wrappers.
func registerWithOwner(c *daemonclient.Client, code, projectPath string, ownerPID int) error {
	_, err := c.Conn().Request(protocol.VerbSession, protocol.SubVerbRegister, code).
		WithJSON(map[string]interface{}{
			"project_path": projectPath,
			"command":      "test",
			"owner_pid":    ownerPID,
		}).JSON()
	return err
}

// Two sessions that draw the same code: SESSION REGISTER treated the second as
// a reconnect and handed it the first one's registration, so the first
// session's exit then tore the second one down.
func TestSessionRegister_RefusesCodeHeldByAnotherLiveOwner(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)
	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))

	// Session A is owned by this test process — alive for the whole test.
	require.NoError(t, registerWithOwner(c, "claude-dup", dirA, os.Getpid()))

	// A different, live wrapper presents the same code.
	other := connectClient(t, c.SocketPath())
	err := registerWithOwner(other, "claude-dup", dirB, os.Getppid())
	require.Error(t, err, "a second live owner must not be merged into the first session")
	assert.Contains(t, err.Error(), "already registered")

	sess, ok := d.sessionRegistry.Get("claude-dup")
	require.True(t, ok)
	assert.Equal(t, dirA, sess.ProjectPath, "the first session's registration is untouched")
	assert.Equal(t, os.Getpid(), sess.OwnerPID)

	// The same owner re-registering is a reconnect and still works.
	require.NoError(t, registerWithOwner(other, "claude-dup", dirA, os.Getpid()))
}

// A code whose recorded owner has exited is free to be taken over: that is a
// wrapper that died without unregistering, not a collision.
func TestSessionRegister_AllowsTakeoverFromDeadOwner(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))

	require.NoError(t, registerWithOwner(c, "claude-dead", dir, exitedOwnerPID(t)))
	other := connectClient(t, c.SocketPath())
	require.NoError(t, registerWithOwner(other, "claude-dead", dir, os.Getpid()))

	sess, ok := d.sessionRegistry.Get("claude-dead")
	require.True(t, ok)
	assert.Equal(t, os.Getpid(), sess.OwnerPID)
}

// A reconnect installs a fresh *Session. The investigation record was not
// carried over, so a socket blip wiped it mid-investigation.
func TestSessionReconnect_KeepsInvestigation(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))

	_, err := c.SessionRegister("claude-inv", "", dir, "test", nil)
	require.NoError(t, err)
	before, ok := d.sessionRegistry.Get("claude-inv")
	require.True(t, ok)
	before.MergeInvestigation(protocol.InvestigationPatch{FailedAreas: []string{"the cache is stale"}})
	require.NotNil(t, before.Investigation())

	// Reconnect: same code, same owner, new connection.
	again := connectClient(t, c.SocketPath())
	_, err = again.SessionRegister("claude-inv", "", dir, "test", nil)
	require.NoError(t, err)

	after, ok := d.sessionRegistry.Get("claude-inv")
	require.True(t, ok)
	require.NotSame(t, before, after, "precondition: a reconnect installs a fresh session identity")
	require.NotNil(t, after.Investigation(), "the investigation record must survive a reconnect")
	assert.Equal(t, []string{"the cache is stale"}, after.Investigation().FailedAreas)
}

// SESSION ATTACH binds a connection to the session for scoping, and the hub
// reports every bound connection's close. An MCP connection going away used to
// schedule cleanup of a session whose `agnt run` was still connected.
func TestAttachedConnectionClose_DoesNotScheduleSessionCleanup(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))

	_, err := c.SessionRegister("claude-ctl", "", dir, "test", nil)
	require.NoError(t, err)

	attached := daemonclient.NewClient(daemonclient.WithSocketPath(c.SocketPath()))
	require.NoError(t, attached.Connect())
	_, err = attached.SessionAttach(dir)
	require.NoError(t, err)
	require.NoError(t, attached.Close())

	pending := func() bool {
		_, ok := d.pendingCleanups.Load("claude-ctl")
		return ok
	}
	// The hub's close callback runs on the connection's own goroutine. Wait for
	// the daemon to have dropped the client before judging what it scheduled.
	require.Eventually(t, func() bool { return d.hub.ClientCount() == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.Never(t, pending, 300*time.Millisecond, 10*time.Millisecond,
		"an attached connection closing must not schedule cleanup while the control connection is open")

	// The control connection closing is the real signal.
	require.NoError(t, c.Close())
	require.Eventually(t, pending, 5*time.Second, 5*time.Millisecond,
		"the control connection closing must schedule the deferred cleanup")
}

// STOP-ALL reported processes stopped while their auto-restart registrations
// stayed live: the monitor saw the stop as an exit and relaunched them.
func TestStopAll_QuiescesAutoRestartPendingLaunchesAndScriptState(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))
	_, err := c.SessionRegister("sess-stop", "", dir, "test", nil)
	require.NoError(t, err)

	// A script-registry entry whose process is running under auto-restart.
	entry, err := d.registerScriptEntry("web", dir, &script.Config{Command: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	require.NoError(t, err)
	proc := startSleeper(t, d, entry.ProcessID, dir)
	entry.SetState(script.StateRunning)
	restartCfg := DefaultAutoRestartConfig()
	restartCfg.Enabled = true
	restartCfg.RestartDelay = 10 * time.Millisecond
	d.autoRestarter.Register(entry.ProcessID, restartCfg, "/bin/sh", []string{"-c", "sleep 30"}, nil, nil, dir, dir)
	require.True(t, d.autoRestarter.IsRegistered(entry.ProcessID))

	// A launch still waiting on a dependency.
	launchCtx, cancelLaunch := context.WithCancel(context.Background())
	defer cancelLaunch()
	pendingID := makeProcessID(dir, "worker")
	d.pendingProcs.RegisterLaunch(PendingProcess{ProcessID: pendingID, Name: "worker", ProjectPath: dir}, []string{entry.ProcessID}, cancelLaunch)

	_, err = c.StopAll()
	require.NoError(t, err)

	assert.False(t, d.autoRestarter.IsRegistered(entry.ProcessID), "STOP-ALL must unregister auto-restart")
	_, stillPending := d.pendingProcs.Get(pendingID)
	assert.False(t, stillPending, "STOP-ALL must cancel launches waiting on dependencies")
	assert.Error(t, launchCtx.Err(), "the waiting launch goroutine must be cancelled, not just forgotten")
	assert.Equal(t, script.StateStopped, entry.State(), "the script registry must not keep reporting a stopped script as running")

	require.Eventually(t, func() bool { return !proc.IsRunning() }, 5*time.Second, 10*time.Millisecond)
	// With a 10ms restart delay, a surviving monitor would have relaunched it
	// long before this window closes.
	assert.Never(t, func() bool {
		cur, getErr := d.hub.ProcessManager().GetByPath(entry.ProcessID, dir)
		return getErr == nil && cur.IsRunning()
	}, 750*time.Millisecond, 25*time.Millisecond, "a stopped process must stay stopped")
}

// RESTART-ALL was a drifted copy of the single-resource restarts: it relaunched
// processes with no env, moved proxies to a fresh port, and left them with no
// incident owner, no overlay binding and no persisted state.
func TestRestartAll_KeepsLaunchSettings(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, sockPath := bootFastStartDaemon(t)
	c := connectClient(t, sockPath)
	dir := normalizePath(shortTempDir(t))
	_, err := c.SessionRegister("sess-restart", "http://127.0.0.1:19191", dir, "test", nil)
	require.NoError(t, err)

	workDir := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	d.registerIncidentProcessOwner("api", "sess-restart")
	oldProc, err := d.hub.ProcessManager().StartCommand(context.Background(), goprocess.ProcessConfig{
		ID:          "api",
		ProjectPath: dir,
		WorkingDir:  workDir,
		Command:     "/bin/sh",
		Args:        []string{"-c", "sleep 30"},
		Env:         []string{"AGNT_REVIEW_MARK=kept"},
	})
	require.NoError(t, err)

	oldProxy, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:            "front",
		TargetURL:     ephemeralTargetURL(t),
		ListenPort:    -1,
		Path:          dir,
		PublicURL:     "https://example.test",
		SkipTLSVerify: true,
	})
	require.NoError(t, err)
	d.registerIncidentProxyOwner("front", "sess-restart")
	d.trackScriptProxy("api", "front")
	oldPort := oldProxy.BoundPort()
	require.Positive(t, oldPort)

	// Issued from a connection that is NOT bound to the session, naming it by
	// code — the owner must come from the previous lifetime, not the caller.
	unbound := connectClient(t, sockPath)
	resp, err := unbound.RestartAllScoped(protocol.DirectoryFilter{SessionCode: "sess-restart"})
	require.NoError(t, err)
	assert.Equal(t, true, resp["success"])
	assert.Equal(t, float64(1), resp["processes_restarted"])
	assert.Equal(t, float64(1), resp["proxies_restarted"])

	newProc, err := d.hub.ProcessManager().GetByPath("api", dir)
	require.NoError(t, err)
	require.NotSame(t, oldProc, newProc, "the process must have been restarted")
	assert.True(t, newProc.IsRunning())
	assert.Contains(t, newProc.Env, "AGNT_REVIEW_MARK=kept", "the restarted process keeps its env")
	assert.Equal(t, workDir, newProc.WorkingDir, "and its working directory")
	procOwner, ok := d.incidentProcessOwner.Load("api")
	require.True(t, ok, "the restarted process keeps an incident owner")
	assert.Equal(t, "sess-restart", procOwner.(*health.ResourceOwner).SessionCode)

	newProxy, err := d.proxym.Get("front")
	require.NoError(t, err)
	require.NotSame(t, oldProxy, newProxy, "the proxy must have been restarted")
	assert.Equal(t, oldPort, newProxy.BoundPort(), "the proxy comes back on the port it had")
	assert.Equal(t, "https://example.test", newProxy.GetPublicURL())
	assert.True(t, newProxy.SkipTLSVerify)
	proxyOwner, ok := d.incidentProxyOwner.Load("front")
	require.True(t, ok, "the restarted proxy keeps an incident owner")
	assert.Equal(t, "sess-restart", proxyOwner.(*health.ResourceOwner).SessionCode)
	assert.Equal(t, "api", d.linkedScriptForProxy("front"), "a script-linked proxy stays linked")
	assert.Equal(t, []string{"front"}, d.getProxiesForScript("api"), "and linked exactly once")
}

// PROC RESTART re-registered auto-restart with a nil env, the project root as
// working directory, and default limits.
func TestProcRestart_KeepsAutoRestartRegistration(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, sockPath := bootFastStartDaemon(t)
	c := connectClient(t, sockPath)
	dir := normalizePath(shortTempDir(t))
	_, err := c.SessionRegister("sess-ar", "", dir, "test", nil)
	require.NoError(t, err)

	workDir := filepath.Join(dir, "svc")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	_, err = d.hub.ProcessManager().StartCommand(context.Background(), goprocess.ProcessConfig{
		ID: "svc", ProjectPath: dir, WorkingDir: workDir,
		Command: "/bin/sh", Args: []string{"-c", "sleep 30"},
	})
	require.NoError(t, err)

	restartCfg := DefaultAutoRestartConfig()
	restartCfg.Enabled = true
	restartCfg.MaxRestarts = 9
	restartCfg.OnlyOnError = true
	d.autoRestarter.Register("svc", restartCfg, "/bin/sh", []string{"-c", "sleep 30"},
		[]string{"CONFIGURED=1"}, []int{}, dir, workDir)

	_, err = c.ProcRestart("svc")
	require.NoError(t, err)

	reg, ok := d.autoRestarter.Registration("svc")
	require.True(t, ok, "auto-restart must be registered again after a manual restart")
	assert.Equal(t, []string{"CONFIGURED=1"}, reg.Env, "with the env it was registered with")
	assert.Equal(t, workDir, reg.WorkingDir, "and the working directory, not the project root")
	assert.Equal(t, 9, reg.Config.MaxRestarts)
	assert.True(t, reg.Config.OnlyOnError)
	assert.True(t, reg.Config.Enabled)
}

// PROC id verbs looked a bare id up across every project: a caller could stop
// another project's process, and an id two projects shared was "not found".
func TestProcVerbs_ResolveInCallersProjectAndReportAmbiguity(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, c, _ := newBootedDaemonWithClient(t)
	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))
	_, err := c.SessionRegister("sess-a", "", dirA, "test", nil)
	require.NoError(t, err)

	procA := startSleeper(t, d, "dev", dirA)
	procB := startSleeper(t, d, "dev", dirB)
	onlyB := startSleeper(t, d, "only-b", dirB)

	// A connection bound to no session cannot tell them apart — and says so.
	unbound := connectClient(t, c.SocketPath())
	_, err = unbound.ProcStatus("dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Contains(t, err.Error(), dirA)
	assert.Contains(t, err.Error(), dirB)
	assert.NotContains(t, err.Error(), "not found")

	// The bound caller gets its own project's process.
	status, err := c.ProcStatus("dev")
	require.NoError(t, err)
	assert.Equal(t, dirA, status["project_path"])

	_, err = c.ProcStop("dev", false)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !procA.IsRunning() }, 5*time.Second, 10*time.Millisecond)
	assert.True(t, procB.IsRunning(), "the other project's same-named process must survive")

	// An id that exists in exactly one project still resolves from anywhere.
	status, err = c.ProcStatus("only-b")
	require.NoError(t, err)
	assert.Equal(t, dirB, status["project_path"])
	assert.True(t, onlyB.IsRunning())
}

// A process death carried no ProjectPath, so the default project-scoped
// ALERTS QUERY never returned it.
func TestProcessDeathAlert_IsStampedWithProject(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, _, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))

	proc, err := d.hub.ProcessManager().StartCommand(context.Background(), goprocess.ProcessConfig{
		ID: "crasher", ProjectPath: dir,
		Command: "/bin/sh", Args: []string{"-c", "echo boom >&2; exit 3"},
	})
	require.NoError(t, err)
	d.watchProcessExit(proc)

	require.Eventually(t, func() bool {
		for _, a := range d.alertStore.Query(AlertStoreFilter{ProjectPath: dir}) {
			if a.Category == CategoryProcessLifecycle && a.ScriptID == "crasher" {
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond, "a project-scoped alert query must return the project's process deaths")
}

// RUN accepted script_name with no command and then launched an empty command.
func TestRun_RequiresCommand(t *testing.T) {
	_, c, tmpDir := newBootedDaemonWithClient(t)

	_, err := c.Run(protocol.RunConfig{ID: "named-only", Path: tmpDir, ScriptName: "dev"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command required")
}

// RUN goes through the script start path, so the process id is derived from
// the project. The caller must be able to use the id the reply carries AND the
// short id it asked for, from the same project.
func TestRun_ShortAndDerivedIDBothResolve(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))
	_, err := c.SessionRegister("sess-ids", "", dir, "test", nil)
	require.NoError(t, err)

	resp, err := c.Run(protocol.RunConfig{
		ID: "dev", Path: dir, Raw: true,
		Command: "/bin/sh", Args: []string{"-c", "echo dev-up; sleep 30"},
	})
	require.NoError(t, err)

	derived := makeProcessID(dir, "dev")
	assert.Equal(t, derived, resp["process_id"], "process_id is the id every later PROC verb accepts")
	assert.Equal(t, derived, resp["id"])
	assert.Equal(t, "dev", resp["name"], "the short name is reported separately")
	assert.Equal(t, false, resp["reused"])
	pid := resp["pid"]
	require.NotZero(t, pid)

	for _, id := range []string{derived, "dev"} {
		status, err := c.ProcStatus(id)
		require.NoError(t, err, "PROC STATUS %q", id)
		assert.Equal(t, derived, status["id"], "PROC STATUS %q", id)
		assert.Equal(t, pid, status["pid"], "PROC STATUS %q", id)

		require.Eventually(t, func() bool {
			out, outErr := c.ProcOutput(id, protocol.OutputFilter{})
			return outErr == nil && strings.Contains(out, "dev-up")
		}, 5*time.Second, 20*time.Millisecond, "PROC OUTPUT %q", id)

		_, err = c.ProcAutoRestart(id, "status", nil)
		require.NoError(t, err, "PROC AUTORESTART %q", id)
	}

	// It is a managed script like any other: a registry entry that tracks it.
	// A background RUN replies as soon as the process has a PID; the start
	// path registers the entry on its own goroutine, so wait for it.
	require.Eventually(t, func() bool {
		_, found := d.scriptRegistry.Get("dev", dir)
		return found
	}, 5*time.Second, 20*time.Millisecond, "a RUN-started process has a script-registry entry")
	entry, ok := d.scriptRegistry.Get("dev", dir)
	require.True(t, ok)
	assert.Equal(t, derived, entry.ProcessID)
	require.Eventually(t, func() bool { return entry.State() == script.StateRunning }, 5*time.Second, 20*time.Millisecond)

	// Restart by short id, stop by derived id.
	_, err = c.ProcRestart("dev")
	require.NoError(t, err)
	restarted, err := d.hub.ProcessManager().GetByPath(derived, dir)
	require.NoError(t, err)
	require.True(t, restarted.IsRunning())
	_, err = c.ProcStop(derived, false)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !restarted.IsRunning() }, 5*time.Second, 10*time.Millisecond)
}

// A short id resolves inside the caller's project only: the same short id in
// another project is a different process.
func TestRun_ShortIDIsScopedToCallersProject(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, c, _ := newBootedDaemonWithClient(t)
	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))
	_, err := c.SessionRegister("sess-a", "", dirA, "test", nil)
	require.NoError(t, err)

	sleeper := protocol.RunConfig{ID: "dev", Raw: true, Command: "/bin/sh", Args: []string{"-c", "sleep 30"}}
	sleeper.Path = dirA
	respA, err := c.Run(sleeper)
	require.NoError(t, err)
	sleeper.Path = dirB
	respB, err := c.Run(sleeper)
	require.NoError(t, err)
	require.NotEqual(t, respA["process_id"], respB["process_id"], "one short id, two projects, two processes")

	status, err := c.ProcStatus("dev")
	require.NoError(t, err)
	assert.Equal(t, respA["process_id"], status["id"], "the bound caller's short id is its own project's process")

	// With no session there is no project to prefer, and two candidates.
	unbound := connectClient(t, c.SocketPath())
	_, err = unbound.ProcStatus("dev")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")

	_, err = c.ProcStop("dev", false)
	require.NoError(t, err)
	procB, err := d.hub.ProcessManager().GetByPath(respB["process_id"].(string), dirB)
	require.NoError(t, err)
	assert.True(t, procB.IsRunning(), "the other project's process is untouched")
}

// RUN on an id that is already running hands back the running process. Going
// through the start path again would watch a live process's startup a second
// time and attach a second exit watcher — two death records for one death.
func TestRun_ReusesRunningProcess(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))
	cfg := protocol.RunConfig{ID: "svc", Path: dir, Raw: true, Command: "/bin/sh", Args: []string{"-c", "sleep 30"}}

	first, err := c.Run(cfg)
	require.NoError(t, err)
	require.Equal(t, false, first["reused"])
	derived := first["process_id"].(string)
	proc, err := d.hub.ProcessManager().GetByPath(derived, dir)
	require.NoError(t, err)

	// By short id and by the id the first reply returned.
	for _, id := range []string{"svc", derived} {
		cfg.ID = id
		again, err := c.Run(cfg)
		require.NoError(t, err, "RUN %q", id)
		assert.Equal(t, true, again["reused"], "RUN %q", id)
		assert.Equal(t, derived, again["process_id"], "RUN %q", id)
		assert.Equal(t, first["pid"], again["pid"], "RUN %q", id)
	}
	same, err := d.hub.ProcessManager().GetByPath(derived, dir)
	require.NoError(t, err)
	require.Same(t, proc, same, "a reused RUN must not replace the process")

	// One process, one exit watcher: its death is recorded exactly once.
	deaths := func() int {
		n := 0
		for _, a := range d.alertStore.Query(AlertStoreFilter{ProjectPath: dir}) {
			if a.Category == CategoryProcessLifecycle && a.ScriptID == derived {
				n++
			}
		}
		return n
	}
	require.NoError(t, d.hub.ProcessManager().StopProcess(context.Background(), proc))
	require.Eventually(t, func() bool { return deaths() >= 1 }, 10*time.Second, 20*time.Millisecond)
	assert.Never(t, func() bool { return deaths() > 1 }, 500*time.Millisecond, 25*time.Millisecond,
		"a reused RUN attached a second exit watcher")
}

// A background RUN replies once the process exists. The startup watch that the
// script start path runs must not hold the reply.
func TestRun_BackgroundDoesNotWaitOutStartupWatch(t *testing.T) {
	// No t.Parallel: starts real processes.
	sockPath := shortSockPath(t)
	// A watch far longer than any reply should take. The start path records
	// "script_started" only when the watch ends, so its absence right after
	// the reply shows the reply did not wait for it.
	d := newBootedDaemonWithConfig(t, DaemonConfig{SocketPath: sockPath, StartupMonitorTimeout: 3 * time.Second})
	c := connectClient(t, sockPath)
	dir := normalizePath(shortTempDir(t))

	resp, err := c.Run(protocol.RunConfig{ID: "slow", Path: dir, Raw: true, Command: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	require.NoError(t, err)
	derived := resp["process_id"].(string)
	require.NotZero(t, resp["pid"], "the reply carries the spawned pid")

	started := func() bool {
		for _, e := range d.startupErrorStore.Query(StartupLogFilter{ProcessID: derived, Limit: 20}) {
			if e.EventType == "script_started" {
				return true
			}
		}
		return false
	}
	assert.False(t, started(), "the reply arrived while the startup watch was still running")
	// The watch still completes on the daemon's context after the reply.
	require.Eventually(t, started, 15*time.Second, 50*time.Millisecond,
		"the start path must finish after the reply: registry entry running, script_started recorded")
	entry, ok := d.scriptRegistry.Get("slow", dir)
	require.True(t, ok)
	assert.Equal(t, script.StateRunning, entry.State())
}

// A background RUN whose process dies at once is still a start: the reply is
// not an error, and the death reaches the caller through the lifecycle alert.
func TestRun_BackgroundQuickFailureIsReportedByLifecycle(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))

	resp, err := c.Run(protocol.RunConfig{ID: "boom", Path: dir, Raw: true, Command: "/bin/sh", Args: []string{"-c", "echo dying >&2; exit 3"}})
	require.NoError(t, err, "a process that started and then failed is not a refused start")
	derived := resp["process_id"].(string)

	require.Eventually(t, func() bool {
		for _, a := range d.alertStore.Query(AlertStoreFilter{ProjectPath: dir}) {
			if a.Category == CategoryProcessLifecycle && a.ScriptID == derived {
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond, "the early exit must surface as a process-lifecycle alert")

	status, err := c.ProcStatus("boom")
	require.NoError(t, err)
	assert.Equal(t, float64(3), status["exit_code"])
}

// A command that cannot be spawned at all is a refused start, in every mode.
func TestRun_SpawnFailureIsAnError(t *testing.T) {
	_, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))

	_, err := c.Run(protocol.RunConfig{ID: "ghost", Path: dir, Raw: true, Command: "/nonexistent/agnt-review-binary"})
	require.Error(t, err)
}

// The foreground modes keep their reply: they return when the process exits,
// with its exit code — including a non-zero exit inside the startup watch.
func TestRun_ForegroundRepliesWithExit(t *testing.T) {
	// No t.Parallel: starts real processes.
	_, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))

	resp, err := c.Run(protocol.RunConfig{
		ID: "oneshot", Path: dir, Raw: true, Mode: "foreground-raw",
		Command: "/bin/sh", Args: []string{"-c", "echo out-line; echo err-line >&2; exit 4"},
	})
	require.NoError(t, err)
	assert.Equal(t, float64(4), resp["exit_code"])
	assert.Equal(t, makeProcessID(dir, "oneshot"), resp["process_id"])
	assert.Contains(t, resp["stdout"], "out-line")
	assert.Contains(t, resp["stderr"], "err-line")
	assert.NotEmpty(t, resp["runtime"])
}

// enable_stdin is honoured through the script start path, and PROC STDIN
// reaches the process by short id as well as by derived id.
func TestRun_EnableStdin(t *testing.T) {
	// No t.Parallel: starts real processes.
	_, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))
	_, err := c.SessionRegister("sess-stdin", "", dir, "test", nil)
	require.NoError(t, err)

	resp, err := c.Run(protocol.RunConfig{ID: "echoer", Path: dir, Raw: true, EnableStdin: true, Command: "/bin/cat"})
	require.NoError(t, err)
	derived := resp["process_id"].(string)

	_, err = c.Conn().Request(protocol.VerbProc, "STDIN", "echoer").WithData([]byte("via-short\n")).JSON()
	require.NoError(t, err)
	_, err = c.Conn().Request(protocol.VerbProc, "STDIN", derived).WithData([]byte("via-derived\n")).JSON()
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		out, outErr := c.ProcOutput(derived, protocol.OutputFilter{})
		return outErr == nil && strings.Contains(out, "via-short") && strings.Contains(out, "via-derived")
	}, 5*time.Second, 20*time.Millisecond, "both writes must reach the process's stdin")
}

// A RUN process is an ad-hoc command, not something .agnt.kdl declares. Config
// reconcile stops scripts that are running but no longer declared; it must not
// treat every RUN-started process as one of those.
func TestRun_ProcessSurvivesConfigReconcile(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))

	resp, err := c.Run(protocol.RunConfig{ID: "adhoc", Path: dir, Raw: true, Command: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	require.NoError(t, err)
	derived := resp["process_id"].(string)
	entry, ok := d.scriptRegistry.Get("adhoc", dir)
	require.True(t, ok)
	require.Eventually(t, func() bool { return entry.State() == script.StateRunning }, 5*time.Second, 20*time.Millisecond)

	plan, err := d.ReconcileProjectConfig(context.Background(), dir)
	require.NoError(t, err)
	assert.Empty(t, plan.StopScripts, "a RUN process is not a removed config script")
	assert.Empty(t, plan.RestartScripts)
	proc, err := d.hub.ProcessManager().GetByPath(derived, dir)
	require.NoError(t, err)
	assert.True(t, proc.IsRunning())
}

// A RUN-started process had no output callback: its output reached no stream
// sink and no alert scanner, and it had no incident owner.
func TestRun_WiresOutputAndIncidentOwner(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))
	_, err := c.SessionRegister("sess-run", "", dir, "test", nil)
	require.NoError(t, err)

	sink := d.eventHub.AddStreamSink(streamFilter{})
	defer d.eventHub.RemoveStreamSink(sink)

	_, err = c.Run(protocol.RunConfig{
		ID: "talker", Path: dir, Raw: true,
		Command: "/bin/sh", Args: []string{"-c", "echo agnt-review-line; sleep 30"},
	})
	require.NoError(t, err)

	talker := makeProcessID(dir, "talker")
	owner, ok := d.incidentProcessOwner.Load(talker)
	require.True(t, ok, "a RUN-started process must have an incident owner")
	assert.Equal(t, "sess-run", owner.(*health.ResourceOwner).SessionCode)

	deadline := time.After(10 * time.Second)
	for {
		select {
		case entry := <-sink.Ch:
			if entry.ProcessOutput != nil && entry.ProcessOutput.ProcessID == talker &&
				entry.ProcessOutput.Line == "agnt-review-line" {
				return
			}
		case <-deadline:
			t.Fatal("the RUN-started process's output never reached a stream sink")
		}
	}
}
