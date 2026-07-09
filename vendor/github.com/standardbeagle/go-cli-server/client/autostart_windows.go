//go:build windows

package client

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

// lockOwnerAlive reports whether the startup-lock owner PID is still a live
// process. OpenProcess fails once the PID is gone.
func lockOwnerAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	syscall.CloseHandle(handle)
	return true
}
