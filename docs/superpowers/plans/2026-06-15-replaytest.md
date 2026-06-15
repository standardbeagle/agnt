# Replay-Test Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a license-gated front-end testing pipeline that records real API traffic + interactions through the agnt proxy, replays them from an in-page Web Worker mock (no network), and drives chromedp through a deterministic seed replay plus subagent-driven exploration to catch JS crashes and DOM-assertion regressions.

**Architecture:** New package `internal/replaytest/` with focused units (scenario model, matcher, worker-bundle codegen, recorder, refine, driver, report). A single action-dispatched MCP tool `replaytest` gates every mutating action behind `license.CapAdvancedTesting`. Recordings persist as JSON under `.agnt/replaytests/`. Worker-mock JS is generated in-memory and injected by the existing proxy injector only when a replay session is active.

**Tech Stack:** Go 1.24, `go-sdk/mcp`, existing `internal/proxy` (TrafficLogger, injector), `internal/chromedp`, `internal/license`, `internal/incident`, blake3 hashing, testify.

**Spec:** `docs/superpowers/specs/2026-06-15-replaytest-design.md`

---

## File Structure

| File | Responsibility |
|------|----------------|
| `internal/replaytest/scenario.go` | Scenario / Step / Recording / Assertion types + JSON load/save + path-param templating + blob out-of-lining. |
| `internal/replaytest/match.go` | Pure request→recording matcher: build match key, normalize path, sort query keys, body signature, ordered hits queue. Go mirror of the JS matcher. |
| `internal/replaytest/domsig.go` | Normalized DOM signature (strip masked nodes, collapse whitespace, drop volatile attrs) → blake3. |
| `internal/replaytest/fuzz.go` | Fuzz preset registry; each mutates a response body copy, never the on-disk Scenario. |
| `internal/replaytest/worker_bundle.go` | Pure codegen: emit main-thread shim JS + Web Worker JS with recordings embedded. |
| `internal/replaytest/recorder.go` | Pull `TrafficLogger.Query` over a record window; assemble a Scenario from HTTP/Response/Interaction/Mutation entries. |
| `internal/replaytest/refine.go` | One-time AI pass: mask dynamic DOM noise, flag high-signal assertions; write back into Scenario. |
| `internal/replaytest/driver.go` | Inject bundle, drive chromedp seed lane, collect JS errors + assertion results. |
| `internal/replaytest/report.go` | Pass/fail rollup struct + JSON persistence + incident emission. |
| `internal/replaytest/store.go` | On-disk layout helpers: `.agnt/replaytests/<name>.json`, `<name>.report.json`. |
| `internal/tools/replaytest_tool.go` | MCP tool `replaytest`: action dispatch + license gating + session scoping. |
| `internal/tools/replaytest_tool_test.go` | License-gate + dispatch tests. |

Each `*.go` gets a sibling `*_test.go`. Tasks are ordered so each builds on committed, tested predecessors.

---

## Task 1: Scenario data model

**Files:**
- Create: `internal/replaytest/scenario.go`
- Test: `internal/replaytest/scenario_test.go`

- [ ] **Step 1: Write the failing test**

```go
package replaytest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScenarioRoundTrip(t *testing.T) {
	s := &Scenario{
		Name:    "demo",
		Version: 1,
		BaseURL: "http://localhost:3000",
		Steps: []Step{{
			Index: 0, Kind: StepNavigate, Selector: "a",
			DOMSignature: "blake3:abc",
			Assertions:   []Assertion{{Selector: "h1", Type: AssertText, Expect: "Today"}},
		}},
		Recordings: []Recording{{
			Match:  MatchKey{Method: "GET", Path: "/api/items", QueryKeys: []string{"date"}},
			Status: 200, Headers: map[string]string{"content-type": "application/json"},
			BodyRef: "blob:0", Hits: 3,
		}},
		Blobs: map[string]string{"blob:0": `{"ok":true}`},
	}
	data, err := s.MarshalJSON()
	require.NoError(t, err)
	got, err := UnmarshalScenario(data)
	require.NoError(t, err)
	assert.Equal(t, s.Name, got.Name)
	assert.Equal(t, s.Steps[0].Assertions[0].Expect, got.Steps[0].Assertions[0].Expect)
	assert.Equal(t, 3, got.Recordings[0].Hits)
	assert.Equal(t, `{"ok":true}`, got.Blobs["blob:0"])
	assert.Equal(t, []string{"date"}, got.Recordings[0].Match.QueryKeys)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/replaytest/ -run TestScenarioRoundTrip -v`
Expected: FAIL — undefined types.

- [ ] **Step 3: Write minimal implementation**

```go
package replaytest

import "encoding/json"

type StepKind string

const (
	StepNavigate StepKind = "navigate"
	StepClick    StepKind = "click"
	StepInput    StepKind = "input"
	StepSubmit   StepKind = "submit"
)

type AssertType string

const (
	AssertText    AssertType = "text"
	AssertPresent AssertType = "present"
)

type Assertion struct {
	Selector string     `json:"selector"`
	Type     AssertType `json:"type"`
	Expect   string     `json:"expect"`
	Mask     bool       `json:"mask"`
}

type Step struct {
	Index        int         `json:"index"`
	Kind         StepKind    `json:"kind"`
	Selector     string      `json:"selector"`
	Value        string      `json:"value,omitempty"`
	DOMSignature string      `json:"dom_signature"`
	Assertions   []Assertion `json:"assertions"`
}

type MatchKey struct {
	Method    string   `json:"method"`
	Path      string   `json:"path"`
	QueryKeys []string `json:"query_keys"`
}

type Recording struct {
	Match          MatchKey          `json:"match"`
	RequestBodySig string            `json:"request_body_sig,omitempty"`
	Status         int               `json:"status"`
	Headers        map[string]string `json:"headers"`
	BodyRef        string            `json:"body_ref"`
	Hits           int               `json:"hits"`
}

type Scenario struct {
	Name       string            `json:"name"`
	Version    int               `json:"version"`
	RecordedAt string            `json:"recorded_at"`
	BaseURL    string            `json:"base_url"`
	Steps      []Step            `json:"steps"`
	Recordings []Recording       `json:"recordings"`
	Blobs      map[string]string `json:"blobs"`
}

func (s *Scenario) MarshalJSON() ([]byte, error) {
	type alias Scenario
	return json.MarshalIndent((*alias)(s), "", "  ")
}

func UnmarshalScenario(data []byte) (*Scenario, error) {
	var s Scenario
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/replaytest/ -run TestScenarioRoundTrip -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/scenario.go internal/replaytest/scenario_test.go
git commit -m "feat(replaytest): scenario data model"
```

---

## Task 2: Path-param templating

**Files:**
- Modify: `internal/replaytest/scenario.go` (add `TemplatePath`)
- Test: `internal/replaytest/scenario_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestTemplatePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/api/items/42", "/api/items/:id"},
		{"/api/items/42/notes/7", "/api/items/:id/notes/:id"},
		{"/api/users/abc123def456ghi", "/api/users/:id"}, // long opaque id
		{"/api/items", "/api/items"},                      // no params
		{"/api/v2/items", "/api/v2/items"},                // v2 is not an id
	}
	for _, c := range cases {
		assert.Equal(t, c.want, TemplatePath(c.in), "input %q", c.in)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/replaytest/ -run TestTemplatePath -v`
