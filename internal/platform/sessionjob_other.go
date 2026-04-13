//go:build !windows

package platform

// KillSessionJobObject is a no-op on non-Windows platforms. The
// session-wide process containment on Unix is implemented via POSIX
// process groups (see sessionpgid_unix.go's KillSessionPGID) so this
// stub exists only so cross-platform callers in internal/daemon can
// invoke the API unconditionally without build tags. The handle
// parameter is always zero on non-Windows (the client never reports a
// job handle when SessionJobHandle is unset) and this function
// returns nil immediately.
func KillSessionJobObject(handle uintptr) error {
	return nil
}
