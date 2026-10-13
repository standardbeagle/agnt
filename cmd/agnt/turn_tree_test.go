package main

import (
	"errors"
	"testing"

	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/stretchr/testify/assert"
)

func TestTurnDescendants(t *testing.T) {
	const self = 100
	procs := []platform.ProcInfo{
		{PID: 1, PPID: 0},
		{PID: self, PPID: 50},
		{PID: 50, PPID: 1},     // our parent (the shell)
		{PID: 110, PPID: self}, // a helper started before the turn
		{PID: 111, PPID: 110},  // ...and its child
		{PID: 200, PPID: self}, // the agent CLI for this turn
		{PID: 210, PPID: 200},  // its tool shell
		{PID: 220, PPID: 210},  // the runaway command
		{PID: 300, PPID: 1},    // unrelated
	}
	baseline := map[int]bool{110: true}

	got := turnDescendants(procs, self, baseline)

	assert.Equal(t, []int{220, 210, 200}, got, "the turn's tree, deepest first")
	for _, never := range []int{1, 50, self, 110, 111, 300} {
		assert.NotContains(t, got, never)
	}
}

func TestTurnDescendants_NothingNew(t *testing.T) {
	procs := []platform.ProcInfo{{PID: 100, PPID: 1}, {PID: 110, PPID: 100}}
	assert.Empty(t, turnDescendants(procs, 100, map[int]bool{110: true}))
}

// A torn snapshot can show a parent/child cycle (pid reuse between reads).
// The walk must terminate and must never return this process.
func TestTurnDescendants_CycleTerminates(t *testing.T) {
	procs := []platform.ProcInfo{
		{PID: 200, PPID: 100},
		{PID: 210, PPID: 200},
		{PID: 200, PPID: 210},
		{PID: 100, PPID: 210},
	}
	got := turnDescendants(procs, 100, nil)
	assert.ElementsMatch(t, []int{200, 210}, got)
}

func TestTurnTree_CaptureAndStop(t *testing.T) {
	var killed []int
	tree := &turnTree{
		self:     100,
		baseline: map[int]bool{110: true},
		scan: func() ([]platform.ProcInfo, error) {
			return []platform.ProcInfo{
				{PID: 110, PPID: 100},
				{PID: 200, PPID: 100},
				{PID: 210, PPID: 200},
			}, nil
		},
	}
	pids := tree.capture()
	assert.Equal(t, []int{210, 200}, pids)

	done := make(chan int, len(pids))
	tree.kill = func(pid int) { done <- pid }
	tree.stop(pids)
	close(done)
	for pid := range done {
		killed = append(killed, pid)
	}
	assert.ElementsMatch(t, []int{210, 200}, killed)
}

// When the process table cannot be read the interrupt must still be usable:
// capture reports nothing rather than failing.
func TestTurnTree_ScanFailureCapturesNothing(t *testing.T) {
	tree := &turnTree{self: 100, scan: func() ([]platform.ProcInfo, error) {
		return nil, errors.New("no /proc")
	}}
	assert.Nil(t, tree.capture())
}