Expected: FAIL — undefined `TemplatePath`.

- [ ] **Step 3: Write minimal implementation**

```go
import (
	"regexp"
	"strings"
)

// idSegment matches a path segment that looks like a volatile identifier:
// pure digits, or a long (>=12 char) opaque token (hex/uuid-like).
var idSegment = regexp.MustCompile(`^([0-9]+|[0-9a-fA-F-]{12,})$`)

// TemplatePath replaces identifier-looking path segments with ":id" so that
// recordings match across differing ids at replay time.
func TemplatePath(path string) string {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if idSegment.MatchString(seg) {
			segs[i] = ":id"
		}
	}
	return strings.Join(segs, "/")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/replaytest/ -run TestTemplatePath -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/scenario.go internal/replaytest/scenario_test.go
git commit -m "feat(replaytest): path-param templating"
```

---

## Task 3: Request matcher

**Files:**
- Create: `internal/replaytest/match.go`
- Test: `internal/replaytest/match_test.go`

- [ ] **Step 1: Write the failing test**

```go
package replaytest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatcherKeyAndQueue(t *testing.T) {
	recs := []Recording{
		{Match: MatchKey{Method: "GET", Path: "/api/items", QueryKeys: []string{"date"}}, Status: 200, BodyRef: "b0", Hits: 1},
		{Match: MatchKey{Method: "GET", Path: "/api/items", QueryKeys: []string{"date"}}, Status: 200, BodyRef: "b1", Hits: 1},
		{Match: MatchKey{Method: "POST", Path: "/api/items"}, RequestBodySig: "sig1", Status: 201, BodyRef: "b2", Hits: 1},
	}
	m := NewMatcher(recs)

	// query-key order must not matter; templated path used.
	r1, ok := m.Match("GET", "/api/items/99?date=2026-06-15&_=1", "")
	require.True(t, ok)
	assert.Equal(t, "b0", r1.BodyRef)
	r2, ok := m.Match("GET", "/api/items?date=x", "")
	require.True(t, ok)
	assert.Equal(t, "b1", r2.BodyRef) // queue advanced

	_, ok = m.Match("GET", "/api/items?date=x", "")
	assert.False(t, ok) // queue exhausted -> miss

	rp, ok := m.Match("POST", "/api/items", "sig1")
	require.True(t, ok)
	assert.Equal(t, 201, rp.Status)
}

func TestMatchKeyString(t *testing.T) {
	k := buildKey("get", "/api/items/5?b=2&a=1", "")
	assert.Equal(t, "GET /api/items/:id ?a,b", k)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/replaytest/ -run 'TestMatcher|TestMatchKey' -v`
Expected: FAIL — undefined `NewMatcher`/`buildKey`.

- [ ] **Step 3: Write minimal implementation**

```go
package replaytest

import (
	"net/url"
	"sort"
	"strings"
)

// buildKey produces the canonical match key for a live request: uppercased
// method, templated path, sorted query-key names, and body signature appended
// when present. Query VALUES are ignored; only key names participate.
func buildKey(method, rawURL, bodySig string) string {
	method = strings.ToUpper(method)
	path := rawURL
	query := ""
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		path, query = rawURL[:i], rawURL[i+1:]
	}
	path = TemplatePath(path)

	var keys []string
	if query != "" {
		if vals, err := url.ParseQuery(query); err == nil {
			for k := range vals {
				if k == "_" { // common cache-buster
					continue
				}
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	key := method + " " + path
	if len(keys) > 0 {
		key += " ?" + strings.Join(keys, ",")
	}
	if bodySig != "" {
		key += " #" + bodySig
	}
	return key
}

func recKey(r Recording) string {
	keys := append([]string(nil), r.Match.QueryKeys...)
	sort.Strings(keys)
	key := strings.ToUpper(r.Match.Method) + " " + TemplatePath(r.Match.Path)
	if len(keys) > 0 {
		key += " ?" + strings.Join(keys, ",")
	}
	if r.RequestBodySig != "" {
		key += " #" + r.RequestBodySig
	}
	return key
}

// Matcher resolves live requests to recordings, advancing an ordered queue per
// key so repeated identical calls return successive recordings.
type Matcher struct {
	queues map[string][]Recording
}

func NewMatcher(recs []Recording) *Matcher {
	q := make(map[string][]Recording)
	for _, r := range recs {
		k := recKey(r)
		n := r.Hits
		if n < 1 {
			n = 1
		}
		for i := 0; i < n; i++ {
			q[k] = append(q[k], r)
		}
	}
	return &Matcher{queues: q}
}

// Match returns the next recording for the request, or ok=false on a miss.
func (m *Matcher) Match(method, rawURL, bodySig string) (Recording, bool) {
	k := buildKey(method, rawURL, bodySig)
	q := m.queues[k]
	if len(q) == 0 {
		return Recording{}, false
	}
	r := q[0]
	m.queues[k] = q[1:]
	return r, true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/replaytest/ -run 'TestMatcher|TestMatchKey' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/match.go internal/replaytest/match_test.go
git commit -m "feat(replaytest): request matcher with ordered hits queue"
```

---

## Task 4: Fuzz presets

**Files:**
- Create: `internal/replaytest/fuzz.go`
- Test: `internal/replaytest/fuzz_test.go`

- [ ] **Step 1: Write the failing test**

```go
package replaytest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFuzzPresetsTransformAndPreserveInput(t *testing.T) {
	orig := `{"items":[{"id":1,"name":"a"}],"count":1}`
	for _, name := range PresetNames() {
		p, ok := Preset(name)
		require.True(t, ok, name)
		in := orig
		out := p.Apply(200, in)
		// Input string is never mutated in place (Go strings are immutable, but
		// assert the contract that orig is untouched and out differs meaningfully).
		assert.Equal(t, orig, in, "preset %s mutated input", name)
		switch name {
		case "empty_array":
			assert.Contains(t, out.Body, `[]`)
		case "http_error":
			assert.GreaterOrEqual(t, out.Status, 500)
		case "truncated_json":
			var v any
			assert.Error(t, json.Unmarshal([]byte(out.Body), &v), "should be invalid json")
		case "null_fields":
			assert.Contains(t, out.Body, `null`)
		}
	}
}

func TestFuzzUnknownPreset(t *testing.T) {
	_, ok := Preset("does_not_exist")
	assert.False(t, ok)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/replaytest/ -run TestFuzz -v`
Expected: FAIL — undefined `Preset`/`PresetNames`.

- [ ] **Step 3: Write minimal implementation**

