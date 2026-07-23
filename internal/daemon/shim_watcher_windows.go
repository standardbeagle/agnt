//go:build windows

package daemon

import (
	"os"
	"os/exec"

	"github.com/standardbeagle/agnt/internal/platform"
)

// detachShimWatcher is a no-op on Windows: the daemon does not place the
// watcher in a Job Object, so the watcher already outlives the daemon.
func detachShimWatcher(cmd *exec.Cmd) {}

// shimWatcherAlive probes the recorded watcher PID. FindProcess on Windows
// opens a real handle and fails for dead PIDs, unlike on Unix. Birth-token
// comparison happens inside platform.ProcessBirthID availability; when a
// token was recorded we additionally require it to match so a recycled PID
// is not mistaken for the watcher.
func shimWatcherAlive(pid int, birth string) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	if birth == "" {
		return true
	}
	current, ok := platform.ProcessBirthID(pid)
	return ok && current == birth
}
