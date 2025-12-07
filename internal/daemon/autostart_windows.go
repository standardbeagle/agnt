//go:build windows

package daemon

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr sets Windows-specific process attributes for daemon mode.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// CREATE_NEW_PROCESS_GROUP - detach from parent console
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
