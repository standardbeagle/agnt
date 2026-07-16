//go:build !windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"strconv"
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
func KillSessionPGID(sessionPGID int, killSelfPID int, gracefulTimeout time.Duration, aggressive bool) error {
	if sessionPGID <= 1 {
		return fmt.Errorf("invalid session pgid %d", sessionPGID)
	}

	// In aggressive mode, skip the SIGTERM + grace window.
	if aggressive || gracefulTimeout <= 0 {
		if err := killgOnce(sessionPGID, killSelfPID, syscall.SIGKILL); err != nil {
			return err
		}
		return waitForPGIDDrain(sessionPGID, killSelfPID, 250*time.Millisecond)
	}

	// Graceful: SIGTERM first, grace window, SIGKILL on any survivors.
	if err := killgOnce(sessionPGID, killSelfPID, syscall.SIGTERM); err != nil {
		return err
	}

	deadline := time.Now().Add(gracefulTimeout)
	tick := 50 * time.Millisecond
	for time.Now().Before(deadline) {
		hasMembers, err := pgidHasMembers(sessionPGID, killSelfPID)
		if err != nil {
			return err
		}
		if !hasMembers {
			return nil
		}
		time.Sleep(tick)
	}

	// Escalate.
	if err := killgOnce(sessionPGID, killSelfPID, syscall.SIGKILL); err != nil {
		return err
	}
	return waitForPGIDDrain(sessionPGID, killSelfPID, 250*time.Millisecond)
}

// killgOnce sends a single signal to every process currently in pgid,
// skipping killSelfPID. Uses syscall.Kill(-pgid, sig) first to reach every
// member atomically; if that fails with ESRCH (empty group) we return
// success because there is nothing to do.
//
// The atomic -pgid broadcast cannot skip a single member, so it would signal
// killSelfPID too if the caller shares the group. When that is the case
// (killSelfPID's own pgid == pgid) we must take the per-PID path so self is
// excluded — otherwise the daemon would SIGKILL itself.
func killgOnce(pgid int, killSelfPID int, sig syscall.Signal) error {
	// If the caller is a member of the target group, the atomic broadcast
	// would hit it. Fall back to per-PID delivery that skips killSelfPID.
	if killSelfPID > 0 && selfPGID(killSelfPID) == pgid {
		members, err := membersOfPGIDChecked(pgid)
		if err != nil {
			return err
		}
		for _, pid := range members {
			if pid == killSelfPID || pid <= 1 {
				continue
			}
			_ = syscall.Kill(pid, sig)
		}
		return nil
	}

	err := syscall.Kill(-pgid, sig)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return nil // group empty — done
	}
	// EPERM or any other partial failure: fall through to per-PID best
	// effort so members we CAN signal get reaped.
	members, scanErr := membersOfPGIDChecked(pgid)
	if scanErr != nil {
		return fmt.Errorf("scan pgid %d after group signal failure: %w", pgid, scanErr)
	}
	for _, pid := range members {
		if pid == killSelfPID || pid <= 1 {
			continue
		}
		_ = syscall.Kill(pid, sig)
	}
	return fmt.Errorf("signal process group %d: %w", pgid, err)
}

// selfPGID returns the process group id of pid, preferring /proc (Linux/WSL)
// and falling back to the Getpgid syscall on other Unix. Returns 0 when
// neither source can resolve it.
func selfPGID(pid int) int {
	if gp := readPGID(pid); gp != 0 {
		return gp
	}
	if gp, err := syscall.Getpgid(pid); err == nil {
		return gp
	}
	return 0
}

// pgidHasMembers returns true if any process other than killSelfPID is
// still in pgid. Used to determine whether the SIGTERM grace window
// succeeded without racing the killer itself.
func pgidHasMembers(pgid int, killSelfPID int) (bool, error) {
	members, err := membersOfPGIDChecked(pgid)
	if err != nil {
		return false, fmt.Errorf("scan pgid %d members: %w", pgid, err)
	}
	for _, pid := range members {
		if pid == killSelfPID || pid <= 1 {
			continue
		}
		return true, nil
	}
	return false, nil
}

func waitForPGIDDrain(pgid, killSelfPID int, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	for {
		hasMembers, err := pgidHasMembers(pgid, killSelfPID)
		if err != nil {
			return err
		}
		if !hasMembers {
			return nil
		}
		if time.Now().After(deadline) {
			members, scanErr := membersOfPGIDChecked(pgid)
			if scanErr != nil {
				return fmt.Errorf("verify pgid %d drain: %w", pgid, scanErr)
			}
			return fmt.Errorf("pgid %d still has surviving members %v after SIGKILL", pgid, members)
		}
		time.Sleep(10 * time.Millisecond)
	}
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
	members, _ := membersOfPGIDChecked(pgid)
	return members
}

func membersOfPGIDChecked(pgid int) ([]int, error) {
	return membersOfPGIDWith(pgid, Scan, readPGID, syscall.Getpgid)
}

func membersOfPGIDWith(pgid int, scanFn func() ([]ProcInfo, error), readFn func(int) int,
	getpgidFn func(int) (int, error),
) ([]int, error) {
	procs, err := scanFn()
	if err != nil {
		return nil, err
	}
	var out []int
	for _, p := range procs {
		got := readFn(p.PID)
		if got == 0 {
			// Fallback for non-Linux Unix: ask the kernel directly.
			if gp, err := getpgidFn(p.PID); err == nil {
				got = gp
			}
		}
		if got == pgid {
			out = append(out, p.PID)
		}
	}
	return out, nil
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
	// After comm we have: state(0), ppid(1), pgrp(2).
	fields := parseStatFieldsAfterComm(data)
	if len(fields) < 3 {
		return 0
	}
	pgrp, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0
	}
	return pgrp
}
