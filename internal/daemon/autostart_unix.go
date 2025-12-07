//go:build unix

package daemon

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
