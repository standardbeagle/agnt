//go:build unix

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/require"
)

// PROC STOP force must kill at once. The client sent "force" but the handler
// never read it, so a process that ignores SIGTERM sat out the whole graceful
// timeout before being killed.
func TestProcStop_ForceSkipsGracefulTimeout(t *testing.T) {
	// No t.Parallel(): starts real processes.
	const graceful = 30 * time.Second
	procCfg := process.DefaultManagerConfig()
	procCfg.GracefulTimeout = graceful
	d := NewForTest(t, DaemonConfig{
		SocketPath:            shortSockPath(t),
		MaxClients:            4,
		WriteTimeout:          2 * time.Second,
		StartupMonitorTimeout: 150 * time.Millisecond,
		ProcessConfig:         procCfg,
	})
	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "stubborn")
	// An ignored signal stays ignored across exec, so sleep ignores it too.
	_, err := d.StartScript(context.Background(), StartScriptConfig{
		ProcessID: id, ProjectPath: dir, WorkingDir: dir,
		Command: "sh", Args: []string{"-c", "trap '' TERM; while :; do sleep 0.1; done"},
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		proc, getErr := d.hub.ProcessManager().GetByPath(id, dir)
		return getErr == nil && proc.IsRunning()
	}, 10*time.Second, 10*time.Millisecond)

	client := daemonclient.NewClient(daemonclient.WithSocketPath(d.config.SocketPath))
	require.NoError(t, client.Connect())
	t.Cleanup(func() { _ = client.Close() })

	start := time.Now()
	result, err := client.ProcStop(id, true)
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Less(t, elapsed, graceful/2, "force stop waited out the graceful timeout")
	require.Contains(t, result["message"], "killed")

	proc, err := d.hub.ProcessManager().GetByPath(id, dir)
	require.NoError(t, err)
	require.Eventually(t, proc.IsDone, 5*time.Second, 10*time.Millisecond)
}