```go
package replaytest

import (
	"encoding/json"
	"sort"
	"strings"
)

// FuzzResult is the post-mutation response a preset yields.
type FuzzResult struct {
	Status int
	Body   string
}

// FuzzPreset perturbs a recorded response to probe front-end resilience.
type FuzzPreset struct {
	Name  string
	Apply func(status int, body string) FuzzResult
}

var presets = map[string]FuzzPreset{
	"empty_array": {Name: "empty_array", Apply: func(s int, b string) FuzzResult {
		return FuzzResult{Status: s, Body: replaceArraysWithEmpty(b)}
	}},
	"http_error": {Name: "http_error", Apply: func(s int, b string) FuzzResult {
		return FuzzResult{Status: 500, Body: `{"error":"injected"}`}
	}},
	"truncated_json": {Name: "truncated_json", Apply: func(s int, b string) FuzzResult {
		if len(b) > 1 {
			b = b[:len(b)/2]
		}
		return FuzzResult{Status: s, Body: b}
	}},
	"null_fields": {Name: "null_fields", Apply: func(s int, b string) FuzzResult {
		return FuzzResult{Status: s, Body: nullifyLeafValues(b)}
	}},
	"reordered": {Name: "reordered", Apply: func(s int, b string) FuzzResult {
		return FuzzResult{Status: s, Body: reverseTopArray(b)}
	}},
	"type_flip": {Name: "type_flip", Apply: func(s int, b string) FuzzResult {
		return FuzzResult{Status: s, Body: flipScalarTypes(b)}
	}},
}

func Preset(name string) (FuzzPreset, bool) { p, ok := presets[name]; return p, ok }

func PresetNames() []string {
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func replaceArraysWithEmpty(b string) string {
	var v any
	if json.Unmarshal([]byte(b), &v) != nil {
		return b
	}
	out, _ := json.Marshal(emptyArrays(v))
	return string(out)
}

func emptyArrays(v any) any {
	switch t := v.(type) {
	case []any:
		return []any{}
	case map[string]any:
		for k, vv := range t {
			t[k] = emptyArrays(vv)
		}
		return t
	default:
		return v
	}
}

func nullifyLeafValues(b string) string {
	var v any
	if json.Unmarshal([]byte(b), &v) != nil {
		return b
	}
	out, _ := json.Marshal(nullifyLeaves(v))
	return string(out)
}

func nullifyLeaves(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k := range t {
			t[k] = nil
		}
		return t
	case []any:
		for i := range t {
			t[i] = nil
		}
		return t
	default:
		return nil
	}
}

func reverseTopArray(b string) string {
	var arr []any
	if json.Unmarshal([]byte(b), &arr) != nil {
		return b
	}
	for i, j := 0, len(arr)-1; i < j; i, j = i+1, j-1 {
		arr[i], arr[j] = arr[j], arr[i]
	}
	out, _ := json.Marshal(arr)
	return string(out)
}

func flipScalarTypes(b string) string {
	// Turn numbers into strings and vice-versa at the top object level.
	var m map[string]any
	if json.Unmarshal([]byte(b), &m) != nil {
		return b
	}
	for k, v := range m {
		switch tv := v.(type) {
		case float64:
			m[k] = "flipped"
		case string:
			m[k] = len(tv)
		}
	}
	out, _ := json.Marshal(m)
	return string(out)
}

var _ = strings.TrimSpace // keep strings import if later edits drop usages
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/replaytest/ -run TestFuzz -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/fuzz.go internal/replaytest/fuzz_test.go
git commit -m "feat(replaytest): fuzz preset registry"
```

---

## Task 5: DOM signature

**Files:**
- Create: `internal/replaytest/domsig.go`
- Test: `internal/replaytest/domsig_test.go`

Uses the blake3 dependency already in the module (`grep -r blake3 go.mod`; if absent, `go get lukechampine.com/blake3` first and commit `go.mod`/`go.sum` in this task's Step 5).

- [ ] **Step 1: Write the failing test**

```go
package replaytest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDOMSignatureNormalizesNoise(t *testing.T) {
	a := `<div>  Today   <span data-ts="123">x</span></div>`
	b := `<div>Today <span data-ts="999">x</span></div>`
	// volatile attr data-ts and whitespace differences must collapse.
	sigA := DOMSignature(a, []string{"data-ts"})
	sigB := DOMSignature(b, []string{"data-ts"})
	assert.Equal(t, sigA, sigB)

	c := `<div>Yesterday <span data-ts="1">x</span></div>`
	assert.NotEqual(t, sigA, DOMSignature(c, []string{"data-ts"}))
	assert.Contains(t, sigA, "blake3:")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/replaytest/ -run TestDOMSignature -v`
Expected: FAIL — undefined `DOMSignature`.

- [ ] **Step 3: Write minimal implementation**

```go
package replaytest

import (
	"encoding/hex"
	"regexp"
	"strings"

	"lukechampine.com/blake3"
)

var wsRun = regexp.MustCompile(`\s+`)

// DOMSignature returns a stable hash of an HTML fragment with volatile
// attributes removed and whitespace collapsed, so cosmetic noise does not
// register as a regression.
func DOMSignature(html string, volatileAttrs []string) string {
	norm := html
	for _, attr := range volatileAttrs {
		re := regexp.MustCompile(regexp.QuoteMeta(attr) + `="[^"]*"`)
		norm = re.ReplaceAllString(norm, "")
	}
	norm = wsRun.ReplaceAllString(norm, " ")
	norm = strings.TrimSpace(norm)
	sum := blake3.Sum256([]byte(norm))
	return "blake3:" + hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/replaytest/ -run TestDOMSignature -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/domsig.go internal/replaytest/domsig_test.go go.mod go.sum
git commit -m "feat(replaytest): normalized DOM signature"
```

---

## Task 6: Worker-bundle codegen

**Files:**
- Create: `internal/replaytest/worker_bundle.go`
- Test: `internal/replaytest/worker_bundle_test.go`

- [ ] **Step 1: Write the failing test**

```go
package replaytest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerBundleEmitsShimAndWorker(t *testing.T) {
	s := &Scenario{
		Name: "demo", BaseURL: "http://x",
		Recordings: []Recording{{Match: MatchKey{Method: "GET", Path: "/api/items"}, Status: 200, BodyRef: "b0", Hits: 1}},
		Blobs:      map[string]string{"b0": `{"ok":true}`},
	}
	js, err := GenerateBundle(s, "empty_array")
	require.NoError(t, err)
	// shim overrides network primitives
	assert.Contains(t, js, "window.fetch")
	assert.Contains(t, js, "XMLHttpRequest")
	// recordings + miss sentinel + active preset present
	assert.Contains(t, js, "__replay_miss")
	assert.Contains(t, js, `"b0"`)
	assert.Contains(t, js, "empty_array")
	// worker is spawned from an inline blob, not a network URL
	assert.Contains(t, js, "Blob(")
	assert.True(t, strings.Count(js, "postMessage") >= 1)
}

