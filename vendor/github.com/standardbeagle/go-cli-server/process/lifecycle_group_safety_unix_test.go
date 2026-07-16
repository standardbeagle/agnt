//go:build !windows

package process

import (
	"syscall"
	"testing"
)

func TestSignalVerifiedProcessGroupRejectsNonLeaderAndRecycledPID(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pgid       int
		identities []string
	}{
		{name: "non-leader", pgid: 7, identities: []string{"same", "same"}},
		{name: "recycled", pgid: 42, identities: []string{"old", "new"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			killed := false
			signalVerifiedProcessGroup(42, syscall.SIGKILL,
				func(int) string { v := tc.identities[reads]; reads++; return v },
				func(int) (int, error) { return tc.pgid, nil },
				func(int, syscall.Signal) error { killed = true; return nil },
			)
			if killed {
				t.Fatal("unsafe negative-PID signal emitted")
			}
		})
	}
}
