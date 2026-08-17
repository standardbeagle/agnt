//go:build unix

package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/selflog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCleanupTestDaemon constructs a started daemon configured with a tight
// cleanup grace period so deferred-cleanup tests don't drag.
//
// OrphanScanEnabled is left at its zero value (false): the regular daemon
// test suite must never issue real kill(2) syscalls against host pgids.
// Tests that need the scan to run (daemon_orphan_pgid_test.go under the
// `procisolation` build tag) construct their own daemon with
// OrphanScanEnabled: true.
func newCleanupTestDaemon(t *testing.T, grace time.Duration) *Daemon {
	t.Helper()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	d := New(DaemonConfig{
		SocketPath:         sockPath,
		MaxClients:         10,
		WriteTimeout:       5 * time.Second,
		CleanupGracePeriod: grace,
		// OrphanScanEnabled left zero — test-safe default per iter 15.
	})

	require.NoError(t, d.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})

	return d
}

// blockingAutostartFn returns an AutostartStartFunc that blocks until its
// context is cancelled. cancelled is set to 1 if the goroutine observed
// ctx.Done() before its hard timeout.
func blockingAutostartFn(cancelled *atomic.Int32) AutostartStartFunc {
	return func(ctx context.Context, _ chan<- AutostartProgress) *AutostartResult {
		select {
		case <-ctx.Done():
			cancelled.Store(1)
		case <-time.After(10 * time.Second):
			// Hard timeout — guards the test from hanging if cancellation
			// wiring is broken.
		}
		return &AutostartResult{}
	}
}

// registerSession is a small helper that puts a session into the registry
// without going through the full hub handshake.
func registerSession(t *testing.T, d *Daemon, code, projectPath string) {
	t.Helper()
	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code:        code,
		ProjectPath: projectPath,
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}))
}

// TestCleanupSessionResources_CancelsAutostart_LastSession verifies that the
// running autostart context is cancelled when the last session for a project
// is cleaned up.
func TestCleanupSessionResources_CancelsAutostart_LastSession(t *testing.T) {
	t.Parallel()
	d := newCleanupTestDaemon(t, 10*time.Millisecond)

	tmpDir := t.TempDir()
	registerSession(t, d, "session-A", tmpDir)

	var cancelled atomic.Int32
	handle := d.autostartManager.GetOrCreate(tmpDir, blockingAutostartFn(&cancelled))
	require.NotNil(t, handle)

	// Sanity: handle is still in flight (not done) before cleanup.
	select {
	case <-handle.Done():
		t.Fatal("handle done too early — blockingAutostartFn returned without cancellation")
	case <-time.After(20 * time.Millisecond):
	}

	d.CleanupSessionResources("session-A")

	// Wait for the cancellation to propagate and the worker to return.
	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("autostart handle did not finish after Cancel()")
	}

	assert.Equal(t, int32(1), cancelled.Load(),
		"autostart goroutine should have observed ctx.Done()")
	assert.Nil(t, d.autostartManager.Get(tmpDir),
		"handle should be removed from the manager after last-session cleanup")
}

// TestCleanupSessionResources_KeepsAutostart_OtherSessionsRemain verifies
// that disconnecting a non-last session does NOT cancel the autostart run.
func TestCleanupSessionResources_KeepsAutostart_OtherSessionsRemain(t *testing.T) {
	t.Parallel()
	d := newCleanupTestDaemon(t, 10*time.Millisecond)

	tmpDir := t.TempDir()
	registerSession(t, d, "session-A", tmpDir)
	registerSession(t, d, "session-B", tmpDir)

	var cancelled atomic.Int32
	handle := d.autostartManager.GetOrCreate(tmpDir, blockingAutostartFn(&cancelled))
	require.NotNil(t, handle)

	// Disconnect only one session.
	d.CleanupSessionResources("session-A")

	// Give Cancel a chance to (incorrectly) propagate.
	select {
	case <-handle.Done():
		t.Fatal("autostart handle should NOT be done — another session remains")
	case <-time.After(150 * time.Millisecond):
	}

	assert.Equal(t, int32(0), cancelled.Load(),
		"autostart goroutine must not be cancelled while sessions remain")
	assert.NotNil(t, d.autostartManager.Get(tmpDir),
		"handle should still be tracked in the manager")

	// Cleanly tear down the second session so the test exits without leaks.
	d.CleanupSessionResources("session-B")
	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("autostart handle did not finish after final cleanup")
	}
	assert.Equal(t, int32(1), cancelled.Load(),
		"autostart goroutine should be cancelled after the last session leaves")
}

