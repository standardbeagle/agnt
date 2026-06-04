# Agent Inbox — Phase 1: Typed Envelope + Storm Collapse Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `Type` discriminator to incident events (foundation for the typed
agent-inbound queue) and collapse HTTP 4xx/5xx storms into a single inbox entry
with a bounded distinct-URL sample set.

**Architecture:** All changes live inside `internal/incident/`. A new
`MessageType` field defaults to `"error"` so every existing producer keeps
working unchanged. A storm fingerprint keyed on `(source, status-class, proxyID)`
makes a down-dependency 5xx flood across many URLs merge into one inbox entry;
the inbox entry accumulates up to 10 sample URLs plus a distinct-URL count.

**Tech Stack:** Go 1.24, `crypto/sha256`, `github.com/stretchr/testify`. Tests run
with `go test ./internal/incident/`.

**Scope note:** This is Phase 1 of a larger spec
(`docs/superpowers/specs/2026-06-04-unified-agent-inbox-design.md`). It is
self-contained, leaves the build green, and directly fixes the 5xx-storm spam
once the pipeline is the delivery path. Later phases (typed lanes, availability
gate, digest, UI adapter, `get_inbox` tool, AlertHub→EventHub split, config) get
their own plans. A roadmap is at the end of this document.

---

### Task 1: `MessageType` discriminator on events

**Files:**
- Create: `internal/incident/message_type.go`
- Modify: `internal/incident/envelope.go` (add field to `IncidentEvent`, set
  default in `NewIncidentEvent`)
- Test: `internal/incident/message_type_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/incident/message_type_test.go
package incident

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewIncidentEvent_DefaultsToErrorType(t *testing.T) {
	ev := NewIncidentEvent(
		SourceHTTP5xx, SeverityError, "500",
		"GET /api/x → 500", Context{ProxyID: "dev", URL: "/api/x"}, nil,
	)
	assert.Equal(t, MessageError, ev.Type, "events default to the error lane")
}

func TestMessageType_Constants(t *testing.T) {
	assert.Equal(t, MessageType("error"), MessageError)
	assert.Equal(t, MessageType("drawing"), MessageDrawing)
	assert.Equal(t, MessageType("comment"), MessageComment)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/incident/ -run 'TestNewIncidentEvent_DefaultsToErrorType|TestMessageType_Constants' -v`
Expected: FAIL — `undefined: MessageError` / `ev.Type undefined`.

- [ ] **Step 3: Create the `MessageType` type**

```go
// internal/incident/message_type.go
package incident

// MessageType partitions the agent-inbound queue into per-type lanes. The error
// lane is severity-banded; drawing/comment lanes are FIFO. New types are added
// here plus a lane config — the gate/digest machinery is type-agnostic.
type MessageType string

const (
	// MessageError is the lane for diagnostics, HTTP errors, crashes, etc.
	MessageError MessageType = "error"
	// MessageDrawing is the lane for sketch-mode wireframes.
	MessageDrawing MessageType = "drawing"
	// MessageComment is the lane for floating-panel user messages.
	MessageComment MessageType = "comment"
)
```

- [ ] **Step 4: Add the `Type` field and default it**

In `internal/incident/envelope.go`, add `Type` to the `IncidentEvent` struct
(place it right after `Fingerprint`):

```go
	Fingerprint string // sha256(source|category|canonical_msg|location)[:16]
	Type        MessageType
```

In `NewIncidentEvent`, set the default in the struct literal (add the line after
`Fingerprint: fp,`):

```go
	ev := IncidentEvent{
		ID:          newID(),
		Fingerprint: fp,
		Type:        MessageError,
		ReceivedAt:  time.Now(),
		Source:      src,
		Severity:    sev,
		Category:    category,
		Summary:     summary,
		Ctx:         ctx,
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/incident/ -run 'TestNewIncidentEvent_DefaultsToErrorType|TestMessageType_Constants' -v`
Expected: PASS.

- [ ] **Step 6: Run the full package to check no regression**

Run: `go test ./internal/incident/`
Expected: PASS (all existing tests still green; the new field is additive).

- [ ] **Step 7: Commit**

