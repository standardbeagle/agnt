package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/stretchr/testify/require"
)

func newSessionRetirementTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := &Daemon{
		sessionRegistry: NewSessionRegistry(time.Minute),
		incidentBus:     incident.NewMPSCBus(nil),
	}
	t.Cleanup(d.incidentBus.Close)
	return d
}

func TestNoProjectCleanupFinalizesOnlyExactSession(t *testing.T) {
	t.Parallel()
	d := newSessionRetirementTestDaemon(t)
	retired := &Session{Code: "retired", Status: SessionStatusActive}
	unrelated := &Session{Code: "unrelated", Status: SessionStatusActive}
	require.NoError(t, d.sessionRegistry.Register(retired))
	require.NoError(t, d.sessionRegistry.Register(unrelated))
	d.addIncidentSession(retired.Code)
	d.addIncidentSession(unrelated.Code)
	d.forwardMappings.Store(retired.Code, "retired-forward")
	d.forwardMappings.Store(unrelated.Code, "unrelated-forward")
	d.forwardingPaused.Store(retired.Code, true)
	d.forwardingPaused.Store(unrelated.Code, true)

	d.doCleanupExact(retired.Code, retired, nil)

	_, ok := d.sessionRegistry.Get(retired.Code)
	require.False(t, ok)
	_, ok = d.forwardMappings.Load(retired.Code)
	require.False(t, ok)
	_, ok = d.forwardingPaused.Load(retired.Code)
	require.False(t, ok)
	entries, stats := d.incidentBus.QuerySession(retired.Code, incident.QueryFilter{})
	require.Empty(t, entries)
	require.Zero(t, stats)

	got, ok := d.sessionRegistry.Get(unrelated.Code)
	require.True(t, ok)
	require.Same(t, unrelated, got)
	_, ok = d.forwardMappings.Load(unrelated.Code)
	require.True(t, ok)
	_, ok = d.forwardingPaused.Load(unrelated.Code)
	require.True(t, ok)

	// An intact unrelated pipeline still accepts events after the other
	// session's pipeline (including inbox, dedup, and blob store) is removed.
	d.incidentBus.Publish(incident.NewIncidentEvent(
		incident.SourceProcessOutput, incident.SeverityError, "test", "unrelated survives",
		incident.Context{SessionID: unrelated.Code}, nil,
	))
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession(unrelated.Code, incident.QueryFilter{})
		return len(entries) == 1
	}, time.Second, time.Millisecond)
}

func TestStaleCleanupCannotRetireReregisteredSession(t *testing.T) {
	t.Parallel()
	d := newSessionRetirementTestDaemon(t)
	old := &Session{Code: "same-code", Status: SessionStatusActive, SessionPGID: 999999}
	fresh := &Session{Code: "same-code", Status: SessionStatusActive, SessionPGID: 888888}
	require.NoError(t, d.sessionRegistry.Register(old))
	d.addIncidentSession(old.Code)

	// Hold the exact gate so cleanup reaches a deterministic barrier before it
	// can inspect or reap the captured lifetime. Replacing the registry entry
	// models the atomic reconnect step performed under this same gate.
	unlock := d.sessionLifecycle.lock(old.Code)
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(entered)
		d.doCleanupExact(old.Code, old, nil)
		close(done)
	}()
	<-entered
	require.True(t, d.sessionRegistry.ReplaceExact(old.Code, old, fresh))
	d.addIncidentSession(fresh.Code)
	unlock()
	<-done

	got, ok := d.sessionRegistry.Get(fresh.Code)
	require.True(t, ok)
	require.Same(t, fresh, got)
	// A stale cleanup must not remove any state belonging to the fresh lifetime.
	d.incidentBus.Publish(incident.NewIncidentEvent(
		incident.SourceProcessOutput, incident.SeverityError, "test", "fresh survives",
		incident.Context{SessionID: fresh.Code}, nil,
	))
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession(fresh.Code, incident.QueryFilter{})
		return len(entries) == 1
	}, time.Second, time.Millisecond)
}

// TestNoProjectCleanupSkipsFinalizeWhenReregistered covers the gate re-check arm
// in CleanupSessionResources (daemon_session_cleanup.go): when a session is
// absent at the first Get, cleanup parks on the per-code lifecycle gate before
// finalizing state. If a fresh registration wins that gate first, the re-check
// sees it registered and skips finalizeSessionState — the new lifetime's registry
// entry, forward mapping, and incident pipeline must all survive.
func TestNoProjectCleanupSkipsFinalizeWhenReregistered(t *testing.T) {
	t.Parallel()
	d := newSessionRetirementTestDaemon(t)
	const code = "reregistered-during-cleanup"

	// Hold the gate so the cleanup goroutine parks on it after observing the
	// session is absent, giving the registration below a deterministic window.
	unlock := d.sessionLifecycle.lock(code)

	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(entered)
		// Session is not registered yet, so this takes the not-registered branch,
		// then blocks acquiring the gate we hold.
		d.CleanupSessionResources(code)
		close(done)
	}()
	<-entered

	// Wait until the cleanup goroutine has entered lock() for this code (refs>=2:
	// our hold plus its blocked acquire). refs is only incremented AFTER cleanup's
	// first Get returned absent, so registering now cannot be seen by that Get —
	// the goroutine is committed to the not-registered branch.
	require.Eventually(t, func() bool {
		d.sessionLifecycle.mu.Lock()
		defer d.sessionLifecycle.mu.Unlock()
		g := d.sessionLifecycle.gates[code]
		return g != nil && g.refs >= 2
	}, time.Second, time.Millisecond)

	// A fresh registration wins: install the session plus the session-scoped state
	// a real SESSION REGISTER would, all keyed to this code.
	fresh := &Session{Code: code, Status: SessionStatusActive}
	require.NoError(t, d.sessionRegistry.Register(fresh))
	d.addIncidentSession(code)
	d.forwardMappings.Store(code, "fresh-forward")

	unlock()
	<-done

	// The gate re-check saw the fresh registration, so finalizeSessionState was
	// skipped: registry entry, forward mapping, and incident pipeline survive.
	got, ok := d.sessionRegistry.Get(code)
	require.True(t, ok)
	require.Same(t, fresh, got)
	_, ok = d.forwardMappings.Load(code)
	require.True(t, ok, "stale cleanup cleared a fresh lifetime's forward mapping")
	d.incidentBus.Publish(incident.NewIncidentEvent(
		incident.SourceProcessOutput, incident.SeverityError, "test", "fresh survives",
		incident.Context{SessionID: code}, nil,
	))
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession(code, incident.QueryFilter{})
		return len(entries) == 1
	}, time.Second, time.Millisecond)
}
