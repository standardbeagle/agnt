package daemon

import (
	"testing"
)

func TestKillSessionPGIDIfIdentityMatches_SkipsRecycledLeaderWithIdenticalMetadata(t *testing.T) {
	t.Parallel()

	expected := sessionPGIDIdentity{pgid: 4242, members: map[int]string{4242: "birth-1"}}
	killCalls := 0
	killed, err := killSessionPGIDIfIdentityMatches(4242, expected,
		func(int) []int { return []int{4242} },
		// A recycled impostor may have identical cmdline/cwd. Its kernel birth
		// ID is nevertheless distinct, which is the identity signal that matters.
		func(int) (string, bool) { return "birth-2", true },
		func(int) error { killCalls++; return nil },
	)
	if err != nil || killed || killCalls != 0 {
		t.Fatalf("recycled pgid was killed: killed=%v calls=%d err=%v", killed, killCalls, err)
	}
}

func TestKillSessionPGIDIfIdentityMatches_ReapsCapturedDescendantAfterLeaderExit(t *testing.T) {
	t.Parallel()

	expected := sessionPGIDIdentity{pgid: 5252, members: map[int]string{5252: "leader-birth", 5253: "child-birth"}}
	killCalls := 0
	killed, err := killSessionPGIDIfIdentityMatches(5252, expected,
		func(int) []int { return []int{5253} }, // leader exited; child survives
		func(pid int) (string, bool) { return expected.members[pid], true },
		func(pgid int) error {
			if pgid != expected.pgid {
				t.Fatalf("kill pgid=%d, want %d", pgid, expected.pgid)
			}
			killCalls++
			return nil
		},
	)
	if err != nil || !killed || killCalls != 1 {
		t.Fatalf("dead-leader descendant result: killed=%v calls=%d err=%v", killed, killCalls, err)
	}
}

func TestKillSessionPGIDIfIdentityMatches_FailsClosedForUnknownMember(t *testing.T) {
	t.Parallel()

	expected := sessionPGIDIdentity{pgid: 6262, members: map[int]string{6262: "leader-birth"}}
	killCalls := 0
	killed, err := killSessionPGIDIfIdentityMatches(6262, expected,
		func(int) []int { return []int{6263} },
		func(int) (string, bool) { return "unknown-birth", true },
		func(int) error { killCalls++; return nil },
	)
	if err != nil || killed || killCalls != 0 {
		t.Fatalf("unknown member did not fail closed: killed=%v calls=%d err=%v", killed, killCalls, err)
	}
}
