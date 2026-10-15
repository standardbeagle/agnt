package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/store"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No t.Parallel in this file: the tests start real OS processes.

func newReviewBatchDaemon(t *testing.T) *Daemon {
	t.Helper()
	return NewForTest(t, DaemonConfig{
		SocketPath:            shortSockPath(t),
		MaxClients:            4,
		WriteTimeout:          2 * time.Second,
		StartupMonitorTimeout: 150 * time.Millisecond,
	})
}

func startupEvents(d *Daemon, processID, eventType string) []*StartupLogEntry {
	var out []*StartupLogEntry
	for _, e := range d.startupErrorStore.Query(StartupLogFilter{ProcessID: processID}) {
		if e.EventType == eventType {
			out = append(out, e)
		}
	}
	return out
}

// A process that crashes inside the startup watch on every launch got one
// auto-restart: the monitor relaunched with autoRestart=false, so the second
// crash came back as a startup error and the monitor quit. It must instead go
// through shouldRestart and the backoff until MaxRestarts gives up.
func TestAutoRestart_FastCrasherReachesMaxRestarts(t *testing.T) {
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "crasher")
	cmd, args := failExec()

	cfg := DefaultAutoRestartConfig()
	cfg.Enabled = true
	cfg.MaxRestarts = 3
	cfg.RestartWindow = time.Minute
	cfg.RestartDelay = 10 * time.Millisecond

	_, err := d.StartScript(context.Background(), StartScriptConfig{
		ProcessID:         id,
		ProjectPath:       dir,
		WorkingDir:        dir,
		Command:           cmd,
		Args:              args,
		AutoRestart:       true,
		AutoRestartConfig: &cfg,
	})
	require.NoError(t, err, "with auto-restart a quick exit is the first crash cycle")

	require.Eventually(t, func() bool {
		return len(startupEvents(d, id, "restart_gave_up")) > 0
	}, 30*time.Second, 25*time.Millisecond, "a fast crasher must run out of restarts, not stop after one")

	assert.Len(t, startupEvents(d, id, "restarted"), cfg.MaxRestarts,
		"every allowed restart must happen")
	assert.Empty(t, startupEvents(d, id, "restart_failed"),
		"a crash in the startup watch is a crash cycle, not a failed restart")
	assert.NotEmpty(t, startupEvents(d, id, "crash_loop_backoff"),
		"rapid failures must go through the backoff")
	assert.False(t, d.autoRestarter.IsRegistered(id), "the registration retires with the monitor")
}

// A relaunch under the same id inherited the previous lifetime's URLs (false
// readiness in the startup watch) and its exhausted scan offsets (the new
// lifetime's URL never detected).
func TestStartScriptWithRetry_ClearsStaleURLState(t *testing.T) {
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "web")
	pm := d.hub.ProcessManager()

	failCmd, failArgs := failExec()
	dead, err := pm.StartCommand(context.Background(), goprocess.ProcessConfig{
		ID: id, ProjectPath: dir, Command: failCmd, Args: failArgs,
	})
	require.NoError(t, err)
	<-dead.Done()

	// The dead lifetime announced a URL and used up its scan window.
	d.urlTracker.mu.Lock()
	d.urlTracker.urls[id] = []string{"http://localhost:5173/"}
	d.urlTracker.seenURLs[id] = map[string]bool{"http://localhost:5173/": true}
	d.urlTracker.scannedStdout[id] = maxScanBytes
	d.urlTracker.scannedStderr[id] = maxScanBytes
	d.urlTracker.mu.Unlock()

	cmd, args := stayAliveExec()
	proc, startupErr := d.startScriptWithRetry(context.Background(), id, dir, dir, cmd, args, nil, nil, false)
	require.Nil(t, startupErr)
	t.Cleanup(func() { _ = pm.StopProcess(context.Background(), proc) })
	require.NotSame(t, dead, proc)

	assert.Empty(t, d.urlTracker.GetURLs(id), "the new lifetime must not inherit the old URLs")
	d.urlTracker.mu.RLock()
	stdoutOff, stderrOff := d.urlTracker.scannedStdout[id], d.urlTracker.scannedStderr[id]
	d.urlTracker.mu.RUnlock()
	assert.Less(t, stdoutOff, maxScanBytes, "the stdout scan window must start over")
	assert.Less(t, stderrOff, maxScanBytes, "the stderr scan window must start over")
}

