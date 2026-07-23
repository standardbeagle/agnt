//go:build !windows

package daemon

import (
	"os/exec"
	"syscall"

	"github.com/standardbeagle/agnt/internal/platform"
)

// detachShimWatcher puts the watcher in its own session so it survives the
// daemon's process group (and any session-pgid reaping) after a SIGKILL.
func detachShimWatcher(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// shimWatcherAlive probes the recorded watcher PID with PID-recycle
// protection: the process must exist AND (when a birth token was recorded)
// still carry the same birth identity. Without the birth check, a recycled
// PID would make the daemon believe a dead watcher is alive forever.
func shimWatcherAlive(pid int, birth string) bool {
	err := syscall.Kill(pid, 0)
	if err != nil && err != syscall.EPERM {
		return false
	}
	if birth == "" {
		return true // platform gave no token at spawn; PID-only fallback
	}
	current, ok := platform.ProcessBirthID(pid)
	return ok && current == birth
}
