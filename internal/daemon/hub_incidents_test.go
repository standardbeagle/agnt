//go:build unix

package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHubIncidents_NoSession_ReturnsError verifies INCIDENTS QUERY with no
// session attached returns an error (session code is empty).
func TestHubIncidents_NoSession_ReturnsError(t *testing.T) {
	t.Parallel()
	client := newBootedClient(t)

	_, err := client.Conn().Request(protocol.VerbIncidents, protocol.SubVerbQuery).WithJSON(protocol.IncidentQueryFilter{}).JSON()
	assert.Error(t, err, "INCIDENTS QUERY with no session must fail")
}

// TestHubIncidents_EmptyInbox verifies INCIDENTS QUERY returns empty results
// for a freshly registered session with no published incidents.
func TestHubIncidents_EmptyInbox(t *testing.T) {
	t.Parallel()
	_, client, tmpDir := newBootedDaemonWithClient(t)

	// Attach to a session.
	_, err := client.Conn().Request("SESSION", "REGISTER", "inc-empty", tmpDir).WithJSON(map[string]interface{}{
		"project_path": tmpDir,
	}).JSON()
	require.NoError(t, err)

	result, err := client.Conn().Request(protocol.VerbIncidents, protocol.SubVerbQuery).WithJSON(protocol.IncidentQueryFilter{}).JSON()
	require.NoError(t, err)

	incs, _ := result["incidents"].([]interface{})
	assert.Empty(t, incs, "expected no incidents in empty inbox")
}

// TestHubIncidents_QueryReturnsPublishedEvents verifies that events published
// to the incident bus appear in INCIDENTS QUERY results.
func TestHubIncidents_QueryReturnsPublishedEvents(t *testing.T) {
	t.Parallel()
	d, client, tmpDir := newBootedDaemonWithClient(t)

	// Register a session so conn.SessionCode() is set.
	_, err := client.Conn().Request("SESSION", "REGISTER", "inc-query", tmpDir).WithJSON(map[string]interface{}{
		"project_path": tmpDir,
	}).JSON()
	require.NoError(t, err)

	// Wait for the session to be registered and for AddSession to wire the pipeline.
	var sessionCode string
	require.Eventually(t, func() bool {
		s, ok := d.sessionRegistry.FindByDirectory(tmpDir)
		if !ok {
			return false
		}
		sessionCode = s.Code
		return d.incidentBus.HasSession(sessionCode)
	}, 2*time.Second, 10*time.Millisecond, "session pipeline not registered")

	// Publish an event to the bus.
	ev := incident.NewIncidentEvent(incident.SourceBrowserJS, incident.SeverityError, "TypeError", "test incident", incident.Context{SessionID: sessionCode}, nil)
	d.incidentBus.Publish(ev)

	// Poll until inbox has the event.
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession(sessionCode, incident.QueryFilter{})
		return len(entries) > 0
	}, 2*time.Second, 20*time.Millisecond, "incident never landed in inbox")

	// Query via protocol.
	result, err := client.Conn().Request(protocol.VerbIncidents, protocol.SubVerbQuery).WithJSON(protocol.IncidentQueryFilter{}).JSON()
	require.NoError(t, err)

	incs, ok := result["incidents"].([]interface{})
	require.True(t, ok, "incidents field should be a list")
	assert.NotEmpty(t, incs, "expected at least one incident")

	first, ok := incs[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "error", first["severity"])
	assert.Equal(t, "browser_js", first["source"])
}

