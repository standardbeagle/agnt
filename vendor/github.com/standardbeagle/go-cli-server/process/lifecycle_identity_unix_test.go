//go:build !windows

package process

import "testing"

func TestCleanupReapedProcessGroupSkipsRecycledLeaderPID(t *testing.T) {
	t.Parallel()
	killed := false
	cleanupReapedProcessGroupWith(4200, "old-start:1000",
		func(int) string { return "new-start:1000" },
		func(int) bool { return true },
		func(int) { killed = true },
	)
	if killed {
		t.Fatal("recycled leader PID authorized a process-group kill")
	}
}

func TestKillStoredDescendantsRequiresCapturedMatchingIdentity(t *testing.T) {
	t.Parallel()
	killed := make([]int, 0)
	killStoredDescendantsWith([]int{51, 52, 53}, map[int]string{51: "old", 52: "same"},
		func(pid int) string {
			switch pid {
			case 51:
				return "recycled"
			case 52:
				return "same"
			default:
				return "untrusted"
			}
		},
		func(int) bool { return true },
		func(pid int) { killed = append(killed, pid) },
	)
	if len(killed) != 1 || killed[0] != 52 {
		t.Fatalf("killed=%v, want only identity-matched pid 52", killed)
	}
}
