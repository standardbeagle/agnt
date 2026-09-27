//go:build e2e

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// `agnt daemon restart` must hand back a DETACHED daemon and return. It used to stop the
// daemon and then run `daemon start` in its own foreground, so the command never
// returned: a script or agent that ran it hung, and the new daemon died with the shell
// that started it (seen on beagle-ab2, 2026-09-27).
func TestE2E_DaemonRestart_ReturnsAndLeavesADetachedDaemon(t *testing.T) {
	bin := e2eAgntBinary(t)
	dir, err := os.MkdirTemp("", "agnt-restart-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")

	first := exec.Command(bin, "daemon", "start", "--socket", socket)
	require.NoError(t, first.Start())
	t.Cleanup(func() {
		_ = exec.Command(bin, "daemon", "stop", "--socket", socket).Run()
		_ = first.Process.Kill()
		_, _ = first.Process.Wait()
	})
	require.Eventually(t, func() bool {
		return exec.Command(bin, "daemon", "status", "--socket", socket).Run() == nil
	}, 20*time.Second, 100*time.Millisecond, "the first daemon never came up")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	restart := exec.CommandContext(ctx, bin, "daemon", "restart", "--socket", socket)
	out, err := restart.CombinedOutput()
	require.NoError(t, ctx.Err(), "daemon restart did not return within 30s:\n%s", out)
	require.NoError(t, err, "daemon restart failed:\n%s", out)

	require.NoError(t, exec.Command(bin, "daemon", "status", "--socket", socket).Run(),
		"no daemon is running after restart:\n%s", out)
	pidText, err := os.ReadFile(socket + ".pid")
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	require.NoError(t, err)
	require.NotEqual(t, restart.Process.Pid, pid, "the daemon is the restart command itself")
	require.True(t, isProcessAlive(pid), "the restarted daemon (pid %d) is not alive", pid)
}
