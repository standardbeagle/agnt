//go:build !windows

package process

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// killOrphanProcess kills an orphan process. When pgid is a real process-group
// leader id (pid == pgid, i.e. the process was started with Setpgid) we signal
// the whole group to catch children; otherwise we only signal the single pid.
// Treating an arbitrary descendant pid as a pgid (kill(-pid)) would blast an
// unrelated process group that happens to share the numeric id.
func killOrphanProcess(pid, pgid int) {
	if pgid > 1 && pgid == pid {
		// pid is its own group leader — killing the group catches its children.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// scanDescendantPIDs returns all descendant PIDs for the given process.
// Delegates to findAllDescendants which uses /proc (Linux) or pgrep (macOS).
func scanDescendantPIDs(pid int) []int {
	return findAllDescendants(pid)
}

// processIdentity returns an opaque token capturing the OS-level identity of a
// process so a recycled PID can be distinguished from the original. On Linux it
// combines the process start time (field 22 of /proc/<pid>/stat, monotonic in
// jiffies since boot) with the owning uid. Returns "" when identity cannot be
// determined (non-Linux, or the process is already gone) — callers must treat
// an empty identity as "cannot verify" and fall back to a liveness check only.
func processIdentity(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	s := string(data)
	// comm (field 2) is parenthesized and may contain spaces/parens; anchor on
	// the last ')'. After ") " the fields are state(f3), ppid(f4), ... The
	// starttime is field 22 → index 19 in the post-')' slice.
	idx := strings.LastIndexByte(s, ')')
	if idx < 0 || idx+2 >= len(s) {
		return ""
	}
	fields := strings.Fields(s[idx+2:])
	if len(fields) < 20 {
		return ""
	}
	starttime := fields[19]

	uid := "?"
	if fi, err := os.Stat(fmt.Sprintf("/proc/%d", pid)); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			uid = fmt.Sprintf("%d", st.Uid)
		}
	}
	return starttime + ":" + uid
}
