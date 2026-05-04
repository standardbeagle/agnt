//go:build linux

// Linux orphan-pgid scanner. Walks /proc to enumerate processes, classify
// orphan pgids (leader dead, members live), and surface ancestor metadata
// for the daemon-startup ownership gate.
//
// macOS uses sysctl KERN_PROC_ALL — see orphanpgid_darwin.go.
// Other Unixes (FreeBSD, OpenBSD, etc.) get stubs in orphanpgid_other.go.
// Windows uses Job Objects — see orphanpgid_windows.go.

package platform

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// (OrphanPGID and AncestorInfo are defined in orphanpgid_classify.go so
// they exist on every non-windows platform — darwin and other-unix stubs
// reference the same types.)

// ScanOrphanedPGIDs enumerates /proc and returns every pgid that qualifies
// as orphaned for the purpose of daemon-startup cleanup. A pgid is orphaned
// when ALL of the following are true:
//
//  1. The pgid itself is > 1 (pid 0/1 are never treated as pgids).
//  2. The pgid is NOT in excludePGIDs (typically: the caller's own pgid plus
//     the pgid of the daemon process and any ancestor we don't want to kill).
//  3. The leader PID (the process whose PID equals the pgid, i.e. the
//     original session leader) does NOT currently exist. This is the
//     "leader died, members reparented to init" case Slice B targets.
//  4. At least one live process is still a member of the pgid.
//  5. EVERY live member is owned by the caller's real uid. This is the
//     safety barrier: if any member belongs to a different uid, we skip
//     the entire pgid to avoid ever touching another user's processes.
//     (root daemon, for example, must not reap a non-root user's pgids.)
//
// callerUID is the uid that members must match. Pass syscall.Getuid() in
// production; tests pass an arbitrary value to exercise filtering paths.
//
// Returns nil on /proc read error or when /proc is unavailable (non-Linux
// Unix). Callers that need absolute correctness on those platforms must
// layer their own fallback. On Linux and WSL2 /proc is always present.
func ScanOrphanedPGIDs(callerUID int, excludePGIDs map[int]bool) []OrphanPGID {
	procs, err := Scan()
	if err != nil || len(procs) == 0 {
		return nil
	}

	// Build pgid -> []pid index in a single pass. Only include processes
	// for which we can read the pgid (readPGID) OR fall back to
	// syscall.Getpgid. Processes we cannot classify are dropped.
	index := make(map[int][]int, len(procs))
	for _, p := range procs {
		if p.PID <= 1 {
			continue
		}
		pgid := readPGID(p.PID)
		if pgid == 0 {
			if gp, err := syscall.Getpgid(p.PID); err == nil {
				pgid = gp
			}
		}
		if pgid <= 1 {
			continue
		}
		index[pgid] = append(index[pgid], p.PID)
	}

	// Classify each pgid. Uid filtering reads /proc/<pid>/status which is
	// a second syscall per member, so we short-circuit on the cheaper
	// checks first (exclude set, leader-alive).
	var out []OrphanPGID
	for pgid, members := range index {
		if excludePGIDs[pgid] {
			continue
		}
		if isProcessAlive(pgid) {
			// Leader still running -- this is a live session, not an
			// orphan. Skip.
			continue
		}
		if len(members) == 0 {
			continue
		}
		if !allMembersOwnedBy(members, callerUID) {
			continue
		}
		out = append(out, OrphanPGID{PGID: pgid, Members: append([]int(nil), members...)})
	}
	return out
}

// isProcessAlive returns true when the kernel can deliver signal 0 to pid.
// This is the standard POSIX "does this process exist" probe. A return of
// ESRCH means the pid has been reaped; EPERM means it exists but we cannot
// signal it (still alive for our purposes -- the leader is not dead). Any
// other error is treated as alive to err on the side of NOT killing.
func isProcessAlive(pid int) bool {
	if pid <= 1 {
		return true // pid 1 and kernel threads are always "alive enough"
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.ESRCH) {
		return false
	}
	// EPERM, EINVAL, etc -- process exists but is in some state we cannot
	// probe. Treat as alive.
	return true
}

// allMembersOwnedBy returns true when every pid in members has a real uid
// matching wantUID. If /proc/<pid>/status cannot be read for any member
// (race: process exited between Scan and status read) that member is
// skipped, not failed -- the remaining members still have to match. If
// ALL members raced away the group is effectively empty and we return
// false so ScanOrphanedPGIDs does not emit an empty-member orphan.
func allMembersOwnedBy(members []int, wantUID int) bool {
	matched := 0
	for _, pid := range members {
		uid, ok := readProcUID(pid)
		if !ok {
			continue
		}
		if uid != wantUID {
			return false
		}
		matched++
	}
	return matched > 0
}

