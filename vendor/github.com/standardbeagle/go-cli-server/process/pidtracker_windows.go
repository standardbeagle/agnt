//go:build windows

package process

import "os"

// killOrphanProcess kills an orphan process on Windows.
// On Windows, we don't have process groups in the Unix sense,
// so we just kill the process directly.
func killOrphanProcess(pid, pgid int) {
	// Try to terminate the process
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Kill()
}

// scanDescendantPIDs is a no-op on Windows.
// Windows uses Job Objects which automatically track all descendants.
func scanDescendantPIDs(pid int) []int {
	return nil
}

// killStoredDescendants is a no-op on Windows.
// Windows Job Objects handle descendant cleanup automatically.
// UPSTREAM: signature parity for the identity-bearing Unix cleanup contract;
// Windows Job Objects own descendant teardown.
func killStoredDescendants(pids []int, identities map[int]string) {}

// processIdentity cannot be captured cheaply on Windows here; return "" so
// callers fall back to a liveness-only check. Job Objects already bound
// descendant lifetime, so the recycled-PID window this guards is narrower.
func processIdentity(pid int) string { return "" }
