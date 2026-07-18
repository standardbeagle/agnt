package tools

// Tool-side coverage for the currentpage GET/SUMMARY converters against the
// lean wire schema emitted by the daemon's compactPageSession: resources as URL
// strings, per-kind counts, errors carrying a derived `type`, interactions with
// `event_type`, mutations with `mutation_type`, performance with domain tags.
// These rollups were silently broken (all-"unknown" / zero counts) before the
// schema was aligned — pinned here so a future drift fails loud.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compactWireSession mirrors what compactPageSession marshals onto the wire.
func compactWireSession() map[string]interface{} {
	return map[string]interface{}{
		"id":                "page-1",
		"url":               "/dashboard",
		"page_title":        "Dashboard",
		"active":            true,
		"resource_count":    float64(3),
		"error_count":       float64(2),
		"interaction_count": float64(4),
		"mutation_count":    float64(2),
		"has_performance":   true,
		"load_time_ms":      float64(1234),
		"resources":         []interface{}{"/app.js", "/style.css", "/logo.png"},
		"errors": []interface{}{
			map[string]interface{}{"message": "Uncaught ReferenceError: x", "type": "ReferenceError"},
			map[string]interface{}{"message": "Uncaught TypeError: y", "type": "TypeError"},
		},
		"interactions": []interface{}{
			map[string]interface{}{"event_type": "click"},
			map[string]interface{}{"event_type": "click"},
			map[string]interface{}{"event_type": "keydown"},
			map[string]interface{}{"event_type": "scroll"},
		},
		"mutations": []interface{}{
			map[string]interface{}{"mutation_type": "added"},
			map[string]interface{}{"mutation_type": "removed"},
		},
		"performance": map[string]interface{}{
			"load_event_end":     float64(1234),
			"first_paint":        float64(300),
			"dom_content_loaded": float64(800),
			"page_width":         float64(1280),
			"page_height":        float64(3000),
			"viewport_width":     float64(1024),
			"viewport_height":    float64(768),
		},
	}
}

func TestConvertToPageSummary_RollupsAndCounts(t *testing.T) {
	s := convertToPageSummary(compactWireSession(), map[string]bool{}, 5)

	// Counts come straight from the wire.
	assert.Equal(t, 3, s.ResourceCount)
	assert.Equal(t, 2, s.ErrorCount)
	assert.Equal(t, 4, s.InteractionCount)
	assert.Equal(t, 2, s.MutationCount)
	assert.Equal(t, int64(1234), s.LoadTimeMs, "load time read from the wire / performance")

	// Resource categorization by URL suffix.
	assert.Equal(t, map[string]int{"js": 1, "css": 1, "image": 1}, s.ResourcesByType)

	// Interaction rollup reads event_type (was the all-"unknown" bug).
	assert.Equal(t, 2, s.InteractionsByType["click"])
	assert.Equal(t, 1, s.InteractionsByType["keydown"])
	assert.Equal(t, 1, s.InteractionsByType["scroll"])
	assert.NotContains(t, s.InteractionsByType, "unknown", "no events fall through to unknown")

	// Mutation rollup reads mutation_type.
	assert.Equal(t, map[string]int{"added": 1, "removed": 1}, s.MutationsByType)

	// Error grouping reads the derived type.
	assert.Equal(t, 1, s.ErrorsByType["ReferenceError"])
	assert.Equal(t, 1, s.ErrorsByType["TypeError"])
	assert.Len(t, s.UniqueErrors, 2, "distinct messages deduped into unique errors")

	// Performance domain tags mapped through.
	assert.Equal(t, int64(300), s.FirstPaintMs)
	assert.Equal(t, int64(800), s.DOMContentLoaded)
	assert.Equal(t, 1280, s.PageWidth)
	assert.Equal(t, 768, s.ViewportHeight)

	// Default (no detail sections requested): recent windows present, full arrays absent.
	assert.NotEmpty(t, s.RecentInteractions, "recent interactions surfaced by default")
	assert.Empty(t, s.Interactions, "full interaction array withheld unless detail requested")
	assert.Empty(t, s.DetailSections)
}

func TestConvertToPageSummary_DetailSectionsHonorLimit(t *testing.T) {
	s := convertToPageSummary(compactWireSession(), map[string]bool{"interactions": true, "errors": true}, 2)

	assert.ElementsMatch(t, []string{"interactions", "errors"}, s.DetailSections)
	assert.Len(t, s.Interactions, 2, "interaction detail capped at limit")
	assert.LessOrEqual(t, len(s.Errors), 2, "error detail capped at limit")
	assert.Empty(t, s.Mutations, "unrequested section stays empty")
	assert.Equal(t, 2, s.DetailLimit)
}

func TestConvertToPageSessionOutput_GetCountsAndResources(t *testing.T) {
	out := convertToPageSessionOutput(compactWireSession())

	assert.Equal(t, "page-1", out.ID)
	assert.Equal(t, "/dashboard", out.URL)
	assert.Equal(t, 3, out.ResourceCount, "resource_count surfaced on GET")
	assert.Equal(t, 2, out.ErrorCount, "error_count surfaced on GET")
	assert.Equal(t, 4, out.InteractionCount)
	assert.Equal(t, 2, out.MutationCount)
	assert.Equal(t, int64(1234), out.LoadTime)

	// raw GET exposes resources as URL strings (no HTTPLogEntry objects).
	require.Len(t, out.Resources, 3)
	assert.Equal(t, []string{"/app.js", "/style.css", "/logo.png"}, out.Resources)
	require.Len(t, out.Errors, 2, "raw GET exposes the error detail array")
}

func TestConvertToPageSummary_UniqueErrorsRankedByCount(t *testing.T) {
	// 7 distinct messages with distinct counts: ranging the dedup map and
	// taking the first 5 (previous behavior) picked an arbitrary subset in
	// Go's random map order, so a frequent error could be shadowed by a rare
	// one. The summary must keep the 5 most frequent, ordered count desc.
	var errs []interface{}
	for i, msg := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		for n := 0; n <= i; n++ {
			errs = append(errs, map[string]interface{}{"message": msg, "type": "Error"})
		}
	}
	s := convertToPageSummary(map[string]interface{}{"errors": errs}, map[string]bool{}, 5)

	require.Len(t, s.UniqueErrors, 5, "top-5 cap")
	assert.Equal(t, []string{"g", "f", "e", "d", "c"},
		[]string{s.UniqueErrors[0].Message, s.UniqueErrors[1].Message, s.UniqueErrors[2].Message, s.UniqueErrors[3].Message, s.UniqueErrors[4].Message},
		"ranked by count desc, least frequent (a, b) dropped")
	assert.Equal(t, 7, s.UniqueErrors[0].Count)
	assert.Equal(t, 3, s.UniqueErrors[4].Count)
}