// TestHubIncidents_SeverityFilter verifies that the severity filter works.
func TestHubIncidents_SeverityFilter(t *testing.T) {
	t.Parallel()
	d, client, tmpDir := newBootedDaemonWithClient(t)

	_, err := client.Conn().Request("SESSION", "REGISTER", "inc-sev", tmpDir).WithJSON(map[string]interface{}{
		"project_path": tmpDir,
	}).JSON()
	require.NoError(t, err)

	var sessionCode string
	require.Eventually(t, func() bool {
		s, ok := d.sessionRegistry.FindByDirectory(tmpDir)
		if !ok {
			return false
		}
		sessionCode = s.Code
		return d.incidentBus.HasSession(sessionCode)
	}, 2*time.Second, 10*time.Millisecond)

	errEv := incident.NewIncidentEvent(incident.SourceBrowserJS, incident.SeverityError, "Err", "error msg", incident.Context{SessionID: sessionCode}, nil)
	warnEv := incident.NewIncidentEvent(incident.SourceHTTP4xx, incident.SeverityWarning, "Warn", "warn msg", incident.Context{SessionID: sessionCode}, nil)
	d.incidentBus.Publish(errEv)
	d.incidentBus.Publish(warnEv)

	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession(sessionCode, incident.QueryFilter{})
		return len(entries) >= 2
	}, 2*time.Second, 20*time.Millisecond, "waiting for both incidents")

	// Filter to errors only via protocol.
	result, err := client.Conn().Request(protocol.VerbIncidents, protocol.SubVerbQuery).WithJSON(protocol.IncidentQueryFilter{
		Severities: []string{"error"},
	}).JSON()
	require.NoError(t, err)

	incs, _ := result["incidents"].([]interface{})
	for _, inc := range incs {
		rec, _ := inc.(map[string]interface{})
		assert.Equal(t, "error", rec["severity"])
	}
	assert.NotEmpty(t, incs)
}

// TestHubIncidents_InboxStatsNewReflectsUnread verifies the wire inbox_stats.new
// is populated with the unread count (previously always 0, so an agent gating
// further pulls on `new` would stop polling a non-empty inbox) and drops to 0
// once the inbox is drained via a mark_read pull.
func TestHubIncidents_InboxStatsNewReflectsUnread(t *testing.T) {
	t.Parallel()
	d, client, tmpDir := newBootedDaemonWithClient(t)

	_, err := client.Conn().Request("SESSION", "REGISTER", "inc-new", tmpDir).WithJSON(map[string]interface{}{
		"project_path": tmpDir,
	}).JSON()
	require.NoError(t, err)

	var sessionCode string
	require.Eventually(t, func() bool {
		s, ok := d.sessionRegistry.FindByDirectory(tmpDir)
		if !ok {
			return false
		}
		sessionCode = s.Code
		return d.incidentBus.HasSession(sessionCode)
	}, 2*time.Second, 10*time.Millisecond)

	d.incidentBus.Publish(incident.NewIncidentEvent(incident.SourceBrowserJS, incident.SeverityError, "Err", "e", incident.Context{SessionID: sessionCode}, nil))
	d.incidentBus.Publish(incident.NewIncidentEvent(incident.SourceHTTP4xx, incident.SeverityWarning, "Warn", "w", incident.Context{SessionID: sessionCode}, nil))

	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession(sessionCode, incident.QueryFilter{})
		return len(entries) >= 2
	}, 2*time.Second, 20*time.Millisecond)

	statsNew := func(res map[string]interface{}) float64 {
		st, ok := res["inbox_stats"].(map[string]interface{})
		require.True(t, ok, "inbox_stats should be present")
		n, _ := st["new"].(float64)
		return n
	}

	// Unread: new must reflect the 2 unread entries, not 0.
	res, err := client.Conn().Request(protocol.VerbIncidents, protocol.SubVerbQuery).WithJSON(protocol.IncidentQueryFilter{}).JSON()
	require.NoError(t, err)
	assert.Equal(t, float64(2), statsNew(res), "inbox_stats.new must equal unread count")

	// Drain via a mark_read pull, then re-query: new must drop to 0.
	_, err = client.Conn().Request(protocol.VerbIncidents, protocol.SubVerbQuery).WithJSON(protocol.IncidentQueryFilter{MarkRead: true}).JSON()
	require.NoError(t, err)

	res, err = client.Conn().Request(protocol.VerbIncidents, protocol.SubVerbQuery).WithJSON(protocol.IncidentQueryFilter{}).JSON()
	require.NoError(t, err)
	assert.Equal(t, float64(0), statsNew(res), "inbox_stats.new must be 0 after inbox drained")
}

