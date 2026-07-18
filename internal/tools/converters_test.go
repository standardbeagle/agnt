package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- convertToCompactError ---------------------------------------------------

func TestConvertToCompactError_MessageCap(t *testing.T) {
	long := strings.Repeat("x", 600)
	got := convertToCompactError(map[string]interface{}{"message": long})

	assert.Len(t, got.Message, 500, "message capped at 500 runes")
	assert.True(t, strings.HasSuffix(got.Message, "..."), "capped message ends with ellipsis")
	assert.Equal(t, strings.Repeat("x", 497)+"...", got.Message, "497 chars + ellipsis")

	// Exactly 500 must NOT be truncated.
	exact := strings.Repeat("y", 500)
	gotExact := convertToCompactError(map[string]interface{}{"message": exact})
	assert.Equal(t, exact, gotExact.Message, "exactly 500 chars untouched")

	// Short message passes through.
	gotShort := convertToCompactError(map[string]interface{}{"message": "boom"})
	assert.Equal(t, "boom", gotShort.Message)
}

func TestConvertToCompactError_TypeDefault(t *testing.T) {
	got := convertToCompactError(map[string]interface{}{"message": "x"})
	assert.Equal(t, "Error", got.Type, "empty type defaults to Error")

	gotTyped := convertToCompactError(map[string]interface{}{"message": "x", "type": "TypeError"})
	assert.Equal(t, "TypeError", gotTyped.Type)

	assert.Equal(t, "", got.URL)
	assert.Equal(t, "", got.Location)
}

func TestConvertToCompactError_Location(t *testing.T) {
	tests := []struct {
		name   string
		em     map[string]interface{}
		wantLo string
	}{
		{
			name:   "file line col from basename",
			em:     map[string]interface{}{"source": "https://app.dev/src/components/List.tsx", "lineno": float64(42), "colno": float64(15)},
			wantLo: "List.tsx:42:15",
		},
		{
			name:   "colno zero falls back to file:line",
			em:     map[string]interface{}{"source": "/abs/path/main.js", "lineno": float64(10), "colno": float64(0)},
			wantLo: "main.js:10",
		},
		{
			name:   "lineno zero falls back to bare filename",
			em:     map[string]interface{}{"source": "a/b/c/app.js", "lineno": float64(0), "colno": float64(99)},
			wantLo: "app.js",
		},
		{
			name:   "source without slash uses whole string",
			em:     map[string]interface{}{"source": "inline.js", "lineno": float64(3), "colno": float64(7)},
			wantLo: "inline.js:3:7",
		},
		{
			name:   "empty source yields empty location",
			em:     map[string]interface{}{"lineno": float64(5), "colno": float64(5)},
			wantLo: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := convertToCompactError(tc.em)
			assert.Equal(t, tc.wantLo, got.Location)
		})
	}
}

func TestConvertToCompactError_StackPreview(t *testing.T) {
	// 5-line stack -> 3-line preview + "(2 more lines)".
	stack := "line0\nline1\nline2\nline3\nline4"
	got := convertToCompactError(map[string]interface{}{"message": "x", "stack": stack})

	lines := strings.Split(got.StackPreview, "\n")
	require.Len(t, lines, 4, "3 preview lines + 1 more-lines marker")
	assert.Equal(t, "line0", lines[0])
	assert.Equal(t, "line1", lines[1])
	assert.Equal(t, "line2", lines[2])
	assert.Equal(t, "... (2 more lines)", lines[3])

	// Fewer than 3 lines: no marker, all lines retained.
	got2 := convertToCompactError(map[string]interface{}{"message": "x", "stack": "only0\nonly1"})
	assert.Equal(t, "only0\nonly1", got2.StackPreview)
	assert.NotContains(t, got2.StackPreview, "more lines")

	// Each line trimmed and capped at 117+"...".
	longLine := "  " + strings.Repeat("z", 200)
	got3 := convertToCompactError(map[string]interface{}{"message": "x", "stack": longLine})
	assert.Equal(t, strings.Repeat("z", 117)+"...", got3.StackPreview, "trimmed then capped at 117+...")

	// Empty stack -> empty preview.
	got4 := convertToCompactError(map[string]interface{}{"message": "x"})
	assert.Equal(t, "", got4.StackPreview)
}