// TestCleanupSessionResources_CancelsAutostart_AllSessionsLeave verifies
// the multi-session shutdown path: with two sessions, autostart survives the
// first disconnect and is cancelled when the second leaves.
func TestCleanupSessionResources_CancelsAutostart_AllSessionsLeave(t *testing.T) {
	t.Parallel()
	d := newCleanupTestDaemon(t, 10*time.Millisecond)

	tmpDir := t.TempDir()
	registerSession(t, d, "session-A", tmpDir)
	registerSession(t, d, "session-B", tmpDir)

	var cancelled atomic.Int32
	handle := d.autostartManager.GetOrCreate(tmpDir, blockingAutostartFn(&cancelled))
	require.NotNil(t, handle)

	d.CleanupSessionResources("session-A")
	// Still alive after first disconnect.
	require.NotNil(t, d.autostartManager.Get(tmpDir))
	require.Equal(t, int32(0), cancelled.Load())

	d.CleanupSessionResources("session-B")
	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("autostart handle did not finish after the last session left")
	}

	assert.Equal(t, int32(1), cancelled.Load())
	assert.Nil(t, d.autostartManager.Get(tmpDir))
}

// TestCleanupSessionResourcesDeferred_ReconnectWithdrawsCancel verifies that
// a re-registration during the grace period cancels the deferred cleanup,
// and the autostart context is NOT cancelled.
func TestCleanupSessionResourcesDeferred_ReconnectWithdrawsCancel(t *testing.T) {
	t.Parallel()
	const grace = 200 * time.Millisecond
	d := newCleanupTestDaemon(t, grace)

	tmpDir := t.TempDir()
	registerSession(t, d, "session-A", tmpDir)

	var cancelled atomic.Int32
	handle := d.autostartManager.GetOrCreate(tmpDir, blockingAutostartFn(&cancelled))
	require.NotNil(t, handle)

	// Connection drop: schedule deferred cleanup.
	d.CleanupSessionResourcesDeferred("session-A")

	// Reconnect well within the grace period: bump LastSeen and cancel the
	// pending timer (this is what hubHandleSessionRegister does on reconnect).
	time.Sleep(grace / 4)
	if s, ok := d.sessionRegistry.Get("session-A"); ok {
		s.LastSeen = time.Now()
	}
	d.cancelPendingCleanup("session-A")

	// Wait until well past the grace window.
	time.Sleep(grace + 200*time.Millisecond)

	select {
	case <-handle.Done():
		t.Fatal("autostart handle was cancelled — reconnect should have withdrawn the cleanup")
	default:
	}
	assert.Equal(t, int32(0), cancelled.Load(),
		"autostart goroutine must survive a reconnect within the grace period")
	assert.NotNil(t, d.autostartManager.Get(tmpDir),
		"handle should remain in the manager after a reconnect")

	// Final tear-down: explicit cleanup so the test exits cleanly.
	d.CleanupSessionResources("session-A")
	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("autostart handle did not finish after final cleanup")
	}
}

