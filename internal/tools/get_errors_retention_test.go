package tools

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/alert"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnifiedIDParity_StoreVsConverter pins the daemon-side AlertEntry.
// UnifiedID recipe to the converter's local fallback: both must yield the
// same id for the same entry, or pin-by-id would dangle on version skew.
func TestUnifiedIDParity_StoreVsConverter(t *testing.T) {
	cases := []alert.AlertEntry{
		{ScriptID: "web", Category: "go", Description: "Go panic", Line: "panic: boom", Severity: "error"},
		{ScriptID: "web", Category: "", Description: "mystery", Line: "", Severity: "error"},
		{ScriptID: "api", Category: "process_lifecycle", Description: "process api exited (code 1)", Line: "panic: db down", Severity: "error"},
		{ScriptID: "api", Category: "process_lifecycle", Description: "process api exited (code 0)", Line: "", Severity: "info"},
	}
	for _, e := range cases {
		e.Timestamp = time.Now()
		// Marshal WITHOUT the id (simulating an old daemon) so the converter
		// exercises its local recipe.
		b, err := json.Marshal(e)
		require.NoError(t, err)
		var am map[string]interface{}
		require.NoError(t, json.Unmarshal(b, &am))
		delete(am, "id")

		ue := alertMapToUnifiedError(am)
		require.NotNil(t, ue)
		assert.Equal(t, e.UnifiedID(), ue.ID, "store and converter ids diverge for %+v", e)
	}
}

func TestConverter_PrefersDaemonStampedID(t *testing.T) {
	am := map[string]interface{}{"id": "deadbeef", "script_id": "web", "category": "go", "line": "boom", "severity": "error"}
	ue := alertMapToUnifiedError(am)
	require.NotNil(t, ue)
	assert.Equal(t, "deadbeef", ue.ID)
}

func TestMergePinned(t *testing.T) {
	live := []unifiedError{
		{ID: "aaa", Severity: "error", Message: "live and pinned", Count: 3},
		{ID: "bbb", Severity: "error", Message: "live only"},
	}
	pinned := []unifiedError{
		{ID: "aaa", Pinned: true, Tag: "keep", Message: "stale saved copy"},
		{ID: "ccc", Pinned: true, Tag: "gone-but-saved", Severity: "error", Message: "cleared long ago"},
	}
	merged := mergePinned(live, pinned)
	require.Len(t, merged, 3)

	byID := map[string]unifiedError{}
	for _, e := range merged {
		byID[e.ID] = e
	}
	assert.True(t, byID["aaa"].Pinned, "live entry stamped pinned")
	assert.Equal(t, "keep", byID["aaa"].Tag)
	assert.Equal(t, "live and pinned", byID["aaa"].Message, "fresh live data wins over saved copy")
	assert.False(t, byID["bbb"].Pinned)
	assert.Equal(t, "cleared long ago", byID["ccc"].Message, "pin with no live counterpart appended")

	assert.Equal(t, live[:2], mergePinned(live[:2], nil), "nil pins pass through")
}

// TestFormatErrorsOutput_PinnedExemptions: pins survive the warnings filter
// and the display limit, and the compact output labels them.
func TestFormatErrorsOutput_PinnedExemptions(t *testing.T) {
	now := time.Now()
	all := []unifiedError{
		{ID: "w1", Severity: "warning", Pinned: true, Tag: "flaky-db", Message: "pinned warning", Category: "GO", Source: "process:web", Count: 1, LastSeen: now},
		{ID: "e1", Severity: "error", Message: "err one", Category: "GO", Source: "process:web", Count: 1, LastSeen: now},
		{ID: "e2", Severity: "error", Message: "err two", Category: "GO", Source: "process:web", Count: 1, LastSeen: now.Add(-time.Second)},
	}

	// include_warnings=false must still show the pinned warning.
	_, out := formatErrorsOutput(all, false, 25, false)
	assert.Contains(t, out.Summary, "pinned warning")
	assert.Contains(t, out.Summary, "[pinned: flaky-db]")

	// limit=1 keeps the pin plus one unpinned entry.
	_, out = formatErrorsOutput(all, true, 1, false)
	assert.Contains(t, out.Summary, "pinned warning")
	assert.Contains(t, out.Summary, "err one")
	assert.NotContains(t, out.Summary, "err two")
}

func TestCollectPinnedErrors_ParsesQueryResponse(t *testing.T) {
	raw := map[string]interface{}{
		"pinned": []interface{}{
			map[string]interface{}{
				"id": "abc12345", "source": "process:web", "severity": "critical",
				"category": "go", "message": "saved", "tag": "note",
				"first_seen": now3339(t),
			},
		},
	}
	// Exercise the parsing path directly (no live daemon in unit tests).
	entries := parsePinnedFromQueryResult(raw)
	require.Len(t, entries, 1)
	e := entries[0]
	assert.True(t, e.Pinned)
	assert.Equal(t, "abc12345", e.ID)
	assert.Equal(t, "error", e.Severity, "critical folds to error")
	assert.Equal(t, "note", e.Tag)
	assert.False(t, e.LastSeen.IsZero())
}

func now3339(t *testing.T) string {
	t.Helper()
	return time.Now().Format(time.RFC3339)
}
