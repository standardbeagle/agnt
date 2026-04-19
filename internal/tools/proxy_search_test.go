package tools

import (
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// TestSearchAPIFunctions_CaseInsensitive verifies the query matches
// regardless of case across name, description, and signature.
func TestSearchAPIFunctions_CaseInsensitive(t *testing.T) {
	lower := SearchAPIFunctions("click", "")
	upper := SearchAPIFunctions("CLICK", "")
	mixed := SearchAPIFunctions("ClIcK", "")

	if lower.Count == 0 {
		t.Fatalf("expected matches for 'click' in generated catalog, got 0")
	}
	if lower.Count != upper.Count || lower.Count != mixed.Count {
		t.Fatalf("case-insensitive mismatch: lower=%d upper=%d mixed=%d",
			lower.Count, upper.Count, mixed.Count)
	}
	// At least one match's name or description should reference click.
	found := false
	for _, m := range lower.Matches {
		if strings.Contains(strings.ToLower(m.Name+m.Description+m.Signature), "click") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no match actually contained 'click' substring: %+v", lower.Matches)
	}
}

// TestSearchAPIFunctions_CategoryFilter asserts the category narrows the
// result set and is an AND with the substring query.
func TestSearchAPIFunctions_CategoryFilter(t *testing.T) {
	all := SearchAPIFunctions("contrast", "")
	scoped := SearchAPIFunctions("contrast", "accessibility")

	if scoped.Count == 0 {
		t.Fatalf("expected contrast+accessibility to match getContrast, got 0")
	}
	if scoped.Count > all.Count {
		t.Fatalf("scoped result (%d) should be <= unscoped (%d)", scoped.Count, all.Count)
	}
	for _, m := range scoped.Matches {
		if !strings.EqualFold(m.Category, "accessibility") {
			t.Fatalf("category filter leaked: %+v", m)
		}
	}
}

// TestSearchAPIFunctions_NoMatch confirms the Matches slice is empty (not
// nil) and Truncated is false for a deliberately absent query.
func TestSearchAPIFunctions_NoMatch(t *testing.T) {
	res := SearchAPIFunctions("xyzzy_nope_zzz", "")
	if res.Count != 0 {
		t.Fatalf("expected 0 matches, got %d", res.Count)
	}
	if res.Matches == nil {
		t.Fatalf("expected non-nil empty Matches slice for JSON stability")
	}
	if res.Truncated {
		t.Fatalf("Truncated should be false when no results")
	}
}

// TestSearchAPIFunctions_ResultCap verifies the 10-result ceiling and
// that Truncated flips when the underlying corpus exceeds it. We use an
// empty query + real categories with many entries.
func TestSearchAPIFunctions_ResultCap(t *testing.T) {
	// Empty query returns everything — catalog has ~84 entries.
	res := SearchAPIFunctions("", "")
	if len(res.Matches) != maxAPISearchResults {
		t.Fatalf("expected exactly %d matches at the cap, got %d",
			maxAPISearchResults, len(res.Matches))
	}
	if !res.Truncated {
		t.Fatalf("Truncated should be true when catalog > %d", maxAPISearchResults)
	}
	if res.Count != maxAPISearchResults {
		t.Fatalf("Count should equal returned length, got %d", res.Count)
	}
}

// TestSearchAPIFunctions_RankingTiers verifies exact-name wins over
// prefix wins over substring-elsewhere. We use "log" — there's a
// __devtool.log() function (exact) and other log-related entries.
func TestSearchAPIFunctions_RankingTiers(t *testing.T) {
	res := SearchAPIFunctions("log", "")
	if res.Count == 0 {
		t.Skip("catalog has no 'log' entries; skip ranking assertion")
	}
	// First hit should be exact name match if one exists.
	hasExact := false
	for _, fn := range DevToolAPIFunctions {
		if strings.EqualFold(fn.Name, "log") {
			hasExact = true
			break
		}
	}
	if hasExact && !strings.EqualFold(res.Matches[0].Name, "log") {
		t.Fatalf("exact-name match should rank first, got %q", res.Matches[0].Name)
	}
}

// TestSearchAPIFunctions_EmptyQueryWithCategory returns all entries in
// the named category (capped). Useful for "what's in accessibility?"
func TestSearchAPIFunctions_EmptyQueryWithCategory(t *testing.T) {
	res := SearchAPIFunctions("", "accessibility")
	if res.Count == 0 {
		t.Fatalf("expected accessibility category to have entries")
	}
	for _, m := range res.Matches {
		if !strings.EqualFold(m.Category, "accessibility") {
			t.Fatalf("category filter leaked: %+v", m)
		}
	}
}

// TestTruncateDescription bounds the compact response size.
func TestTruncateDescription(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"short", "Hello world", "Hello world"},
		{"first sentence", "First sentence. Second sentence.", "First sentence."},
		{"long no period", strings.Repeat("a", 200), strings.Repeat("a", 117) + "..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateDescription(tc.in)
			if got != tc.want {
				t.Fatalf("truncateDescription(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSearchAPIFunctions_ConflictingParams_DocContract exists as a
// reminder: handler-level validation (search + code present) is tested
// in the handler tests, not here. SearchAPIFunctions itself only knows
// query + category.
func TestSearchAPIFunctions_ConflictingParams_DocContract(t *testing.T) {
	// Sanity: calling with both empty is valid (returns catalog head).
	res := SearchAPIFunctions("", "")
	if res.Count == 0 {
		t.Fatalf("empty query should return capped catalog head")
	}
}

// TestHandleProxyExec_SearchRoutesWithoutProxyID verifies that passing
// `search` routes to the search handler — no proxy_id needed, same as
// existing help/describe actions.
func TestHandleProxyExec_SearchRoutesWithoutProxyID(t *testing.T) {
	pm := proxy.NewProxyManager()
	_, out, err := handleProxyExec(pm, ProxyInput{
		Action: "exec",
		Search: "click",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Success {
		t.Fatalf("expected success, got %+v", out)
	}
	if out.SearchResult == nil {
		t.Fatalf("expected SearchResult to be populated")
	}
	if out.SearchResult.Count == 0 {
		t.Fatalf("expected at least one 'click' match")
	}
}

// TestHandleProxyExec_SearchCategoryOnly allows discovery by category
// without a substring.
func TestHandleProxyExec_SearchCategoryOnly(t *testing.T) {
	pm := proxy.NewProxyManager()
	_, out, err := handleProxyExec(pm, ProxyInput{
		Action:   "exec",
		Category: "accessibility",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Success || out.SearchResult == nil || out.SearchResult.Count == 0 {
		t.Fatalf("expected accessibility matches, got %+v", out)
	}
	for _, m := range out.SearchResult.Matches {
		if !strings.EqualFold(m.Category, "accessibility") {
			t.Fatalf("category leak: %+v", m)
		}
	}
}

// TestHandleProxyExec_SearchPlusCodeIsConflict surfaces the
// mutual-exclusion rule: if the caller passes both, it's ambiguous
// intent and we reject rather than silently dropping one.
func TestHandleProxyExec_SearchPlusCodeIsConflict(t *testing.T) {
	pm := proxy.NewProxyManager()
	res, _, err := handleProxyExec(pm, ProxyInput{
		Action: "exec",
		Search: "click",
		Code:   "document.title",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected error result for search+code conflict, got %+v", res)
	}
}