```bash
git add internal/incident/message_type.go internal/incident/envelope.go internal/incident/message_type_test.go
git commit -m "feat(incident): add MessageType discriminator, default events to error lane"
```

---

### Task 2: Storm fingerprint for HTTP 4xx/5xx

**Files:**
- Modify: `internal/incident/envelope.go` (add `computeStormFingerprint`)
- Modify: `internal/incident/adapter_http.go` (use it in `FromHTTPEntry`)
- Test: `internal/incident/adapter_http_test.go` (create if absent; otherwise
  append)

**Why:** `computeFingerprint` includes the URL, so each distinct-URL 5xx is a
distinct fingerprint and never merges. The storm fingerprint drops the URL and
folds in `proxyID`, collapsing a flood from one proxy into one entry while
keeping two proxies' floods distinct.

- [ ] **Step 1: Write the failing test**

```go
// internal/incident/adapter_http_test.go
package incident

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func httpEntry(method, url string, status int) proxy.HTTPLogEntry {
	return proxy.HTTPLogEntry{Method: method, URL: url, StatusCode: status}
}

func TestFromHTTPEntry_StormFingerprint_MergesDistinctURLsSameProxy(t *testing.T) {
	a, okA := FromHTTPEntry(httpEntry("GET", "/api/users", 500), "dev")
	b, okB := FromHTTPEntry(httpEntry("GET", "/api/orders", 503), "dev")
	require.True(t, okA)
	require.True(t, okB)
	assert.Equal(t, a.Fingerprint, b.Fingerprint,
		"any 5xx from the same proxy shares one storm fingerprint")
	assert.Equal(t, "5xx", a.Category)
}

func TestFromHTTPEntry_StormFingerprint_DistinctPerProxy(t *testing.T) {
	a, _ := FromHTTPEntry(httpEntry("GET", "/api/users", 500), "dev")
	b, _ := FromHTTPEntry(httpEntry("GET", "/api/users", 500), "staging")
	assert.NotEqual(t, a.Fingerprint, b.Fingerprint,
		"two proxies' storms must not merge")
}

func TestFromHTTPEntry_StormFingerprint_4xxDistinctFrom5xx(t *testing.T) {
	a, _ := FromHTTPEntry(httpEntry("GET", "/x", 500), "dev")
	b, _ := FromHTTPEntry(httpEntry("GET", "/x", 404), "dev")
	assert.NotEqual(t, a.Fingerprint, b.Fingerprint)
	assert.Equal(t, "5xx", a.Category)
	assert.Equal(t, "4xx", b.Category)
}

func TestFromHTTPEntry_SummaryKeepsURL(t *testing.T) {
	ev, _ := FromHTTPEntry(httpEntry("GET", "/api/users", 500), "dev")
	assert.Contains(t, ev.Summary, "/api/users",
		"summary keeps the human URL even though the fingerprint drops it")
	assert.Equal(t, "/api/users", ev.Ctx.URL)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/incident/ -run TestFromHTTPEntry -v`
Expected: FAIL — `a.Fingerprint != b.Fingerprint` (today URL is in the
fingerprint) and `Category` is `"500"`/`"503"` not `"5xx"`.

- [ ] **Step 3: Add `computeStormFingerprint`**

In `internal/incident/envelope.go`, directly below `computeFingerprint`:

```go
// computeStormFingerprint produces a URL-independent fingerprint so that a flood
// of same-class errors from one proxy (e.g. a down dependency returning 5xx on
// many endpoints) collapses into a single inbox entry. proxyID is folded in so
// two proxies' floods stay distinct.
func computeStormFingerprint(source, statusClass, proxyID string) string {
	h := sha256.Sum256([]byte("storm|" + source + "|" + statusClass + "|" + proxyID))
	return hex.EncodeToString(h[:])[:16]
}
```

- [ ] **Step 4: Use it in `FromHTTPEntry`**

Replace the body of `internal/incident/adapter_http.go::FromHTTPEntry` with:

