package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stopManagedForTest stops a process a test started through the hub, so a
// keep-alive does not outlive the test.
func stopManagedForTest(t *testing.T, d *Daemon, processID, projectPath string) {
	t.Helper()
	t.Cleanup(func() {
		if proc, err := d.hub.ProcessManager().GetByPath(processID, projectPath); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = d.hub.ProcessManager().StopProcess(ctx, proc)
		}
	})
}

// An inline PROC RUN is an ad-hoc command: in the registry, in no config.
// Config reconcile stops scripts that are "running, no longer declared" and
// the autostart prune deletes entries "not in config"; both treated every
// inline process as one of those.
func TestProcRunInline_IsLeftAloneByReconcileAndAutostartPrune(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, client, tmpDir := newHubProcTestDaemon(t)
	dir := normalizePath(tmpDir)
	command, args := stayAliveExec()

	_, err := client.ProcRun("adhoc", daemonclient.ProcRunConfig{Command: command, Args: args, ProjectPath: tmpDir})
	require.NoError(t, err)
	processID := makeProcessID(dir, "adhoc")
	stopManagedForTest(t, d, processID, dir)
	require.True(t, d.isAdHocLaunch(processID), "an inline PROC RUN is an ad-hoc launch")

	running := func() bool {
		proc, getErr := d.hub.ProcessManager().GetByPath(processID, dir)
		return getErr == nil && proc.IsRunning()
	}
	require.True(t, running())

	// Config reconcile: nothing to stop or restart.
	plan, err := d.ReconcileProjectConfig(context.Background(), dir)
	require.NoError(t, err)
	assert.Empty(t, plan.StopScripts, "an inline process is not a removed config script")
	assert.Empty(t, plan.RestartScripts)
	assert.True(t, running(), "reconcile must leave the inline process running")

	// Autostart prune: the entry stays, so the live process stays tracked.
	d.registerAndStartScripts(context.Background(), &config.AgntConfig{}, dir, &AutostartResult{}, nil)
	_, kept := d.scriptRegistry.Get("adhoc", dir)
	assert.True(t, kept, "the autostart prune must keep an inline process's registry entry")
	assert.True(t, d.isAdHocLaunch(processID), "and its ad-hoc mark")
	assert.True(t, running())
}

// The members of a PROC RUN-GROUP are inline definitions too.
func TestProcRunGroup_MembersAreAdHocLaunches(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, client, tmpDir := newHubProcTestDaemon(t)
	dir := normalizePath(tmpDir)
	command, args := stayAliveExec()

	_, err := client.ProcRunGroup(daemonclient.ProcRunGroupConfig{
		ProjectPath: tmpDir,
		Processes:   []protocol.GroupProcess{{Name: "member", Command: command, Args: args}},
	})
	require.NoError(t, err)
	processID := makeProcessID(dir, "member")
	stopManagedForTest(t, d, processID, dir)

	assert.True(t, d.isAdHocLaunch(processID))
	plan, err := d.ReconcileProjectConfig(context.Background(), dir)
	require.NoError(t, err)
	assert.Empty(t, plan.StopScripts)
}

// A PROC RUN from_config process IS declared in config and keeps following it:
// when the script is removed from .agnt.kdl, reconcile stops it and the
// autostart prune drops its entry.
func TestProcRunFromConfig_StillFollowsConfig(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, client, tmpDir := newHubProcTestDaemon(t)
	dir := normalizePath(tmpDir)
	command, args := stayAliveExec()
	kdlPath := filepath.Join(tmpDir, ".agnt.kdl")
	declared := "scripts {\n    svc {\n        " + kdlExec(command, args) + "\n    }\n}\n"
	require.NoError(t, os.WriteFile(kdlPath, []byte(declared), 0o644))

	// An earlier ad-hoc launch under the same name must not leave the
	// declared script exempt.
	processID := makeProcessID(dir, "svc")
	d.markAdHocLaunch(processID, false)

	_, err := client.ProcRun("svc", daemonclient.ProcRunConfig{ProjectPath: tmpDir, FromConfig: true})
	require.NoError(t, err)
	stopManagedForTest(t, d, processID, dir)
	require.False(t, d.isAdHocLaunch(processID), "a from_config launch is the declared script, not an ad-hoc one")
	proc, err := d.hub.ProcessManager().GetByPath(processID, dir)
	require.NoError(t, err)
	require.True(t, proc.IsRunning())

	// The script leaves the config.
	require.NoError(t, os.WriteFile(kdlPath, []byte("scripts {\n}\n"), 0o644))

	plan, err := d.ReconcileProjectConfig(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"svc"}, plan.StopScripts, "a config script removed from config is stopped by reconcile")
	require.Eventually(t, func() bool { return !proc.IsRunning() }, 15*time.Second, 20*time.Millisecond)

	// And an entry for a script no longer in config is pruned at autostart.
	_, err = d.registerScriptEntry("svc", dir, scriptConfigToEntry(&config.ScriptConfig{Command: command, Args: args}))
	require.NoError(t, err)
	d.registerAndStartScripts(context.Background(), &config.AgntConfig{}, dir, &AutostartResult{}, nil)
	_, kept := d.scriptRegistry.Get("svc", dir)
	assert.False(t, kept, "a config script's entry is pruned once the config no longer declares it")
}

// startupLogEntries returns the project's STARTUP-LOG entries for one process,
// through the hub verb the `daemon startup_log` tool uses.
func startupLogEntries(t *testing.T, client *daemonclient.Client, projectPath, processID string) []map[string]interface{} {
	t.Helper()
	resp, err := client.StartupLog(100, protocol.DirectoryFilter{Directory: projectPath})
	require.NoError(t, err)
	raw, _ := resp["entries"].([]interface{})
	var out []map[string]interface{}
	for _, e := range raw {
		if entry, ok := e.(map[string]interface{}); ok && entry["process_id"] == processID {
			out = append(out, entry)
		}
	}
	return out
}

