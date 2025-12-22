//go:build !windows

package daemon

import "syscall"

// killOrphanProcess kills an orphan process and its process group.
func killOrphanProcess(pid, pgid int) {
	// Try to kill the process group first (gets all children)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)

	// Also try direct kill in case process group fails
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// isProcessAlive checks if a process is still running.
func isProcessAlive(pid int) bool {
	// Sending signal 0 checks if we can signal the process
	// without actually sending a signal
	err := syscall.Kill(pid, syscall.Signal(0))
	return err == nil
}
