//go:build windows

package process

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcAttr sets platform-specific process attributes for Windows.
// On Windows, we don't use process groups the same way as Unix.
func setProcAttr(cmd *exec.Cmd) {
	// On Windows, we use CREATE_NEW_PROCESS_GROUP to allow Ctrl+C handling
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// signalProcessGroup sends a termination signal to the process on Windows.
// Windows doesn't have the same signal semantics as Unix.
func (pm *ProcessManager) signalProcessGroup(pid int, sig syscall.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	// On Windows, Kill() sends TerminateProcess which is like SIGKILL
	return proc.Kill()
}

// signalTerm attempts graceful termination on Windows.
// Since Windows doesn't have SIGTERM, we just kill the process.
func signalTerm(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// signalKill forcefully terminates the process on Windows.
func signalKill(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}

// isProcessAlive checks if a process is still running on Windows.
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Windows, FindProcess always succeeds for valid PIDs.
	// We need to try to open the process to check if it exists.
	// A simple approach is to try Signal(0) equivalent
	// But os.Process doesn't have that, so we use a workaround
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// isNoSuchProcess returns true if the error indicates the process doesn't exist.
func isNoSuchProcess(err error) bool {
	// On Windows, check for common "process not found" errors
	return os.IsNotExist(err) || err == os.ErrProcessDone
}