// A live process under the id is reused by the launch, so its URL state is
// still true and must be kept.
func TestClearURLStateForLaunch_KeepsLiveProcessState(t *testing.T) {
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "live")
	cmd, args := stayAliveExec()
	proc, err := d.hub.ProcessManager().StartCommand(context.Background(), goprocess.ProcessConfig{
		ID: id, ProjectPath: dir, Command: cmd, Args: args,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.hub.ProcessManager().StopProcess(context.Background(), proc) })

	d.urlTracker.mu.Lock()
	d.urlTracker.urls[id] = []string{"http://localhost:5173/"}
	d.urlTracker.mu.Unlock()

	d.clearURLStateForLaunch(id, dir)
	assert.Equal(t, []string{"http://localhost:5173/"}, d.urlTracker.GetURLs(id))
}

// PROC RESTART relaunched with proc.Env, which already held the injected
// secrets: a deleted secret survived and every restart appended another copy.
func TestRestartProcess_ReinjectsCurrentSecrets(t *testing.T) {
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "api")
	require.NoError(t, d.storem.Set(dir, store.ScopeGlobal, "", "API_KEY", "v1-secret-value",
		map[string]any{store.MetaSecret: true}))

	cmd, args := stayAliveExec()
	proc, startupErr := d.startScriptWithRetry(context.Background(), id, dir, dir, cmd, args, []string{"BASE=1"}, nil, false)
	require.Nil(t, startupErr)
	assert.Equal(t, []string{"BASE=1", "API_KEY=v1-secret-value"}, proc.Env)

	require.NoError(t, d.storem.Delete(dir, store.ScopeGlobal, "", "API_KEY"))

	newProc, err := d.restartProcess(context.Background(), proc, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.hub.ProcessManager().StopProcess(context.Background(), newProc) })
	assert.Equal(t, []string{"BASE=1"}, newProc.Env,
		"a deleted secret must not survive a restart, and nothing may be appended twice")
}

// PROC AUTORESTART enable registered proc.Env, which already held the injected
// secrets: every auto-restart injected them again on top, so a deleted secret
// came back and each injected pair was doubled.
func TestProcAutoRestartEnable_RegistersPreInjectionEnv(t *testing.T) {
	d := newReviewBatchDaemon(t)
	client := daemonclient.NewClient(daemonclient.WithSocketPath(d.config.SocketPath))
	require.NoError(t, client.Connect())
	t.Cleanup(func() { _ = client.Close() })

	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "api")
	require.NoError(t, d.storem.Set(dir, store.ScopeGlobal, "", "API_KEY", "v1-secret-value",
		map[string]any{store.MetaSecret: true}))

	cmd, args := stayAliveExec()
	proc, startupErr := d.startScriptWithRetry(context.Background(), id, dir, dir, cmd, args, []string{"BASE=1"}, nil, false)
	require.Nil(t, startupErr)
	t.Cleanup(func() { _ = d.hub.ProcessManager().StopProcess(context.Background(), proc) })
	require.Equal(t, []string{"BASE=1", "API_KEY=v1-secret-value"}, proc.Env)

	_, err := client.ProcAutoRestart(id, "enable", nil)
	require.NoError(t, err)

	reg, ok := d.autoRestarter.Take(id)
	require.True(t, ok, "AUTORESTART enable must register the process")
	assert.Equal(t, []string{"BASE=1"}, reg.Env,
		"the registration holds the env as launched; each auto-restart injects the current secrets itself")
}

// A store read failure launched the process without its secrets and said so
// only in the debug log.
func TestInjectSecretEnv_StoreReadFailureIsVisible(t *testing.T) {
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	storeFile := filepath.Join(dir, store.StoreDir, store.ScopeGlobal, "global.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(storeFile), 0o755))
	require.NoError(t, os.WriteFile(storeFile, []byte("{not json"), 0o644))

	id := makeProcessID(dir, "api")
	env := d.injectSecretEnv(dir, id, []string{"BASE=1"})
	assert.Equal(t, []string{"BASE=1"}, env)

	entries := startupEvents(d, id, "secret_injection_failed")
	require.Len(t, entries, 1, "the failure must reach the startup log")
	assert.Equal(t, "warning", entries[0].Level)
}

// An EADDRINUSE retry that failed while its process was still alive returned
// without stopping it or watching its exit.
func TestStartScriptWithRetry_FailedRetryStopsLiveProcess(t *testing.T) {
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "binder")

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())

	// Logs EADDRINUSE on stderr and stays alive — on both attempts.
	var cmd string
	var args []string
	if runtime.GOOS == "windows" {
		cmd, args = "cmd.exe", []string{"/c", fmt.Sprintf("echo Error: listen EADDRINUSE 127.0.0.1:%d 1>&2 & ping -n %d 127.0.0.1 >nul", port, stayAliveSeconds+1)}
	} else {
		cmd, args = "/bin/sh", []string{"-c", fmt.Sprintf("echo 'Error: listen EADDRINUSE 127.0.0.1:%d' >&2; sleep %d", port, stayAliveSeconds)}
	}

	proc, startupErr := d.startScriptWithRetry(context.Background(), id, dir, dir, cmd, args, nil, nil, false)
	require.Nil(t, proc)
	require.NotNil(t, startupErr)
	assert.True(t, startupErr.Retried)
	assert.Equal(t, "EADDRINUSE", startupErr.ErrorType)

	cur, getErr := d.hub.ProcessManager().GetByPath(id, dir)
	require.NoError(t, getErr, "the failed retry stays registered so its exit is on record")
	require.Eventually(t, func() bool { return !cur.IsRunning() }, 10*time.Second, 25*time.Millisecond,
		"a failed retry must not be left running")
}