// TestCleanupSessionResources_NoAutostartHandle_NoPanic verifies that the
// cleanup path is robust when no autostart handle was ever created for the
// project (e.g., empty .agnt.kdl, programmatic registration, tests).
func TestCleanupSessionResources_NoAutostartHandle_NoPanic(t *testing.T) {
	t.Parallel()
	d := newCleanupTestDaemon(t, 10*time.Millisecond)

	tmpDir := t.TempDir()
	registerSession(t, d, "session-A", tmpDir)

	// No GetOrCreate — handle never registered.
	require.NotPanics(t, func() {
		d.CleanupSessionResources("session-A")
	})
	assert.Nil(t, d.autostartManager.Get(tmpDir))
}

// TestCleanupSessionResources_ExplicitProxy_LastSession covers the T5
// acceptance contract for the "last session disconnects" branch.
//
// Setup: session A is the sole observer of a project. Session A starts
// an explicit proxy via handleExplicitStart (the same path autostart
// and the MCP tool now share after T3). The proxy is wired into
// proxym + stateMgr + proxyEntries in that single call.
//
// Expected on cleanup: the proxy is stopped, its admin entry is gone
// from proxyEntries, and its persisted config is removed from stateMgr
// so the next daemon restart does not silently re-create a dead proxy.
func TestCleanupSessionResources_ExplicitProxy_LastSession(t *testing.T) {
	t.Parallel()
	d := newCleanupTestDaemon(t, 10*time.Millisecond)

	tmpDir := t.TempDir()
	registerSession(t, d, "session-A", tmpDir)

	proxyID := makeProcessID(tmpDir, "web")
	d.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  &config.ProxyConfig{URL: "http://127.0.0.1:65040"},
		Path:    tmpDir,
	})

	// Preconditions: proxy exists on every surface.
	require.Len(t, d.proxyEntries.List(tmpDir), 1,
		"precondition: explicit proxy must register a proxy-kind admin entry")
	_, getErr := d.proxym.Get(proxyID)
	require.NoError(t, getErr, "precondition: proxy must exist in proxym")
	if d.stateMgr != nil {
		_, ok := d.stateMgr.GetProxy(proxyID)
		require.True(t, ok, "precondition: proxy must be persisted to stateMgr")
	}

	d.CleanupSessionResources("session-A")

	// Postconditions: proxy is stopped, admin entry is cleared, stateMgr
	// no longer remembers the proxy.
	assert.Empty(t, d.proxyEntries.List(tmpDir),
		"proxy-kind admin entries must be cleared after last session leaves")
	_, getErr = d.proxym.Get(proxyID)
	assert.Error(t, getErr, "proxy must be stopped in proxym after cleanup")
	if d.stateMgr != nil {
		_, ok := d.stateMgr.GetProxy(proxyID)
		assert.False(t, ok, "stateMgr must not leak a stopped proxy across restart")
	}
}

// TestCleanupSessionResources_ExplicitProxy_OtherSessionRemains covers
// the T5 acceptance contract for the "non-last session leaves" branch.
//
// Setup: sessions A and B both observe the same project. Session A
// starts the explicit proxy. When session A disconnects, session B is
// still an observer — the proxy must stay running and visible in
// SCRIPT LIST so session B's overlay still renders its indicator.
//
// This tightly mirrors the existing script-registry multi-session
// semantics: process-kind entries transfer ownership, proxy-kind
// entries stay up for as long as any session observes the project.
func TestCleanupSessionResources_ExplicitProxy_OtherSessionRemains(t *testing.T) {
	t.Parallel()
	d := newCleanupTestDaemon(t, 10*time.Millisecond)

	tmpDir := t.TempDir()
	registerSession(t, d, "session-A", tmpDir)
	registerSession(t, d, "session-B", tmpDir)

	proxyID := makeProcessID(tmpDir, "api")
	d.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  &config.ProxyConfig{URL: "http://127.0.0.1:65041"},
		Path:    tmpDir,
	})

	require.Len(t, d.proxyEntries.List(tmpDir), 1, "precondition: admin entry registered")

	d.CleanupSessionResources("session-A")

	// Session B still observes this project: proxy must still be up.
	assert.Len(t, d.proxyEntries.List(tmpDir), 1,
		"proxy-kind admin entry must persist while another session observes")
	_, getErr := d.proxym.Get(proxyID)
	assert.NoError(t, getErr, "proxy must remain running while other sessions remain")

	// SCRIPT LIST (admin surface) must still include the proxy row so
	// session B's overlay renders the indicator.
	summaries := d.buildScriptListSummaries(tmpDir)
	var sawProxy bool
	for _, s := range summaries {
		if s["kind"] == string(ScriptKindProxy) && s["process_id"] == proxyID {
			sawProxy = true
			break
		}
	}
	assert.True(t, sawProxy,
		"SCRIPT LIST must still surface the proxy-kind row for session B")
}

