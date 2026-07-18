package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/alert"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func alertMatch(patternID, category, sevLine string, sev overlay.AlertSeverity, ts time.Time) *overlay.AlertMatch {
	return &overlay.AlertMatch{
		Pattern: &overlay.AlertPattern{
			ID:          patternID,
			Severity:    sev,
			Category:    category,
			Description: patternID,
		},
		Line:      sevLine,
		Timestamp: ts,
	}
}

// TestRetention_BuildSuccessRetiresStaleErrors is the end-to-end shape of
// trigger 1: errors before a rebuild-build-success signal are retired from
// the alert store AND the incident inbox; errors after it survive.
func TestRetention_BuildSuccessRetiresStaleErrors(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	d.addIncidentSession("sess-ret")

	base := time.Now()
	stale := alertMatch("go-panic", "go", "panic: stale error", overlay.AlertSeverityError, base.Add(-time.Minute))
	stale.ScriptID = "web"
	d.ingestProcessAlert(stale, base)

	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession("sess-ret", incident.QueryFilter{})
		return len(entries) > 0
	}, 3*time.Second, 20*time.Millisecond, "stale error never reached inbox")
	require.NotZero(t, d.alertStore.Len())

	success := alertMatch("rebuild-build-success", "rebuild", "Build succeeded.", overlay.AlertSeverityInfo, base)
	success.ScriptID = "web"
	d.ingestProcessAlert(success, base)

	fresh := alertMatch("go-panic", "go", "panic: fresh error after build", overlay.AlertSeverityError, base.Add(time.Second))
	fresh.ScriptID = "web"
	d.ingestProcessAlert(fresh, base.Add(time.Second))

	// Alert store: the stale panic is gone, the success signal itself and the
	// fresh panic remain (both stamped at/after the boundary).
	require.Eventually(t, func() bool {
		var hasStale, hasFresh bool
		for _, e := range d.alertStore.Query(AlertStoreFilter{ProcessID: "web"}) {
			switch e.Line {
			case "panic: stale error":
				hasStale = true
			case "panic: fresh error after build":
				hasFresh = true
			}
		}
		return !hasStale && hasFresh
	}, 3*time.Second, 20*time.Millisecond, "alert store retention mismatch")

	// Inbox: stale gone, fresh present.
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession("sess-ret", incident.QueryFilter{})
		var hasStale, hasFresh bool
		for _, e := range entries {
			if e.Sample == nil {
				continue
			}
			switch {
			case e.Sample.Summary == "panic: stale error":
				hasStale = true
			case e.Sample.Summary == "panic: fresh error after build":
				hasFresh = true
			}
		}
		return !hasStale && hasFresh
	}, 3*time.Second, 20*time.Millisecond, "inbox retention mismatch")
}

// TestRetention_ConfigGateDisablesBuildSuccessClear pins Config Authority:
// retention { on-build-success false } must actually stop the clear.
func TestRetention_ConfigGateDisablesBuildSuccessClear(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	off := false
	d.SetRetentionConfig(&config.RetentionConfig{OnBuildSuccess: &off})

	base := time.Now()
	stale := alertMatch("go-panic", "go", "panic: stale error", overlay.AlertSeverityError, base.Add(-time.Minute))
	stale.ScriptID = "web"
	d.ingestProcessAlert(stale, base)

	success := alertMatch("rebuild-build-success", "rebuild", "Build succeeded.", overlay.AlertSeverityInfo, base)
	success.ScriptID = "web"
	d.ingestProcessAlert(success, base)

	var found bool
	for _, e := range d.alertStore.Query(AlertStoreFilter{ProcessID: "web"}) {
		if e.Line == "panic: stale error" {
			found = true
		}
	}
	assert.True(t, found, "gate off: stale error must survive build success")

	// Other triggers stay independently gated (default on).
	assert.NotZero(t, d.maybeRetireOnProcStop("web"))
}

// TestRetention_PinnedSurvivesEveryTrigger: a pinned copy outlives build
// success, proc-stop, and session-end clears.
func TestRetention_PinnedSurvivesEveryTrigger(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})

	entry := &AlertEntry{
		Severity: "error", Category: "go", Line: "panic: keep me",
		ScriptID: "web", ProjectPath: "/proj", Timestamp: time.Now().Add(-time.Minute),
	}
	d.alertStore.Add(entry)
	require.NotEmpty(t, entry.ID)

	pin, found := d.findErrorByID(entry.ID, "/proj", "", "")
	require.True(t, found, "stamped id must be addressable")
	pin.Tag = "investigating"
	pin.ProjectPath = "/proj"
	require.NoError(t, d.pinnedStore.Pin(pin))

	d.retireProcessErrors("web", time.Now(), "build-success")
	d.maybeRetireOnProcStop("web")
	d.maybeRetireOnSessionEnd("/proj")

	assert.Zero(t, d.alertStore.Len(), "ring entries cleared")
	pins := d.pinnedStore.List("/proj", false)
	require.Len(t, pins, 1, "pin survives all triggers")
	assert.Equal(t, "panic: keep me", pins[0].Message)
	assert.Equal(t, "investigating", pins[0].Tag)
}

// TestRetention_SessionEndClearsOnlyOwnProject: trigger 3 is project-scoped.
func TestRetention_SessionEndClearsOnlyOwnProject(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	now := time.Now()
	d.alertStore.Add(&AlertEntry{Line: "own", ScriptID: "a", ProjectPath: "/own", Severity: "error", Timestamp: now})
	d.alertStore.Add(&AlertEntry{Line: "other", ScriptID: "b", ProjectPath: "/other", Severity: "error", Timestamp: now})

	d.maybeRetireOnSessionEnd("/own")

	left := d.alertStore.Query(AlertStoreFilter{})
	require.Len(t, left, 1)
	assert.Equal(t, "/other", left[0].ProjectPath)
}

// TestFindErrorByID_IncidentFingerprint pins the inbox half of pin lookup.
func TestFindErrorByID_IncidentFingerprint(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	d.addIncidentSession("sess-pin")

	ev := incident.NewIncidentEvent(incident.SourceBrowserJS, incident.SeverityError,
		"TypeError", "cannot read properties of undefined", incident.Context{URL: "http://localhost/x"}, nil)
	d.incidentBus.Fire(&ev)

	require.Eventually(t, func() bool {
		return d.incidentBus.FindFingerprintSession("sess-pin", ev.Fingerprint) != nil
	}, 3*time.Second, 20*time.Millisecond)

	pin, found := d.findErrorByID(ev.Fingerprint, "/proj", "sess-pin", "")
	require.True(t, found)
	assert.Equal(t, ev.Fingerprint, pin.ID)
	assert.Equal(t, string(incident.SourceBrowserJS), pin.Source)
	assert.Equal(t, "http://localhost/x", pin.Page)

	_, found = d.findErrorByID("nope1234", "/proj", "sess-pin", "")
	assert.False(t, found, "unknown id fails loud, not a silent pin of nothing")

	// PinnedError shape sanity: severity strings match the store scale.
	assert.Contains(t, []string{"critical", "error", "warning", "info"}, pin.Severity)
	_ = alert.PinnedError{}
}
