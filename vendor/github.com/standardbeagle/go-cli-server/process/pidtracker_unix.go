//go:build !windows

package process

import "syscall"

// killOrphanProcess kills an orphan process and its process group.
func killOrphanProcess(pid, pgid int) {
	// Try to kill the process group first (gets all children)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)

	// Also try direct kill in case process group fails
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// scanDescendantPIDs returns all descendant PIDs for the given process.
// Delegates to findAllDescendants which uses /proc (Linux) or pgrep (macOS).
func scanDescendantPIDs(pid int) []int {
	return findAllDescendants(pid)
}
