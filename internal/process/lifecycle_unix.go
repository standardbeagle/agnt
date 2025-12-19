//go:build !windows

package process

import (
	"os/exec"
	"syscall"
)

// setProcAttr sets platform-specific process attributes for Unix systems.
// On Linux, children inherit the daemon's process group so that killing
// the daemon's process group kills all children automatically.
// On macOS/BSD, we still create separate groups because they lack other
// mechanisms like PDEATHSIG for lifecycle binding.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: false, // Inherit parent's process group (daemon's PGID)
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

// SetupJobObject is a no-op on Unix.
// On Windows, this creates a Job Object to manage child processes.
func SetupJobObject(cmd *exec.Cmd) error {
	return nil
}

// CleanupJobObject is a no-op on Unix.
// On Windows, this closes the Job Object handle.
func CleanupJobObject(pid int) {
	// No-op on Unix - process groups are handled by the kernel
}
