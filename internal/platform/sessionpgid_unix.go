//go:build !windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// KillSessionPGID sends SIGTERM to the POSIX process group identified by
// sessionPGID, waits up to gracefulTimeout for the group to drain, then
// escalates to SIGKILL. This is the session-shutdown equivalent of
// signalProcessGroup: it reaps every process that inherited the pgid from
// the PTY child session leader, including `npm run dev &` style jobs the
// coding agent spawned via non-interactive bash (which does not enable job
// control and therefore does not give backgrounded pipelines their own
// pgid).
//
// aggressive selects SIGKILL immediately (used when the daemon is in
// aggressive-shutdown mode with a tight deadline, matching the existing
// ProcessManager.StopAll behavior).
//
// killSelfPID is the caller's own PID. It is EXCLUDED from the kill so the
// daemon does not signal itself when it happens to share the pgid (it
// shouldn't, but defensive). Pass 0 to disable self-exclusion.
//
// Returns an error only for the final verification step; best-effort EPERM
// and ESRCH during signal delivery are not reported since they are normal
// (the group may drain mid-loop).
func KillSessionPGID(sessionPGID int, killSelfPID int, gracefulTimeout time.Duration, aggressive bool) error {
	if sessionPGID <= 1 {
		return fmt.Errorf("invalid session pgid %d", sessionPGID)
	}

	// In aggressive mode, skip the SIGTERM + grace window.
	if aggressive || gracefulTimeout <= 0 {
		return killgOnce(sessionPGID, killSelfPID, syscall.SIGKILL)
	}

	// Graceful: SIGTERM first, grace window, SIGKILL on any survivors.
	_ = killgOnce(sessionPGID, killSelfPID, syscall.SIGTERM)

	deadline := time.Now().Add(gracefulTimeout)
	tick := 50 * time.Millisecond
	for time.Now().Before(deadline) {
		if !pgidHasMembers(sessionPGID, killSelfPID) {
			return nil
		}
		time.Sleep(tick)
	}

	// Escalate.
	return killgOnce(sessionPGID, killSelfPID, syscall.SIGKILL)
}

// killgOnce sends a single signal to every process currently in pgid,
// skipping killSelfPID. Uses syscall.Kill(-pgid, sig) first to reach every
// member atomically; if that fails with ESRCH (empty group) we return
// success because there is nothing to do.
func killgOnce(pgid int, killSelfPID int, sig syscall.Signal) error {
	err := syscall.Kill(-pgid, sig)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return nil // group empty — done
	}
	// EPERM or any other partial failure: fall through to per-PID best
	// effort so members we CAN signal get reaped.
	for _, pid := range membersOfPGID(pgid) {
		if pid == killSelfPID || pid <= 1 {
			continue
		}
		_ = syscall.Kill(pid, sig)
	}
	return nil
}

// pgidHasMembers returns true if any process other than killSelfPID is
// still in pgid. Used to determine whether the SIGTERM grace window
// succeeded without racing the killer itself.
func pgidHasMembers(pgid int, killSelfPID int) bool {
	for _, pid := range membersOfPGID(pgid) {
		if pid == killSelfPID || pid <= 1 {
			continue
		}
		return true
	}
	return false
}

// MembersOfPGID returns the PIDs of every process currently in the given
// POSIX process group. Callers: session cleanup (to verify the pgid is
// empty after SIGKILL), orphan scanning (to find sessions whose leaders
// are gone but children still live), and tests.
//
// On WSL2 the same /proc layout applies, so no special-casing is needed.
// On platforms without /proc this falls back to best-effort:
// Getpgid(pid) on every pid in Scan() output. Returns nil on error.
func MembersOfPGID(pgid int) []int {
	return membersOfPGID(pgid)
}

// membersOfPGID is the internal, unexported implementation used by the
// kill helpers in this file. External callers use MembersOfPGID.
func membersOfPGID(pgid int) []int {
	procs, err := Scan()
	if err != nil {
		return nil
	}
	var out []int
	for _, p := range procs {
		got := readPGID(p.PID)
		if got == 0 {
			// Fallback for non-Linux Unix: ask the kernel directly.
			if gp, err := syscall.Getpgid(p.PID); err == nil {
				got = gp
			}
		}
		if got == pgid {
			out = append(out, p.PID)
		}
	}
	return out
}

// readPGID extracts the pgid field from /proc/<pid>/stat. Returns 0 on
// any error (missing file, unparseable, kernel thread) — the caller is
// expected to fall back to syscall.Getpgid.
//
// /proc/<pid>/stat layout (1-indexed):
//
//	1  pid
//	2  comm (in parens, may contain spaces/parens)
//	3  state
//	4  ppid
//	5  pgrp  <-- this is what we want
//	...
func readPGID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// Find the last ')' to safely skip comm, since comm is the only
	// field allowed to contain whitespace.
	idx := strings.LastIndex(string(data), ")")
	if idx < 0 || idx+1 >= len(data) {
		return 0
	}
	fields := strings.Fields(string(data[idx+1:]))
	// After comm we have: state, ppid, pgrp — so pgrp is index 2.
	if len(fields) < 3 {
		return 0
	}
	pgrp, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0
	}
	return pgrp
}
