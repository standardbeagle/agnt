package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests PROVE the cross-project alert leak that the session-scope
// chokepoint epic (01KSY0GMB56NCN88EEG14QPM3H) closes. They are RED on
// purpose: `hubHandleAlertsQuery` (internal/daemon/hub_alerts.go) never
// reads conn.SessionCode() and never sets AlertStoreFilter.ProjectPath,
// so get_errors (ALERTS QUERY) walks the global alert ring buffer and
// returns every project's alerts to every session.
//
// Expected failure mode until C3/C4 land: the per-session queries below
// see BOTH projects' alerts instead of only their own.

// decodeAlerts re-marshals an ALERTS QUERY response map into typed
// AlertEntry values so assertions can read ProjectPath/Severity directly.
func decodeAlerts(t *testing.T, raw map[string]interface{}) []*AlertEntry {
	t.Helper()
	b, err := json.Marshal(raw)
	require.NoError(t, err)
	var decoded struct {
		Alerts []*AlertEntry `json:"alerts"`
		Count  int           `json:"count"`
	}
	require.NoError(t, json.Unmarshal(b, &decoded))
	return decoded.Alerts
}

// registerSessionClient connects a fresh client on sockPath and binds it
// to a session rooted at projectPath. The returned client's connection
// carries the session code, so subsequent ALERTS QUERY calls are the
// exact surface the scope gate must filter.
func registerSessionClient(t *testing.T, sockPath, code, projectPath string) *Client {
	t.Helper()
	c := NewClient(WithSocketPath(sockPath))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	_, err := c.SessionRegister(code, "/tmp/"+code+".sock", projectPath, "test", nil)
	require.NoError(t, err)
	return c
}

// reportAlert stores one alert tagged with the owning project path via
// the ALERTS REPORT path (which, unlike the scanner ingest path, already
// stamps ProjectPath from the payload).
func reportAlert(t *testing.T, c *Client, projectPath, line, severity string) {
	t.Helper()
	require.NoError(t, c.AlertReport(protocol.AlertReportPayload{
		PatternID:   "test-pattern",
		Severity:    severity,
		Category:    "generic",
		Description: "test alert",
		Line:        line,
		ScriptID:    "proc:" + line,
		ProjectPath: projectPath,
		Timestamp:   time.Now().Format(time.RFC3339),
	}))
}

// TestSessionScope_GetErrorsLeaksAcrossProjects is the core leak proof:
// two sessions on different project roots must each see ONLY their own
// project's alerts through ALERTS QUERY. RED until the chokepoint gate
// scopes the query by the connection's session project path.
func TestSessionScope_GetErrorsLeaksAcrossProjects(t *testing.T) {
	// No t.Parallel(): shares the daemon's global alert ring buffer.
	_, sockPath := newBootedDaemon(t)

	projA := normalizePath(t.TempDir())
	projB := normalizePath(t.TempDir())
	require.NotEqual(t, projA, projB)

	// A neutral reporter seeds the global ring with alerts for both
	// projects. Ownership is carried by the payload ProjectPath.
	reporter := NewClient(WithSocketPath(sockPath))
	require.NoError(t, reporter.Connect())
	t.Cleanup(func() { _ = reporter.Close() })
	reportAlert(t, reporter, projA, "alpha-error-A", "error")
	reportAlert(t, reporter, projA, "beta-error-A", "error")
	reportAlert(t, reporter, projB, "gamma-error-B", "error")

	clientA := registerSessionClient(t, sockPath, "sess-a", projA)
	clientB := registerSessionClient(t, sockPath, "sess-b", projB)

	rawA, err := clientA.AlertQuery(protocol.AlertQueryFilter{})
	require.NoError(t, err)
	alertsA := decodeAlerts(t, rawA)

	rawB, err := clientB.AlertQuery(protocol.AlertQueryFilter{})
	require.NoError(t, err)
	alertsB := decodeAlerts(t, rawB)

	// Session A must see its two alerts and NONE of B's.
	require.Len(t, alertsA, 2, "session A should see exactly its own project's alerts")
	for _, a := range alertsA {
		assert.Equal(t, projA, a.ProjectPath, "session A leaked a foreign-project alert: %q", a.Line)
	}

	// Session B must see its one alert and NONE of A's.
	require.Len(t, alertsB, 1, "session B should see exactly its own project's alerts")
	for _, a := range alertsB {
		assert.Equal(t, projB, a.ProjectPath, "session B leaked a foreign-project alert: %q", a.Line)
	}

	// Invariant: the two scoped views must be disjoint.
	for _, a := range alertsA {
		for _, b := range alertsB {
			assert.NotEqual(t, a.Line, b.Line, "scoped views overlap on %q", a.Line)
		}
	}
}

