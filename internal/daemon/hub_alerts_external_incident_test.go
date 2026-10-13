package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/require"
)

// error_queue promises "the entry appears in get_incidents". ALERTS REPORT only
// wrote the alert ring, which the incident inbox never reads, so an externally
// reported failure was invisible on the only incident pull surface. This drives
// the real hub: REPORT on one connection, INCIDENTS QUERY on the sessions'.
func TestAlertsReport_ExternalReachesProjectSessionInboxes(t *testing.T) {
	// No t.Parallel(): shares the daemon's global alert ring buffer.
	_, sockPath := newBootedDaemon(t)

	projA := normalizePath(t.TempDir())
	projB := normalizePath(t.TempDir())
	a1 := registerSessionClient(t, sockPath, "ext-a1", projA)
	a2 := registerSessionClient(t, sockPath, "ext-a2", projA)
	b := registerSessionClient(t, sockPath, "ext-b", projB)

	report := func(patternID, line string) {
		t.Helper()
		require.NoError(t, a1.AlertReport(protocol.AlertReportPayload{
			PatternID:   patternID,
			Severity:    "error",
			Category:    "ci",
			Description: "external error",
			Line:        line,
			ScriptID:    "github-actions",
			ProjectPath: projA,
			Timestamp:   time.Now().Format(time.RFC3339),
		}))
	}
	report("external.github-actions", "GitHub Actions build failed: go test ./...")
	// A PTY-scanned (non-external) report must NOT be published from here: its
	// signal already reaches the bus from its own daemon-side source.
	report("go-build-fail", "build failed")

	query := func(c interface {
		IncidentQuery(protocol.IncidentQueryFilter) (*protocol.IncidentQueryResult, error)
	}) []protocol.IncidentRecord {
		t.Helper()
		res, err := c.IncidentQuery(protocol.IncidentQueryFilter{Profile: "full", Limit: 50})
		require.NoError(t, err)
		return res.Incidents
	}

	for name, c := range map[string]interface {
		IncidentQuery(protocol.IncidentQueryFilter) (*protocol.IncidentQueryResult, error)
	}{"ext-a1": a1, "ext-a2": a2} {
		var got []protocol.IncidentRecord
		require.Eventually(t, func() bool {
			got = query(c)
			return len(got) == 1
		}, 5*time.Second, 10*time.Millisecond, "session %s never received the external incident (have %d)", name, len(got))
		require.Equal(t, "external", got[0].Source)
		require.Equal(t, "error", got[0].Severity)
		require.Contains(t, got[0].Summary, "GitHub Actions build failed")
	}

	// Bus delivery is FIFO on one goroutine, so once project A's inboxes hold
	// the incident, anything misrouted to B would already have landed.
	require.Empty(t, query(b), "external incident leaked into another project's inbox")
}
