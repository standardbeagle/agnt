//go:build !windows

package platform

import (
	"syscall"
	"testing"
)

func TestKillPIDNonLeaderNeverSignalsNegativePID(t *testing.T) {
	var targets []int
	err := killPIDWith(42, 0,
		func(int) (string, bool) { return "birth", true },
		func(int) (int, error) { return 7, nil },
		func(pid int, _ syscall.Signal) error { targets = append(targets, pid); return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target < 0 {
			t.Fatalf("non-leader emitted group signal target %d", target)
		}
	}
}

func TestKillPIDRechecksIdentityBeforeDelayedSIGKILL(t *testing.T) {
	reads := 0
	var signals []syscall.Signal
	err := killPIDWith(42, 0,
		func(int) (string, bool) {
			reads++
			if reads == 1 {
				return "original", true
			}
			return "recycled", true
		},
		func(int) (int, error) { return 42, nil },
		func(_ int, sig syscall.Signal) error { signals = append(signals, sig); return nil },
	)
	if err == nil {
		t.Fatal("identity change before escalation returned nil")
	}
	for _, sig := range signals {
		if sig == syscall.SIGKILL {
			t.Fatal("recycled PID received SIGKILL")
		}
	}
}
