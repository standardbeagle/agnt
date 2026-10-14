package daemon

import (
	"context"
	"testing"
	"time"

	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No t.Parallel in this file: the tests start real OS processes.

func scriptProxyIDs(d *Daemon, scriptID string) []string {
	d.scriptProxyMu.RLock()
	defer d.scriptProxyMu.RUnlock()
	return append([]string(nil), d.scriptProxies[scriptID]...)
}

// startExited starts a process that exits at once and watches it the way the
// launch paths do. It stays registered in the process manager, which is the
// case the URL tracker's removal path never reports.
func startExited(t *testing.T, d *Daemon, name string) (string, *goprocess.ManagedProcess) {
	t.Helper()
	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, name)
	cmd, args := failExec()
	proc, err := d.hub.ProcessManager().StartCommand(context.Background(), goprocess.ProcessConfig{
		ID: id, ProjectPath: dir, Command: cmd, Args: args,
	})
	require.NoError(t, err)
	d.trackScriptProxy(id, "proxy-of-"+name)
	return id, proc
}

// A script that exits on its own must not leave its proxies fronting a dead
// upstream until doctor or the next session connect.
func TestNaturalExit_TearsDownScriptProxies(t *testing.T) {
	d := newReviewBatchDaemon(t)
	id, proc := startExited(t, d, "web")
	d.watchProcessExit(proc)

	require.Eventually(t, func() bool { return len(scriptProxyIDs(d, id)) == 0 },
		10*time.Second, 10*time.Millisecond, "the exited script's proxies must be torn down")
}

// The exit teardown removes proxies only. A one-shot prerequisite that exited
// 0 was signalled ready (StartScriptExplicit) and a failed relaunch signalled
// failed; resetting readiness here would leave their dependents waiting.
func TestNaturalExit_KeepsReadiness(t *testing.T) {
	d := newReviewBatchDaemon(t)
	id, proc := startExited(t, d, "migrate")
	<-proc.Done()
	d.readySignaler.SignalReady(id)
	d.watchProcessExit(proc)

	require.Eventually(t, func() bool { return len(scriptProxyIDs(d, id)) == 0 },
		10*time.Second, 10*time.Millisecond, "premise: the exit teardown ran")
	assert.NoError(t, d.readySignaler.WaitReady(id, 200*time.Millisecond),
		"the exited process's readiness must survive the teardown")
}

// A manual restart stops the old lifetime first; that exit must not take the
// proxies with it.
func TestNaturalExit_KeepsProxiesWhenRestartPending(t *testing.T) {
	d := newReviewBatchDaemon(t)
	id, proc := startExited(t, d, "web")
	d.restartPending.Store(id, struct{}{})
	d.watchProcessExit(proc)

	require.Eventually(t, func() bool { _, ok := d.processExitInfo.Get(id); return ok },
		10*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond) // give a wrongly emitted event time to land
	assert.Equal(t, []string{"proxy-of-web"}, scriptProxyIDs(d, id))
}

// With auto-restart the monitor owns the decision: proxies stay while it
// restarts, and go when it gives up.
func TestAutoRestartGiveUp_TearsDownScriptProxies(t *testing.T) {
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "crasher")
	cmd, args := failExec()
	d.trackScriptProxy(id, "proxy-of-crasher")

	cfg := DefaultAutoRestartConfig()
	cfg.Enabled = true
	cfg.MaxRestarts = 1
	cfg.RestartDelay = 10 * time.Millisecond
	_, err := d.StartScript(context.Background(), StartScriptConfig{
		ProcessID: id, ProjectPath: dir, WorkingDir: dir, Command: cmd, Args: args,
		AutoRestart: true, AutoRestartConfig: &cfg,
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(startupEvents(d, id, "restart_gave_up")) > 0 },
		30*time.Second, 25*time.Millisecond)
	require.Eventually(t, func() bool { return len(scriptProxyIDs(d, id)) == 0 },
		10*time.Second, 10*time.Millisecond, "giving up must tear the proxies down")
}

// ScriptStopped is delivered asynchronously; by the time it lands a
// replacement may be running and owns the proxies.
func TestHandleScriptStopped_KeepsProxiesOfLiveReplacement(t *testing.T) {
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "web")
	cmd, args := stayAliveExec()
	proc, err := d.hub.ProcessManager().StartCommand(context.Background(), goprocess.ProcessConfig{
		ID: id, ProjectPath: dir, Command: cmd, Args: args,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.hub.ProcessManager().StopProcess(context.Background(), proc) })
	require.Eventually(t, func() bool { return proc.PID() > 0 }, 5*time.Second, 10*time.Millisecond)
	d.trackScriptProxy(id, "proxy-of-web")

	d.handleScriptStopped(ProxyEvent{Type: ScriptStopped, ScriptID: id})
	assert.Equal(t, []string{"proxy-of-web"}, scriptProxyIDs(d, id))
}

// Take reads and removes one registration; Registration followed by
// Unregister could drop a registration made in between and return the old one.
func TestAutoRestarterTake_ReturnsAndRemovesRegistration(t *testing.T) {
	d := newReviewBatchDaemon(t)
	r := d.autoRestarter
	cfg := DefaultAutoRestartConfig()
	cfg.Enabled = true
	r.Register("p1", cfg, "true", nil, []string{"A=1"}, []int{3000}, "/proj", "/proj/web")

	got, ok := r.Take("p1")
	require.True(t, ok)
	assert.Equal(t, []string{"A=1"}, got.Env)
	assert.Equal(t, []int{3000}, got.ExpectedPorts)
	assert.Equal(t, "/proj/web", got.WorkingDir)
	assert.False(t, r.IsRegistered("p1"))

	_, ok = r.Take("p1")
	assert.False(t, ok)
}
