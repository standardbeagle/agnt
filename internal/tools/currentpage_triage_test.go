package tools

// Coverage for the triage view: top-N rollups, newest-first last actions,
// failed-resource surfacing, and the after-last-action correlation that answers
// "the thing I just clicked isn't working — what happened right after?".

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ts(sec string) string { return "2026-06-26T10:00:" + sec + "Z" }

func triageWireSession() map[string]interface{} {
	return map[string]interface{}{
		"id":                "page-1",
		"url":               "/checkout",
		"page_title":        "Checkout",
		"active":            true,
		"load_time_ms":      float64(2200),
		"error_count":       float64(3),
		"interaction_count": float64(3),
		"mutation_count":    float64(2),
		"resource_count":    float64(4),
		"errors": []interface{}{
			// one BEFORE the last action, two (same msg) AFTER it
			map[string]interface{}{"message": "old boot warning", "type": "Error", "timestamp": ts("00")},
			map[string]interface{}{"message": "Uncaught TypeError: submit is not a function", "type": "TypeError", "timestamp": ts("04")},
			map[string]interface{}{"message": "Uncaught TypeError: submit is not a function", "type": "TypeError", "timestamp": ts("05")},
		},
		"interactions": []interface{}{
			map[string]interface{}{"event_type": "click", "target": map[string]interface{}{"selector": "#a"}, "timestamp": ts("01")},
			map[string]interface{}{"event_type": "scroll", "timestamp": ts("02")},
			map[string]interface{}{"event_type": "click", "target": map[string]interface{}{"selector": "#submit"}, "timestamp": ts("03")},
		},
		"mutations": []interface{}{
			map[string]interface{}{"mutation_type": "added", "timestamp": ts("06")},
			map[string]interface{}{"mutation_type": "removed", "timestamp": ts("00")},
		},
		"failed_resources": []interface{}{
			map[string]interface{}{"url": "/api/submit", "status": float64(500)},
		},
		"performance": map[string]interface{}{
			"load_event_end":         float64(2200),
			"first_contentful_paint": float64(450),
			"dom_content_loaded":     float64(900),
		},
	}
}

func TestConvertToPageTriage_FullSignals(t *testing.T) {
	tr := convertToPageTriage(triageWireSession())

	assert.Equal(t, "/checkout", tr.URL)
	assert.Equal(t, "Checkout", tr.PageTitle)
	assert.True(t, tr.Active)
	assert.Equal(t, int64(2200), tr.LoadTimeMs)

	// Top errors: the duplicated TypeError outranks the single boot warning.
	require.NotEmpty(t, tr.TopErrors)
	assert.Equal(t, "TypeError", tr.TopErrors[0].Type)
	assert.Equal(t, 2, tr.TopErrors[0].Count, "duplicate errors grouped and counted")

	// Last actions newest-first; the click the user just made is first.
	require.Len(t, tr.LastActions, 3)
	assert.Equal(t, "click", tr.LastActions[0].EventType)
	assert.Equal(t, "#submit", tr.LastActions[0].Selector, "most recent action surfaced first")
	assert.Equal(t, "scroll", tr.LastActions[1].EventType)

	// Failed resources surfaced.
	require.Len(t, tr.FailedResources, 1)
	assert.Equal(t, "/api/submit", tr.FailedResources[0].URL)
	assert.Equal(t, 500, tr.FailedResources[0].Status)

	// Performance headline numbers.
	require.NotNil(t, tr.Performance)
	assert.Equal(t, int64(2200), tr.Performance.LoadMs)
	assert.Equal(t, int64(450), tr.Performance.FCPMs)

	// Correlation: the click on #submit was followed by 2 errors + 1 mutation.
	require.NotNil(t, tr.AfterLastAction)
	assert.Equal(t, "click #submit", tr.AfterLastAction.Action)
	assert.Equal(t, 2, tr.AfterLastAction.ErrorsSince, "both post-click errors counted; the pre-click warning excluded")
	assert.Equal(t, 1, tr.AfterLastAction.MutationsSince, "only the post-click mutation counted")
	assert.Contains(t, tr.AfterLastAction.SampleError, "submit is not a function")

	assert.NotEmpty(t, tr.NextTools, "points at the deeper tools for remediation")
}

func TestConvertToPageTriage_NoSignals(t *testing.T) {
	tr := convertToPageTriage(map[string]interface{}{
		"id": "page-1", "url": "/", "active": true,
	})
	assert.Nil(t, tr.AfterLastAction, "no interactions → no correlation")
	assert.Empty(t, tr.TopErrors)
	assert.NotEmpty(t, tr.Hint, "empty page gets a helpful hint")
}

func TestConvertToPageTriage_ActionWithoutConsequenceIsQuiet(t *testing.T) {
	// A last action with nothing after it must not fabricate a consequence.
	tr := convertToPageTriage(map[string]interface{}{
		"id": "page-1", "url": "/",
		"interactions": []interface{}{
			map[string]interface{}{"event_type": "click", "target": map[string]interface{}{"selector": "#ok"}, "timestamp": ts("09")},
		},
		"errors": []interface{}{
			map[string]interface{}{"message": "earlier", "timestamp": ts("01")},
		},
	})
	assert.Nil(t, tr.AfterLastAction, "errors only BEFORE the action → no false 'click broke it' signal")
}

func TestPickActiveSessionID_PrefersActiveThenNewest(t *testing.T) {
	list := map[string]interface{}{"sessions": []interface{}{
		map[string]interface{}{"id": "old-active", "active": true, "last_activity": ts("01")},
		map[string]interface{}{"id": "new-active", "active": true, "last_activity": ts("05")},
		map[string]interface{}{"id": "newest-inactive", "active": false, "last_activity": ts("09")},
	}}
	assert.Equal(t, "new-active", pickActiveSessionID(list), "newest ACTIVE wins over a newer inactive one")

	none := map[string]interface{}{"sessions": []interface{}{}}
	assert.Equal(t, "", pickActiveSessionID(none), "no sessions → empty")
}