func TestWorkerBundleNoPresetIsClean(t *testing.T) {
	s := &Scenario{Name: "d", Recordings: nil, Blobs: map[string]string{}}
	js, err := GenerateBundle(s, "")
	require.NoError(t, err)
	assert.Contains(t, js, `"activePreset":""`)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/replaytest/ -run TestWorkerBundle -v`
Expected: FAIL — undefined `GenerateBundle`.

- [ ] **Step 3: Write minimal implementation**

Create `worker_bundle.go`. The Go side serializes the scenario recordings + active preset to JSON and embeds it into a static JS template (kept in a Go raw-string constant). The JS template contains the shim and the worker source; the worker performs match + mutate. Mutation in JS mirrors `fuzz.go` for the presets used in the worker.

```go
package replaytest

import (
	"encoding/json"
	"fmt"
)

// bundlePayload is the JSON handed to the in-page worker at boot.
type bundlePayload struct {
	Recordings   []Recording       `json:"recordings"`
	Blobs        map[string]string `json:"blobs"`
	ActivePreset string            `json:"activePreset"`
}

// GenerateBundle returns the full JavaScript (shim + worker) to inject for a
// replay session. Pure function: same inputs -> same output.
func GenerateBundle(s *Scenario, preset string) (string, error) {
	payload := bundlePayload{Recordings: s.Recordings, Blobs: s.Blobs, ActivePreset: preset}
	if payload.Blobs == nil {
		payload.Blobs = map[string]string{}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(bundleTemplate, string(data)), nil
}

// bundleTemplate: %s is replaced by the JSON payload. The worker source is
// embedded as a string and spawned via Blob URL so no extra HTTP fetch occurs.
const bundleTemplate = `(function(){
  var PAYLOAD = %s;
  var workerSrc = ` + "`" + workerJS + "`" + `;
  var blob = new Blob([workerSrc], {type:'application/javascript'});
  var worker = new Worker(URL.createObjectURL(blob));
  worker.postMessage({type:'init', payload: PAYLOAD});

  var pending = {}, seq = 0;
  worker.onmessage = function(e){
    var m = e.data;
    if (m.type === 'reply' && pending[m.id]) { pending[m.id](m); delete pending[m.id]; }
  };
  function ask(method, url, body){
    return new Promise(function(resolve){
      var id = ++seq; pending[id] = resolve;
      worker.postMessage({type:'match', id:id, method:method, url:url, body:body||''});
    });
  }

  var realFetch = window.fetch;
  window.fetch = function(input, opts){
    opts = opts || {};
    var url = (typeof input === 'string') ? input : input.url;
    var method = opts.method || 'GET';
    return ask(method, url, opts.body).then(function(r){
      if (r.miss) { return new Response('{"__replay_miss":true}', {status:599}); }
      return new Response(r.body, {status:r.status, headers:r.headers});
    });
  };

  var RealXHR = window.XMLHttpRequest;
  function FakeXHR(){ this._h = {}; }
  FakeXHR.prototype.open = function(m,u){ this._m=m; this._u=u; };
  FakeXHR.prototype.setRequestHeader = function(k,v){ this._h[k]=v; };
  FakeXHR.prototype.send = function(body){
    var self=this;
    ask(self._m, self._u, body).then(function(r){
      self.status = r.miss ? 599 : r.status;
      self.responseText = r.miss ? '{"__replay_miss":true}' : r.body;
      self.readyState = 4;
      if (self.onreadystatechange) self.onreadystatechange();
      if (self.onload) self.onload();
    });
  };
  window.XMLHttpRequest = FakeXHR;
  window.__replay_active = true;
})();`
```

Then add the worker source constant (mirrors `match.go` key logic + the JS-side presets) in the same file:

```go
const workerJS = `
var QUEUES = {}, BLOBS = {}, PRESET = '';
function templatePath(p){ return p.split('/').map(function(s){
  return (/^([0-9]+|[0-9a-fA-F-]{12,})$/.test(s)) ? ':id' : s; }).join('/'); }
function buildKey(method, rawURL, bodySig){
  var path=rawURL, query='';
  var i=rawURL.indexOf('?'); if(i>=0){ path=rawURL.slice(0,i); query=rawURL.slice(i+1); }
  path=templatePath(path);
  var keys=[];
  if(query){ query.split('&').forEach(function(kv){ var k=kv.split('=')[0]; if(k && k!=='_') keys.push(k); }); }
  keys.sort();
  var key=method.toUpperCase()+' '+path;
  if(keys.length) key+=' ?'+keys.join(',');
  if(bodySig) key+=' #'+bodySig;
  return key;
}
function recKey(r){
  var keys=(r.match.query_keys||[]).slice().sort();
  var key=r.match.method.toUpperCase()+' '+templatePath(r.match.path);
  if(keys.length) key+=' ?'+keys.join(',');
  if(r.request_body_sig) key+=' #'+r.request_body_sig;
  return key;
}
function mutate(status, body){
  if(!PRESET) return {status:status, body:body};
  try {
    if(PRESET==='http_error') return {status:500, body:'{"error":"injected"}'};
    if(PRESET==='truncated_json') return {status:status, body: body.slice(0, Math.max(1, body.length/2|0))};
    var v=JSON.parse(body);
    if(PRESET==='empty_array'){ (function e(x){ if(Array.isArray(x)) return []; if(x&&typeof x==='object'){ for(var k in x) x[k]=Array.isArray(x[k])?[]:e(x[k]); } return x; })(v); }
    if(PRESET==='null_fields'){ if(v&&typeof v==='object'){ for(var k in v) v[k]=null; } }
    if(PRESET==='reordered'&&Array.isArray(v)) v.reverse();
    if(PRESET==='type_flip'&&v&&typeof v==='object'){ for(var k in v){ if(typeof v[k]==='number') v[k]='flipped'; else if(typeof v[k]==='string') v[k]=v[k].length; } }
    return {status:status, body:JSON.stringify(v)};
  } catch(e){ return {status:status, body:body}; }
}
self.onmessage=function(e){
  var m=e.data;
  if(m.type==='init'){
    PRESET=m.payload.activePreset||''; BLOBS=m.payload.blobs||{};
    (m.payload.recordings||[]).forEach(function(r){
      var k=recKey(r); var n=r.hits||1; QUEUES[k]=QUEUES[k]||[];
      for(var i=0;i<n;i++) QUEUES[k].push(r);
    });
    return;
  }
  if(m.type==='match'){
    var k=buildKey(m.method, m.url, '');
    var q=QUEUES[k];
    if(!q||!q.length){ self.postMessage({type:'reply', id:m.id, miss:true}); return; }
    var r=q.shift();
    var body=BLOBS[r.body_ref]||'';
    var mut=mutate(r.status, body);
    self.postMessage({type:'reply', id:m.id, status:mut.status, body:mut.body, headers:r.headers||{}});
  }
};`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/replaytest/ -run TestWorkerBundle -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/worker_bundle.go internal/replaytest/worker_bundle_test.go
git commit -m "feat(replaytest): worker-bundle codegen (shim + worker)"
```

---

## Task 7: On-disk store

**Files:**
- Create: `internal/replaytest/store.go`
- Test: `internal/replaytest/store_test.go`

- [ ] **Step 1: Write the failing test**

```go
package replaytest

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreSaveLoadList(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	s := &Scenario{Name: "alpha", Version: 1}
	require.NoError(t, st.SaveScenario(s))
	assert.FileExists(t, filepath.Join(dir, ".agnt", "replaytests", "alpha.json"))

	got, err := st.LoadScenario("alpha")
	require.NoError(t, err)
	assert.Equal(t, "alpha", got.Name)

	names, err := st.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha"}, names)

	_, err = st.LoadScenario("missing")
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/replaytest/ -run TestStore -v`
Expected: FAIL — undefined `NewStore`.

- [ ] **Step 3: Write minimal implementation**

```go
package replaytest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store persists Scenarios and reports under <projectDir>/.agnt/replaytests/.
type Store struct{ dir string }

func NewStore(projectDir string) *Store {
	return &Store{dir: filepath.Join(projectDir, ".agnt", "replaytests")}
}

func (s *Store) SaveScenario(sc *Scenario) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := sc.MarshalJSON()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, sc.Name+".json"), data, 0o644)
}

