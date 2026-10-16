//go:build linux

package platform

import (
	"fmt"
	"os"
	"strconv"
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

// processLineage returns pid's parent PID and birth identity from a single
// read of /proc/<pid>/stat, so both describe the same process. A zombie
// reports false: it has exited and cannot be killed.
func processLineage(pid int) (ppid int, birth string, ok bool) {
	if pid <= 1 {
		return 0, "", false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, "", false
	}
	fields := parseStatFieldsAfterComm(data)
	if len(fields) <= 19 || fields[0] == "Z" || fields[19] == "" {
		return 0, "", false
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, "", false
	}
	return ppid, fields[19], true
}
