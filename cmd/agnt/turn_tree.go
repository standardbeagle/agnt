package main

import (
	"os"
	"sync"

	"github.com/standardbeagle/agnt/internal/platform"
)

// turnTree finds and stops the processes one agent turn started.
//
// Cancelling a turn's context only kills the agent CLI itself (the SDK starts
// it with exec.CommandContext). A tool command the agent had already launched
// — the runaway the user pressed Ctrl+C to stop — is orphaned and keeps
// running. The tree is therefore captured while it is still linked to this
// process, before the cancel, and stopped afterwards.
//
// Only children that appeared after the baseline are followed, so helpers
// this process started earlier (a daemon auto-start, a PTY child) are never
// touched.
type turnTree struct {
	self     int
	baseline map[int]bool
	scan     func() ([]platform.ProcInfo, error)
	kill     func(pid int)
}

// newTurnTree records this process's current children as the baseline. Call
// it immediately before starting the turn.
func newTurnTree() *turnTree {
	t := &turnTree{
		self: os.Getpid(),
		scan: platform.Scan,
		kill: func(pid int) { _ = platform.KillPID(pid, 2) },
	}
	t.baseline = map[int]bool{}
	if procs, err := t.scan(); err == nil {
		for _, p := range procs {
			if p.PPID == t.self {
				t.baseline[p.PID] = true
			}
		}
	}
	return t
}

// capture returns every process descended from a child of this process that
// was not in the baseline, deepest first. An enumeration failure yields nil:
// the interrupt still cancels the turn, it just cannot reach the orphans.
func (t *turnTree) capture() []int {
	procs, err := t.scan()
	if err != nil {
		return nil
	}
	return turnDescendants(procs, t.self, t.baseline)
}

// stop terminates the captured processes and returns once each has been
// signalled and reaped or escalated.
func (t *turnTree) stop(pids []int) {
	var wg sync.WaitGroup
	for _, pid := range pids {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			t.kill(pid)
		}(pid)
	}
	wg.Wait()
}

// turnDescendants returns the processes descended from the children of self
// that are not in baseline, deepest first so a parent cannot respawn or wait
// on a child that is about to be stopped. self and baseline members are never
// returned, and a cycle in a torn snapshot cannot loop.
func turnDescendants(procs []platform.ProcInfo, self int, baseline map[int]bool) []int {
	children := make(map[int][]int, len(procs))
	for _, p := range procs {
		if p.PID > 1 && p.PID != self {
			children[p.PPID] = append(children[p.PPID], p.PID)
		}
	}

	var out []int
	seen := map[int]bool{self: true}
	var walk func(pid int)
	walk = func(pid int) {
		if seen[pid] {
			return
		}
		seen[pid] = true
		for _, c := range children[pid] {
			walk(c)
		}
		out = append(out, pid)
	}
	for _, c := range children[self] {
		if !baseline[c] {
			walk(c)
		}
	}
	return out
}
