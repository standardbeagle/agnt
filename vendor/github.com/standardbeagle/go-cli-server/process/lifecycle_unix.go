//go:build !windows

package process

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// setProcAttr sets platform-specific process attributes for Unix systems.
// Each child process gets its own process group so we can signal it
// independently without affecting other processes or the parent daemon.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // Create new process group for this process
	}
}

// signalProcessGroup sends a signal to the process group AND every descendant
// of pid, including setsid-escaped grandchildren that live in independent
// sessions or process groups.
//
// Since child processes are started with Setpgid=true, the root pid is the
// process-group leader — pgid equals pid. We never call Getpgid on pid because
// by the time we reach this function the root may already have been SIGKILLed
// by the exec.CommandContext watcher (triggered from proc.Cancel()). Getpgid
// on a dead process returns ESRCH and loses the pgroup signal entirely.
//
// The caller should snapshot descendants via ProcessManager.snapshotDescendants
// BEFORE cancelling the process context. The managed-process descendant cache
// and the persistent tracker are consulted here to catch setsid-escaped
// grandchildren that have since been reparented to init.
func (pm *ProcessManager) signalProcessGroup(pid int, sig syscall.Signal) error {
	// Signal the root process group. pgid == pid for Setpgid=true children.
	// This reaches every process still in the root's group — including the
	// root itself, its foreground children, and any same-group grandchildren.
	_ = syscall.Kill(-pid, sig)

	// Build the descendant set from every available source:
	//   1. live PPID walk (best-effort, may be empty if root is already gone)
	//   2. descendant cache on the managed process (snapshotted pre-cancel)
	//   3. persistent PID tracker (background scanner results)
	seen := make(map[int]struct{})
	addDescendant := func(childPID int) {
		if childPID <= 1 || childPID == pid {
			return
		}
		if _, ok := seen[childPID]; ok {
			return
		}
		seen[childPID] = struct{}{}
		_ = syscall.Kill(childPID, sig)
		// Setsid-escaped descendants have their own pgid — signal that
		// group so any grandchildren of the escapee die too.
		if childPgid, err := syscall.Getpgid(childPID); err == nil && childPgid > 0 && childPgid != pid {
			_ = syscall.Kill(-childPgid, sig)
		}
	}

	for _, d := range findAllDescendants(pid) {
		addDescendant(d)
	}
	if proc := pm.lookupByPID(pid); proc != nil {
		for _, d := range proc.Descendants() {
			addDescendant(d)
		}
	}
	if dt, ok := pm.pidTracker.(VerifiedDescendantTracker); ok {
		for _, d := range dt.GetVerifiedDescendants(pid) {
			addDescendant(d)
		}
	} else if dt, ok := pm.pidTracker.(DescendantTracker); ok {
		for _, d := range dt.GetDescendants(pid) {
			addDescendant(d)
		}
	}

	return nil
}

// cleanupProcessTree kills all descendants of a process, including those that
// escaped to a new session/process group (e.g., dotnet watch → dotnet app).
// First signals the process group, then walks the descendant tree via /proc
// or pgrep to catch escapees.
func cleanupProcessTree(pid int) {
	// 1. Kill the process group (catches most children)
	pgid, err := syscall.Getpgid(pid)
	if err == nil && pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}

	// 2. Walk the descendant tree to catch children in different groups
	descendants := findAllDescendants(pid)
	for _, childPID := range descendants {
		_ = syscall.Kill(childPID, syscall.SIGKILL)
	}
}

// killStoredDescendants kills PIDs from the tracker's stored descendant list.
// This catches processes that were descendants at the last scan but may have
// been reparented or escaped since then.
func killStoredDescendants(pids []int) {
	for _, pid := range pids {
		if isProcessAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// findAllDescendants returns all descendant PIDs recursively.
//
// On Linux, walks the full /proc table by PPID to a fixed point. This is
// robust against processes that call setsid() and are reparented to init
// when an intermediate ancestor dies: as long as the walk happens while
// the tree is still intact, it catches every descendant regardless of
// process-group membership.
//
// On non-Linux systems, falls back to `pgrep -P PID` recursion.
func findAllDescendants(pid int) []int {
	if descendants, ok := findAllDescendantsProc(pid); ok {
		return descendants
	}
	return pgrepChildren(pid)
}

// findAllDescendantsProc walks /proc/*/stat PPID fields, building the
// descendant set iteratively until it stabilizes. Returns (nil, false)
// when /proc is not available (non-Linux).
func findAllDescendantsProc(root int) ([]int, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false
	}

	// Collect (pid, ppid) for every numeric /proc entry.
	type procEntry struct {
		pid  int
		ppid int
	}
	procs := make([]procEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if len(name) == 0 || name[0] < '0' || name[0] > '9' {
			continue
		}
		pid, err := strconv.Atoi(name)
		if err != nil || pid <= 0 {
			continue
		}
		ppid, ok := readProcPPID(pid)
		if !ok {
			continue
		}
		procs = append(procs, procEntry{pid: pid, ppid: ppid})
	}

	// Iterative BFS/fixed-point: start with root, repeatedly pull in any
	// process whose PPID is already in the set. Each iteration is O(n).
	set := map[int]struct{}{root: {}}
	for {
		added := false
		for _, p := range procs {
			if _, already := set[p.pid]; already {
				continue
			}
			if _, parentIn := set[p.ppid]; parentIn {
				set[p.pid] = struct{}{}
				added = true
			}
		}
		if !added {
			break
		}
	}

	result := make([]int, 0, len(set)-1)
	for pid := range set {
		if pid == root {
			continue
		}
		result = append(result, pid)
	}
	return result, true
}

// readProcPPID parses the PPID (field 4) out of /proc/<pid>/stat.
// The comm field (field 2) is parenthesized and may contain spaces or
// parentheses, so we anchor on the last ')'.
func readProcPPID(pid int) (int, bool) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	s := string(data)
	idx := strings.LastIndexByte(s, ')')
	if idx < 0 || idx+2 >= len(s) {
		return 0, false
	}
	// After ") ": state PPID ...
	fields := strings.Fields(s[idx+2:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

func pgrepChildren(pid int) []int {
	out, err := exec.Command("pgrep", "-P", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	var result []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		childPID, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || childPID <= 0 {
			continue
		}
		result = append(result, childPID)
		result = append(result, pgrepChildren(childPID)...)
	}
	return result
}

// Backward compat alias
func cleanupProcessGroup(pgid int) {
	cleanupProcessTree(pgid)
}

// signalKill sends SIGKILL to the process.
func signalKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// isProcessAlive checks if a process is still running.
func isProcessAlive(pid int) bool {
	return syscall.Kill(pid, syscall.Signal(0)) == nil
}

// getProcessGroupID returns the process group ID for a given PID.
func getProcessGroupID(pid int) int {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return pid
	}
	return pgid
}

// SetupJobObject is a no-op on Unix.
func SetupJobObject(cmd *exec.Cmd) error {
	return nil
}

// CleanupJobObject is a no-op on Unix.
func CleanupJobObject(pid int) {
}