// TestRestoreProxies_RegistersAdminEntry covers the T5 daemon-restart
// acceptance contract. An explicit proxy survives a daemon restart via
// the stateMgr persistence path wired up in T1 (handleExplicitStart →
// stateMgr.AddProxy). On the next boot, restoreProxies re-creates the
// proxy in proxym — T5 adds the parallel registration in proxyEntries
// so the restored proxy appears in SCRIPT LIST (and therefore in the
// overlay status bar) just like it did pre-restart.
//
// Without this, a restart silently drops the proxy from the admin
// surface even though proxym has it running again — the indicator
// disappears for the developer even though the proxy is healthy.
func TestRestoreProxies_RegistersAdminEntry(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sockPath := filepath.Join(stateDir, "d.sock")
	d := New(DaemonConfig{
		SocketPath:             sockPath,
		MaxClients:             10,
		WriteTimeout:           5 * time.Second,
		CleanupGracePeriod:     10 * time.Millisecond,
		EnableStatePersistence: true,
		StatePath:              filepath.Join(stateDir, "state.json"),
	})
	require.NoError(t, d.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	require.NotNil(t, d.stateMgr, "test requires a configured stateMgr")

	tmpDir := t.TempDir()
	proxyID := makeProcessID(tmpDir, "persisted")

	// Simulate a pre-restart persisted state entry.
	d.stateMgr.AddProxy(PersistentProxyConfig{
		ID:         proxyID,
		TargetURL:  "http://127.0.0.1:65042",
		Port:       0,
		MaxLogSize: 100,
		Path:       tmpDir,
	})

	// Sanity: no admin entry yet — restoreProxies owns creating it.
	require.Empty(t, d.proxyEntries.List(tmpDir),
		"precondition: no admin entry before restore")

	d.restoreProxies()

	// Proxy re-created in proxym.
	_, getErr := d.proxym.Get(proxyID)
	require.NoError(t, getErr, "restored proxy must exist in proxym")

	// Admin entry registered so SCRIPT LIST surfaces it post-restart.
	entries := d.proxyEntries.List(tmpDir)
	require.Len(t, entries, 1, "restoreProxies must register a proxy-kind admin entry")
	assert.Equal(t, proxyID, entries[0].ProxyID(),
		"admin entry must map back to the restored proxy ID")
}

// --- session pgid reap diagnostics -----------------------------------------
//
// The reap kills the session's whole process group, which on an `agnt run`
// session includes the coding agent itself. From the user's seat that is an
// unexplained SIGKILL, so every reap that actually signals must leave a
// user-readable record in selflog (`agnt hook log`). These tests pin that the
// record rides the path that kills, and that the two skip reasons are told
// apart.

// selflogSink redirects selflog.Record for one test and returns a reader for
// whatever was written.
func selflogSink(t *testing.T) func() []selflog.Entry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "errors.log")
	t.Setenv("AGNT_ERROR_LOG", path)
	return func() []selflog.Entry {
		entries, err := selflog.Read(path, 0)
		require.NoError(t, err)
		return entries
	}
}