func TestConvertToCompactError_Timestamp(t *testing.T) {
	ts := "2026-05-28T10:00:00Z"
	got := convertToCompactError(map[string]interface{}{"message": "x", "timestamp": ts})
	assert.Equal(t, ts, got.Timestamp, "timestamp passed through verbatim")

	// Non-string timestamp ignored.
	got2 := convertToCompactError(map[string]interface{}{"message": "x", "timestamp": float64(123)})
	assert.Equal(t, "", got2.Timestamp)
}

// --- categorizeResource ------------------------------------------------------

func TestCategorizeResource(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://x/app.js", "js"},
		{"https://x/app.js?v=123", "js"},
		{"/styles/main.css", "css"},
		{"/styles/main.css?hash=abc", "css"},
		{"/img/logo.png", "image"},
		{"/img/photo.JPG", "image"},
		{"/img/icon.svg", "image"},
		{"/fonts/foo.woff2", "font"},
		{"/fonts/foo.ttf", "font"},
		{"/data/config.json", "api"},
		{"/api/users", "api"},
		{"https://host/api/v1/items?page=2", "api"},
		{"/index.html", "html"},
		{"https://host/", "html"},
		{"/some/unknown/thing.bin", "other"},
		{"/no-extension-path", "other"},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			assert.Equal(t, tc.want, categorizeResource(tc.url))
		})
	}
}

// --- convertToCompactHTTP ----------------------------------------------------

func TestConvertToCompactHTTP(t *testing.T) {
	longURL := "https://host/" + strings.Repeat("a", 200)
	data := map[string]interface{}{
		"method":      "POST",
		"url":         longURL,
		"status_code": float64(503),
		"duration":    float64(2_500_000), // ns
		"error":       "upstream timeout",
		"timestamp":   "2026-05-28T10:00:00Z",
	}
	got := convertToCompactHTTP(data)

	assert.Equal(t, "POST", got.Method)
	assert.Equal(t, 503, got.StatusCode)
	assert.Equal(t, int64(2), got.Duration, "ns divided by 1e6 -> ms (integer)")
	assert.Equal(t, "upstream timeout", got.Error)
	assert.Len(t, got.URL, 100, "URL capped at 100")
	assert.True(t, strings.HasSuffix(got.URL, "..."))
	assert.Equal(t, longURL[:97]+"...", got.URL)
	assert.False(t, got.Timestamp.IsZero(), "RFC3339 parsed")

	// Bad timestamp -> zero time, no panic.
	got2 := convertToCompactHTTP(map[string]interface{}{"method": "GET", "url": "/x", "timestamp": "not-a-time"})
	assert.True(t, got2.Timestamp.IsZero())
	assert.Equal(t, "/x", got2.URL, "short URL untouched")
	assert.Equal(t, int64(0), got2.Duration)
}

// --- convertToCompactPerformance ---------------------------------------------

func TestConvertToCompactPerformance(t *testing.T) {
	longURL := strings.Repeat("p", 150)
	got := convertToCompactPerformance(map[string]interface{}{
		"url":                longURL,
		"load_event_end":     float64(1200),
		"first_paint":        float64(300),
		"dom_content_loaded": float64(800),
		"timestamp":          "2026-05-28T10:00:00Z",
	})
	assert.Equal(t, int64(1200), got.LoadTimeMs)
	assert.Equal(t, int64(300), got.FirstPaintMs)
	assert.Equal(t, int64(800), got.DOMContentLoaded)
	assert.Len(t, got.URL, 100)
	assert.Equal(t, longURL[:97]+"...", got.URL)
	assert.False(t, got.Timestamp.IsZero())

	// Empty / short URL untouched.
	got2 := convertToCompactPerformance(map[string]interface{}{"url": "short"})
	assert.Equal(t, "short", got2.URL)
	assert.True(t, got2.Timestamp.IsZero())
}

// --- convertToCompactInteraction ---------------------------------------------

