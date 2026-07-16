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

func TestVerifiedDescendantRecycleBeforeCleanupUsesScannerIdentity(t *testing.T) {
	t.Parallel()

	identities := make(map[int]string)
	descendants := mergeVerifiedDescendants(nil, identities, []VerifiedDescendant{{
		PID:      61,
		Identity: "scanner-captured",
	}})

	// Simulate PID reuse after tracker verification but before cleanup. The
	// replacement's identity must be compared with scanner evidence, never
	// installed as the new expected identity.
	killed := false
	killStoredDescendantsWith(descendants, identities,
		func(int) string { return "recycled-after-verification" },
		func(int) bool { return true },
		func(int) { killed = true },
	)
	if killed {
		t.Fatal("PID recycled after verification was killed")
	}
	if identities[61] != "scanner-captured" {
		t.Fatalf("cleanup identity=%q, want scanner-captured evidence", identities[61])
	}
}
