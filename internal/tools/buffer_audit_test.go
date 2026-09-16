package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// bufferAuditFixtureJSON is a deterministic AI-optimized (non-raw) audit
// object carrying every finding type of both buffer audits. Each entry
// carries exactly the fields the AI-optimized findingsByType projection in
// audit-api.js / audit-loading.js emits: {id, severity, selector, message}
// plus `template` for n-plus-one. Detection output shape mirrors
// internal/proxy/scripts/audit-api.js + audit-loading.js.
const bufferAuditFixtureJSON = `{
  "score": 62,
  "grade": "D",
  "summary": "2 warnings, 1 info",
  "findingsByType": {
    "waterfall": [
      {
        "id": "aa000001",
        "severity": "critical",
        "selector": "https://x/api/a",
        "message": "3 calls run serially, ~210ms wasted vs parallel"
      }
    ],
    "n-plus-one": [
      {
        "id": "aa000002",
        "severity": "warning",
        "selector": "https://x/api/items/1",
        "message": "7× GET /api/items/{id} → batch",
        "template": "GET /api/items/{id}"
      }
    ],
    "duplicate-call": [
      {
        "id": "aa000003",
        "severity": "warning",
        "selector": "https://x/api/config",
        "message": "GET /api/config fetched 4× in 800ms"
      }
    ],
    "chatty-load": [
      {
        "id": "aa000004",
        "severity": "info",
        "selector": "",
        "message": "23 API calls in the first 3s of load"
      }
    ],
    "spinner-cascade": [
      {
        "id": "bb000001",
        "severity": "warning",
        "selector": "#content .spinner",
        "message": "3 loaders fired serially over 2400ms"
      }
    ],
    "spinner-fragmentation": [
      {
        "id": "bb000002",
        "severity": "info",
        "selector": "#grid",
        "message": "4 concurrent sub-loaders under #grid"
      }
    ]
  }
}`

// bufferAuditNPlusOneCallURLs are the recorded call URLs the fixture's
// n-plus-one template "GET /api/items/{id}" was generalized from.
var bufferAuditNPlusOneCallURLs = []string{
	"https://x/api/items/1",
	"https://x/api/items/2",
	"https://x/api/items/3",
	"https://x/api/items/4",
	"https://x/api/items/5",
	"https://x/api/items/6",
	"https://x/api/items/7",
}

func bufferAuditFixture(t *testing.T) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(bufferAuditFixtureJSON), &parsed); err != nil {
		t.Fatalf("fixture unmarshal: %v", err)
	}
	return parsed
}

// extractURLPattern pulls the url_pattern value out of a proxylog query
// next-action string of the form
// `proxylog {action:"query", proxy_id:"dev", url_pattern:"<pattern>"}`.
func extractURLPattern(t *testing.T, next string) string {
	t.Helper()
	const marker = `url_pattern:"`
	i := strings.Index(next, marker)
	if i < 0 {
		t.Fatalf("next action %q carries no url_pattern", next)
	}
	rest := next[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("next action %q has unterminated url_pattern", next)
	}
	return rest[:j]
}

// TestBufferAuditCompactEveryFindingHasIDAndNext: every compact finding line
// of both buffer-audit specs must be followed by an `id:` and a `next:` line
// rendered by the single shared formatter.
func TestBufferAuditCompactEveryFindingHasIDAndNext(t *testing.T) {
	for _, spec := range []bufferAuditSpec{apiAuditSpec, loadingAuditSpec} {
		t.Run(spec.module, func(t *testing.T) {
			out := formatBufferAuditCompact(spec.headline, bufferAuditFixture(t), "dev", AuditProfileFull)
			lines := strings.Split(out, "\n")
			finding, id, next := 0, 0, 0
			for _, ln := range lines {
				trim := strings.TrimSpace(ln)
				if strings.HasPrefix(trim, "[") && strings.Contains(trim, "]") {
					finding++
					continue
				}
				if strings.HasPrefix(trim, "id: ") {
					id++
				}
				if strings.HasPrefix(trim, "next: ") {
					next++
				}
			}
			if finding == 0 {
				t.Fatalf("%s: no finding lines rendered", spec.module)
			}
			if id != finding || next != finding {
				t.Errorf("%s: %d findings but %d id: lines and %d next: lines; output:\n%s",
					spec.module, finding, id, next, out)
			}
		})
	}
}

// TestBufferAuditNextActionCarriesURLPatternFromFinding: n-plus-one and
// duplicate-call findings render a proxylog query whose url_pattern is taken
// from the finding itself (truncated template for n+1, url selector
// otherwise).
func TestBufferAuditNextActionCarriesURLPatternFromFinding(t *testing.T) {
	out := formatBufferAuditCompact(apiAuditSpec.headline, bufferAuditFixture(t), "dev", AuditProfileFull)
	wantN1 := `next: proxylog {action:"query", proxy_id:"dev", url_pattern:"/api/items/"}`
	if !strings.Contains(out, wantN1) {
		t.Errorf("n-plus-one next line missing truncated-template url_pattern; want line:\n%s\nin output:\n%s", wantN1, out)
	}
	wantDup := `next: proxylog {action:"query", proxy_id:"dev", url_pattern:"https://x/api/config"}`
	if !strings.Contains(out, wantDup) {
		t.Errorf("duplicate-call next line missing url_pattern from finding selector; want line:\n%s\nin output:\n%s", wantDup, out)
	}
	if !strings.Contains(out, `next: proxylog {action:"summary", proxy_id:"dev"}`) {
		t.Errorf("waterfall next must be a proxylog summary action; output:\n%s", out)
	}
	if !strings.Contains(out, `next: currentpage {action:"layout", proxy_id:"dev"}`) {
		t.Errorf("spinner findings next must be a currentpage layout action; output:\n%s", out)
	}
}