func TestConvertToCompactInteraction(t *testing.T) {
	// event_type preferred over type.
	got := convertToCompactInteraction(map[string]interface{}{
		"event_type": "click",
		"type":       "ignored",
		"target":     map[string]interface{}{"selector": "#submit"},
		"timestamp":  "2026-05-28T10:00:00Z",
	})
	assert.Equal(t, "click", got.Type)
	assert.Equal(t, "#submit", got.Target)
	assert.False(t, got.Timestamp.IsZero())

	// Falls back to type when event_type empty.
	got2 := convertToCompactInteraction(map[string]interface{}{"type": "scroll"})
	assert.Equal(t, "scroll", got2.Type)

	// Target selector fallback: tag#id when no selector.
	got3 := convertToCompactInteraction(map[string]interface{}{
		"event_type": "click",
		"target":     map[string]interface{}{"tag_name": "button", "id": "go"},
	})
	assert.Equal(t, "button#go", got3.Target)

	// Bare tag when no selector and no id.
	got4 := convertToCompactInteraction(map[string]interface{}{
		"event_type": "click",
		"target":     map[string]interface{}{"tag_name": "div"},
	})
	assert.Equal(t, "div", got4.Target)

	// Target truncation at 80 (77 + "...").
	longSel := strings.Repeat("s", 100)
	got5 := convertToCompactInteraction(map[string]interface{}{
		"event_type": "click",
		"target":     map[string]interface{}{"selector": longSel},
	})
	assert.Len(t, got5.Target, 80)
	assert.Equal(t, longSel[:77]+"...", got5.Target)
}

// --- convertToCompactMutation ------------------------------------------------

func TestConvertToCompactMutation(t *testing.T) {
	got := convertToCompactMutation(map[string]interface{}{
		"type":      "added",
		"target":    map[string]interface{}{"selector": ".list"},
		"nodes":     []interface{}{1, 2, 3},
		"timestamp": "2026-05-28T10:00:00Z",
	})
	assert.Equal(t, "added", got.Type)
	assert.Equal(t, ".list", got.Target)
	assert.Equal(t, 3, got.Count, "node count from nodes slice length")
	assert.False(t, got.Timestamp.IsZero())

	// tag#id fallback + no nodes -> count 0.
	got2 := convertToCompactMutation(map[string]interface{}{
		"type":   "removed",
		"target": map[string]interface{}{"tag_name": "li", "id": "row5"},
	})
	assert.Equal(t, "li#row5", got2.Target)
	assert.Equal(t, 0, got2.Count)

	// Target truncation.
	longSel := strings.Repeat("m", 90)
	got3 := convertToCompactMutation(map[string]interface{}{
		"type":   "modified",
		"target": map[string]interface{}{"selector": longSel},
	})
	assert.Len(t, got3.Target, 80)
	assert.Equal(t, longSel[:77]+"...", got3.Target)
}

// --- convertToCompactLogEntry ------------------------------------------------

func TestConvertToCompactLogEntry(t *testing.T) {
	got := convertToCompactLogEntry(map[string]interface{}{
		"type":      "custom",
		"message":   "hello",
		"timestamp": "2026-05-28T10:00:00Z",
	})
	assert.Equal(t, "custom", got.Type)
	assert.Equal(t, "hello", got.Message)
	assert.False(t, got.Timestamp.IsZero())

	// message falls back to level when message empty.
	got2 := convertToCompactLogEntry(map[string]interface{}{"type": "diag", "level": "warn"})
	assert.Equal(t, "warn", got2.Message)

	// Message truncation at 200 (197 + "...").
	longMsg := strings.Repeat("L", 300)
	got3 := convertToCompactLogEntry(map[string]interface{}{"type": "x", "message": longMsg})
	assert.Len(t, got3.Message, 200)
	assert.Equal(t, longMsg[:197]+"...", got3.Message)

	// Bad timestamp -> zero, message empty when neither message nor level.
	got4 := convertToCompactLogEntry(map[string]interface{}{"type": "x", "timestamp": "bad"})
	assert.True(t, got4.Timestamp.IsZero())
	assert.Equal(t, "", got4.Message)
}

// --- buildProxyLogSummary ----------------------------------------------------

// httpEntry builds a {type:"http", http:{...}} log entry map.
func httpEntry(method string, status int, durationNs int64, ts string) map[string]interface{} {
	return map[string]interface{}{
		"type": "http",
		"http": map[string]interface{}{
			"method":      method,
			"status_code": float64(status),
			"duration":    float64(durationNs),
			"url":         "/req",
			"timestamp":   ts,
		},
	}
}

func errorEntry(msg, typ, ts string) map[string]interface{} {
	return map[string]interface{}{
		"type": "error",
		"error": map[string]interface{}{
			"message":   msg,
			"type":      typ,
			"timestamp": ts,
		},
	}
}

