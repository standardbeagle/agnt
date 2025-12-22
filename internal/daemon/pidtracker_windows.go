//go:build windows

package daemon

import (
	"os"

	"golang.org/x/sys/windows"
)

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

// isProcessAlive checks if a process is still running on Windows.
func isProcessAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	err = windows.GetExitCodeProcess(handle, &exitCode)
	if err != nil {
		return false
	}

	// STILL_ACTIVE (259) means the process is still running
	return exitCode == 259
}
