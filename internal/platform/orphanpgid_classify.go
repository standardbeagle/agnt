//go:build !windows

// Pure orphan-pgid classifier shared between the darwin sysctl path and
// any future BSD/illumos port. Lives outside the linux file because the
// linux impl pre-existed with its own /proc-driven structure (it
// classifies inline against the procfs pass) and the diff cost of
// retrofitting it onto this seam outweighs the dedupe benefit.
//
// This file is `!windows`-tagged (not `darwin`) so the tests can run on
// Linux CI and verify the classifier without a macOS host. The actual
// snapshot source on darwin is sysctl (orphanpgid_darwin.go); the classifier
// itself is pure and has no platform dependencies.

package platform

// OrphanPGID describes a POSIX process group whose session leader PID is
// no longer alive but whose members are still running, owned by the
// caller's euid, and not members of any of the exclude sets the caller
// provided.
//
// Found by ScanOrphanedPGIDs (linux: /proc walk; darwin: sysctl
// KERN_PROC_ALL; other unix: stub returns nil). Consumed by daemon
// startup cleanup to reap sessions that leaked across a daemon restart.
type OrphanPGID struct {
	PGID    int   // process group ID (= original session leader PID)
	Members []int // live member PIDs currently in this pgid
}

// AncestorInfo captures the per-process fields consulted by daemon-startup
// ownership gates when deciding whether an orphan pgid is plausibly owned
// by this daemon. Populated from /proc/<pid>/{cmdline,cwd,stat} on linux,
// from sysctl KERN_PROC_PID + lsof on darwin, and left empty on other
// platforms.
//
// PID is the ancestor's PID. Cmdline is the argv joined by spaces (lossy
// for arguments that contain spaces). Cwd is the resolved working
// directory. Any field may be the empty string if the source data was
// unreadable (races where the process disappeared mid-walk are tolerated).
type AncestorInfo struct {
	PID     int
	PPID    int
	Cmdline string
	Cwd     string
}

// darwinProcRow is the per-process record consumed by the orphan-pgid
// classifier on darwin (and by tests on any unix). Held intentionally
// minimal: only the fields the classifier reads. The "darwin" prefix is
// historical — these rows could just as easily come from BSD sysctl or
// any future non-/proc enumerator.
type darwinProcRow struct {
	PID  int
	PPID int
	PGID int
	UID  int
}

// procSourceFn is the injectable seam between the classifier and the
// process enumerator. Returns the live snapshot, or an error if the
// underlying syscall failed. An error is treated by the classifier as
// "no processes visible" → returns nil orphans.
type procSourceFn func() ([]darwinProcRow, error)

// errFakeSysctl is a sentinel used by tests to drive the source-error
// branch of scanOrphanedPGIDsDarwin. Unused outside tests but defined
// here so the test file does not need to reach into a separate test-only
// package.
var errFakeSysctl = sentinelErr("fake sysctl failure")

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }

// scanOrphanedPGIDsDarwin classifies orphan pgids from a snapshot. The
// five-rule contract is identical to the linux ScanOrphanedPGIDs:
//
//  1. pgid > 1 (pid 0/1 are never treated as pgids).
//  2. pgid is NOT in excludePGIDs.
//  3. The leader (pid == pgid) is NOT in the snapshot.
//  4. At least one live member exists.
//  5. EVERY live member is owned by callerUID.
//
// Pure function: no syscalls, no global state. Suitable for table-driven
// testing on any host.
func scanOrphanedPGIDsDarwin(callerUID int, excludePGIDs map[int]bool, src procSourceFn) []OrphanPGID {
	procs, err := src()
	if err != nil || len(procs) == 0 {
		return nil
	}

	// Build pgid -> []pid index and a leader-present set in a single pass.
	index := make(map[int][]int, len(procs))
	uidByPID := make(map[int]int, len(procs))
	leaderPresent := make(map[int]bool, len(procs))

	for _, p := range procs {
		if p.PID <= 1 {
			continue
		}
		uidByPID[p.PID] = p.UID
		leaderPresent[p.PID] = true
		if p.PGID <= 1 {
			continue
		}
		index[p.PGID] = append(index[p.PGID], p.PID)
	}

	var out []OrphanPGID
	for pgid, members := range index {
		if excludePGIDs[pgid] {
			continue
		}
		// Rule 3: leader (pid == pgid) must NOT be in the live snapshot.
		// On darwin "alive" = "present in the sysctl pass". This is a
		// stronger check than the linux variant (which uses kill(0))
		// because sysctl is atomic per snapshot, eliminating the
		// race window between leader-alive probe and member enumeration.
		if leaderPresent[pgid] {
			continue
		}
		if len(members) == 0 {
			continue
		}
		// Rule 5: every member must be owned by callerUID. If ANY
		// member is foreign, skip the whole group — same semantics as
		// the linux allMembersOwnedBy gate.
		ok := allMembersMatchUID(members, uidByPID, callerUID)
		if !ok {
			continue
		}
		out = append(out, OrphanPGID{PGID: pgid, Members: append([]int(nil), members...)})
	}
	return out
}

// allMembersMatchUID returns true iff every pid in members is in uidByPID
// AND has uid matching wantUID. The two-arg map lookup is the equivalent
// of the linux readProcUID call but driven from the snapshot instead of a
// per-pid syscall, which is cheaper and atomic.
//
// If a pid is missing from uidByPID it is treated as "raced away" and
// skipped (matches linux semantics: missing /proc/<pid>/status entries
// are skipped, not failed). If ALL members race away the group is
// effectively empty and we return false so the classifier does not emit
// an empty-member orphan.
func allMembersMatchUID(members []int, uidByPID map[int]int, wantUID int) bool {
	matched := 0
	for _, pid := range members {
		uid, ok := uidByPID[pid]
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
