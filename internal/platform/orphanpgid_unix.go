//go:build !windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// OrphanPGID describes a POSIX process group whose session leader PID is
// no longer alive but whose members are still running, owned by the caller's
// euid, and not members of any of the exclude sets the caller provided.
//
// Found by ScanOrphanedPGIDs. Consumed by daemon startup cleanup to reap
// sessions that leaked across a daemon restart (see Slice B of task
// O9QzO07vM8JB).
type OrphanPGID struct {
	PGID    int   // process group ID (= original session leader PID)
	Members []int // live member PIDs currently in this pgid
}

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
