//go:build !windows

package process

import (
	"os/exec"
	"syscall"
)

// setProcAttr sets platform-specific process attributes for Unix systems.
// This enables process groups for clean child process shutdown.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// signalProcessGroup sends a signal to the process group.
func (pm *ProcessManager) signalProcessGroup(pid int, sig syscall.Signal) error {
	// Try to get process group ID
	pgid, err := syscall.Getpgid(pid)
	if err == nil && pgid > 0 {
		// Use negative PID to signal entire process group
		return syscall.Kill(-pgid, sig)
	}
	// Fall back to signaling just the process
	return syscall.Kill(pid, sig)
}

// signalTerm sends SIGTERM to the process.
func signalTerm(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// signalKill sends SIGKILL to the process.
func signalKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// isProcessAlive checks if a process is still running.
func isProcessAlive(pid int) bool {
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

// isNoSuchProcess returns true if the error indicates the process doesn't exist.
func isNoSuchProcess(err error) bool {
	return err == syscall.ESRCH
}