// matchingIdentity builds an expected identity plus the members/birth probes
// that agree with it, so the guarded path reaches its kill.
func matchingIdentity(pgid int, pids ...int) (sessionPGIDIdentity, func(int) []int, func(int) (string, bool)) {
	identity := sessionPGIDIdentity{pgid: pgid, members: make(map[int]string, len(pids))}
	for _, pid := range pids {
		identity.members[pid] = fmt.Sprintf("birth-%d", pid)
	}
	members := func(int) []int { return pids }
	birth := func(pid int) (string, bool) {
		b, ok := identity.members[pid]
		return b, ok
	}
	return identity, members, birth
}

// TestReapSessionPGID_GuardedSuccessRecordsSelflog is the regression this task
// exists for: the identity-guarded branch is the branch that reaps live
// sessions, and it used to kill without recording anything.
func TestReapSessionPGID_GuardedSuccessRecordsSelflog(t *testing.T) {
	read := selflogSink(t)
	expected, members, birth := matchingIdentity(4242, 4242, 4243)

	var killed []int
	outcome, err := reapSessionPGID("cdsp-7654", "/home/dev/proj", 4242, nil, &expected,
		members, birth, func(pgid int) error { killed = append(killed, pgid); return nil })

	require.NoError(t, err)
	assert.Equal(t, sessionPGIDReapKilled, outcome)
	assert.Equal(t, []int{4242}, killed, "guarded success must still deliver the signal")

	entries := read()
	require.Len(t, entries, 1, "a successful guarded reap must leave a selflog record")
	msg := entries[0].Message
	assert.Equal(t, "daemon", entries[0].Component)
	assert.Contains(t, msg, "cdsp-7654", "record must name the session that reaped")
	assert.Contains(t, msg, "4242", "record must name the pgid")
	assert.Contains(t, msg, "/home/dev/proj", "record must name the project path")
}

// TestReapSessionPGID_NilExpectedRecordsSelflog covers the unguarded caller
// contract (CleanupSessionResources -> doCleanupExact(..., nil)): an immediate
// cleanup captured no identity, kills unconditionally, and must say so.
func TestReapSessionPGID_NilExpectedRecordsSelflog(t *testing.T) {
	read := selflogSink(t)

	var killed []int
	outcome, err := reapSessionPGID("cdsp-1", "/home/dev/proj", 99, nil,
		nil, func(int) []int { return []int{99} },
		nil, func(pgid int) error { killed = append(killed, pgid); return nil })

	require.NoError(t, err)
	assert.Equal(t, sessionPGIDReapKilled, outcome)
	assert.Equal(t, []int{99}, killed)
	entries := read()
	require.Len(t, entries, 1, "the unguarded immediate-cleanup kill must also be recorded")
	assert.Contains(t, entries[0].Message, "cdsp-1")
}

// TestReapSessionPGID_SkipsAreDistinctAndSilent asserts the fail-safe halves:
// neither skip signals anything, neither writes a "we killed you" record, and
// a capture failure is not reported as an identity change.
func TestReapSessionPGID_SkipsAreDistinctAndSilent(t *testing.T) {
	read := selflogSink(t)
	kill := func(int) error { t.Fatalf("skip path must never signal"); return nil }

	// Capture failure: inspectSessionPGIDIdentity returned false, so the
	// scheduler stored a zero-value identity — we never had ownership evidence.
	zero := sessionPGIDIdentity{}
	outcome, err := reapSessionPGID("s1", "/p", 4242, nil, &zero,
		func(int) []int { return []int{4242} },
		func(int) (string, bool) { return "birth-4242", true }, kill)
	require.NoError(t, err)
	assert.Equal(t, sessionPGIDReapIdentityUnavailable, outcome)

	// Identity change: we had evidence, the live group no longer matches it.
	expected, members, _ := matchingIdentity(4242, 4242)
	outcome, err = reapSessionPGID("s2", "/p", 4242, nil, &expected, members,
		func(int) (string, bool) { return "birth-of-someone-else", true }, kill)
	require.NoError(t, err)
	assert.Equal(t, sessionPGIDReapIdentityChanged, outcome)

	assert.NotEqual(t, sessionPGIDReapIdentityUnavailable, sessionPGIDReapIdentityChanged,
		"the two skip reasons must be distinguishable outcomes")
	assert.Empty(t, read(), "a skipped reap must not claim it terminated anything")
}

