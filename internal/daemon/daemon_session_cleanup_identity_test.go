package daemon

import (
	"testing"
	"time"
)

func TestInspectSessionPGIDIdentity_RecognizesLiveGroupLeader(t *testing.T) {
	pgid, _ := startPGIDLeader(t)

	identity, ok := inspectSessionPGIDIdentity(pgid)
	if !ok {
		t.Fatalf("live pgid leader %d was not inspectable", pgid)
	}
	if identity.pid != pgid || identity.cmdline == "" {
		t.Fatalf("identity = %+v, want pid=%d and non-empty cmdline", identity, pgid)
	}

	// Give /proc enumeration a chance to expose accidental transient behavior;
	// the same leader must remain recognizable throughout the cleanup window.
	time.Sleep(10 * time.Millisecond)
	current, ok := inspectSessionPGIDIdentity(pgid)
	if !ok || !sessionPGIDIdentityMatches(identity, current) {
		t.Fatalf("live leader identity changed: before=%+v after=%+v ok=%v", identity, current, ok)
	}
}

func TestKillSessionPGIDIfIdentityMatches_SkipsRecycledLeader(t *testing.T) {
	t.Parallel()

	expected := sessionPGIDIdentity{pid: 4242, cmdline: "agnt run claude", cwd: "/work/owned"}
	recycled := sessionPGIDIdentity{pid: 4242, cmdline: "editor", cwd: "/home/user"}
	killCalls := 0

	killed, err := killSessionPGIDIfIdentityMatches(4242, expected,
		func(int) (sessionPGIDIdentity, bool) { return recycled, true },
		func(int) error { killCalls++; return nil },
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if killed || killCalls != 0 {
		t.Fatalf("recycled pgid was killed: killed=%v calls=%d", killed, killCalls)
	}
}

func TestKillSessionPGIDIfIdentityMatches_KillsSameLeader(t *testing.T) {
	t.Parallel()

	expected := sessionPGIDIdentity{pid: 5252, cmdline: "agnt run claude", cwd: "/work/owned"}
	killCalls := 0
	killed, err := killSessionPGIDIfIdentityMatches(5252, expected,
		func(int) (sessionPGIDIdentity, bool) { return expected, true },
		func(pgid int) error {
			if pgid != expected.pid {
				t.Fatalf("kill pgid=%d, want %d", pgid, expected.pid)
			}
			killCalls++
			return nil
		},
	)
	if err != nil || !killed || killCalls != 1 {
		t.Fatalf("same leader result: killed=%v calls=%d err=%v", killed, killCalls, err)
	}
}

func TestKillSessionPGIDIfIdentityMatches_FailsClosedWhenInspectionUnavailable(t *testing.T) {
	t.Parallel()

	killCalls := 0
	killed, err := killSessionPGIDIfIdentityMatches(6262, sessionPGIDIdentity{pid: 6262},
		func(int) (sessionPGIDIdentity, bool) { return sessionPGIDIdentity{}, false },
		func(int) error { killCalls++; return nil },
	)
	if err != nil || killed || killCalls != 0 {
		t.Fatalf("unavailable inspection did not fail closed: killed=%v calls=%d err=%v", killed, killCalls, err)
	}
}
