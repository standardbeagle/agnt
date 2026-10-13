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

// A target that has exited by escalation time is a completed kill. On macOS
// getpgid answers ESRCH for a zombie while kill(pid, 0) still succeeds, so the
// grace loop ran out and the leadership lookup then failed; that was reported
// as "pid changed before SIGKILL escalation" although the process was gone.
func TestKillPIDExitedBeforeEscalationIsSuccess(t *testing.T) {
	lookups := 0
	var signals []syscall.Signal
	err := killPIDWith(42, 0,
		func(int) (string, bool) { return "birth", true },
		func(int) (int, error) {
			lookups++
			if lookups == 1 {
				return 42, nil // alive and leading its group when first checked
			}
			return 0, syscall.ESRCH // gone by the time escalation looks
		},
		func(_ int, sig syscall.Signal) error { signals = append(signals, sig); return nil },
	)
	if err != nil {
		t.Fatalf("a process that no longer exists at escalation is a successful kill, got: %v", err)
	}
	for _, sig := range signals {
		if sig == syscall.SIGKILL {
			t.Fatal("SIGKILL sent although the process was already gone")
		}
	}
}

// ESRCH is the only lookup failure that means "gone". Any other failure at
// escalation still fails closed, and so does a live process with a different
// birth identity (covered by the Rechecks/RejectsReuse tests above).
func TestKillPIDOtherLookupFailureAtEscalationStillFailsClosed(t *testing.T) {
	lookups := 0
	var signals []syscall.Signal
	err := killPIDWith(42, 0,
		func(int) (string, bool) { return "birth", true },
		func(int) (int, error) {
			lookups++
			if lookups == 1 {
				return 42, nil
			}
			return 0, syscall.EPERM
		},
		func(_ int, sig syscall.Signal) error { signals = append(signals, sig); return nil },
	)
	if err == nil {
		t.Fatal("a non-ESRCH lookup failure at escalation returned nil")
	}
	for _, sig := range signals {
		if sig == syscall.SIGKILL {
			t.Fatal("SIGKILL sent without a verified identity")
		}
	}
}

// A zombie is gone: it must end the grace period at once instead of being
// polled until the ceiling (kill(pid, 0) keeps succeeding for it).
func TestKillPIDZombieEndsGracePeriod(t *testing.T) {
	var signals []syscall.Signal
	polls := 0
	err := killPIDProbed(42, 3600, // an hour of grace: only the probe can end this
		func(int) (string, bool) { return "birth", true },
		func(int) (int, error) { return 42, nil },
		func(_ int, sig syscall.Signal) error { signals = append(signals, sig); return nil },
		func(int) bool { polls++; return true },
	)
	if err != nil {
		t.Fatal(err)
	}
	if polls != 1 {
		t.Fatalf("zombie probe consulted %d times, want 1 (first poll ends the wait)", polls)
	}
	for _, sig := range signals {
		if sig == syscall.SIGKILL {
			t.Fatal("zombie was escalated to SIGKILL")
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
