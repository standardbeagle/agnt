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
			if reads <= 2 {
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

func TestKillPIDRejectsReuseDuringInitialLeadershipLookup(t *testing.T) {
	reads := 0
	var targets []int
	err := killPIDWith(42, 0,
		func(int) (string, bool) {
			reads++
			if reads == 1 {
				return "original", true
			}
			return "recycled", true
		},
		func(int) (int, error) { return 42, nil },
		func(pid int, _ syscall.Signal) error { targets = append(targets, pid); return nil },
	)
	if err == nil || len(targets) != 0 {
		t.Fatalf("err=%v targets=%v, want fail-closed before TERM", err, targets)
	}
}

func TestKillPIDRejectsReuseDuringDelayedLeadershipLookup(t *testing.T) {
	reads := 0
	var signals []syscall.Signal
	err := killPIDWith(42, 0,
		func(int) (string, bool) {
			reads++
			if reads < 3 {
				return "original", true
			}
			return "recycled", true
		},
		func(int) (int, error) { return 42, nil },
		func(_ int, sig syscall.Signal) error { signals = append(signals, sig); return nil },
	)
	if err == nil {
		t.Fatal("reuse during delayed leadership lookup returned nil")
	}
	for _, sig := range signals {
		if sig == syscall.SIGKILL {
			t.Fatal("recycled PID received SIGKILL")
		}
	}
}

func TestKillPIDUnavailableBirthIdentityFailsClosed(t *testing.T) {
	called := false
	err := killPIDWith(42, 0,
		func(int) (string, bool) { return "", false },
		func(int) (int, error) { return 42, nil },
		func(int, syscall.Signal) error { called = true; return nil },
	)
	if err == nil || called {
		t.Fatalf("err=%v signalCalled=%v, want fail-closed", err, called)
	}
}
