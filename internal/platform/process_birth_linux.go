//go:build linux

package platform

import (
	"fmt"
	"os"
)

// ProcessBirthID returns Linux's per-process starttime clock tick from
// /proc/<pid>/stat. The kernel assigns it at process creation, so PID reuse
// produces a different value even when command line and cwd are identical.
func ProcessBirthID(pid int) (string, bool) {
	if pid <= 1 {
		return "", false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", false
	}
	fields := parseStatFieldsAfterComm(data)
	// starttime is stat field 22; fields[0] is field 3 (state).
	if len(fields) <= 19 || fields[19] == "" {
		return "", false
	}
	return fields[19], true
}