```go
func FromHTTPEntry(he proxy.HTTPLogEntry, proxyID string) (IncidentEvent, bool) {
	var src Source
	var sev Severity
	var statusClass string

	switch {
	case he.StatusCode >= 500:
		src = SourceHTTP5xx
		sev = SeverityError
		statusClass = "5xx"
	case he.StatusCode >= 400:
		src = SourceHTTP4xx
		sev = SeverityWarning
		statusClass = "4xx"
	default:
		return IncidentEvent{}, false
	}

	msg := fmt.Sprintf("%s %s → %d", he.Method, he.URL, he.StatusCode)
	if he.Error != "" {
		msg += "\n" + he.Error
	} else if he.ResponseBody != "" {
		body := he.ResponseBody
		if len(body) > 500 {
			body = body[:500]
		}
		msg += "\n" + body
	}

	ev := NewIncidentEvent(
		src, sev, statusClass, msg,
		Context{ProxyID: proxyID, URL: he.URL},
		nil,
	)
	// Collapse the storm: one fingerprint per (source, status-class, proxy),
	// independent of the URL that NewIncidentEvent folded in.
	ev.Fingerprint = computeStormFingerprint(string(src), statusClass, proxyID)
	return ev, true
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/incident/ -run TestFromHTTPEntry -v`
Expected: PASS (all four).

- [ ] **Step 6: Run the full package**

Run: `go test ./internal/incident/`
Expected: PASS. (If a pre-existing `adapter_http` test asserted the old
per-URL/exact-code behavior, update it to the storm semantics — that assertion
is now wrong by design. Run `go test ./internal/incident/ -run Adapter -v` to
spot it.)

- [ ] **Step 7: Commit**

```bash
git add internal/incident/envelope.go internal/incident/adapter_http.go internal/incident/adapter_http_test.go
git commit -m "feat(incident): collapse HTTP 4xx/5xx storms via (source,status-class,proxy) fingerprint"
```

---

### Task 3: Bounded distinct-URL sample set on inbox entries

**Files:**
- Modify: `internal/incident/inbox.go` (`InboxEntry` fields + `addSampleURL`
  helper + call it in `Ingest`)
- Modify: `internal/incident/bus.go` (`ingestToSession` already passes
  `Sample: &de.Last`; no change needed — the URL rides on `Sample.Ctx.URL`)
- Test: `internal/incident/inbox_test.go` (append)

**Why:** A collapsed storm entry should render `47x across 12 URLs` with
examples. The entry accumulates up to 10 sample URLs and counts distinct URLs up
to a hard cap so memory stays bounded under any flood.

- [ ] **Step 1: Write the failing test**

```go
// append to internal/incident/inbox_test.go
func TestInbox_StormEntry_AccumulatesBoundedSampleURLs(t *testing.T) {
	inbox := NewInbox("s1")
	fp := "stormfp00000000"
	for i := 0; i < 20; i++ {
		url := fmt.Sprintf("/api/e%d", i)
		ev := NewIncidentEvent(SourceHTTP5xx, SeverityError, "5xx",
			"GET "+url+" → 500", Context{ProxyID: "dev", URL: url}, nil)
		ev.Fingerprint = fp
		inbox.Ingest(&InboxEntry{
			Fingerprint: fp,
			LastSeenAt:  ev.ReceivedAt,
			Count:       1,
			Sample:      &ev,
			Severity:    SeverityError,
		})
	}

	results, _ := inbox.Query(QueryFilter{})
	require.Len(t, results, 1, "all 20 collapse into one entry")
	e := results[0]
	assert.Equal(t, 20, e.Count)
	assert.LessOrEqual(t, len(e.SampleURLs), 10, "sample list is capped at 10")
	assert.Greater(t, len(e.SampleURLs), 0)
	assert.Equal(t, 20, e.DistinctURLs, "distinct count tracks all 20 URLs")
}

func TestInbox_SampleURLs_DedupeRepeatedURL(t *testing.T) {
	inbox := NewInbox("s1")
	fp := "stormfp11111111"
	for i := 0; i < 5; i++ {
		ev := NewIncidentEvent(SourceHTTP5xx, SeverityError, "5xx",
			"GET /same → 500", Context{ProxyID: "dev", URL: "/same"}, nil)
		ev.Fingerprint = fp
		inbox.Ingest(&InboxEntry{
			Fingerprint: fp, LastSeenAt: ev.ReceivedAt, Count: 1,
			Sample: &ev, Severity: SeverityError,
		})
	}
	results, _ := inbox.Query(QueryFilter{})
	require.Len(t, results, 1)
	assert.Equal(t, []string{"/same"}, results[0].SampleURLs)
	assert.Equal(t, 1, results[0].DistinctURLs)
}
```

