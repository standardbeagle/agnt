//go:build windows

package daemon

import (
	"os/exec"
	"time"

	"golang.org/x/sys/windows"

	"github.com/standardbeagle/agnt/internal/platform"
)

// configureHookProcessGroup makes a hook timeout kill the hook's whole process
// tree, not just the shell. CommandContext's default cancel terminates only the
// direct child, so a hook's children (a dev tool it launched, a pinger) outlived
// the timeout — the Windows side of the Unix process-group kill.
//
// The tree is found from the process snapshot's parent links at kill time
// rather than with a job object: a job with KILL_ON_JOB_CLOSE would also reap
// children a hook backgrounds on purpose when the hook finishes normally,
// which the Unix path does not do. WaitDelay bounds how long Wait hangs on a
// descendant that still holds an inherited handle.
func configureHookProcessGroup(c *exec.Cmd) {
	c.Cancel = func() error {
		return killHookTree(c.Process.Pid)
	}
	c.WaitDelay = 2 * time.Second
}

// killHookTree terminates root and every descendant reachable through parent
// links. The tree is collected before anything is killed (a dead parent's
// children are no longer reported under it), then one more pass catches a
// child spawned while the first pass ran.
func killHookTree(root int) error {
	rootBorn, ok := hookProcessBirth(root)
	if !ok {
		return platform.KillPID(root, 0)
	}
	// born holds every process in the tree with its creation time; a parent
	// link is trusted only when the child is younger than the parent, since
	// Windows keeps a dead parent's PID in its children and reuses PIDs.
	born := map[int]uint64{root: rootBorn}
	killed := map[int]bool{}
	var rootErr error
	for pass := 0; pass < 2; pass++ {
		collectHookTree(born)
		for pid := range born {
			if killed[pid] {
				continue
			}
			killed[pid] = true
			if err := platform.KillPID(pid, 0); err != nil && pid == root {
				rootErr = err
			}
		}
	}
	return rootErr
}

// collectHookTree adds to born every live process whose parent is already in
// born and which was created after that parent.
func collectHookTree(born map[int]uint64) {
	procs, err := platform.Scan()
	if err != nil {
		return
	}
	children := map[int][]int{}
	for _, p := range procs {
		if p.PPID != 0 && p.PPID != p.PID {
			children[p.PPID] = append(children[p.PPID], p.PID)
		}
	}
	queue := make([]int, 0, len(born))
	for pid := range born {
		queue = append(queue, pid)
	}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			if _, seen := born[child]; seen {
				continue
			}
			t, ok := hookProcessBirth(child)
			if !ok || t < born[parent] {
				continue // gone, or a stale link to a reused parent PID
			}
			born[child] = t
			queue = append(queue, child)
		}
	}
}

// hookProcessBirth returns a process's creation time in 100ns ticks.
func hookProcessBirth(pid int) (uint64, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, false
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), true
}