// TestReapSessionPGID_RefusesGroupItDoesNotExclusivelyOwn is the guard that
// makes reaping the user's terminal structurally impossible.
//
// creack/pty puts the PTY child in a fresh POSIX session (measured on this
// project's Linux/WSL2 target: child pgid == pid == sid), so neither the daemon
// nor the `agnt run` wrapper that owns the session can legitimately be a member
// of the session's own process group. If either one IS a member, the reported
// SessionPGID is an ancestor group — the terminal — and signalling it kills
// processes that never belonged to this session.
//
// Both cleanup triggers are covered: the identity-guarded deferred path AND the
// unguarded explicit-UNREGISTER path (expected == nil), which has no recycle
// guard at all and would otherwise signal an ancestor group unconditionally.
func TestReapSessionPGID_RefusesGroupItDoesNotExclusivelyOwn(t *testing.T) {
	const (
		terminalPGID = 4242
		daemonPID    = 777
		ownerPID     = 888
	)
	// Errorf, not Fatalf: this closure runs inside subtests, and FailNow from a
	// parent's *testing.T there aborts the wrong goroutine.
	kill := func(int) error {
		t.Errorf("a group containing the daemon or the session owner must never be signalled")
		return nil
	}

	cases := []struct {
		name    string
		members []int
	}{
		{"daemon is a member", []int{terminalPGID, daemonPID}},
		{"owning agnt wrapper is a member", []int{terminalPGID, ownerPID}},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/deferred", func(t *testing.T) {
			read := selflogSink(t)
			expected, _, birth := matchingIdentity(terminalPGID, tc.members...)
			outcome, err := reapSessionPGID("cdsp-7654", "/p", terminalPGID,
				[]int{daemonPID, ownerPID}, &expected,
				func(int) []int { return tc.members }, birth, kill)
			require.NoError(t, err)
			assert.Equal(t, sessionPGIDReapNotExclusive, outcome)
			assert.Empty(t, read(), "a refused reap must not claim it terminated anything")
		})
		t.Run(tc.name+"/unregister", func(t *testing.T) {
			read := selflogSink(t)
			outcome, err := reapSessionPGID("cdsp-7654", "/p", terminalPGID,
				[]int{daemonPID, ownerPID}, nil,
				func(int) []int { return tc.members }, nil, kill)
			require.NoError(t, err)
			assert.Equal(t, sessionPGIDReapNotExclusive, outcome)
			assert.Empty(t, read())
		})
	}

	// An unset OwnerPID (0) must not be matched against anything: pid 0 is
	// never a real group member, and treating "unknown owner" as a violation
	// would disable every legitimate reap.
	t.Run("unset owner pid does not match", func(t *testing.T) {
		var killed []int
		outcome, err := reapSessionPGID("cdsp-7654", "/p", terminalPGID,
			[]int{daemonPID, 0}, nil,
			func(int) []int { return []int{terminalPGID} }, nil,
			func(pgid int) error { killed = append(killed, pgid); return nil })
		require.NoError(t, err)
		assert.Equal(t, sessionPGIDReapKilled, outcome)
		assert.Equal(t, []int{terminalPGID}, killed)
	})

	// Membership is the evidence the guard rests on. Without it exclusivity is
	// unprovable, and unprovable must fail safe rather than fall through.
	t.Run("unreadable membership fails safe", func(t *testing.T) {
		outcome, err := reapSessionPGID("cdsp-7654", "/p", terminalPGID,
			[]int{daemonPID, ownerPID}, nil, nil, nil, kill)
		require.NoError(t, err)
		assert.Equal(t, sessionPGIDReapNotExclusive, outcome)
	})
}

