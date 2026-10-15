package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	goprocess "github.com/standardbeagle/go-cli-server/process"
)

// PROC STOP on a process still in its spawn window (Starting) answered
// "already stopped" and left it alone; it was published Running moments later
// and kept running after the user had stopped it.
func TestProcStop_StartingProcessIsStopped(t *testing.T) {
	// No t.Parallel(): starts a real process.
	d := newReviewBatchDaemon(t)
	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "slowstart")
	cmd, args := stayAliveExec()
	_, err := d.StartScript(context.Background(), StartScriptConfig{
		ProcessID: id, ProjectPath: dir, WorkingDir: dir, Command: cmd, Args: args,
	})
	require.NoError(t, err)
	proc, err := d.hub.ProcessManager().GetByPath(id, dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.hub.ProcessManager().Stop(context.Background(), id) })

	// Put it back in the spawn window, then let the spawn finish shortly after
	// PROC STOP arrives — the order a stop racing a start sees.
	require.True(t, proc.CompareAndSwapState(goprocess.StateRunning, goprocess.StateStarting))
	go func() {
		time.Sleep(200 * time.Millisecond)
		proc.CompareAndSwapState(goprocess.StateStarting, goprocess.StateRunning)
	}()

	client := daemonclient.NewClient(daemonclient.WithSocketPath(d.config.SocketPath))
	require.NoError(t, client.Connect())
	t.Cleanup(func() { _ = client.Close() })
	_, err = client.ProcStop(id, false)
	require.NoError(t, err)

	assert.Eventually(t, proc.IsDone, 10*time.Second, 10*time.Millisecond,
		"a process stopped while starting must not be left running (state %s)", proc.State())
}