// A background RUN of a command that exits 0 inside the startup watch is a
// one-shot that succeeded. The start path recorded it as a failed start, so
// the agent's startup log showed an error for a command that worked.
func TestRun_ZeroExitOneShotIsNotAStartupError(t *testing.T) {
	// No t.Parallel: starts real processes.
	_, client, tmpDir := newHubProcTestDaemon(t)
	dir := normalizePath(tmpDir)

	command, args := echoExec("done")
	resp, err := client.Run(protocol.RunConfig{ID: "oneshot", Path: tmpDir, Raw: true, Command: command, Args: args})
	require.NoError(t, err)
	processID, _ := resp["process_id"].(string)
	require.Equal(t, makeProcessID(dir, "oneshot"), processID)

	// The start path finishes after the reply; wait for its verdict.
	var entries []map[string]interface{}
	require.Eventually(t, func() bool {
		entries = startupLogEntries(t, client, tmpDir, processID)
		for _, e := range entries {
			if e["event_type"] == "completed" {
				return true
			}
		}
		return false
	}, 15*time.Second, 25*time.Millisecond, "a zero exit must be recorded as a completed command")
	for _, e := range entries {
		assert.NotEqual(t, "error", e["level"], "a successful one-shot must not appear as an error: %v", e)
	}

	// A non-zero exit is still a failure, and still says so.
	failCommand, failArgs := failExec()
	resp, err = client.Run(protocol.RunConfig{ID: "broken", Path: tmpDir, Raw: true, Command: failCommand, Args: failArgs})
	require.NoError(t, err, "a background RUN that started and then failed is not a refused start")
	failedID, _ := resp["process_id"].(string)
	require.Eventually(t, func() bool {
		for _, e := range startupLogEntries(t, client, tmpDir, failedID) {
			if e["level"] == "error" {
				return true
			}
		}
		return false
	}, 15*time.Second, 25*time.Millisecond, "a non-zero exit inside the startup watch must still be an error entry")
	for _, e := range startupLogEntries(t, client, tmpDir, failedID) {
		assert.NotEqual(t, "completed", e["event_type"], "a failed command must not be recorded as completed: %v", e)
	}
}

// The same holds for an inline PROC RUN, and the registry agrees with the log.
func TestProcRunInline_ZeroExitOneShotIsNotAStartupError(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, client, tmpDir := newHubProcTestDaemon(t)
	dir := normalizePath(tmpDir)

	command, args := echoExec("done")
	_, err := client.ProcRun("oneshot", daemonclient.ProcRunConfig{Command: command, Args: args, ProjectPath: tmpDir})
	require.NoError(t, err)
	processID := makeProcessID(dir, "oneshot")

	for _, e := range startupLogEntries(t, client, tmpDir, processID) {
		assert.NotEqual(t, "error", e["level"], "a successful one-shot must not appear as an error: %v", e)
	}
	entry, ok := d.scriptRegistry.Get("oneshot", dir)
	require.True(t, ok)
	assert.NotEqual(t, "failed", entry.State().String(), "the registry must not call a successful one-shot failed")
}

// An inline PROC RUN is how the run tool sends an ad-hoc command that carries
// depends_on, so the caller's session owns it as it owns a RUN — including
// when the launch waited on a dependency first — and session cleanup stops it.
func TestProcRunInline_CallerSessionOwnsLaunch(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, client, tmpDir := newHubProcTestDaemon(t)
	dir := normalizePath(tmpDir)
	_, err := client.SessionRegister("inline-owner", "-", tmpDir, "test", nil)
	require.NoError(t, err)

	command, args := stayAliveExec()
	_, err = client.ProcRun("api", daemonclient.ProcRunConfig{
		Command: command, Args: args, ProjectPath: tmpDir,
		DependsOn: []string{"db"}, DependsOnTimeout: 30,
	})
	require.NoError(t, err)
	processID := makeProcessID(dir, "api")
	stopManagedForTest(t, d, processID, dir)
	d.readySignaler.SignalReady(makeProcessID(dir, "db"))

	require.Eventually(t, func() bool {
		proc, getErr := d.hub.ProcessManager().GetByPath(processID, dir)
		return getErr == nil && proc.IsRunning()
	}, 15*time.Second, 20*time.Millisecond)
	entry, ok := d.scriptRegistry.Get("api", dir)
	require.True(t, ok)
	assert.Equal(t, "inline-owner", entry.Owner())
}

// A caller's negative depends_on_timeout on a from_config launch means
// unbounded, and wins over the wait the script declares, as a positive one
// does.
func TestProcRunFromConfig_CallerUnboundedTimeoutWins(t *testing.T) {
	d, client, tmpDir := newHubProcTestDaemon(t)
	dir := normalizePath(tmpDir)
	kdl := "scripts {\n    db {\n        run \"echo db\"\n    }\n    api {\n        run \"echo api\"\n        depends-on \"db\" timeout=60\n    }\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, ".agnt.kdl"), []byte(kdl), 0o644))

	resp, err := client.ProcRun("api", daemonclient.ProcRunConfig{ProjectPath: tmpDir, FromConfig: true, DependsOnTimeout: -1})
	require.NoError(t, err)
	assert.Equal(t, "pending", resp["state"])
	processID := makeProcessID(dir, "api")
	t.Cleanup(func() { d.pendingProcs.Cancel(processID) })

	pending, ok := d.pendingProcs.Get(processID)
	require.True(t, ok)
	assert.True(t, pending.Deadline.IsZero(), "an unbounded caller timeout must replace the configured 60s, got deadline %v", pending.Deadline)
}