// TestReapSessionPGID_EmptyGroupIsNotReportedAsATermination closes the false
// alarm this investigation traced. Every clean `agnt run` exit ends with an
// explicit UNREGISTER, by which time the PTY child is already gone and its
// group is empty — yet the reap was recorded as "any agent running in it was
// terminated". That line is what made ten benign session ends read as
// unexplained kills in `agnt hook log`.
func TestReapSessionPGID_EmptyGroupIsNotReportedAsATermination(t *testing.T) {
	read := selflogSink(t)

	var killed []int
	outcome, err := reapSessionPGID("cdsp-7654", "/p", 4242, []int{777, 888}, nil,
		func(int) []int { return nil }, nil,
		func(pgid int) error { killed = append(killed, pgid); return nil })

	require.NoError(t, err)
	assert.Equal(t, sessionPGIDReapKilled, outcome)
	assert.Equal(t, []int{4242}, killed, "an empty group is still signalled — it is simply a no-op")
	assert.Empty(t, read(), "signalling an empty group terminated nothing and must not say it did")
}

// TestRecordSessionPGIDReap_NamesTheTrigger keeps the two cleanup triggers
// distinguishable in the user-visible log. Without it, an explicit unregister
// (the agent already exited — benign) and a deferred disconnect cleanup (the
// agent may have been live — the dangerous case) produce byte-identical
// records, which is why the six reported incidents could not be adjudicated
// from the log at all.
func TestRecordSessionPGIDReap_NamesTheTrigger(t *testing.T) {
	read := selflogSink(t)

	recordSessionPGIDReap("s-unreg", "/p", 4242, 2, false)
	recordSessionPGIDReap("s-deferred", "/p", 4243, 3, true)

	entries := read()
	require.Len(t, entries, 2)
	assert.NotEqual(t, entries[0].Message, entries[1].Message,
		"the two cleanup triggers must be distinguishable in the record")
	assert.Contains(t, entries[0].Message, "2 process")
	assert.Contains(t, entries[1].Message, "3 process")
}

// TestKillSessionPGID_SkipReasonsAreDistinctInStartupLog drives the daemon
// method itself and asserts the operator-visible startup-log entries differ.
// Both cases are kill-free by construction, so no real process is signalled.
func TestKillSessionPGID_SkipReasonsAreDistinctInStartupLog(t *testing.T) {
	pgid := os.Getpid() // read-only: every case below skips before any kill

	newSession := func(code string) *Session {
		return &Session{Code: code, ProjectPath: "/p", SessionPGID: pgid}
	}
	entryFor := func(expected *sessionPGIDIdentity, code string) StartupLogEntry {
		d := &Daemon{startupErrorStore: NewStartupLogStore(50)}
		d.killSessionPGID(newSession(code), expected)
		entries := d.startupErrorStore.Query(StartupLogFilter{})
		require.Len(t, entries, 1, "a skipped reap must be visible to the operator")
		return *entries[0]
	}

	unavailable := entryFor(&sessionPGIDIdentity{}, "s-unavailable")
	changed := entryFor(&sessionPGIDIdentity{
		pgid:    pgid,
		members: map[int]string{pgid: "birth-of-someone-else"},
	}, "s-changed")

	assert.NotEqual(t, unavailable.EventType, changed.EventType,
		"capture failure and identity change must be different event types")
	assert.NotEqual(t, unavailable.Message, changed.Message,
		"capture failure must not be reported as an identity change")
	assert.Contains(t, changed.Message, "s-changed")
	assert.Contains(t, unavailable.Message, "s-unavailable")
}
