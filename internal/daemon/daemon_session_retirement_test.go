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