func (s *Store) LoadScenario(name string) (*Scenario, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, name+".json"))
	if err != nil {
		return nil, err
	}
	return UnmarshalScenario(data)
}

func (s *Store) SaveReport(name string, data []byte) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, name+".report.json"), data, 0o644)
}

func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".json") && !strings.HasSuffix(n, ".report.json") {
			names = append(names, strings.TrimSuffix(n, ".json"))
		}
	}
	sort.Strings(names)
	return names, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/replaytest/ -run TestStore -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/store.go internal/replaytest/store_test.go
git commit -m "feat(replaytest): on-disk scenario/report store"
```

---

## Task 8: Recorder (TrafficLogger → Scenario)

**Files:**
- Create: `internal/replaytest/recorder.go`
- Test: `internal/replaytest/recorder_test.go`

Read `internal/proxy/logger.go` first for the exact `HTTPLogEntry`, `InteractionEvent`, and `LogFilter` field names; the test below uses the canonical accessors. The recorder pulls via `TrafficLogger.Query(LogFilter)` over `[startedAt, now]` — it does NOT call `SetOnLogEntry` (already owned by the daemon hub).

- [ ] **Step 1: Write the failing test**

```go
package replaytest

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecorderAssemblesScenario(t *testing.T) {
	// Build synthetic log entries as the proxy would emit them.
	entries := []proxy.LogEntry{
		{Type: proxy.LogTypeInteraction, Interaction: &proxy.InteractionEvent{Kind: "click", Selector: "a#log"}},
		{Type: proxy.LogTypeHTTP, HTTP: &proxy.HTTPLogEntry{Method: "GET", URL: "/api/items/7?date=x", Status: 200,
			ResponseBody: `{"ok":true}`, ResponseHeaders: map[string]string{"content-type": "application/json"}}},
	}
	sc := AssembleScenario("demo", "http://localhost:3000", entries)
	require.NotNil(t, sc)
	assert.Equal(t, "demo", sc.Name)
	require.Len(t, sc.Recordings, 1)
	assert.Equal(t, "/api/items/:id", sc.Recordings[0].Match.Path) // templated
	assert.Equal(t, []string{"date"}, sc.Recordings[0].Match.QueryKeys)
	body := sc.Blobs[sc.Recordings[0].BodyRef]
	assert.Equal(t, `{"ok":true}`, body)
	require.Len(t, sc.Steps, 1)
	assert.Equal(t, StepClick, sc.Steps[0].Kind)
	assert.Equal(t, "a#log", sc.Steps[0].Selector)
}
```

> If the real field names differ (e.g. `InteractionEvent.Type` instead of `.Kind`, or `HTTPLogEntry.ResponseBody` is named differently), adjust the test AND `AssembleScenario` together to the actual struct — the behavior (templated path, extracted query keys, blob out-of-lining, step kind mapping) is the contract, not the field spelling.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/replaytest/ -run TestRecorder -v`
Expected: FAIL — undefined `AssembleScenario`.

- [ ] **Step 3: Write minimal implementation**

```go
package replaytest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// AssembleScenario converts a window of proxy log entries into a Scenario:
// interaction entries become ordered steps, HTTP entries become recordings with
// bodies out-of-lined into blobs.
func AssembleScenario(name, baseURL string, entries []proxy.LogEntry) *Scenario {
	sc := &Scenario{
		Name: name, Version: 1, BaseURL: baseURL,
		RecordedAt: time.Now().UTC().Format(time.RFC3339),
		Blobs:      map[string]string{},
	}
	blobN := 0
	for _, e := range entries {
		switch e.Type {
		case proxy.LogTypeInteraction:
			if e.Interaction == nil {
				continue
			}
			sc.Steps = append(sc.Steps, Step{
				Index:    len(sc.Steps),
				Kind:     mapInteractionKind(e.Interaction.Kind),
				Selector: e.Interaction.Selector,
				Value:    e.Interaction.Value,
			})
		case proxy.LogTypeHTTP:
			h := e.HTTP
			if h == nil {
				continue
			}
			path, keys := splitPathQuery(h.URL)
			ref := fmt.Sprintf("blob:%d", blobN)
			blobN++
			sc.Blobs[ref] = h.ResponseBody
			sc.Recordings = append(sc.Recordings, Recording{
				Match:          MatchKey{Method: h.Method, Path: TemplatePath(path), QueryKeys: keys},
				RequestBodySig: bodySig(h.RequestBody),
				Status:         h.Status,
				Headers:        h.ResponseHeaders,
				BodyRef:        ref,
				Hits:           1,
			})
		}
	}
	coalesceHits(sc)
	return sc
}

func mapInteractionKind(k string) StepKind {
	switch strings.ToLower(k) {
	case "click":
		return StepClick
	case "input", "change", "keydown":
		return StepInput
	case "submit":
		return StepSubmit
	default:
		return StepNavigate
	}
}

func splitPathQuery(rawURL string) (string, []string) {
	path := rawURL
	var keys []string
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		path = rawURL[:i]
		if vals, err := url.ParseQuery(rawURL[i+1:]); err == nil {
			for k := range vals {
				if k != "_" {
					keys = append(keys, k)
				}
			}
		}
	}
	return path, keys
}

func bodySig(body string) string {
	if body == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:8])
}

// coalesceHits merges consecutive identical recordings into a single entry with
// an incremented Hits count, preserving the body of each via the queue model.
func coalesceHits(sc *Scenario) {
	if len(sc.Recordings) < 2 {
		return
	}
	out := sc.Recordings[:1]
	for _, r := range sc.Recordings[1:] {
		last := &out[len(out)-1]
		if recKey(*last) == recKey(r) && last.BodyRef == r.BodyRef {
			last.Hits++
			continue
		}
		out = append(out, r)
	}
	sc.Recordings = out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/replaytest/ -run TestRecorder -v`
