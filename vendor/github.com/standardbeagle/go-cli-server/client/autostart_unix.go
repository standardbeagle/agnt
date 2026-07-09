//go:build unix

package client

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr sets Unix-specific process attributes for daemon mode.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// Create new process group
		Setpgid: true,
		// Note: Setsid removed - causes "operation not permitted" in sandboxed environments
		// The daemon still works without being a session leader
	}
}

// lockOwnerAlive reports whether the startup-lock owner PID is still a live
// process. Signal 0 probes existence without delivering anything. EPERM means the
// PID exists but is owned by another user — still alive, so the lock is held.
func lockOwnerAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
