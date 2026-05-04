//go:build !windows

// Tests for the pure orphan-pgid classifier (orphanpgid_classify.go).
// These exercise the same five-rule contract as ScanOrphanedPGIDs on
// linux but against a synthetic snapshot, so they run natively on Linux
// CI to validate the darwin code path without needing a macOS host.
//
// Build tag is `!windows` (not `darwin`) intentionally: the classifier is
// pure Go with no platform deps, and running its tests on Linux is the
// only practical way to gate regressions in PRs on Linux runners.

package platform

import (
	"testing"
)

// fakeProc is the test-side mirror of darwinProcRow. Kept as a separate
// type so test fixtures stay readable without exporting the internal row.
type fakeProc struct {
	pid  int
	pgid int
	uid  int
}

func fakeSource(procs []fakeProc) procSourceFn {
	return func() ([]darwinProcRow, error) {
		out := make([]darwinProcRow, 0, len(procs))
		for _, p := range procs {
			out = append(out, darwinProcRow{
				PID:  p.pid,
				PGID: p.pgid,
				UID:  p.uid,
			})
		}
		return out, nil
	}
}

func TestScanOrphanedPGIDsDarwin_FindsLeaderDeadPGID(t *testing.T) {
	// pgid 100: leader (pid 100) is absent from snapshot, member 200 is
	// present, owned by uid 501.
	src := fakeSource([]fakeProc{
		{pid: 200, pgid: 100, uid: 501}, // leader pid 100 missing → orphan
		{pid: 300, pgid: 300, uid: 501}, // unrelated live session, leader present
	})

	orphans := scanOrphanedPGIDsDarwin(501, nil, src)

	var found *OrphanPGID
	for i := range orphans {
		if orphans[i].PGID == 100 {
			found = &orphans[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("scanOrphanedPGIDsDarwin did not find pgid 100; got %d orphans: %+v", len(orphans), orphans)
	}
	if len(found.Members) != 1 || found.Members[0] != 200 {
		t.Errorf("orphan pgid 100 members = %v, want [200]", found.Members)
	}
}

func TestScanOrphanedPGIDsDarwin_ExcludesLiveLeader(t *testing.T) {
	// pgid 300: leader (pid 300) is in the snapshot — must NOT appear in
	// results, even though pid 301 is also a member.
	src := fakeSource([]fakeProc{
		{pid: 300, pgid: 300, uid: 501}, // leader alive
		{pid: 301, pgid: 300, uid: 501}, // member of live session
	})

	orphans := scanOrphanedPGIDsDarwin(501, nil, src)

	for _, o := range orphans {
		if o.PGID == 300 {
			t.Errorf("live-leader pgid 300 incorrectly flagged as orphan: %+v", o)
		}
	}
}

func TestScanOrphanedPGIDsDarwin_HonorsExcludeSet(t *testing.T) {
	src := fakeSource([]fakeProc{
		{pid: 200, pgid: 100, uid: 501}, // leader pid 100 dead
	})

	exclude := map[int]bool{100: true}
	orphans := scanOrphanedPGIDsDarwin(501, exclude, src)

	for _, o := range orphans {
		if o.PGID == 100 {
			t.Errorf("pgid 100 in exclude set but still returned: %+v", o)
		}
	}
}

func TestScanOrphanedPGIDsDarwin_ForeignUIDIsFiltered(t *testing.T) {
	// Members owned by uid 501; caller passes uid 999 — pgid must NOT appear.
	src := fakeSource([]fakeProc{
		{pid: 200, pgid: 100, uid: 501},
	})

	orphans := scanOrphanedPGIDsDarwin(999, nil, src)

	for _, o := range orphans {
		if o.PGID == 100 {
			t.Errorf("pgid 100 returned for foreign uid 999 (members owned by 501): %+v", o)
		}
	}
}

func TestScanOrphanedPGIDsDarwin_MixedUIDFiltersWholeGroup(t *testing.T) {
	// One member owned by foreign uid 999 → entire pgid is skipped, even
	// though another member matches the caller. Matches the linux
	// allMembersOwnedBy semantics.
	src := fakeSource([]fakeProc{
		{pid: 200, pgid: 100, uid: 501}, // ours
		{pid: 201, pgid: 100, uid: 999}, // foreign — disqualifies the group
	})

	orphans := scanOrphanedPGIDsDarwin(501, nil, src)

	for _, o := range orphans {
		if o.PGID == 100 {
			t.Errorf("pgid 100 with mixed-uid members returned: %+v", o)
		}
	}
}

func TestScanOrphanedPGIDsDarwin_SkipsPGIDLessThanOrEqualToOne(t *testing.T) {
	// pgid 0 and pgid 1 must never be classified as orphans.
	src := fakeSource([]fakeProc{
		{pid: 200, pgid: 0, uid: 501},
		{pid: 201, pgid: 1, uid: 501},
	})

	orphans := scanOrphanedPGIDsDarwin(501, nil, src)

	for _, o := range orphans {
		if o.PGID <= 1 {
			t.Errorf("pgid %d (≤1) incorrectly classified as orphan", o.PGID)
		}
	}
}

func TestScanOrphanedPGIDsDarwin_EmptySnapshot(t *testing.T) {
	orphans := scanOrphanedPGIDsDarwin(501, nil, fakeSource(nil))
	if len(orphans) != 0 {
		t.Errorf("empty snapshot produced %d orphans, want 0", len(orphans))
	}
}

func TestScanOrphanedPGIDsDarwin_SourceErrorReturnsNil(t *testing.T) {
	bad := func() ([]darwinProcRow, error) {
		return nil, errFakeSysctl
	}
	orphans := scanOrphanedPGIDsDarwin(501, nil, bad)
	if orphans != nil {
		t.Errorf("source error should yield nil, got %+v", orphans)
	}
}

func TestScanOrphanedPGIDsDarwin_LeaderInGroupIsAliveCheck(t *testing.T) {
	// Leader pid 500 is itself a member of pgid 500 (the normal case).
	// As long as the leader pid appears in the snapshot, the group is
	// NOT orphaned.
	src := fakeSource([]fakeProc{
		{pid: 500, pgid: 500, uid: 501}, // leader alive in snapshot
		{pid: 501, pgid: 500, uid: 501}, // member
	})

	orphans := scanOrphanedPGIDsDarwin(501, nil, src)

	for _, o := range orphans {
		if o.PGID == 500 {
			t.Errorf("pgid 500 with leader present misclassified as orphan: %+v", o)
		}
	}
}

func TestScanOrphanedPGIDsDarwin_MultipleOrphansReturnedIndependently(t *testing.T) {
	// Two independent orphan pgids: 100 (leader dead, 1 member) and
	// 200 (leader dead, 2 members). Both must appear, with correct
	// member lists, in any order.
	src := fakeSource([]fakeProc{
		{pid: 150, pgid: 100, uid: 501},
		{pid: 250, pgid: 200, uid: 501},
		{pid: 251, pgid: 200, uid: 501},
	})

	orphans := scanOrphanedPGIDsDarwin(501, nil, src)
	if len(orphans) != 2 {
		t.Fatalf("got %d orphans, want 2: %+v", len(orphans), orphans)
	}

	byPGID := make(map[int][]int, 2)
	for _, o := range orphans {
		byPGID[o.PGID] = o.Members
	}

	if got := byPGID[100]; len(got) != 1 || got[0] != 150 {
		t.Errorf("pgid 100 members = %v, want [150]", got)
	}
	if got := byPGID[200]; len(got) != 2 {
		t.Errorf("pgid 200 members = %v, want 2 entries", got)
	}
}

func TestScanOrphanedPGIDsDarwin_PreservesMemberOrderIsolation(t *testing.T) {
	// Mutating the returned Members slice must not affect the source
	// snapshot. The classifier copies Members defensively (matching the
	// linux variant's `append([]int(nil), members...)` pattern).
	procs := []fakeProc{
		{pid: 200, pgid: 100, uid: 501},
	}
	orphans := scanOrphanedPGIDsDarwin(501, nil, fakeSource(procs))
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %d", len(orphans))
	}

	// Mutate the returned slice; re-run to confirm the source is unchanged.
	orphans[0].Members[0] = 9999

	orphans2 := scanOrphanedPGIDsDarwin(501, nil, fakeSource(procs))
	if len(orphans2) != 1 || orphans2[0].Members[0] != 200 {
		t.Errorf("source snapshot was mutated: orphans2=%+v, want member 200", orphans2)
	}
}