func perfEntry(loadEnd int64) map[string]interface{} {
	return map[string]interface{}{
		"type": "performance",
		"performance": map[string]interface{}{
			"load_event_end": float64(loadEnd),
		},
	}
}

func TestBuildProxyLogSummary_CountsAndBuckets(t *testing.T) {
	entries := []interface{}{
		httpEntry("GET", 200, 0, "2026-05-28T10:00:00Z"),
		httpEntry("GET", 201, 0, "2026-05-28T10:00:01Z"),
		httpEntry("POST", 404, 0, "2026-05-28T10:00:02Z"),
		httpEntry("POST", 500, 0, "2026-05-28T10:00:03Z"),
		errorEntry("ReferenceError: x is not defined", "ReferenceError", "2026-05-28T10:00:04Z"),
		errorEntry("boom", "", "2026-05-28T10:00:05Z"),
		perfEntry(1000),
		perfEntry(3000),
		map[string]interface{}{"type": "interaction", "interaction": map[string]interface{}{"event_type": "click"}},
		map[string]interface{}{"type": "mutation", "mutation": map[string]interface{}{"type": "added"}},
		map[string]interface{}{"type": "sketch", "sketch": map[string]interface{}{}},
	}

	s := buildProxyLogSummary(entries, map[string]bool{}, 5)

	assert.Equal(t, 11, s.TotalEntries)
	assert.Equal(t, 4, s.EntriesByType["http"])
	assert.Equal(t, 2, s.EntriesByType["error"])
	assert.Equal(t, 2, s.EntriesByType["performance"])

	// Per-type counts.
	assert.Equal(t, 4, s.HTTPCount)
	assert.Equal(t, 2, s.ErrorCount)
	assert.Equal(t, 2, s.PerformanceCount)
	assert.Equal(t, 1, s.InteractionCount)
	assert.Equal(t, 1, s.MutationCount)
	assert.Equal(t, 1, s.OtherCount)
	assert.Equal(t, 1, s.OtherTypes["sketch"])

	// Status-group bucketing (Nxx).
	assert.Equal(t, 2, s.HTTPByStatus["2xx"], "200 and 201 both 2xx")
	assert.Equal(t, 1, s.HTTPByStatus["4xx"])
	assert.Equal(t, 1, s.HTTPByStatus["5xx"])

	// Method counts.
	assert.Equal(t, 2, s.HTTPByMethod["GET"])
	assert.Equal(t, 2, s.HTTPByMethod["POST"])

	// Error type bucketing + default.
	assert.Equal(t, 1, s.ErrorsByType["ReferenceError"])
	assert.Equal(t, 1, s.ErrorsByType["Error"], "empty type defaults to Error")

	// Interaction / mutation type buckets.
	assert.Equal(t, 1, s.InteractionsByType["click"])
	assert.Equal(t, 1, s.MutationsByType["added"])

	// Avg load time = (1000+3000)/2.
	assert.Equal(t, int64(2000), s.AvgLoadTime)

	// Time range spans first and last parsed timestamps.
	assert.False(t, s.TimeRange.Start.IsZero())
	assert.False(t, s.TimeRange.End.IsZero())
	assert.True(t, s.TimeRange.End.After(s.TimeRange.Start))
}

func TestBuildProxyLogSummary_UniqueErrorDedupCap(t *testing.T) {
	// 12 distinct error messages -> UniqueErrors capped at 10.
	var entries []interface{}
	for i := 0; i < 12; i++ {
		entries = append(entries, errorEntry("msg-"+string(rune('A'+i)), "Error", ""))
	}
	s := buildProxyLogSummary(entries, map[string]bool{}, 5)
	assert.Equal(t, 12, s.ErrorCount)
	assert.Len(t, s.UniqueErrors, 10, "unique errors capped at 10")

	// Duplicate messages coalesce into one entry with incremented count.
	dupEntries := []interface{}{
		errorEntry("same", "Error", ""),
		errorEntry("same", "Error", ""),
		errorEntry("same", "Error", ""),
	}
	s2 := buildProxyLogSummary(dupEntries, map[string]bool{}, 5)
	require.Len(t, s2.UniqueErrors, 1)
	assert.Equal(t, 3, s2.UniqueErrors[0].Count)
	assert.Equal(t, "same", s2.UniqueErrors[0].Message)
}