Expected: PASS. (If it fails on field names, fix per the Step-1 note, re-run.)

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/recorder.go internal/replaytest/recorder_test.go
git commit -m "feat(replaytest): recorder assembles scenario from proxy log window"
```

---

## Task 9: Report rollup

**Files:**
- Create: `internal/replaytest/report.go`
- Test: `internal/replaytest/report_test.go`

- [ ] **Step 1: Write the failing test**

```go
package replaytest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReportRollup(t *testing.T) {
	r := NewReport("demo")
	r.AddSeedResult("baseline", true, nil)
	r.AddSeedResult("empty_array", false, []string{"h1 expected 'Today' got ''"})
	r.AddCrash("/log", "button.add", "TypeError: x is undefined")
	assert.False(t, r.Passed())
	assert.Equal(t, 1, r.CrashCount())

	data, err := r.JSON()
	require.NoError(t, err)
	assert.Contains(t, string(data), "empty_array")
	assert.Contains(t, string(data), "TypeError")

	clean := NewReport("ok")
	clean.AddSeedResult("baseline", true, nil)
	assert.True(t, clean.Passed())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/replaytest/ -run TestReport -v`
Expected: FAIL — undefined `NewReport`.

- [ ] **Step 3: Write minimal implementation**

```go
package replaytest

import "encoding/json"

type SeedResult struct {
	Preset  string   `json:"preset"`
	Passed  bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
}

type Crash struct {
	Route    string `json:"route"`
	Selector string `json:"selector"`
	Error    string `json:"error"`
}

type Report struct {
	Scenario string       `json:"scenario"`
	Seeds    []SeedResult `json:"seeds"`
	Crashes  []Crash      `json:"crashes"`
	NewAsserts []Assertion `json:"new_assertions,omitempty"`
}

func NewReport(scenario string) *Report { return &Report{Scenario: scenario} }

func (r *Report) AddSeedResult(preset string, passed bool, failures []string) {
	r.Seeds = append(r.Seeds, SeedResult{Preset: preset, Passed: passed, Failures: failures})
}

func (r *Report) AddCrash(route, selector, errMsg string) {
	r.Crashes = append(r.Crashes, Crash{Route: route, Selector: selector, Error: errMsg})
}

func (r *Report) CrashCount() int { return len(r.Crashes) }

func (r *Report) Passed() bool {
	if len(r.Crashes) > 0 {
		return false
	}
	for _, s := range r.Seeds {
		if !s.Passed {
			return false
		}
	}
	return true
}

func (r *Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/replaytest/ -run TestReport -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/report.go internal/replaytest/report_test.go
git commit -m "feat(replaytest): report rollup"
```

---

## Task 10: Driver seed lane (integration, behind build tag)

**Files:**
- Create: `internal/replaytest/driver.go`
- Test: `internal/replaytest/driver_integration_test.go` (tag `//go:build integration`)

The driver injects the bundle into a chromedp context pointed at the proxy, drives `Steps`, recomputes `DOMSignature`, runs `Assertions`, and scrapes JS errors. This task's test is integration-tagged because it needs a headless browser; the pure pieces are already covered by Tasks 1–9. Read `internal/chromedp/session.go` and `internal/tools/automation_tools.go` for the existing chromedp session API (navigate, evaluate, screenshot) and reuse it — do NOT introduce a second chromedp wrapper.

- [ ] **Step 1: Write the failing test**

```go
//go:build integration

package replaytest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDriverSeedLanePassAndFail(t *testing.T) {
	// Fixture: a tiny SPA served on a static port that fetches /api/items and
	// renders the first item's name into <h1>. (Provide under testdata/spa/.)
	fix := startFixtureSPA(t) // helper returns base URL; t.Cleanup stops it
	sc := &Scenario{
		Name: "fixture", BaseURL: fix.URL,
		Recordings: []Recording{{Match: MatchKey{Method: "GET", Path: "/api/items"}, Status: 200, BodyRef: "b0", Hits: 1}},
		Blobs:      map[string]string{"b0": `{"items":[{"name":"Hello"}]}`},
		Steps: []Step{{Index: 0, Kind: StepNavigate, Selector: "/",
			Assertions: []Assertion{{Selector: "h1", Type: AssertText, Expect: "Hello"}}}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	d := NewDriver(fix.ProxyID)
	// clean replay passes
	rep, err := d.RunSeed(ctx, sc, "")
	require.NoError(t, err)
	assert.True(t, rep.Passed())

	// empty_array fuzz makes items empty -> h1 text empty -> assertion fails
	repFuzz, err := d.RunSeed(ctx, sc, "empty_array")
	require.NoError(t, err)
	assert.False(t, repFuzz.Passed())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags integration ./internal/replaytest/ -run TestDriverSeedLane -v`
Expected: FAIL — undefined `NewDriver`/`startFixtureSPA`.

- [ ] **Step 3: Write minimal implementation**

Implement `driver.go`:
- `NewDriver(proxyID string) *Driver`.
- `RunSeed(ctx, sc, preset) (*Report, error)`:
  1. `js, _ := GenerateBundle(sc, preset)`.
  2. Open a chromedp session via the existing `internal/chromedp` API routed through `proxyID`.
  3. Inject `js` (via `Page.addScriptToEvaluateOnNewDocument` equivalent the chromedp wrapper exposes) BEFORE navigation so the shim is installed first.
  4. For each `Step`: perform the action (navigate to `BaseURL+selector` for navigate; `click`/`type`/`submit` via selector), wait for settle, `Evaluate` to read `document.body.outerHTML`, compute `DOMSignature`, evaluate each assertion (`textContent`/presence), record failures.
  5. Collect JS errors via the existing console/error hook the chromedp session exposes (or `window.onerror` buffer evaluated at step end); `AddCrash` per error.
  6. Return the assembled `Report`.

Add `startFixtureSPA` test helper + `testdata/spa/index.html` (a 20-line page that `fetch('/api/items')` and writes `data.items[0]?.name || ''` into `<h1>`).

> Exact chromedp calls depend on the wrapper's surface — match `automation_tools.go`'s `evaluate`/`navigate` actions. Keep `driver.go` thin; all matching/mutation already lives in the worker.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags integration ./internal/replaytest/ -run TestDriverSeedLane -v`
Expected: PASS (clean replay passes, fuzzed replay fails the assertion).

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/driver.go internal/replaytest/driver_integration_test.go internal/replaytest/testdata/
git commit -m "feat(replaytest): chromedp seed-lane driver"
```

---

## Task 11: MCP tool with license gating

**Files:**
- Create: `internal/tools/replaytest_tool.go`
- Test: `internal/tools/replaytest_tool_test.go`
- Modify: wherever tools are registered (find with `grep -rn "AddTool" internal/tools/ cmd/agnt/ | grep -i automation` — register `replaytest` alongside the others).

Read `internal/tools/get_errors.go` for the daemon-aware tool + session-scoping pattern, and `internal/license/gate.go` for `Check`. The gate: `warning, err := mgr.Check(license.CapAdvancedTesting)`; non-nil `err` ⇒ blocked ⇒ return `CallToolResult{IsError:true}` with an activation hint (NOT a Go error). `list`/`show` skip the gate.

- [ ] **Step 1: Write the failing test**

```go
package tools

import (
	"context"
	"testing"

	"github.com/standardbeagle/agnt/internal/license"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaytestGate(t *testing.T) {
	gated := []string{"record", "stop", "refine", "replay", "explore"}
	free := []string{"list", "show"}

	// Missing license: gated actions are blocked, free actions are not.
	missing := newReplaytestHandler(license.NewManager()) // unloaded => StateMissing
	for _, a := range gated {
		res, _, err := missing.handle(context.Background(), ReplaytestInput{Action: a, Name: "x"})
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.True(t, res.IsError, "action %s should be license-blocked", a)
		assert.Contains(t, resultText(res), "activate")
	}
	for _, a := range free {
		res, _, err := missing.handle(context.Background(), ReplaytestInput{Action: a})
		require.NoError(t, err)
		assert.False(t, res.IsError, "action %s should be free", a)
	}

	// Unknown action -> IsError, not a silent no-op.
	res, _, err := missing.handle(context.Background(), ReplaytestInput{Action: "bogus"})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}
```

> `newReplaytestHandler`, `ReplaytestInput`, `handle`, and `resultText` are defined in this task. If `resultText` already exists in the tools test package, reuse it; otherwise add a small helper that concatenates `res.Content` text.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/ -run TestReplaytestGate -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write minimal implementation**

Implement `replaytest_tool.go`:

```go
package tools

import (
	"context"
	"fmt"

	"github.com/standardbeagle/agnt/internal/license"
	"github.com/standardbeagle/agnt/internal/replaytest"

	"github.com/standardbeagle/go-sdk/mcp"
)

type ReplaytestInput struct {
	Action        string `json:"action" jsonschema:"Action to perform: record, stop, refine, replay, explore, list, show"`
	Name          string `json:"name,omitempty" jsonschema:"Scenario name"`
	ProxyID       string `json:"proxy_id,omitempty" jsonschema:"Proxy to record against / replay through"`
	Preset        string `json:"preset,omitempty" jsonschema:"Fuzz preset for replay/explore"`
	ExploreAgents int    `json:"explore_agents,omitempty" jsonschema:"Number of breadth subagents for explore"`
	Directory     string `json:"directory,omitempty" jsonschema:"Project directory (session scoping)"`
	Global        bool   `json:"global,omitempty" jsonschema:"For list: include scenarios from all projects"`
}

type ReplaytestOutput struct {
	Scenarios []string `json:"scenarios,omitempty"`
	Report    string   `json:"report,omitempty"`
	Message   string   `json:"message,omitempty"`
	Success   bool     `json:"success"`
}

type replaytestHandler struct {
	lic *license.Manager
}

func newReplaytestHandler(lic *license.Manager) *replaytestHandler {
	return &replaytestHandler{lic: lic}
}

var gatedActions = map[string]bool{
	"record": true, "stop": true, "refine": true, "replay": true, "explore": true,
}

func (h *replaytestHandler) handle(ctx context.Context, in ReplaytestInput) (*mcp.CallToolResult, ReplaytestOutput, error) {
	switch in.Action {
	case "record", "stop", "refine", "replay", "explore", "list", "show":
	default:
		return errResult(fmt.Sprintf("unknown action %q", in.Action)), ReplaytestOutput{}, nil
	}

	if gatedActions[in.Action] {
		if _, err := h.lic.Check(license.CapAdvancedTesting); err != nil {
			return errResult("advanced_testing requires a Pro license — run `agnt activate <key>` to enable replaytest"),
				ReplaytestOutput{}, nil
		}
	}

	switch in.Action {
	case "list":
		names, err := replaytest.NewStore(in.Directory).List()
		if err != nil {
			return errResult(err.Error()), ReplaytestOutput{}, nil
		}
		return okResult("ok"), ReplaytestOutput{Scenarios: names, Success: true}, nil
	// record/stop/refine/replay/explore/show wired in Task 12.
	default:
		return okResult("not yet implemented"), ReplaytestOutput{Message: "pending", Success: false}, nil
	}
}
```

Add `errResult`/`okResult` if not already present in the package (small wrappers around `mcp.CallToolResult{IsError:...}` with a text content). Reuse existing helpers if they exist (grep first).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools/ -run TestReplaytestGate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/replaytest_tool.go internal/tools/replaytest_tool_test.go
git commit -m "feat(replaytest): MCP tool shell + license gating"
```

---

## Task 12: Wire actions + register tool

**Files:**
- Modify: `internal/tools/replaytest_tool.go` (implement record/stop/refine/replay/explore/show bodies)
- Modify: tool registration site (from the grep in Task 11)
- Test: `internal/tools/replaytest_tool_test.go` (add action-wiring tests using a valid license + a temp dir)

- [ ] **Step 1: Write the failing test**

```go
func TestReplaytestReplayWiring(t *testing.T) {
	dir := t.TempDir()
	// seed a scenario on disk
	st := replaytest.NewStore(dir)
	require.NoError(t, st.SaveScenario(&replaytest.Scenario{Name: "s1", Version: 1}))

	h := newReplaytestHandler(validLicenseManager(t)) // helper returns a Manager in StateValid with CapAdvancedTesting
	res, out, err := h.handle(context.Background(), ReplaytestInput{Action: "show", Name: "s1", Directory: dir})
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Contains(t, out.Report, "s1")
}
```

> `validLicenseManager(t)` builds a `license.Manager` whose `Check(CapAdvancedTesting)` returns nil — construct via the license package's test surface (mirror `internal/license/gate_store_test.go`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tools/ -run TestReplaytestReplayWiring -v`
Expected: FAIL — `show` not implemented / undefined helper.

- [ ] **Step 3: Write minimal implementation**

- Implement `show`: load scenario, return its JSON in `out.Report`.
- Implement `record`/`stop`: `record` stamps a session start (store an in-handler map `name → startTime` + proxyID); `stop` calls `proxyMgr.GetLogger(proxyID).Query(LogFilter{Since:start})`, `replaytest.AssembleScenario`, `store.SaveScenario`. (Pull the proxy manager handle the same way `get_errors.go` / `daemon_proxy.go` does — daemon-aware.)
- Implement `refine`: load scenario, call `replaytest.Refine` (Task 13), save.
- Implement `replay`: load scenario, `driver.RunSeed` across presets (baseline + requested), save report, emit to incident pipeline, return rollup.
- Implement `explore`: dispatch breadth subagents (Task 14).
- Register the tool at the registration site with `mcp.AddTool(server, &mcp.Tool{Name:"replaytest", ...}, handler.handle)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tools/ -run TestReplaytest -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/replaytest_tool.go <registration-file>
git commit -m "feat(replaytest): wire actions + register MCP tool"
```

---

## Task 13: AI refine pass

**Files:**
- Create: `internal/replaytest/refine.go`
- Test: `internal/replaytest/refine_test.go`

Refine takes a Scenario whose steps have raw captured DOM + auto-generated assertions, asks an LLM to (a) flag volatile selectors/text to `mask:true`, and (b) keep high-signal assertions. To keep CI hermetic, `Refine` takes an injected `LLMClient` interface; the test supplies a stub. Live LLM wiring uses the existing claude-go client (see `internal/tools` for how `ai` command constructs it).

- [ ] **Step 1: Write the failing test**

```go
package replaytest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubLLM struct{ maskSelectors []string }

func (s stubLLM) RefineAssertions(ctx context.Context, steps []Step) ([]Step, error) {
	for i := range steps {
		for j := range steps[i].Assertions {
			for _, m := range s.maskSelectors {
				if steps[i].Assertions[j].Selector == m {
					steps[i].Assertions[j].Mask = true
				}
			}
		}
	}
	return steps, nil
}

func TestRefineMasksVolatile(t *testing.T) {
	sc := &Scenario{Steps: []Step{{Assertions: []Assertion{
		{Selector: ".timestamp", Type: AssertText, Expect: "12:04"},
		{Selector: "h1", Type: AssertText, Expect: "Today"},
	}}}}
	err := Refine(context.Background(), sc, stubLLM{maskSelectors: []string{".timestamp"}})
	require.NoError(t, err)
	assert.True(t, sc.Steps[0].Assertions[0].Mask)
	assert.False(t, sc.Steps[0].Assertions[1].Mask)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/replaytest/ -run TestRefine -v`
Expected: FAIL — undefined `Refine`/`LLMClient`.

- [ ] **Step 3: Write minimal implementation**

```go
package replaytest

import "context"

// LLMClient refines auto-captured assertions: masking volatile content and
// keeping high-signal checks. Injected so CI runs against a stub.
type LLMClient interface {
	RefineAssertions(ctx context.Context, steps []Step) ([]Step, error)
}

// Refine mutates the Scenario's steps in place using the provided client.
func Refine(ctx context.Context, sc *Scenario, client LLMClient) error {
	steps, err := client.RefineAssertions(ctx, sc.Steps)
	if err != nil {
		return err
	}
	sc.Steps = steps
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/replaytest/ -run TestRefine -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/refine.go internal/replaytest/refine_test.go
git commit -m "feat(replaytest): AI refine pass (injected client)"
```

---

## Task 14: Subagent breadth fan-out (explore action)

**Files:**
- Create: `internal/replaytest/explore.go`
- Test: `internal/replaytest/explore_test.go`

`explore` partitions exploration seeds, dispatches N browser-debugger subagents (each with its own chromedp context + isolated worker-mock), merges their crash findings into the Report, and promotes newly discovered stable states into the Scenario as additional assertions. The subagent dispatch is behind a `BreadthRunner` interface so the merge/promote logic is unit-testable without spawning real agents.

- [ ] **Step 1: Write the failing test**

```go
package replaytest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubRunner struct{ findings []BreadthFinding }

func (s stubRunner) Run(ctx context.Context, seed ExploreSeed) (BreadthFinding, error) {
	return s.findings[seed.Index], nil
}

func TestExploreMergesAndPromotes(t *testing.T) {
	sc := &Scenario{Name: "x"}
	rep := NewReport("x")
	runner := stubRunner{findings: []BreadthFinding{
		{Crashes: []Crash{{Route: "/a", Selector: "btn", Error: "boom"}}, NewAssertions: []Assertion{{Selector: "h2", Type: AssertPresent}}},
		{Crashes: nil, NewAssertions: nil},
	}}
	err := Explore(context.Background(), sc, rep, runner, 2, "")
	require.NoError(t, err)
	assert.Equal(t, 1, rep.CrashCount())
	assert.Len(t, rep.NewAsserts, 1)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/replaytest/ -run TestExplore -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write minimal implementation**

```go
package replaytest

import "context"

type ExploreSeed struct {
	Index int
	Route string
}

type BreadthFinding struct {
	StatesVisited int          `json:"states_visited"`
	Crashes       []Crash      `json:"crashes"`
	NewAssertions []Assertion  `json:"new_assertions"`
}

// BreadthRunner runs one exploration seed (in production: dispatch a
// browser-debugger subagent against an isolated worker-mocked context).
type BreadthRunner interface {
	Run(ctx context.Context, seed ExploreSeed) (BreadthFinding, error)
}

// Explore fans out `agents` seeds through the runner and merges findings into
// the report; newly discovered stable assertions are promoted onto the report.
func Explore(ctx context.Context, sc *Scenario, rep *Report, runner BreadthRunner, agents int, preset string) error {
	for i := 0; i < agents; i++ {
		f, err := runner.Run(ctx, ExploreSeed{Index: i})
		if err != nil {
			return err
		}
		rep.Crashes = append(rep.Crashes, f.Crashes...)
		rep.NewAsserts = append(rep.NewAsserts, f.NewAssertions...)
	}
	return nil
}
```

Production `BreadthRunner` (wired in Task 12's `explore` body) builds a subagent prompt per seed and dispatches via the agent-dispatch surface the tools package already uses; out of scope for this unit test.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/replaytest/ -run TestExplore -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/explore.go internal/replaytest/explore_test.go
git commit -m "feat(replaytest): subagent breadth fan-out merge/promote"
```

---

## Task 15: Full-package verification + dogfood doc

**Files:**
- Create: `internal/replaytest/doc.go` (package doc)
- Modify: `CLAUDE.md` MCP Tools table (add `replaytest` row)
- Create: `docs/replaytest.md` (usage: record → refine → replay → explore against any proxied app; food-track example)

- [ ] **Step 1: Run the whole package + race**

Run: `go test -race ./internal/replaytest/ ./internal/tools/ -run 'Replaytest|TestScenario|TestMatch|TestFuzz|TestDOM|TestWorker|TestStore|TestRecorder|TestReport|TestRefine|TestExplore'`
Expected: PASS, no races.

- [ ] **Step 2: Build the binary (integration + dogfood need it)**

Run: `make build`
Expected: success.

- [ ] **Step 3: Integration lane**

Run: `go test -tags integration ./internal/replaytest/ -run TestDriverSeedLane -v`
Expected: PASS.

- [ ] **Step 4: Write docs**

Add the `replaytest` row to the `CLAUDE.md` MCP Tools table:
`| `replaytest` | Record→worker-mock→replay front-end testing (Pro: advanced_testing) |`

Write `docs/replaytest.md` covering the action flow and the food-track dogfood walk-through (start proxy → `replaytest record` → drive app → `stop` → `refine` → `replay` across presets → `explore`).

- [ ] **Step 5: Commit**

```bash
git add internal/replaytest/doc.go CLAUDE.md docs/replaytest.md
git commit -m "docs(replaytest): package doc, tool table, usage guide"
```

---

## Self-Review

**Spec coverage:**
- License gate (`CapAdvancedTesting`) → Task 11. ✔
- Scenario model + path templating + matching key → Tasks 1–3. ✔
- Auto-capture DOM baselines (signature) → Task 5; assembly → Task 8. ✔
- AI refine (mask/high-signal) → Task 13. ✔
- Worker-bundle: fetch/XHR shim + worker store/match/mutate + miss sentinel → Task 6. ✔
- Fuzz presets (6, chaos vocabulary) → Task 4; mirrored in worker JS → Task 6. ✔
- Seed replay lane (deterministic, JS-error + assertion capture) → Task 10. ✔
- Subagent breadth fan-out + merge/promote → Task 14, wired Task 12. ✔
- Incident-pipeline surfacing → Task 12 `replay` body (emits report → incidents). ✔
- On-disk JSON layout `.agnt/replaytests/` → Task 7. ✔
- Tool surface (7 actions, list/show free) → Tasks 11–12. ✔
- Testing strategy (pure units + one integration lane) → Tasks 1–10, 15. ✔

**Placeholder scan:** Tasks 10/12/13/14 intentionally defer *production* chromedp/subagent/LLM wiring behind injected interfaces (`Driver`, `BreadthRunner`, `LLMClient`) so each unit is hermetically testable; the wiring itself is an explicit step in Task 12, not a vague "implement later." Field-name caveats in Tasks 8/10 point at the exact structs to read. Acceptable per the isolation principle.

**Type consistency:** `Recording`, `MatchKey`, `Step`, `Assertion`, `Scenario`, `Report`, `Crash`, `Assertion.Mask`, `recKey`/`buildKey`, `GenerateBundle(sc, preset)`, `NewStore(dir)`, `AssembleScenario(name,base,entries)`, `Refine(ctx,sc,client)`, `Explore(ctx,sc,rep,runner,agents,preset)` are used consistently across tasks.

**Known external-dependency risks (flagged, not blocking):** exact `proxy.HTTPLogEntry`/`InteractionEvent` field names (Task 8) and the chromedp wrapper's inject/evaluate surface (Task 10) must be read from source before coding those tasks — the tests pin behavior, the field spellings are adjust-on-contact.
