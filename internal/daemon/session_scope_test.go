package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests PROVE the cross-project alert leak that the session-scope
// chokepoint epic (01KSY0GMB56NCN88EEG14QPM3H) closes. They are RED on
// purpose: `hubHandleAlertsQuery` (internal/daemon/hub_alerts.go) never
// reads conn.SessionCode() and never sets AlertStoreFilter.ProjectPath,
// so an unscoped ALERTS QUERY walks the global alert ring buffer and
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
func registerSessionClient(t *testing.T, sockPath, code, projectPath string) *daemonclient.Client {
	t.Helper()
	c := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	_, err := c.SessionRegister(code, "/tmp/"+code+".sock", projectPath, "test", nil)
	require.NoError(t, err)
	return c
}

// reportAlert stores one alert tagged with the owning project path via
// the ALERTS REPORT path (which, unlike the scanner ingest path, already
// stamps ProjectPath from the payload).
func reportAlert(t *testing.T, c *daemonclient.Client, projectPath, line, severity string) {
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

// TestSessionScope_AlertQueryLeaksAcrossProjects is the core leak proof:
// two sessions on different project roots must each see ONLY their own
// project's alerts through ALERTS QUERY. RED until the chokepoint gate
// scopes the query by the connection's session project path.
func TestSessionScope_AlertQueryLeaksAcrossProjects(t *testing.T) {
	// No t.Parallel(): shares the daemon's global alert ring buffer.
	_, sockPath := newBootedDaemon(t)

	projA := normalizePath(t.TempDir())
	projB := normalizePath(t.TempDir())
	require.NotEqual(t, projA, projB)

	// A neutral reporter seeds the global ring with alerts for both
	// projects. Ownership is carried by the payload ProjectPath.
	reporter := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
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

// TestSessionScope_GlobalOverrideAndSessionlessRejected pins the override
// contract (epic Definition of Done item 2). An explicit global:true query
// returns every project's alerts. A non-global query on a session-less
// connection is rejected fail-loud (mirrors INCIDENTS QUERY "no session
// attached") — session-less is NOT an implicit global. A session-bound
// connection is scoped to its own project but may still opt into global.
func TestSessionScope_GlobalOverrideAndSessionlessRejected(t *testing.T) {
	// No t.Parallel(): shares the daemon's global alert ring buffer.
	_, sockPath := newBootedDaemon(t)

	projA := normalizePath(t.TempDir())
	projB := normalizePath(t.TempDir())

	reporter := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
	require.NoError(t, reporter.Connect())
	t.Cleanup(func() { _ = reporter.Close() })
	reportAlert(t, reporter, projA, "err-A", "error")
	reportAlert(t, reporter, projB, "err-B", "error")

	// Session-less + non-global → rejected fail-loud (no implicit global).
	anon := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
	require.NoError(t, anon.Connect())
	t.Cleanup(func() { _ = anon.Close() })
	_, err := anon.AlertQuery(protocol.AlertQueryFilter{})
	require.Error(t, err, "session-less non-global query must be rejected, not leak all projects")

	// Explicit global override → sees all projects, even session-less.
	rawGlobal, err := anon.AlertQuery(protocol.AlertQueryFilter{Global: protocol.Bool(true)})
	require.NoError(t, err)
	require.Len(t, decodeAlerts(t, rawGlobal), 2, "global override must see all projects")

	// Session-bound connection is scoped to its own project by default...
	clientB := registerSessionClient(t, sockPath, "sess-b", projB)
	rawB, err := clientB.AlertQuery(protocol.AlertQueryFilter{})
	require.NoError(t, err)
	scoped := decodeAlerts(t, rawB)
	require.Len(t, scoped, 1, "session-bound connection must be scoped to its own project")
	assert.Equal(t, projB, scoped[0].ProjectPath)

	// ...but may opt into the global override on demand.
	rawBGlobal, err := clientB.AlertQuery(protocol.AlertQueryFilter{Global: protocol.Bool(true)})
	require.NoError(t, err)
	assert.Len(t, decodeAlerts(t, rawBGlobal), 2, "a session may opt into the global override")
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

	reporter := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
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
			_, err := reporter.AlertClear(protocol.AlertClearFilter{Global: protocol.Bool(true)})
			require.NoError(t, err)
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

// TestSessionScope_AllGatedVerbsUniformContract proves the chokepoint is
// STRUCTURAL, not a per-handler convention: every enumerated non-debug
// list/query verb routes through the single resolveProjectScope gate, so
// they all share one contract — a session-less non-global call is rejected
// fail-loud, and an explicit global:true override is honored. This is the
// cross-verb generalization of the ALERTS QUERY tests above; adding a new
// gated verb without wiring it to the gate makes its row here go RED.
func TestSessionScope_AllGatedVerbsUniformContract(t *testing.T) {
	// No t.Parallel(): boots a daemon and shares its registries.
	_, sockPath := newBootedDaemon(t)

	anon := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
	require.NoError(t, anon.Connect())
	t.Cleanup(func() { _ = anon.Close() })

	verbs := []struct {
		name string
		call func(global bool) error
	}{
		{"ALERTS_QUERY", func(g bool) error {
			_, e := anon.AlertQuery(protocol.AlertQueryFilter{Global: protocol.Bool(g)})
			return e
		}},
		{"PROC_LIST", func(g bool) error { _, e := anon.ProcList(protocol.DirectoryFilter{Global: g}); return e }},
		{"PROXY_LIST", func(g bool) error { _, e := anon.ProxyList(protocol.DirectoryFilter{Global: g}); return e }},
		{"TUNNEL_LIST", func(g bool) error { _, e := anon.TunnelList(protocol.DirectoryFilter{Global: g}); return e }},
		{"SESSION_LIST", func(g bool) error { _, e := anon.SessionList(protocol.DirectoryFilter{Global: g}); return e }},
		{"SESSION_TASKS", func(g bool) error { _, e := anon.SessionTasks(protocol.DirectoryFilter{Global: g}); return e }},
		{"ALERTS_STARTUP_LOG", func(g bool) error { _, e := anon.StartupLog(50, protocol.DirectoryFilter{Global: g}); return e }},
	}

	for _, v := range verbs {
		t.Run(v.name, func(t *testing.T) {
			// Session-less + non-global → rejected fail-loud.
			err := v.call(false)
			require.Error(t, err, "%s must reject a session-less non-global call", v.name)
			assert.Contains(t, err.Error(), "no session attached",
				"%s rejection should carry the canonical scope error", v.name)

			// Explicit global override → accepted (no project required).
			require.NoError(t, v.call(true), "%s must honor the global override", v.name)
		})
	}
}
