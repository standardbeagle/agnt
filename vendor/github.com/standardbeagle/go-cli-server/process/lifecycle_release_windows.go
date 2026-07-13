//go:build windows

package process

import "golang.org/x/sys/windows"

// hasReleasedResources is a downstream compatibility shim for
// github.com/standardbeagle/go-cli-server v0.5.4, whose platform-neutral
// manager calls this helper but only lifecycle_unix.go defines it.
//
// Remove this file when upgrading to an upstream release that provides the
// Windows implementation. Unknown/access-denied states are conservatively
// treated as still holding resources; callers must never report a port free
// merely because process state could not be inspected.
func hasReleasedResources(pid int) bool {
	if pid <= 0 {
		return true
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return isNoSuchProcess(err)
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode != 259 // STILL_ACTIVE
}
