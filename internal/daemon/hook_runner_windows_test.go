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

// hookChildGone reports whether the process id names stops running within a
// generous ceiling.
func hookChildGone(id platform.ProcessIdentity) bool {
	for i := 0; i < 200; i++ {
		if cur, ok := platform.ProcessBirthID(id.PID); !ok || cur != id.Birth {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// identifyHookChild identifies a process the hook under test started and
// registers an identity-checked cleanup: it can never reach a later holder of
// the PID.
func identifyHookChild(t *testing.T, pid int) platform.ProcessIdentity {
	t.Helper()
	id, ok := platform.IdentifyProcess(pid)
	require.True(t, ok, "cannot identify hook child %d", pid)
	t.Cleanup(func() { _ = platform.KillProcess(id, 0) })
	return id
}

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
	run := configureHookProcessGroup(c)
	done := make(chan error, 1)
	go func() { done <- run() }()

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
	child := identifyHookChild(t, childPID)

	cancel() // what the hook timeout does
	<-done

	require.True(t, hookChildGone(child), "hook child %d survived the hook timeout", childPID)
}
