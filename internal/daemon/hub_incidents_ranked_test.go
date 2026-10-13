package daemon

import (
	"fmt"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// backlogInbox fills a real inbox with `stale` old warnings from process "web"
// followed by one fresh error from process "api" — the shape of an inbox an
// agent is pinged about.
func backlogInbox(t *testing.T, stale int) *incident.Inbox {
	t.Helper()
	inbox := incident.NewInbox("ranked")
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	ingest := func(fp string, sev incident.Severity, proc string, at time.Time) {
		inbox.Ingest(&incident.InboxEntry{
			Fingerprint: fp, Severity: sev, FirstSeenAt: at, LastSeenAt: at, Count: 1,
			Sample: &incident.IncidentEvent{Fingerprint: fp, Severity: sev, Source: incident.SourceProcessAlert,
				Ctx: incident.Context{ProcessID: proc}},
		})
	}
	for i := 0; i < stale; i++ {
		ingest(fmt.Sprintf("warn-%02d", i), incident.SeverityWarning, "web", base.Add(time.Duration(i)*time.Second))
	}
	ingest("fresh-error", incident.SeverityError, "api", base.Add(30*time.Minute))
	return inbox
}

func pullIncidents(t *testing.T, inbox *incident.Inbox, filter protocol.IncidentQueryFilter) protocol.IncidentQueryResult {
	t.Helper()
	qf, err := incidentQueryFilterToInternal(filter)
	require.NoError(t, err)
	entries, stats := inbox.Query(qf)
	return buildIncidentQueryResult(entries, stats, filter)
}

// The call an agent makes when pinged is cursor-less. It used to page the
// OLDEST `limit` entries and rank only those, so the error it was pinged about
// was missing as soon as `limit` older entries existed.
func TestIncidentRanked_DefaultPullSurfacesTheNewestSevereEntry(t *testing.T) {
	inbox := backlogInbox(t, 40)

	res := pullIncidents(t, inbox, protocol.IncidentQueryFilter{Limit: 20, Profile: "bug"})

	require.Len(t, res.Incidents, 5, "profile=bug renders the top 5")
	assert.Equal(t, "fresh-error", res.Incidents[0].Fingerprint, "most severe entry first, even though it is the newest of 41")
	// Within a severity, newest first.
	assert.Equal(t, []string{"warn-39", "warn-38", "warn-37", "warn-36"}, fingerprints(res)[1:])
	assert.True(t, res.Truncated, "36 matching rows were not rendered")
	require.NotEmpty(t, res.Cursor)
	cursorAt, _, _, err := decodeIncidentCursor(res.Cursor)
	require.NoError(t, err)
	entries, _ := inbox.Query(incident.QueryFilter{Since: cursorAt, SinceFingerprint: "", HasSinceFingerprint: true})
	assert.Len(t, entries, 41, "every unrendered row stays ahead of the published cursor")
}

// Secondary filters run across the whole inbox on a cursor-less triage pull;
// applied after the oldest-page cut they could not see anything newer.
func TestIncidentRanked_ProcessFilterFindsEntriesNewerThanTheOldestPage(t *testing.T) {
	inbox := backlogInbox(t, 40)

	for _, profile := range []string{"bug", "release", "changed"} {
		res := pullIncidents(t, inbox, protocol.IncidentQueryFilter{Limit: 20, Profile: profile, ProcessID: "api"})
		assert.Equal(t, []string{"fresh-error"}, fingerprints(res), "profile %s", profile)
		assert.False(t, res.Truncated, "profile %s: the one matching row was rendered", profile)
	}

	// release renders everything the limit allows, most severe first.
	res := pullIncidents(t, inbox, protocol.IncidentQueryFilter{Limit: 20, Profile: "release"})
	require.Len(t, res.Incidents, 20)
	assert.Equal(t, "fresh-error", res.Incidents[0].Fingerprint)
	assert.True(t, res.Truncated)
}

// The legacy identity profile and any pull carrying a cursor keep the
// oldest-page sweep semantics exactly.
func TestIncidentRanked_LegacyProfileAndCursorPullsStillPage(t *testing.T) {
	inbox := backlogInbox(t, 40)

	full := pullIncidents(t, inbox, protocol.IncidentQueryFilter{Limit: 3, Profile: "full"})
	assert.Equal(t, []string{"warn-02", "warn-01", "warn-00"}, fingerprints(full), "legacy profile pages oldest-first")

	swept := pullIncidents(t, inbox, protocol.IncidentQueryFilter{Limit: 3, Profile: "release", Since: full.Cursor})
	assert.Equal(t, []string{"warn-05", "warn-04", "warn-03"}, fingerprints(swept), "a cursor pull continues the sweep")

	assert.False(t, rankedIncidentPull(protocol.IncidentQueryFilter{Profile: "full"}))
	assert.False(t, rankedIncidentPull(protocol.IncidentQueryFilter{}))
	assert.False(t, rankedIncidentPull(protocol.IncidentQueryFilter{Profile: "bug", Since: full.Cursor}))
	assert.True(t, rankedIncidentPull(protocol.IncidentQueryFilter{Profile: "bug"}))
}
