package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/standardbeagle/agnt/internal/daemonclient"
)

// PROC STOP on a process that crashed under auto-restart answered "already
// stopped" and left the registration in place, so the monitor relaunched the
// process when its restart delay ran out — after the user had stopped it.
func TestProcStop_ExitedProcessCancelsPendingAutoRestart(t *testing.T) {
	// No t.Parallel(): starts real processes.
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "crasher")
	cmd, args := failExec()

	const restartDelay = 2 * time.Second
	cfg := DefaultAutoRestartConfig()
	cfg.Enabled = true
	cfg.MaxRestarts = 5
	cfg.RestartDelay = restartDelay
	_, err := d.StartScript(context.Background(), StartScriptConfig{
		ProcessID: id, ProjectPath: dir, WorkingDir: dir, Command: cmd, Args: args,
		AutoRestart: true, AutoRestartConfig: &cfg,
	})
	require.NoError(t, err)

	// The crash has happened; the monitor is now waiting out the delay.
	require.Eventually(t, func() bool {
		proc, getErr := d.hub.ProcessManager().GetByPath(id, dir)
		return getErr == nil && proc.IsDone() && len(startupEvents(d, id, "exited")) > 0
	}, 10*time.Second, 10*time.Millisecond)
	require.True(t, d.autoRestarter.IsRegistered(id))

	client := daemonclient.NewClient(daemonclient.WithSocketPath(d.config.SocketPath))
	require.NoError(t, client.Connect())
	t.Cleanup(func() { _ = client.Close() })
	_, err = client.ProcStop(id, false)
	require.NoError(t, err)

	assert.False(t, d.autoRestarter.IsRegistered(id), "PROC STOP must cancel the pending auto-restart")
	time.Sleep(restartDelay + time.Second)
	assert.Empty(t, startupEvents(d, id, "restarted"), "a stopped process must not be relaunched")
	t.Cleanup(func() { _ = d.hub.ProcessManager().Stop(context.Background(), id) })
}
