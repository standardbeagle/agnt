package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inboxPage mimics Inbox.Query's contract: the OLDEST matching entries, newest
// first. ids are given oldest-first.
func inboxPage(base time.Time, ids ...string) []incident.InboxEntry {
	entries := make([]incident.InboxEntry, 0, len(ids))
	for i := len(ids) - 1; i >= 0; i-- {
		entries = append(entries, incident.InboxEntry{
			Fingerprint: ids[i],
			Severity:    incident.SeverityError,
			FirstSeenAt: base,
			LastSeenAt:  base.Add(time.Duration(i+1) * time.Minute),
			Count:       1,
			Sample:      &incident.IncidentEvent{Fingerprint: ids[i], Source: incident.SourceProcessAlert},
		})
	}
	return entries
}

func fingerprints(res protocol.IncidentQueryResult) []string {
	out := make([]string, 0, len(res.Incidents))
	for _, r := range res.Incidents {
		out = append(out, r.Fingerprint)
	}
	return out
}

func requireCursor(t *testing.T, cursor string) (time.Time, string) {
	t.Helper()
	at, fingerprint, hasFingerprint, err := decodeIncidentCursor(cursor)
	require.NoError(t, err)
	require.True(t, hasFingerprint)
	return at, fingerprint
}

// TestIncidentPaging_KeepsOldestPage is the regression for a silently lost
// incident: the hub over-fetches by one and used to keep the newest Limit
// records, dropping the oldest and then publishing a cursor above it. With a
// forward-only `since` cursor that entry could never be returned again.
func TestIncidentPaging_KeepsOldestPage(t *testing.T) {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	// Limit=3 → the inbox is asked for 4 and returns the 4 oldest.
	entries := inboxPage(base, "A1", "A2", "A3", "A4")

	res := buildIncidentQueryResult(entries, incident.Stats{}, protocol.IncidentQueryFilter{Limit: 3})

	require.Len(t, res.Incidents, 3)
	assert.Equal(t, []string{"A3", "A2", "A1"}, fingerprints(res), "oldest page, newest-first")
	assert.True(t, res.Truncated)
	cursorAt, cursorFP := requireCursor(t, res.Cursor)
	assert.True(t, cursorAt.Equal(base.Add(3*time.Minute)), "cursor = newest entry examined")
	assert.Equal(t, "A3", cursorFP)

	// The cursor must not skip A4: a `since=cursor` pull still sees it.
	assert.True(t, entries[0].LastSeenAt.After(base.Add(3*time.Minute)),
		"A4 stays strictly newer than the published cursor")
}

// TestIncidentPaging_CursorSweepsGapFree drains a backlog page by page and
// asserts every incident is seen exactly once.
func TestIncidentPaging_CursorSweepsGapFree(t *testing.T) {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	all := inboxPage(base, "A1", "A2", "A3", "A4", "A5", "A6", "A7")

	var seen []string
	since := time.Time{}
	for range 5 { // bounded: 7 entries at Limit=3 needs 3 pulls
		// Emulate Inbox.Query: matching = LastSeenAt strictly after `since`,
		// newest-first, truncated to the OLDEST Limit+1.
		var matching []incident.InboxEntry
		for _, e := range all {
			if since.IsZero() || e.LastSeenAt.After(since) {
				matching = append(matching, e)
			}
		}
		if len(matching) == 0 {
			break
		}
		if len(matching) > 4 {
			matching = matching[len(matching)-4:]
		}

		res := buildIncidentQueryResult(matching, incident.Stats{}, protocol.IncidentQueryFilter{Limit: 3})
		seen = append(seen, fingerprints(res)...)
		require.NotEmpty(t, res.Cursor, "a non-empty page must publish a cursor")
		since, _ = requireCursor(t, res.Cursor)
	}

	assert.ElementsMatch(t, []string{"A1", "A2", "A3", "A4", "A5", "A6", "A7"}, seen,
		"every incident surfaces exactly once across the sweep")
	assert.Len(t, seen, 7, "no duplicates")
}

func TestIncidentPaging_SameSecondNanosecondsAdvanceCursor(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	all := make([]incident.InboxEntry, 7)
	for i := range all {
		at := base.Add(time.Duration(7-i) * time.Nanosecond)
		all[i] = incident.InboxEntry{Fingerprint: string(rune('A' + i)), Severity: incident.SeverityError, FirstSeenAt: at, LastSeenAt: at, Count: 1}
	}

	var seen []string
	var since time.Time
	for range 5 {
		var matching []incident.InboxEntry
		for _, entry := range all {
			if since.IsZero() || entry.LastSeenAt.After(since) {
				matching = append(matching, entry)
			}
		}
		if len(matching) == 0 {
			break
		}
		if len(matching) > 4 {
			matching = matching[len(matching)-4:]
		}
		result := buildIncidentQueryResult(matching, incident.Stats{}, protocol.IncidentQueryFilter{Limit: 3})
		seen = append(seen, fingerprints(result)...)
		parsed, _ := requireCursor(t, result.Cursor)
		require.True(t, parsed.After(since), "cursor must advance at nanosecond precision")
		since = parsed
	}
	require.Len(t, seen, 7)
}