// TestBufferAuditNPlusOnePatternSubstringMatchesCallURLs: the n-plus-one
// next: url_pattern — the template path truncated before the first {id} —
// must substring-match every recorded request URL in the N+1 group, in both
// the compact and the raw rendering, so the pattern round-trips into a
// proxylog query that actually selects the group.
func TestBufferAuditNPlusOnePatternSubstringMatchesCallURLs(t *testing.T) {
	// Compact path.
	out := formatBufferAuditCompact(apiAuditSpec.headline, bufferAuditFixture(t), "dev", AuditProfileFull)
	var compactPattern string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, `next: proxylog {action:"query"`) && strings.Contains(ln, "/api/items") {
			compactPattern = extractURLPattern(t, strings.TrimSpace(ln)[len("next: "):])
		}
	}
	if compactPattern == "" {
		t.Fatalf("compact output has no n-plus-one proxylog query next line:\n%s", out)
	}
	for _, u := range bufferAuditNPlusOneCallURLs {
		if !strings.Contains(u, compactPattern) {
			t.Errorf("compact url_pattern %q does not substring-match group call URL %q", compactPattern, u)
		}
	}

	// Raw path: annotateBufferAuditNext over a raw (verbose) finding set.
	raw := map[string]any{
		"findings": []any{
			map[string]any{
				"id":       "aa000002",
				"type":     "n-plus-one",
				"severity": "warning",
				"selector": "https://x/api/items/1",
				"message":  "7× GET /api/items/{id} → batch",
				"template": "GET /api/items/{id}",
			},
		},
	}
	annotateBufferAuditNext(raw, "dev")
	f := raw["findings"].([]any)[0].(map[string]any)
	next, ok := f["next"].(string)
	if !ok || next == "" {
		t.Fatalf("raw finding carries no next action: %v", f)
	}
	rawPattern := extractURLPattern(t, next)
	for _, u := range bufferAuditNPlusOneCallURLs {
		if !strings.Contains(u, rawPattern) {
			t.Errorf("raw url_pattern %q does not substring-match group call URL %q", rawPattern, u)
		}
	}
	if rawPattern != compactPattern {
		t.Errorf("raw and compact patterns differ: raw=%q compact=%q", rawPattern, compactPattern)
	}
}

// TestBufferAuditProfileBugTopFive: the bug profile projects to at most 5
// findings, severity-ranked, for both specs.
func TestBufferAuditProfileBugTopFive(t *testing.T) {
	for _, spec := range []bufferAuditSpec{apiAuditSpec, loadingAuditSpec} {
		t.Run(spec.module, func(t *testing.T) {
			out := formatBufferAuditCompact(spec.headline, bufferAuditFixture(t), "dev", AuditProfileBug)
			count := 0
			for _, ln := range strings.Split(out, "\n") {
				trim := strings.TrimSpace(ln)
				if strings.HasPrefix(trim, "[") && strings.Contains(trim, "]") {
					count++
				}
			}
			if count > 5 {
				t.Errorf("%s: bug profile rendered %d findings, want <= 5", spec.module, count)
			}
			if !strings.Contains(out, "[critical]") {
				t.Errorf("%s: bug profile must keep the critical waterfall finding; output:\n%s", spec.module, out)
			}
		})
	}
}

// TestBufferAuditProfileFullUnchanged: the full profile is the identity
// projection — every fixture finding renders, in the grouped-by-type golden
// order, each with its id and next lines.
func TestBufferAuditProfileFullUnchanged(t *testing.T) {
	golden := `=== API Efficiency Audit: D (62) ===
2 warnings, 1 info

chatty-load (1)
  [info] 23 API calls in the first 3s of load
  id: aa000004
  next: proxylog {action:"query", proxy_id:"dev", url_pattern:""}

duplicate-call (1)
  [warning] https://x/api/config — GET /api/config fetched 4× in 800ms
  id: aa000003
  next: proxylog {action:"query", proxy_id:"dev", url_pattern:"https://x/api/config"}

n-plus-one (1)
  [warning] https://x/api/items/1 — 7× GET /api/items/{id} → batch
  id: aa000002
  next: proxylog {action:"query", proxy_id:"dev", url_pattern:"/api/items/"}

spinner-cascade (1)
  [warning] #content .spinner — 3 loaders fired serially over 2400ms
  id: bb000001
  next: currentpage {action:"layout", proxy_id:"dev"}

spinner-fragmentation (1)
  [info] #grid — 4 concurrent sub-loaders under #grid
  id: bb000002
  next: currentpage {action:"layout", proxy_id:"dev"}

waterfall (1)
  [critical] https://x/api/a — 3 calls run serially, ~210ms wasted vs parallel
  id: aa000001
  next: proxylog {action:"summary", proxy_id:"dev"}`
	for _, profile := range []string{"", AuditProfileFull, AuditProfileRelease} {
		t.Run("profile="+profile, func(t *testing.T) {
			got := formatBufferAuditCompact(apiAuditSpec.headline, bufferAuditFixture(t), "dev", profile)
			if got != golden {
				t.Errorf("output drifted from golden:\n--- want ---\n%s\n--- got ---\n%s", golden, got)
			}
		})
	}
}
