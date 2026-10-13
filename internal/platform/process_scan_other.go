//go:build !windows && !linux && !darwin

package platform

import (
	"fmt"
	"runtime"
)

// Scan is not implemented on Unixes other than Linux and macOS. It returns an
// error rather than an empty list so callers can tell "no processes" from "the
// process table was not read": MembersOfPGID reports the failure as nil, and
// the daemon's session pgid reap then declines instead of treating the group
// as empty.
func Scan() ([]ProcInfo, error) {
	return nil, fmt.Errorf("process enumeration is not implemented on %s", runtime.GOOS)
}