// TestHubIncidents_SessionLifecycle_AddOnRegister verifies that SESSION REGISTER
// wires up the incident bus pipeline for the session.
func TestHubIncidents_SessionLifecycle_AddOnRegister(t *testing.T) {
	t.Parallel()
	d, client, tmpDir := newBootedDaemonWithClient(t)

	_, err := client.Conn().Request("SESSION", "REGISTER", "inc-reg", tmpDir).WithJSON(map[string]interface{}{
		"project_path": tmpDir,
	}).JSON()
	require.NoError(t, err)

	// The session pipeline should be added to the incident bus.
	require.Eventually(t, func() bool {
		s, ok := d.sessionRegistry.FindByDirectory(tmpDir)
		if !ok {
			return false
		}
		return d.incidentBus.HasSession(s.Code)
	}, 2*time.Second, 10*time.Millisecond, "incident bus should have session pipeline after REGISTER")
}

// TestHubIncidents_SessionLifecycle_AttachAddsSession verifies that SESSION ATTACH
// also wires up the incident bus pipeline.
func TestHubIncidents_SessionLifecycle_AttachAddsSession(t *testing.T) {
	t.Parallel()
	d, client, tmpDir := newBootedDaemonWithClient(t)

	// Register via first client to create the session.
	_, err := client.Conn().Request("SESSION", "REGISTER", "inc-attach", tmpDir).WithJSON(map[string]interface{}{
		"project_path": tmpDir,
	}).JSON()
	require.NoError(t, err)

	// Connect a second client and ATTACH.
	client2 := daemonclient.NewClient(daemonclient.WithSocketPath(d.config.SocketPath))
	require.NoError(t, client2.Connect())
	t.Cleanup(func() { _ = client2.Close() })

	_, err = client2.Conn().Request("SESSION", "ATTACH", tmpDir).JSON()
	require.NoError(t, err)

	// Attach is idempotent on the bus — session pipeline still present.
	s, ok := d.sessionRegistry.FindByDirectory(tmpDir)
	require.True(t, ok)
	assert.True(t, d.incidentBus.HasSession(s.Code), "incident bus should have session pipeline after ATTACH")
}

// registerIncidentSession registers a session on the connection and returns its
// code once the incident pipeline is wired.
func registerIncidentSession(t *testing.T, d *Daemon, client *daemonclient.Client, name, dir string) string {
	t.Helper()
	_, err := client.Conn().Request("SESSION", "REGISTER", name, dir).WithJSON(map[string]interface{}{
		"project_path": dir,
	}).JSON()
	require.NoError(t, err)

	var sessionCode string
	require.Eventually(t, func() bool {
		s, ok := d.sessionRegistry.FindByDirectory(dir)
		if !ok {
			return false
		}
		sessionCode = s.Code
		return d.incidentBus.HasSession(sessionCode)
	}, 2*time.Second, 10*time.Millisecond, "session pipeline not registered")
	return sessionCode
}