// A superseded deferred launch is marked so it leaves the process to its
// replacement; an explicit cancel is not.
func TestPendingRegisterLaunch_DistinguishesSupersedeFromCancel(t *testing.T) {
	tracker := NewPendingProcessTracker()
	first := tracker.RegisterLaunch(PendingProcess{ProcessID: "api"}, []string{"db"}, func() {})
	second := tracker.RegisterLaunch(PendingProcess{ProcessID: "api"}, []string{"db"}, func() {})
	assert.True(t, first.wasSuperseded())
	assert.False(t, second.wasSuperseded())

	require.True(t, tracker.Cancel("api"))
	assert.False(t, second.wasSuperseded(), "an explicit cancel is not a supersession")
}

// stopCancelledLaunch stopped by bare id, so it could kill a newer lifetime
// that had taken the id. It must stop only the lifetime it was handed.
func TestStopCancelledLaunch_StopsOnlyItsOwnLifetime(t *testing.T) {
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "worker")
	pm := d.hub.ProcessManager()
	cmd, args := stayAliveExec()

	old, err := pm.StartCommand(context.Background(), goprocess.ProcessConfig{ID: id, ProjectPath: dir, Command: cmd, Args: args})
	require.NoError(t, err)
	require.NoError(t, pm.StopProcess(context.Background(), old))
	pm.RemoveByPath(id, dir)

	replacement, err := pm.StartCommand(context.Background(), goprocess.ProcessConfig{ID: id, ProjectPath: dir, Command: cmd, Args: args})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pm.StopProcess(context.Background(), replacement) })

	d.stopCancelledLaunch(old, "worker")
	assert.True(t, replacement.IsRunning(), "the replacement lifetime must survive")

	d.stopCancelledLaunch(replacement, "worker")
	require.Eventually(t, func() bool { return !replacement.IsRunning() }, 10*time.Second, 25*time.Millisecond)
}

// ApplyAlertsConfig runs on every session connect and AUTOSTART RECONCILE. It
// replaced the daemon-wide hold buffer every time and the old one dropped
// what it held.
func TestApplyAlertsConfig_KeepsBufferWhenUnchanged(t *testing.T) {
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	mk := func(window int) *config.AlertsConfig {
		return &config.AlertsConfig{OutageHold: &config.OutageHoldConfig{WindowMs: window}}
	}

	d.ApplyAlertsConfig(dir, mk(60000))
	first := d.holdBufferForProject(dir)
	require.NotNil(t, first)

	d.ApplyAlertsConfig(dir, mk(60000))
	assert.Same(t, first, d.holdBufferForProject(dir), "an unchanged config must keep the running buffer")

	d.ApplyAlertsConfig(dir, mk(30000))
	assert.NotSame(t, first, d.holdBufferForProject(dir), "a changed config installs a new buffer")
}

// One project's alerts block governed every proxy in the daemon: the last
// project to connect set the hold window, cascade patterns, transport
// thresholds and retention gates for all of them.
func TestApplyAlertsConfig_IsPerProject(t *testing.T) {
	d := newReviewBatchDaemon(t)
	a := normalizePath(shortTempDir(t))
	b := normalizePath(shortTempDir(t))
	off := false

	d.ApplyAlertsConfig(a, &config.AlertsConfig{
		OutageHold: &config.OutageHoldConfig{WindowMs: 1000, JSCascadePatterns: []string{"only-in-a"}},
		Retention:  &config.RetentionConfig{OnBuildSuccess: &off},
	})
	d.ApplyAlertsConfig(b, &config.AlertsConfig{
		OutageHold: &config.OutageHoldConfig{WindowMs: 9000, JSCascadePatterns: []string{"only-in-b"}},
	})

	hbA, hbB, def := d.holdBufferForProject(a), d.holdBufferForProject(b), d.holdBuffer.Load()
	require.NotNil(t, hbA)
	require.NotNil(t, hbB)
	assert.NotSame(t, hbA, hbB)
	assert.NotSame(t, def, hbA, "the default buffer must not take a project's settings")
	assert.True(t, hbA.MatchesJSCascade("x only-in-a"))
	assert.False(t, hbA.MatchesJSCascade("x only-in-b"), "b's patterns must not reach a's proxies")
	assert.Same(t, def, d.holdBufferForProject(normalizePath(shortTempDir(t))),
		"a project without an alerts block uses the default buffer")

	assert.False(t, d.retention(a).ClearOnBuildSuccess())
	assert.True(t, d.retention(b).ClearOnBuildSuccess(), "a's retention gates must not reach b")

	// The last session leaving retires the project's settings.
	d.retireProjectAlertsConfig(a)
	assert.Same(t, d.holdBuffer.Load(), d.holdBufferForProject(a))
	assert.True(t, d.retention(a).ClearOnBuildSuccess())
}

