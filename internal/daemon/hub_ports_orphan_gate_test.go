//go:build unix

package daemon

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/platform"
)

func TestReapOrphanCandidates_SkipsUnrelatedSameUIDGroup(t *testing.T) {
	t.Parallel()

	const foreignPGID = 4242
	killCalls := 0
	reaped, failed := reapOrphanCandidates(
		[]platform.OrphanPGID{{PGID: foreignPGID, Members: []int{4243}}},
		[]string{"/srv/projects/owned"},
		"/usr/local/bin/agnt",
		func(int) []platform.AncestorInfo {
			return []platform.AncestorInfo{{
				PID:     4200,
				PPID:    1,
				Cmdline: "agnt run other-agent",
				Cwd:     "/srv/projects/unrelated",
			}}
		},
		func(int) error {
			killCalls++
			return nil
		},
	)

	if killCalls != 0 {
		t.Fatalf("foreign pgid was sent to KillSessionPGID %d time(s), want 0", killCalls)
	}
	if len(reaped) != 0 || len(failed) != 0 {
		t.Fatalf("foreign pgid result: reaped=%v failed=%v, want both empty", reaped, failed)
	}
}

func TestReapOrphanCandidates_ReapsOwnedGroup(t *testing.T) {
	t.Parallel()

	const ownedPGID = 5252
	var killed int
	reaped, failed := reapOrphanCandidates(
		[]platform.OrphanPGID{{PGID: ownedPGID, Members: []int{5253}}},
		[]string{"/srv/projects/owned"},
		"/usr/local/bin/agnt",
		func(int) []platform.AncestorInfo {
			return []platform.AncestorInfo{{
				PID:     5200,
				PPID:    1,
				Cmdline: "agnt run owned-agent",
				Cwd:     "/srv/projects/owned",
			}}
		},
		func(pgid int) error {
			killed = pgid
			return nil
		},
	)

	if killed != ownedPGID {
		t.Fatalf("killed pgid = %d, want %d", killed, ownedPGID)
	}
	if len(reaped) != 1 || reaped[0] != ownedPGID || len(failed) != 0 {
		t.Fatalf("owned pgid result: reaped=%v failed=%v", reaped, failed)
	}
}