// TestHubIncidents_PinUnpinClear drives the retention verbs over the wire and
// asserts the per-item pin state comes back on QUERY. Mutation check: drop
// Pinned/Tag from incidentEntryToRecord and the observability assertions fail.
func TestHubIncidents_PinUnpinClear(t *testing.T) {
	t.Parallel()
	d, client, tmpDir := newBootedDaemonWithClient(t)
	sessionCode := registerIncidentSession(t, d, client, "inc-pin", tmpDir)

	keep := incident.NewIncidentEvent(incident.SourceBrowserJS, incident.SeverityError, "TypeError",
		"keep me", incident.Context{SessionID: sessionCode}, nil)
	drop := incident.NewIncidentEvent(incident.SourceHTTP5xx, incident.SeverityError, "500",
		"drop me", incident.Context{SessionID: sessionCode}, nil)
	d.incidentBus.Publish(keep)
	d.incidentBus.Publish(drop)
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession(sessionCode, incident.QueryFilter{})
		return len(entries) == 2
	}, 2*time.Second, 10*time.Millisecond, "incidents never landed")

	// PIN reports the bound alongside the pin so the agent can see its budget.
	pinRes, err := client.Conn().Request(protocol.VerbIncidents, "PIN").
		WithJSON(protocol.IncidentPinPayload{Fingerprint: keep.Fingerprint, Tag: "under investigation"}).JSON()
	require.NoError(t, err)
	assert.Equal(t, true, pinRes["pinned"])
	assert.Equal(t, "under investigation", pinRes["tag"])
	assert.Equal(t, float64(1), pinRes["pinned_count"])
	assert.Equal(t, float64(incident.MaxPinnedEntries), pinRes["pin_limit"])

	// The per-item flags must be visible on QUERY, or pinning is unobservable.
	byFingerprint := func() map[string]map[string]interface{} {
		t.Helper()
		res, err := client.Conn().Request(protocol.VerbIncidents, protocol.SubVerbQuery).
			WithJSON(protocol.IncidentQueryFilter{}).JSON()
		require.NoError(t, err)
		out := map[string]map[string]interface{}{}
		incs, _ := res["incidents"].([]interface{})
		for _, raw := range incs {
			m, ok := raw.(map[string]interface{})
			require.True(t, ok)
			out[m["fingerprint"].(string)] = m
		}
		return out
	}

	view := byFingerprint()
	require.Contains(t, view, keep.Fingerprint)
	assert.Equal(t, true, view[keep.Fingerprint]["pinned"], "pinned flag missing from the per-item view")
	assert.Equal(t, "under investigation", view[keep.Fingerprint]["tag"], "tag missing from the per-item view")
	require.Contains(t, view, drop.Fingerprint)
	assert.Nil(t, view[drop.Fingerprint]["pinned"], "unpinned entry must not report a pin")

	// CLEAR retires the unpinned entry and keeps the pinned one.
	clearRes, err := client.Conn().Request(protocol.VerbIncidents, "CLEAR").JSON()
	require.NoError(t, err)
	assert.Equal(t, float64(1), clearRes["cleared"])
	assert.Equal(t, float64(1), clearRes["kept"])

	view = byFingerprint()
	assert.Contains(t, view, keep.Fingerprint, "pinned incident was cleared")
	assert.NotContains(t, view, drop.Fingerprint, "unpinned incident survived the clear")

	// UNPIN restores ordinary retention, proven by a second clear removing it.
	unpinRes, err := client.Conn().Request(protocol.VerbIncidents, "UNPIN").
		WithJSON(protocol.IncidentPinPayload{Fingerprint: keep.Fingerprint}).JSON()
	require.NoError(t, err)
	assert.Equal(t, false, unpinRes["pinned"])
	assert.Equal(t, float64(0), unpinRes["pinned_count"])

	clearRes, err = client.Conn().Request(protocol.VerbIncidents, "CLEAR").JSON()
	require.NoError(t, err)
	assert.Equal(t, float64(1), clearRes["cleared"])
	assert.Empty(t, byFingerprint(), "unpinned incident survived the clear")
}

// TestHubIncidents_PinFailsLoud — an unpinnable target and a session-less call
// must both report why, never quietly succeed and preserve nothing.
func TestHubIncidents_PinFailsLoud(t *testing.T) {
	t.Parallel()
	d, client, tmpDir := newBootedDaemonWithClient(t)

	_, err := client.Conn().Request(protocol.VerbIncidents, "PIN").
		WithJSON(protocol.IncidentPinPayload{Fingerprint: "fp-x"}).JSON()
	assert.Error(t, err, "INCIDENTS PIN with no session must fail")

	registerIncidentSession(t, d, client, "inc-pin-loud", tmpDir)

	_, err = client.Conn().Request(protocol.VerbIncidents, "PIN").
		WithJSON(protocol.IncidentPinPayload{Fingerprint: "fp-absent"}).JSON()
	assert.Error(t, err, "pinning an absent fingerprint must fail")

	_, err = client.Conn().Request(protocol.VerbIncidents, "PIN").WithJSON(protocol.IncidentPinPayload{}).JSON()
	assert.Error(t, err, "PIN without a fingerprint must fail")

	_, err = client.Conn().Request(protocol.VerbIncidents, "UNPIN").
		WithJSON(protocol.IncidentPinPayload{Fingerprint: "fp-absent"}).JSON()
	assert.Error(t, err, "unpinning an unpinned fingerprint must fail")
}
