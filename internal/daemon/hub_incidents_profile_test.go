package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHubIncidents_ProfileBugMarkReadCoversRenderedSetOnly is the regression
// for the profile/mark-read mismatch: when profile=bug drops rows from the
// rendered page, mark_read must mark exactly the rendered rows — never the
// dropped ones — and the result must raise Truncated. Marking dropped rows
// read sweeps them past the cursor unseen.
func TestHubIncidents_ProfileBugMarkReadCoversRenderedSetOnly(t *testing.T) {
	t.Parallel()
	d, client, tmpDir := newBootedDaemonWithClient(t)

	_, err := client.Conn().Request("SESSION", "REGISTER", "inc-prof", tmpDir).WithJSON(map[string]interface{}{
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

	// Seven incidents: 2 critical, 2 error, 3 info. profile=bug renders the
	// top 5 by severity; the 3 info rows would be dropped had the page been
	// larger — here exactly the 3 info + 0 of the rest are at risk... so use
	// 1 critical + 6 info: rendered = critical + 4 info, dropped = 2 info.
	d.incidentBus.Publish(incident.NewIncidentEvent(incident.SourceProcessCrash, incident.SeverityCritical, "Crash", "c", incident.Context{SessionID: sessionCode}, nil))
	for i := range 6 {
		d.incidentBus.Publish(incident.NewIncidentEvent(incident.SourceProcessAlert, incident.SeverityInfo, "Info", string(rune('a'+i)), incident.Context{SessionID: sessionCode}, nil))
	}
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession(sessionCode, incident.QueryFilter{})
		return len(entries) >= 7
	}, 2*time.Second, 20*time.Millisecond)

	res, err := client.Conn().Request(protocol.VerbIncidents, protocol.SubVerbQuery).WithJSON(protocol.IncidentQueryFilter{
		MarkRead: true,
		Profile:  "bug",
		Limit:    20,
	}).JSON()
	require.NoError(t, err)

	incs, _ := res["incidents"].([]interface{})
	require.LessOrEqual(t, len(incs), 5, "profile=bug renders at most 5 incidents, got %d", len(incs))
	rendered := make(map[string]bool, len(incs))
	for _, inc := range incs {
		rec, _ := inc.(map[string]interface{})
		fp, _ := rec["fingerprint"].(string)
		rendered[fp] = true
	}
	assert.Equal(t, true, res["truncated"], "profile dropped rows: Truncated must be true")

	// The mark-read set must equal the rendered set: the dropped info rows
	// stay unread, and nothing rendered stays unread.
	entries, _ := d.incidentBus.QuerySession(sessionCode, incident.QueryFilter{})
	require.Len(t, entries, 7)
	var readFPS []string
	for _, e := range entries {
		if e.Read {
			readFPS = append(readFPS, e.Fingerprint)
		}
	}
	assert.Len(t, readFPS, len(rendered), "read set size must equal rendered set size")
	for fp := range rendered {
		found := false
		for _, r := range readFPS {
			if r == fp {
				found = true
				break
			}
		}
		assert.True(t, found, "rendered incident %s was not marked read", fp)
	}
}

// TestHubIncidents_ProfileBugCursorKeepsDroppedRowsAhead is the regression for
// the profiled wire cursor: profile=bug sorts by severity, so the rows it
// drops are typically OLDER than the newest rendered row. A cursor set at the
// newest rendered row sweeps those dropped rows behind it and a since=cursor
// follow-up never returns them. The cursor must sit strictly below the oldest
// dropped row, so a follow-up pull (here profile=changed, the unread
// projection — rendered rows were marked read by the first pull) returns
// exactly the dropped fingerprints and nothing already rendered.
func TestHubIncidents_ProfileBugCursorKeepsDroppedRowsAhead(t *testing.T) {
	t.Parallel()
	d, client, tmpDir := newBootedDaemonWithClient(t)

	_, err := client.Conn().Request("SESSION", "REGISTER", "inc-prof-cur", tmpDir).WithJSON(map[string]interface{}{
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

	// Publish the low-severity rows FIRST and the critical LAST: profile=bug
	// renders the critical (the newest row overall) plus 4 info, dropping the
	// 2 oldest info rows. A cursor at the newest rendered row is then newer
	// than every dropped row — the failure this test pins.
	droppedSummaries := []string{"old-a", "old-b", "info-c", "info-d", "info-e", "info-f"}
	for _, s := range droppedSummaries {
		d.incidentBus.Publish(incident.NewIncidentEvent(incident.SourceProcessAlert, incident.SeverityInfo, "Info", s, incident.Context{SessionID: sessionCode}, nil))
	}
	d.incidentBus.Publish(incident.NewIncidentEvent(incident.SourceProcessCrash, incident.SeverityCritical, "Crash", "newest", incident.Context{SessionID: sessionCode}, nil))
	require.Eventually(t, func() bool {
		entries, _ := d.incidentBus.QuerySession(sessionCode, incident.QueryFilter{})
		return len(entries) >= 7
	}, 2*time.Second, 20*time.Millisecond)

	res, err := client.Conn().Request(protocol.VerbIncidents, protocol.SubVerbQuery).WithJSON(protocol.IncidentQueryFilter{
		MarkRead: true,
		Profile:  "bug",
		Limit:    20,
	}).JSON()
	require.NoError(t, err)

	incs, _ := res["incidents"].([]interface{})
	require.LessOrEqual(t, len(incs), 5)
	rendered := make(map[string]bool, len(incs))
	for _, inc := range incs {
		rec, _ := inc.(map[string]interface{})
		fp, _ := rec["fingerprint"].(string)
		rendered[fp] = true
	}
	cursor, _ := res["cursor"].(string)
	require.NotEmpty(t, cursor, "profile dropped rows: a cursor must still be published")

	// Follow-up: everything ahead of the returned cursor that is still unread
	// must be exactly the rows the profile dropped — never a rendered row, and
	// never missing a dropped one.
	res2, err := client.Conn().Request(protocol.VerbIncidents, protocol.SubVerbQuery).WithJSON(protocol.IncidentQueryFilter{
		Since:   cursor,
		Profile: "changed",
		Limit:   20,
	}).JSON()
	require.NoError(t, err)
	incs2, _ := res2["incidents"].([]interface{})
	got := make(map[string]bool, len(incs2))
	for _, inc := range incs2 {
		rec, _ := inc.(map[string]interface{})
		fp, _ := rec["fingerprint"].(string)
		got[fp] = true
	}
	entries, _ := d.incidentBus.QuerySession(sessionCode, incident.QueryFilter{})
	require.Len(t, entries, 7)
	var dropped []string
	for _, e := range entries {
		if !rendered[e.Fingerprint] {
			dropped = append(dropped, e.Fingerprint)
		}
	}
	require.NotEmpty(t, dropped, "fixture must actually drop rows under profile=bug")
	assert.Len(t, got, len(dropped), "since=cursor follow-up must return exactly the dropped rows, got %d want %d", len(got), len(dropped))
	for _, fp := range dropped {
		assert.True(t, got[fp], "dropped row %s is behind the published cursor — a since=cursor pull never surfaces it", fp)
	}
	for fp := range got {
		assert.False(t, rendered[fp], "since=cursor follow-up re-returned an already-rendered row %s", fp)
	}
}