func TestIncidentPaging_ExactTimestampTieBreakDrainsWithoutLoss(t *testing.T) {
	inbox := incident.NewInbox("ties")
	at := time.Now().UTC().Truncate(time.Second)
	for _, fingerprint := range []string{"A", "B", "C", "D", "E", "F", "G"} {
		inbox.Ingest(&incident.InboxEntry{
			Fingerprint: fingerprint,
			Severity:    incident.SeverityError,
			FirstSeenAt: at,
			LastSeenAt:  at,
			Count:       1,
		})
	}

	filter := protocol.IncidentQueryFilter{Limit: 3}
	seen := make(map[string]int)
	for page := 0; page < 4; page++ {
		query, err := incidentQueryFilterToInternal(filter)
		require.NoError(t, err)
		entries, stats := inbox.Query(query)
		result := buildIncidentQueryResult(entries, stats, filter)
		if page == 3 {
			require.Empty(t, result.Incidents, "terminal page must be empty")
			require.Empty(t, result.Cursor)
			break
		}
		require.NotEmpty(t, result.Incidents)
		for _, record := range result.Incidents {
			seen[record.Fingerprint]++
		}
		filter.Since = result.Cursor
	}
	require.Len(t, seen, 7)
	for fingerprint, count := range seen {
		require.Equal(t, 1, count, "fingerprint %s duplicated", fingerprint)
	}
}

// TestIncidentPaging_TruncatedSurvivesSecondaryFilter: the secondary filters run
// after truncation, so a filter that drops the surplus cannot mask the fact that
// the inbox held more matching entries.
func TestIncidentPaging_TruncatedSurvivesSecondaryFilter(t *testing.T) {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	entries := inboxPage(base, "A1", "A2", "A3", "A4")

	res := buildIncidentQueryResult(entries, incident.Stats{}, protocol.IncidentQueryFilter{
		Limit:        3,
		Fingerprints: []string{"A2"},
	})

	assert.Equal(t, []string{"A2"}, fingerprints(res))
	assert.True(t, res.Truncated, "inbox had more matching entries than the page")
	cursorAt, _ := requireCursor(t, res.Cursor)
	assert.True(t, cursorAt.Equal(base.Add(3*time.Minute)), "cursor advances past filtered-out entries")
}

// TestIncidentPaging_FilteredEmptyPageStillAdvances: when every examined entry
// is rejected by a secondary filter, the cursor must still advance or the caller
// re-polls the same page forever.
func TestIncidentPaging_FilteredEmptyPageStillAdvances(t *testing.T) {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	entries := inboxPage(base, "A1", "A2")

	res := buildIncidentQueryResult(entries, incident.Stats{}, protocol.IncidentQueryFilter{
		Limit:        3,
		Fingerprints: []string{"nope"},
	})

	assert.Empty(t, res.Incidents)
	assert.False(t, res.Truncated)
	cursorAt, _ := requireCursor(t, res.Cursor)
	assert.True(t, cursorAt.Equal(base.Add(2*time.Minute)))
}

func TestIncidentPaging_UnboundedLimitReturnsAll(t *testing.T) {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	res := buildIncidentQueryResult(inboxPage(base, "A1", "A2"), incident.Stats{}, protocol.IncidentQueryFilter{})
	assert.Equal(t, []string{"A2", "A1"}, fingerprints(res))
	assert.False(t, res.Truncated)
}

func TestIncidentPaging_EmptyInboxHasNoCursor(t *testing.T) {
	res := buildIncidentQueryResult(nil, incident.Stats{}, protocol.IncidentQueryFilter{Limit: 3})
	assert.Empty(t, res.Incidents)
	assert.Empty(t, res.Cursor)
	assert.False(t, res.Truncated)
}

func TestApplySecondaryFilters_NilSampleDoesNotBypassMetadata(t *testing.T) {
	entries := []incident.InboxEntry{{Fingerprint: "nil-sample", Severity: incident.SeverityError}}
	for _, filter := range []protocol.IncidentQueryFilter{
		{Sources: []string{string(incident.SourceBrowserJS)}},
		{ProxyID: "proxy"},
		{ProcessID: "process"},
	} {
		assert.Empty(t, applySecondaryFilters(entries, filter))
	}
	assert.Len(t, applySecondaryFilters(entries, protocol.IncidentQueryFilter{Fingerprints: []string{"nil-sample"}}), 1)
}

// TestIncidentQueryFilter_RejectsUnparseableSince: silently dropping `since`
// would return the whole inbox to a caller asking for a slice of it.
func TestIncidentQueryFilter_RejectsUnparseableSince(t *testing.T) {
	_, err := incidentQueryFilterToInternal(protocol.IncidentQueryFilter{Since: "5m ago"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RFC3339")

	ts := time.Now().UTC().Truncate(time.Second)
	qf, err := incidentQueryFilterToInternal(protocol.IncidentQueryFilter{Since: ts.Format(time.RFC3339), Limit: 3})
	require.NoError(t, err)
	assert.True(t, qf.Since.Equal(ts))
	assert.Equal(t, 4, qf.Limit, "over-fetch by one to detect truncation")

	qf, err = incidentQueryFilterToInternal(protocol.IncidentQueryFilter{})
	require.NoError(t, err)
	assert.Zero(t, qf.Limit, "unbounded stays unbounded")
	assert.True(t, qf.Since.IsZero())
}
