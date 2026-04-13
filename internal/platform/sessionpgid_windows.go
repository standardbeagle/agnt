//go:build windows

package platform

import "time"

// KillSessionPGID is a no-op on Windows. Windows has no POSIX process
// groups; the equivalent session-wide containment is a Job Object with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, which is created and torn down by
// the Windows-specific `agnt run` path. See cmd/agnt/run_windows.go.
//
// Returning nil here lets cross-platform callers invoke the function
// unconditionally without a runtime.GOOS check: on Windows the sessionPGID
// parameter is always zero (never reported by the client) so this stub
// simply short-circuits.
func KillSessionPGID(sessionPGID int, killSelfPID int, gracefulTimeout time.Duration, aggressive bool) error {
	return nil
}

// MembersOfPGID has no POSIX equivalent on Windows; returns nil so
// cross-platform callers compile without runtime.GOOS branches. The
// Windows Job Object model does not expose member enumeration through
// this API.
func MembersOfPGID(pgid int) []int {
	return nil
}