// ReadProcCmdline returns the /proc/<pid>/cmdline contents with NUL
// separators rewritten to spaces, trimmed of any trailing whitespace. An
// empty string is returned on any read error or when the entry is empty
// (kernel threads, raced-away processes).
//
// The returned string is suitable for substring matching against
// well-known invocations like "agnt run" or a daemon binary path. Callers
// must not attempt to round-trip it back into argv — the space-join is
// lossy for arguments that themselves contain spaces.
func ReadProcCmdline(pid int) string {
	if pid <= 0 {
		return ""
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(data) == 0 {
		return ""
	}
	// Trim trailing NULs then rewrite remaining NULs as spaces.
	trimmed := strings.TrimRight(string(data), "\x00")
	return strings.ReplaceAll(trimmed, "\x00", " ")
}

// ReadProcCwd returns the resolved /proc/<pid>/cwd symlink target. An
// empty string is returned on any error (permission denied, process gone,
// non-Linux /proc without cwd entries). Callers must not assume the path
// is still meaningful if the target directory has been deleted — the
// kernel reports "(deleted)" suffixes in that case, which this function
// preserves verbatim so callers can detect it if they care.
func ReadProcCwd(pid int) string {
	if pid <= 0 {
		return ""
	}
	cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return ""
	}
	return cwd
}

// readProcPPID returns the parent PID of pid by parsing /proc/<pid>/stat.
// Returns 0 on any error. The stat layout after the comm field is:
//
//	state(1) ppid(2) pgrp(3) ...
//
// where indices are 1-based relative to the first field AFTER the closing
// paren of comm.
func readProcPPID(pid int) int {
	if pid <= 0 {
		return 0
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	idx := strings.LastIndex(string(data), ")")
	if idx < 0 || idx+1 >= len(data) {
		return 0
	}
	fields := strings.Fields(string(data[idx+1:]))
	if len(fields) < 2 {
		return 0
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0
	}
	return ppid
}

// WalkParents walks the parent chain of pid upward, returning each
// visited ancestor as an AncestorInfo with Cmdline and Cwd prepopulated.
// The walk includes PID 1 (init) in the chain — in production that's
// /sbin/init or systemd, whose cmdline and cwd will not match any agnt
// pattern and therefore contribute no evidence to ownership gates. In a
// PID namespace (unshare), PID 1 may be a meaningful ancestor (e.g. the
// namespace's designated init), which is exactly why we visit it: the
// procisolation daemon tests need to resolve reparented grandchildren
// back to a PID-namespaced init that impersonates a live agnt run.
//
// The walk stops when:
//
//  1. The next ppid is <= 0 (we've gone past init or the PID is a kernel
//     thread with no parent recorded).
//  2. The current pid itself is 0 (/proc/0 does not exist).
//  3. /proc/<pid>/stat cannot be read for the current pid (race: the
//     ancestor vanished between visits).
//  4. A cycle is detected (PID re-seen; defensive, should not happen).
//  5. A safety limit of 64 ancestors is reached (defensive).
//
// The returned slice does NOT include pid itself — only strict ancestors.
// Callers that want the starting pid in the chain should prepend it.
//
// This function is used by the daemon-startup orphan-pgid ownership gate
// to find an ancestor whose cmdline/cwd identifies the candidate pgid as
// owned by this daemon. Errors reading individual ancestors are silently
// skipped: the walk proceeds with whatever it can read.
func WalkParents(pid int) []AncestorInfo {
	if pid <= 1 {
		return nil
	}
	const maxDepth = 64
	visited := make(map[int]bool, 8)
	out := make([]AncestorInfo, 0, 8)

	cur := pid
	for depth := 0; depth < maxDepth; depth++ {
		ppid := readProcPPID(cur)
		if ppid <= 0 {
			// No recorded parent — either kernel thread or /proc race.
			return out
		}
		if visited[ppid] {
			// Cycle — defensive. Stop.
			return out
		}
		visited[ppid] = true

		ancestor := AncestorInfo{
			PID:     ppid,
			PPID:    readProcPPID(ppid),
			Cmdline: ReadProcCmdline(ppid),
			Cwd:     ReadProcCwd(ppid),
		}
		out = append(out, ancestor)

		// Stop AFTER visiting init. PID 1 has no meaningful parent to
		// continue walking into; its own ppid in /proc is 0.
		if ppid == 1 {
			return out
		}

		cur = ppid
	}
	return out
}

// readProcUID returns the real uid of pid by parsing /proc/<pid>/status.
// Returns (0, false) on any error. The "Uid:" line has format
// "Uid:\treal\teffective\tsaved\tfs" -- we want the first field.
func readProcUID(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(line[len("Uid:"):])
		if len(fields) == 0 {
			return 0, false
		}
		uid, err := strconv.Atoi(fields[0])
		if err != nil {
			return 0, false
		}
		return uid, true
	}
	return 0, false
}