// TestSessionScope_UnscopedConnectionSeesAllProjects pins the override
// contract (the "global" escape hatch from the epic's Definition of Done
// item 2). A connection with NO bound session — the analog of an explicit
// global override — must see every project's alerts, while a session-bound
// connection must be scoped. The scoped half is RED until the gate exists.
func TestSessionScope_UnscopedConnectionSeesAllProjects(t *testing.T) {
	// No t.Parallel(): shares the daemon's global alert ring buffer.
	_, sockPath := newBootedDaemon(t)

	projA := normalizePath(t.TempDir())
	projB := normalizePath(t.TempDir())

	reporter := NewClient(WithSocketPath(sockPath))
	require.NoError(t, reporter.Connect())
	t.Cleanup(func() { _ = reporter.Close() })
	reportAlert(t, reporter, projA, "err-A", "error")
	reportAlert(t, reporter, projB, "err-B", "error")

	// Unscoped (no SESSION REGISTER) — models the global override.
	global := NewClient(WithSocketPath(sockPath))
	require.NoError(t, global.Connect())
	t.Cleanup(func() { _ = global.Close() })
	rawGlobal, err := global.AlertQuery(protocol.AlertQueryFilter{})
	require.NoError(t, err)
	globalAlerts := decodeAlerts(t, rawGlobal)
	require.Len(t, globalAlerts, 2, "unscoped/global connection must see all projects")

	// Scoped — must be filtered to projB only. RED until the gate lands.
	clientB := registerSessionClient(t, sockPath, "sess-b", projB)
	rawB, err := clientB.AlertQuery(protocol.AlertQueryFilter{})
	require.NoError(t, err)
	scoped := decodeAlerts(t, rawB)
	require.Len(t, scoped, 1, "session-bound connection must be scoped to its own project")
	assert.Equal(t, projB, scoped[0].ProjectPath)
}

// TestSessionScope_NonDebugQueryVerbsRejectCrossProject is the
// table-driven isolation sweep. It varies the alert mix and asserts the
// invariant "a scoped query never returns a foreign-project entry" holds
// across every case. RED until ALERTS QUERY is routed through the gate.
func TestSessionScope_NonDebugQueryVerbsRejectCrossProject(t *testing.T) {
	// No t.Parallel(): shares the daemon's global alert ring buffer.
	_, sockPath := newBootedDaemon(t)

	projOwn := normalizePath(t.TempDir())
	projForeign := normalizePath(t.TempDir())

	reporter := NewClient(WithSocketPath(sockPath))
	require.NoError(t, reporter.Connect())
	t.Cleanup(func() { _ = reporter.Close() })

	owner := registerSessionClient(t, sockPath, "sess-own", projOwn)

	cases := []struct {
		name       string
		ownCount   int
		foreignSev string
		foreign    int
	}{
		{"single-foreign", 1, "error", 1},
		{"foreign-majority", 1, "warning", 4},
		{"foreign-only", 0, "error", 3},
		{"balanced-mixed-severity", 3, "warning", 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, reporter.AlertClear())
			for i := 0; i < tc.ownCount; i++ {
				reportAlert(t, reporter, projOwn, "own-"+tc.name+"-"+itoa(i), "error")
			}
			for i := 0; i < tc.foreign; i++ {
				reportAlert(t, reporter, projForeign, "foreign-"+tc.name+"-"+itoa(i), tc.foreignSev)
			}

			raw, err := owner.AlertQuery(protocol.AlertQueryFilter{})
			require.NoError(t, err)
			got := decodeAlerts(t, raw)

			// Invariant: not a single foreign-project entry may appear.
			for _, a := range got {
				assert.Equal(t, projOwn, a.ProjectPath,
					"foreign-project alert %q leaked into scoped query", a.Line)
			}
			assert.Len(t, got, tc.ownCount,
				"scoped query must return exactly the owner's alerts, got %d", len(got))
		})
	}
}