func TestBuildProxyLogSummary_DetailVsRecentWindowing(t *testing.T) {
	var entries []interface{}
	for i := 0; i < 8; i++ {
		entries = append(entries, httpEntry("GET", 200, 0, ""))
	}

	// No detail: RecentHTTP is last min(5,limit). limit=5 -> 5 recent, no full list.
	s := buildProxyLogSummary(entries, map[string]bool{}, 5)
	assert.Len(t, s.RecentHTTP, 5)
	assert.Empty(t, s.HTTPRequests)
	assert.Empty(t, s.DetailSections)

	// Detail "http": full windowed list capped at limit; no recent.
	s2 := buildProxyLogSummary(entries, map[string]bool{"http": true}, 3)
	assert.Len(t, s2.HTTPRequests, 3, "full detail list capped at limit")
	assert.Empty(t, s2.RecentHTTP)
	assert.Contains(t, s2.DetailSections, "http")

	// start<0 guard: fewer entries than limit -> all entries, no panic.
	few := []interface{}{httpEntry("GET", 200, 0, ""), httpEntry("POST", 200, 0, "")}
	s3 := buildProxyLogSummary(few, map[string]bool{"http": true}, 10)
	assert.Len(t, s3.HTTPRequests, 2, "limit exceeds count -> all entries")

	// recentLimit clamps to limit when limit < 5.
	s4 := buildProxyLogSummary(entries, map[string]bool{}, 2)
	assert.Len(t, s4.RecentHTTP, 2, "recent clamps to limit when limit<5")
}

func TestBuildProxyLogSummary_Empty(t *testing.T) {
	s := buildProxyLogSummary(nil, map[string]bool{}, 5)
	assert.Equal(t, 0, s.TotalEntries)
	assert.Equal(t, 0, s.ErrorCount)
	assert.Empty(t, s.UniqueErrors)
	assert.Empty(t, s.RecentHTTP)
	assert.Equal(t, int64(0), s.AvgLoadTime)
	assert.True(t, s.TimeRange.Start.IsZero())
}

// realErrorEntry builds an error entry as it appears on the wire: FrontendError
// has NO "type" field — the class must be derived from the message.
func realErrorEntry(msg string) map[string]interface{} {
	return map[string]interface{}{
		"type":  "error",
		"error": map[string]interface{}{"message": msg},
	}
}

// realMutationEntry builds a mutation entry with the true wire field
// "mutation_type" (not the synthetic "type" older fixtures used).
func realMutationEntry(mutationType string) map[string]interface{} {
	return map[string]interface{}{
		"type":     "mutation",
		"mutation": map[string]interface{}{"mutation_type": mutationType},
	}
}

func TestBuildProxyLogSummary_ErrorsByTypeFromMessage(t *testing.T) {
	// Real FrontendError entries (no "type" field): the class is derived from
	// the message prefix rather than defaulting everything to "Error".
	entries := []interface{}{
		realErrorEntry("TypeError: undefined is not a function"),
		realErrorEntry("TypeError: cannot read properties of null"),
		realErrorEntry("ReferenceError: x is not defined"),
		realErrorEntry("Uncaught RangeError: invalid array length"),
		realErrorEntry("plain message with no class"),
	}
	s := buildProxyLogSummary(entries, map[string]bool{}, 10)

	assert.Equal(t, 2, s.ErrorsByType["TypeError"])
	assert.Equal(t, 1, s.ErrorsByType["ReferenceError"])
	assert.Equal(t, 1, s.ErrorsByType["RangeError"], "leading 'Uncaught ' stripped")
	assert.Equal(t, 1, s.ErrorsByType["Error"], "message with no class falls back to Error")
	assert.Zero(t, s.ErrorsByType[""], "no empty-string bucket")
}

func TestBuildProxyLogSummary_MutationsByTypeWireField(t *testing.T) {
	entries := []interface{}{
		realMutationEntry("added"),
		realMutationEntry("added"),
		realMutationEntry("removed"),
	}
	s := buildProxyLogSummary(entries, map[string]bool{}, 10)

	assert.Equal(t, 2, s.MutationsByType["added"])
	assert.Equal(t, 1, s.MutationsByType["removed"])
	assert.Empty(t, s.MutationsByType[""], "mutation_type read, not empty 'type'")
}

