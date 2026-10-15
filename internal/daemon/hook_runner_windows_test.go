//go:build windows

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

	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/stretchr/testify/require"
)

// A lifecycle hook that times out must take its children with it, not only
// the shell (the default CommandContext cancel kills just the direct child).
func TestHookTimeout_KillsChildProcessesWindows(t *testing.T) {
	// No t.Parallel(): starts real processes.
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	script := `$p = Start-Process -FilePath ping.exe -ArgumentList '-n','120','127.0.0.1' -WindowStyle Hidden -PassThru; ` +
		`[IO.File]::WriteAllText(` + psSingleQuote(pidFile) + `, [string]$p.Id); Start-Sleep -Seconds 120`

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	configureHookProcessGroup(c)
	require.NoError(t, c.Start())

	var childPID int
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
			require.NoError(t, err)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NotZero(t, childPID, "hook never reported its child PID")
	t.Cleanup(func() { _ = platform.KillPID(childPID, 0) })

	cancel() // what the hook timeout does
	_ = c.Wait()

	gone := false
	for i := 0; i < 40 && !gone; i++ {
		if _, alive := hookProcessBirth(childPID); !alive {
			gone = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.True(t, gone, "hook child %d survived the hook timeout", childPID)
}