(Confirm `inbox_test.go` already imports `fmt`; if not, add it to the import
block.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/incident/ -run 'TestInbox_StormEntry_AccumulatesBoundedSampleURLs|TestInbox_SampleURLs_DedupeRepeatedURL' -v`
Expected: FAIL — `e.SampleURLs undefined` / `e.DistinctURLs undefined`.

- [ ] **Step 3: Add fields + helper**

In `internal/incident/inbox.go`, add to the `InboxEntry` struct (after
`Sample`):

```go
	Sample      *IncidentEvent `json:"sample,omitempty"`
	SampleURLs  []string       `json:"sample_urls,omitempty"`  // up to maxSampleURLs distinct
	DistinctURLs int           `json:"distinct_urls,omitempty"` // distinct URLs seen, capped
	urlSeen     map[string]struct{}                            // unexported: distinct-URL set, capped
```

Add constants near the top `const (...)` block:

```go
	maxSampleURLs   = 10
	maxDistinctURLs = 128 // hard cap so a flood cannot grow the set unbounded
```

Add the helper (place it just above `func (inbox *Inbox) Ingest`):

```go
// addSampleURL records url against entry for storm rendering. It keeps up to
// maxSampleURLs distinct sample strings and counts distinct URLs up to
// maxDistinctURLs so memory stays bounded under a flood. Empty urls are ignored.
func addSampleURL(entry *InboxEntry, url string) {
	if url == "" {
		return
	}
	if entry.urlSeen == nil {
		entry.urlSeen = make(map[string]struct{})
	}
	if _, ok := entry.urlSeen[url]; ok {
		return
	}
	if len(entry.urlSeen) >= maxDistinctURLs {
		return
	}
	entry.urlSeen[url] = struct{}{}
	entry.DistinctURLs = len(entry.urlSeen)
	if len(entry.SampleURLs) < maxSampleURLs {
		entry.SampleURLs = append(entry.SampleURLs, url)
	}
}
```

- [ ] **Step 4: Call the helper on insert and merge**

In `internal/incident/inbox.go::Ingest`, the merge branch updates `existing`
when a fingerprint already exists. Right after the `existing.Count += entry.Count`
line, add:

```go
		existing.Count += entry.Count
		if entry.Sample != nil {
			addSampleURL(existing, entry.Sample.Ctx.URL)
		}
```

For the first-occurrence path, seed the sample from the incoming entry. Just
before the `// First occurrence of this fingerprint.` insert call, add:

```go
	// First occurrence of this fingerprint.
	if entry.Sample != nil {
		addSampleURL(entry, entry.Sample.Ctx.URL)
	}
	inbox.insertIntoBand(inbox.bands[newIdx], entry)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/incident/ -run 'TestInbox_StormEntry_AccumulatesBoundedSampleURLs|TestInbox_SampleURLs_DedupeRepeatedURL' -v`
Expected: PASS.

- [ ] **Step 6: Run the full package with the race detector**

Run: `go test -race ./internal/incident/`
Expected: PASS, no data races. (`urlSeen` mutations happen under the band lock
in `Ingest`; the merge path mutates `existing` while holding `b.mu` is released
before `insertIntoBand` — verify the merge mutation of `existing` is inside the
lock. If the race detector flags `urlSeen`, move the `addSampleURL(existing,…)`
call to before `b.mu.Unlock()` in the merge branch.)

- [ ] **Step 7: Commit**

```bash
git add internal/incident/inbox.go internal/incident/inbox_test.go
git commit -m "feat(incident): accumulate bounded distinct-URL sample set on storm entries"
```

---

### Task 4: End-to-end storm collapse through the bus

**Files:**
- Test only: `internal/incident/bus_test.go` (append) — proves a publish-side
  flood collapses to one queryable entry.

- [ ] **Step 1: Write the failing/again-green test**

```go
// append to internal/incident/bus_test.go
func TestBus_HTTPStorm_CollapsesToOneEntry(t *testing.T) {
	bus := NewMPSCBus(nil)
	defer bus.Close()
	bus.AddSession("sess", nil, nil, nil)

	for i := 0; i < 50; i++ {
		ev, ok := FromHTTPEntry(
			httpEntry("GET", fmt.Sprintf("/api/e%d", i), 500), "dev")
		require.True(t, ok)
		bus.Publish(ev)
	}

	// Drain the dispatch goroutine.
	require.Eventually(t, func() bool {
		entries, _ := bus.QuerySession("sess", QueryFilter{})
		return len(entries) == 1 && entries[0].Count == 50
	}, 2*time.Second, 10*time.Millisecond,
		"50 distinct-URL 5xx collapse into one entry with Count==50")

	entries, _ := bus.QuerySession("sess", QueryFilter{})
	assert.Equal(t, MessageError, entries[0].Sample.Type)
	assert.Equal(t, 50, entries[0].DistinctURLs)
	assert.Len(t, entries[0].SampleURLs, 10)
}
```

(Confirm `bus_test.go` imports `fmt` and `time`; add if missing.)

- [ ] **Step 2: Run it**

Run: `go test ./internal/incident/ -run TestBus_HTTPStorm_CollapsesToOneEntry -v`
Expected: PASS (Tasks 1–3 make this pass with no further code). If it FAILs on
`Count != 50`, the dedup window (30s) is not merging — check that
`ingestToSession` passes the storm `Fingerprint` through unchanged (it reads
`ev.Fingerprint`, which Task 2 set).

- [ ] **Step 3: Full package + race**

Run: `go test -race ./internal/incident/`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/incident/bus_test.go
git commit -m "test(incident): end-to-end HTTP storm collapses to one inbox entry"
```

---

## Self-review (completed)

- **Spec coverage (this phase):** typed envelope (Task 1) and storm collapse +
  sample set (Tasks 2–4) map to the spec's "Typed envelope", "Storm collapse
  (error lane)", and the storm test in "Testing". ✔
- **Placeholders:** none — every step has concrete code and exact commands.
- **Type consistency:** `MessageType`/`MessageError` (Task 1) reused in Task 4;
  `computeStormFingerprint` (Task 2) name matches its call site; `SampleURLs`/
  `DistinctURLs`/`urlSeen`/`addSampleURL` consistent across Tasks 3–4.
- **Race note** flagged in Task 3 Step 6 for the merge-branch lock ordering.

## Remaining phases (separate plans, written just-in-time)

| Phase | Scope | Key files |
|-------|-------|-----------|
| 2 | Typed lanes in `Inbox` (error band preserved; FIFO drawing/comment lanes, drop-oldest) | `inbox.go` |
| 3 | AvailabilityGate generalization — hold ALL types on idle; `stop`-hook force-flush; critical bypass | `ping.go`, `activity.go`, `hub_hook.go` |
| 4 | `DigestEmitter` heartbeat ticker + cross-lane summary + cursor-clear | `ping.go`, `bus.go` |
| 5 | UI interaction adapter — sketch/panel → `drawing`/`comment` envelopes | new `adapter_ui.go`, `hub_helpers.go` |
| 6 | `get_inbox` tool + `get_incidents`/`get_errors` shims | `internal/incident/get_incidents.go`, `internal/tools/` |
| 7 | AlertHub→EventHub split: delete Job A sinks + `processAutoForwardEvent` + flag; keep/rename Job B; wire `AddSession` sinks; re-home synchronous warnings | `alert_hub.go`→`event_hub.go`, `daemon.go`, `hub_helpers.go`, `hub_session.go`, `cmd/agnt/overlay.go`, `ws_handler.go`, `port_preflight.go`, `daemon_shutdown.go`, `config/agnt.go` |
| 8 | Config `digest`/`lanes` block + `incident-pipeline` back-compat tolerate | `config/agnt.go` |
| 9 | Docs: CLAUDE.md + daemon-architecture.md update to single-queue model | docs |