func TestBuildProxyLogSummary_UniqueErrorsRankedByCount(t *testing.T) {
	var entries []interface{}
	// "rare" once, "common" five times, "medium" three times.
	entries = append(entries, realErrorEntry("Error: rare"))
	for i := 0; i < 5; i++ {
		entries = append(entries, realErrorEntry("Error: common"))
	}
	for i := 0; i < 3; i++ {
		entries = append(entries, realErrorEntry("Error: medium"))
	}
	s := buildProxyLogSummary(entries, map[string]bool{}, 10)

	require.Len(t, s.UniqueErrors, 3)
	// Ordered by count desc — the frequent error must lead, not appear in an
	// arbitrary map-iteration slot.
	assert.Equal(t, "Error: common", s.UniqueErrors[0].Message)
	assert.Equal(t, 5, s.UniqueErrors[0].Count)
	assert.Equal(t, "Error: medium", s.UniqueErrors[1].Message)
	assert.Equal(t, 3, s.UniqueErrors[1].Count)
	assert.Equal(t, "Error: rare", s.UniqueErrors[2].Message)
	assert.Equal(t, 1, s.UniqueErrors[2].Count)
}

func TestClassifyErrorType(t *testing.T) {
	cases := map[string]string{
		"TypeError: boom":                        "TypeError",
		"Uncaught ReferenceError: x":             "ReferenceError",
		"SyntaxError: unexpected token":          "SyntaxError",
		"":                                       "Error",
		"no colon here":                          "Error",
		"http://example.com/x: failed to load":   "Error", // URL prefix rejected (slash)
		"a very long sentence: with a colon too": "Error", // spaces rejected
	}
	for msg, want := range cases {
		assert.Equal(t, want, classifyErrorType(msg), "message %q", msg)
	}
}

// --- error_queue helpers -----------------------------------------------------

func TestNormalizeQueueSeverity(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"warning", "warning"},
		{"warn", "warning"},
		{"WARN", "warning"},
		{"  Warning  ", "warning"},
		{"info", "info"},
		{"INFO", "info"},
		{"error", "error"},
		{"", "error"},
		{"garbage", "error"},
		{"critical", "error"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, normalizeQueueSeverity(tc.in))
		})
	}
}

func TestSanitizeQueueSource(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"github-actions", "github-actions"},
		{"GitHub Actions", "github-actions"}, // space -> '-', lowercased
		{"build_kite.ci", "build_kite.ci"},   // _ . preserved
		{"deploy/prod", "deploy-prod"},       // slash -> '-'
		{"  CI  ", "ci"},                     // trimmed + lowered
		{"", "external"},                     // empty -> external
		{"a@b#c", "a-b-c"},                   // multiple specials
		{"日本", "--"},                         // non-ascii multibyte -> one '-' per rune
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeQueueSource(tc.in))
		})
	}
}

// --- proxy_log_tools helpers -------------------------------------------------

func TestFormatLocation(t *testing.T) {
	tests := []struct {
		name   string
		source string
		line   int
		col    int
		want   string
	}{
		{"full path kept (not basename)", "https://app/src/List.tsx", 42, 15, "https://app/src/List.tsx:42:15"},
		{"zero line and col", "main.js", 0, 0, "main.js:0:0"},
		{"empty source -> empty", "", 10, 5, ""},
		{"bare filename", "app.js", 1, 1, "app.js:1:1"},
		{"col present", "x.js", 7, 3, "x.js:7:3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, formatLocation(tc.source, tc.line, tc.col))
		})
	}
}

func TestTruncateStack(t *testing.T) {
	// Keeps first maxLines lines, drops the rest.
	stack := "a\nb\nc\nd\ne"
	assert.Equal(t, "a\nb\nc", truncateStack(stack, 3))

	// Fewer lines than max -> full stack.
	assert.Equal(t, "a\nb", truncateStack("a\nb", 5))

	// No trailing newline, single line.
	assert.Equal(t, "single", truncateStack("single", 3))

	// Empty stack -> empty.
	assert.Equal(t, "", truncateStack("", 3))

	// maxLines 1 -> only first line.
	assert.Equal(t, "first", truncateStack("first\nsecond\nthird", 1))

	// Trailing newline: lines collected up to maxLines.
	assert.Equal(t, "a\nb", truncateStack("a\nb\n", 2))
}
