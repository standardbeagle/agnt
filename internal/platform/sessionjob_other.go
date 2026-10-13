//go:build !windows

package platform

// KillSessionJobObject is a no-op on non-Windows platforms. The
// session-wide process containment on Unix is implemented via POSIX
// process groups (see sessionpgid_unix.go's KillSessionPGID) so this
// stub exists only so cross-platform callers in internal/daemon can
// invoke the API unconditionally without build tags. On Windows it acts
// only on a job this process created (a handle value received from
// another process is refused, never terminated or closed); here there is
// no job object at all, so it returns nil whatever it is given.
func KillSessionJobObject(handle uintptr) error {
	return nil
}