// Each delivery built a fresh transport whose idle keep-alive connection was
// never closed.
func TestScheduler_OverlayClientDoesNotPoolConnections(t *testing.T) {
	s := NewScheduler(DefaultSchedulerConfig(), nil, nil)
	client := s.createOverlayClient(filepath.Join(t.TempDir(), "overlay.sock"))
	tr, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.True(t, tr.DisableKeepAlives)
}

// PROXY STOP on a crashed proxy answered "not found" before its cleanup ran:
// the proxy stayed in persisted state (back on the next daemon start), and its
// admin entry and incident owner went stale.
func TestProxyStop_CrashedProxyIsCleanedUp(t *testing.T) {
	d := NewForTest(t, DaemonConfig{
		SocketPath:             shortSockPath(t),
		MaxClients:             4,
		WriteTimeout:           2 * time.Second,
		EnableStatePersistence: true,
		StatePath:              filepath.Join(t.TempDir(), "state.json"),
	})
	client := daemonclient.NewClient(daemonclient.WithSocketPath(d.config.SocketPath))
	require.NoError(t, client.Connect())
	t.Cleanup(func() { _ = client.Close() })

	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "web")
	d.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: id,
		Config:  &config.ProxyConfig{URL: "http://127.0.0.1:65043"},
		Path:    dir,
	})
	p, err := d.proxym.Get(id)
	require.NoError(t, err)
	d.registerIncidentProxyOwner(id, "sess-crash")
	require.NotNil(t, d.stateMgr)
	_, persisted := d.stateMgr.GetProxy(id)
	require.True(t, persisted, "precondition: the proxy is persisted")
	require.Len(t, d.proxyEntries.List(dir), 1, "precondition: the proxy has an admin entry")

	proxy.MarkCrashedForTest(t, p)

	require.NoError(t, client.ProxyStop(id), "stopping a crashed proxy succeeds")
	_, err = d.proxym.Get(id)
	assert.ErrorIs(t, err, proxy.ErrProxyNotFound)
	_, persisted = d.stateMgr.GetProxy(id)
	assert.False(t, persisted, "a stopped proxy must not come back on the next daemon start")
	assert.Empty(t, d.proxyEntries.List(dir), "the admin entry must be retired")
	_, owned := d.incidentProxyOwner.Load(id)
	assert.False(t, owned, "the incident owner must be retired")
}

// CHAOS SET ignored a payload decode error and applied the zero config, which
// wiped every rule. A payload that does not decode is refused as invalid and
// the rules stay as they were.
func TestChaosSet_UndecodablePayloadLeavesRulesUntouched(t *testing.T) {
	d := newReviewBatchDaemon(t)
	conn := NewConn(d.config.SocketPath)
	require.NoError(t, conn.EnsureConnected())
	t.Cleanup(func() { _ = conn.Close() })

	p, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID: "chaos-decode", TargetURL: "http://127.0.0.1:65044", ListenPort: 0, MaxLogSize: 100,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), p.ID) })
	require.NoError(t, p.ChaosEngine().SetConfig(&proxy.ChaosConfig{
		Enabled: true,
		Rules:   []*proxy.ChaosRule{{ID: "slow", Type: proxy.ChaosLatency, Enabled: true, MinLatencyMs: 100}},
	}))

	err = conn.Request("CHAOS", "SET", p.ID).WithData([]byte("{not json")).OK()
	require.Error(t, err, "an undecodable chaos config must be refused")
	assert.Contains(t, err.Error(), "invalid chaos config")

	cfg := p.ChaosEngine().GetConfig()
	require.NotNil(t, cfg)
	assert.True(t, cfg.Enabled, "chaos must stay enabled")
	require.Len(t, cfg.Rules, 1, "the rules must be left untouched")
	assert.Equal(t, "slow", cfg.Rules[0].ID)
}
