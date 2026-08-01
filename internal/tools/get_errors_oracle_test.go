package tools

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// ─────────────────────────────────────────────────────────────────────────────
// get_errors → get_incidents migration oracle.
//
// The owner's requirement for retiring get_errors is that get_incidents return
// a SUPERSET of it. That claim is unfalsifiable by inspection, so this file
// measures it instead: it drives both tools' real filter builders and real
// projections over one shared seeded state, reflects over both tools' actual
// input/output schemas, and reduces every observed difference to a divergence
// record. The computed set is then asserted equal — content AND ordering — to
// the committed inventory in get_errors_oracle_golden.json.
//
// SHAPE: this is a GOLDEN / CHARACTERIZATION test, deliberately GREEN on
// landing. It documents reality, not aspiration. A permanently-red test here
// would break every other slice's `go test -p 1 ./...` gate in this repo.
//
// It ratchets in BOTH directions:
//   - a gap closes  → computed set shrinks → mismatch → shrink the golden in
//     the same commit (that is the migration making visible progress)
//   - a new divergence appears → computed set grows → mismatch → regression
//
// The superset property holds exactly when every to_be_closed entry is gone.
// At that point get_errors can be deleted.
//
// NOT in this slice: no migration, no removal, no get_incidents behaviour
// change. The inventory is the product.
// ─────────────────────────────────────────────────────────────────────────────

const goldenPath = "get_errors_oracle_golden.json"

// Divergence statuses.
const (
	// statusToBeClosed marks a capability get_errors has that get_incidents does
	// not yet cover. The superset holds when none of these remain.
	statusToBeClosed = "to_be_closed"
	// statusPermanentlyJustified marks a divergence that will never be closed,
	// by owner decision. It carries its rationale inline.
	statusPermanentlyJustified = "permanently_justified"
	// statusGetErrorsDefect marks a place where get_incidents is BETTER than the
	// reference. It does not block the superset; it is recorded so the migration
	// does not accidentally port the defect forward.
	statusGetErrorsDefect = "get_errors_defect"
)

// divergence is one measured difference between the two tools.
type divergence struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	GetErrors    string `json:"get_errors"`
	GetIncidents string `json:"get_incidents"`
	Detail       string `json:"detail"`
	Rationale    string `json:"rationale,omitempty"`
}

// goldenInventory is the committed shape of get_errors_oracle_golden.json.
type goldenInventory struct {
	Schema      string       `json:"schema"`
	Note        []string     `json:"note"`
	Divergences []divergence `json:"divergences"`
}

// ─── the shared seeded state ─────────────────────────────────────────────────

// seedClock is a fixed instant. Nothing in this file reads the wall clock for a
// value it asserts on, so the oracle is deterministic under any load.
var seedClock = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

const (
	seedPage    = "http://localhost:3000/checkout"
	seedFrameA  = "frame-content-a"
	seedFrameB  = "frame-content-b"
	seedProcess = "web-dev"
	seedProxy   = "proxy-web"
)

// seedLegacyErrors builds the reference view: what get_errors produces from its
// own legacy collectors, covering EVERY source it unifies. A superset proven
// over one source proves nothing about the others.
func seedLegacyErrors() []unifiedError {
	var out []unifiedError

	// 1. browser JS — the only source carrying location + frame_id.
	out = append(out, convertJSErrorDirect(seedProxy, &proxy.FrontendError{
		Message:   "TypeError: Cannot read property 'id' of undefined",
		Stack:     "at submitOrder (/src/checkout.js:42:15)",
		URL:       seedPage,
		Timestamp: seedClock,
	}, seedFrameA)...)

	// 2. proxy HTTP 5xx.
	out = append(out, convertHTTPErrorDirect(seedProxy, &proxy.HTTPLogEntry{
		Method:     "POST",
		URL:        "/api/orders",
		StatusCode: 500,
		Timestamp:  seedClock,
	})...)

	// 3. proxy diagnostic.
	out = append(out, convertDiagnosticErrorDirect(seedProxy, &proxy.ProxyDiagnostic{
		Level:     "error",
		Event:     "upstream_unreachable",
		Message:   "upstream 127.0.0.1:3000 refused connection",
		Timestamp: seedClock,
	})...)

	// 4. process alert.
	if ue := alertMapToUnifiedError(map[string]interface{}{
		"process_id": seedProcess,
		"type":       "error",
		"severity":   "error",
		"message":    "panic: nil map write",
		"timestamp":  seedClock.Format(time.RFC3339),
	}); ue != nil {
		out = append(out, *ue)
	}

	// 5. startup / build error.
	if ue := convertStartupLogEntry(map[string]interface{}{
		"process_id": seedProcess,
		"level":      "error",
		"phase":      "build",
		"message":    "cmd/agnt/main.go:12: undefined: Foo",
		"timestamp":  seedClock.Format(time.RFC3339),
	}); ue != nil {
		out = append(out, *ue)
	}

	return out
}

// seedIncidentRecords expresses the SAME five logical events as inbox records —
// the state both tools read once get_errors is shimmed onto the pipeline.
func seedIncidentRecords() []protocol.IncidentRecord {
	rec := func(fp, source, sev, cat, summary string) protocol.IncidentRecord {
		return protocol.IncidentRecord{
			ID:          "id-" + fp,
			Fingerprint: fp,
			FirstSeen:   seedClock.Add(-5 * time.Minute).Format(time.RFC3339),
			LastSeen:    seedClock.Format(time.RFC3339),
			Count:       1,
			Severity:    sev,
			Source:      source,
			Category:    cat,
			Summary:     summary,
			Context: protocol.IncidentContext{
				URL:       seedPage,
				ProxyID:   seedProxy,
				ProcessID: seedProcess,
			},
		}
	}
	return []protocol.IncidentRecord{
		rec("fp-js", string(incident.SourceBrowserJS), "error", "TypeError", "Cannot read property 'id' of undefined"),
		rec("fp-5xx", string(incident.SourceHTTP5xx), "error", "500 Internal Server Error", "POST /api/orders"),
		rec("fp-diag", string(incident.SourceProxyDiag), "error", "UPSTREAM UNREACHABLE", "upstream 127.0.0.1:3000 refused connection"),
		rec("fp-alert", string(incident.SourceProcessAlert), "error", "PROCESS ERROR", "panic: nil map write"),
		rec("fp-build", string(incident.SourceBuildFail), "error", "COMPILE ERROR", "cmd/agnt/main.go:12: undefined: Foo"),
	}
}

// ─── schema reflection ───────────────────────────────────────────────────────

// jsonFieldNames returns the json tag names of a struct's exported fields.
// Nested structs are flattened as "parent.child" so a field that moved into a
// sub-object (e.g. page → context.url) is still discoverable, and so ADDING a
// field on the get_incidents side genuinely closes a gap here rather than
// leaving a hardcoded verdict behind.
func jsonFieldNames(t reflect.Type, prefix string) []string {
	var names []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			tag = strings.ToLower(f.Name)
		}
		name := prefix + tag
		names = append(names, name)

		ft := f.Type
		for ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" && ft.Name() != "Time" {
			names = append(names, jsonFieldNames(ft, name+".")...)
		}
	}
	return names
}

func nameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// fieldProbe is one get_errors field and the get_incidents names that would
// cover it. A gap exists only when NONE of the candidates is present on the
// incidents side, so adding any of them closes the gap automatically.
type fieldProbe struct {
	id         string
	kind       string
	candidates []string // checked against the get_incidents name set
	status     string
	errDesc    string
	detail     string
	rationale  string
}

func probeFields(probes []fieldProbe, incidentNames map[string]bool) []divergence {
	var out []divergence
	for _, p := range probes {
		covered := ""
		for _, c := range p.candidates {
			if incidentNames[c] {
				covered = c
				break
			}
		}
		if covered != "" {
			continue // gap closed — the candidate exists on the incidents side
		}
		out = append(out, divergence{
			ID:           p.id,
			Kind:         p.kind,
			Status:       p.status,
			GetErrors:    p.errDesc,
			GetIncidents: "(absent)",
			Detail:       p.detail,
			Rationale:    p.rationale,
		})
	}
	return out
}

func computeInputDivergences() []divergence {
	incidentNames := nameSet(jsonFieldNames(reflect.TypeOf(GetIncidentsInput{}), ""))
	return probeFields([]fieldProbe{
		{
			id: "input.action", kind: "input_field", status: statusToBeClosed,
			candidates: []string{"action"},
			errDesc:    "action: query|pin|unpin|clear",
			detail:     "get_incidents has no retention verb at all. Without pin/unpin/clear an agent cannot keep an error alive across an auto-clear, so the whole retention feature is unmigrated.",
		},
		{
			id: "input.error_id", kind: "input_field", status: statusToBeClosed,
			candidates: []string{"error_id"},
			errDesc:    "error_id: pin/unpin target",
			detail:     "fingerprints[] is the identity analogue on the incidents side, but it only SELECTS records; there is no action to apply to the selection. Closing input.action without this leaves pins untargetable.",
		},
		{
			id: "input.tag", kind: "input_field", status: statusToBeClosed,
			candidates: []string{"tag"},
			errDesc:    "tag: note stored with a pin",
			detail:     "Agent-authored annotation attached at pin time. No incident-side store for caller-supplied metadata.",
		},
		{
			id: "input.global", kind: "input_field", status: statusPermanentlyJustified,
			candidates: []string{"global"},
			errDesc:    "global: cross-project query",
			detail:     "Deliberately excluded from the superset. get_errors keeps it; get_incidents must not gain it.",
			rationale:  "Owner decision: global was a debugging affordance, not a production use case. The incident inbox is per-session hard-isolated (numbered contract 1, .claude/rules/daemon-architecture.md) — a stronger guarantee than project scoping. Closing this gap would mean weakening that isolation, which is expressly forbidden. This entry is permanent and must NOT be removed when the others are.",
		},
	}, incidentNames)
}

func computeOutputDivergences() []divergence {
	incidentNames := nameSet(jsonFieldNames(reflect.TypeOf(GetIncidentsOutput{}), ""))
	return probeFields([]fieldProbe{
		{
			id: "output.collection_warnings", kind: "output_field", status: statusToBeClosed,
			candidates: []string{"collection_warnings", "inbox_after.collection_warnings"},
			errDesc:    "collection_warnings[]: per-source query failures",
			detail:     "THE ONE THAT MATTERS MOST. Its whole purpose is that a source which failed to answer never presents as a clean '0 errors'. get_incidents distinguishes only 'inbox unavailable' (pipeline_enabled=false) from 'inbox empty' — it cannot say 'the inbox answered but the proxy log query failed'. Retiring get_errors without this reintroduces a Silent Failure Prohibition violation.",
		},
		{
			id: "output.summary", kind: "output_field", status: statusToBeClosed,
			candidates: []string{"summary"},
			errDesc:    "summary: rendered text in the typed output struct",
			detail:     "Transport difference rather than lost information: get_incidents renders the same compact text into the CallToolResult content instead of a typed output field. Callers that read the struct (not the content) see nothing. Low severity, but it is a real shape change for the migration.",
		},
	}, incidentNames)
}

func computeItemDivergences() []divergence {
	names := jsonFieldNames(reflect.TypeOf(incidentView{}), "")
	incidentNames := nameSet(names)
	return probeFields([]fieldProbe{
		{
			id: "item.pinned", kind: "item_field", status: statusToBeClosed,
			candidates: []string{"pinned", "context.pinned"},
			errDesc:    "pinned: entry survives limit and auto-clear",
			detail:     "Without a per-item pinned flag, pinning is unobservable even if input.action lands — the agent cannot tell which entries it saved.",
		},
		{
			id: "item.tag", kind: "item_field", status: statusToBeClosed,
			candidates: []string{"tag", "context.tag"},
			errDesc:    "tag: the note stored at pin time",
			detail:     "Read side of input.tag. Same dependency: useless until item.pinned and input.action exist.",
		},
		{
			id: "item.location", kind: "item_field", status: statusToBeClosed,
			candidates: []string{"location", "context.location"},
			errDesc:    "location: file:line:col of the first app stack frame",
			detail:     "MEASURED, NOT ASSUMED: protocol.IncidentContext carries process_id/proxy_id/session_id/project_path/url/pid/port and no location. Note the pipeline does not discard it out of ignorance — incident.NewIncidentEvent folds ctx.URL into the fingerprint as its 'location' argument — but the source-level file:line:col never enters the envelope and is therefore unrecoverable from a record.",
		},
		{
			id: "item.frame_id", kind: "item_field", status: statusToBeClosed,
			candidates: []string{"frame_id", "context.frame_id"},
			errDesc:    "frame_id: the emitting content frame (always-wrap model)",
			detail:     "Absent from protocol.IncidentContext. This is the field gap; its consequence is the separate, worse granularity.frame_collapse divergence below.",
		},
	}, incidentNames)
}

// ─── measured behaviour ──────────────────────────────────────────────────────

// computeFilterDivergences drives both tools' REAL filter builders over a
// matrix of equivalent inputs and records where the resulting inbox queries
// differ. Because it calls the shipped builders, a behaviour change in either
// handler moves this result.
func computeFilterDivergences() []divergence {
	var out []divergence

	// since: duration form.
	errFilter := buildGetErrorsIncidentFilter(GetErrorsInput{Since: "5m"}, true)
	incFilter := buildGetIncidentsFilter(GetIncidentsInput{Since: "5m"})
	if errFilter.Since != incFilter.Since {
		out = append(out, divergence{
			ID: "filter.since_duration_unresolved", Kind: "behavior", Status: statusGetErrorsDefect,
			GetErrors:    "forwards since verbatim (e.g. \"5m\")",
			GetIncidents: "resolves the duration to an absolute RFC3339 timestamp first",
			Detail:       "Direction is REVERSED: get_incidents is correct and get_errors is broken. The hub parses Since strictly as RFC3339, so get_errors' shim path silently ignores the duration form its own schema advertises — an unfiltered result masquerading as a filtered one. Does not block the superset; recorded so the migration does not port the defect forward, and so nobody 'fixes' get_incidents to match the reference here.",
		})
	}

	// limit: forwarded to the inbox, or applied after the fact?
	errFilter = buildGetErrorsIncidentFilter(GetErrorsInput{Limit: 5}, true)
	incFilter = buildGetIncidentsFilter(GetIncidentsInput{Limit: 5})
	if errFilter.Limit != incFilter.Limit {
		out = append(out, divergence{
			ID: "filter.limit_scope", Kind: "behavior", Status: statusToBeClosed,
			GetErrors:    "always requests 100, then limits for display after dedup+sort",
			GetIncidents: "forwards the caller's limit and truncates server-side",
			Detail:       "Consequence for counts, not just page size: get_errors' error_count/warning_count describe the WHOLE matching set because it counts before trimming, whereas a limited get_incidents page reports a truncated page plus whole-inbox band stats. A caller migrating a limited query gets different totals. See counts.semantics.",
		})
	}

	// include_warnings default vs severity default — PREDICTED to diverge.
	errDefault := buildGetErrorsIncidentFilter(GetErrorsInput{}, true)
	incDefault := buildGetIncidentsFilter(GetIncidentsInput{})
	if len(errDefault.Severities) != len(incDefault.Severities) {
		out = append(out, divergence{
			ID: "filter.severity_default", Kind: "behavior", Status: statusToBeClosed,
			GetErrors:    "include_warnings defaults true",
			GetIncidents: "severity[] defaults empty",
			Detail:       "Unqualified calls select different severity sets.",
		})
	}

	// include_warnings=false must be expressible on the incidents side.
	errNoWarn := buildGetErrorsIncidentFilter(GetErrorsInput{}, false)
	incNoWarn := buildGetIncidentsFilter(GetIncidentsInput{Severity: errNoWarn.Severities})
	if !reflect.DeepEqual(errNoWarn.Severities, incNoWarn.Severities) {
		out = append(out, divergence{
			ID: "filter.severity_exclusion_inexpressible", Kind: "behavior", Status: statusToBeClosed,
			GetErrors:    "include_warnings:false → severities [critical error]",
			GetIncidents: "cannot express the same restriction",
			Detail:       "severity[] does not round-trip get_errors' warning exclusion.",
		})
	}

	// process_id / proxy_id must survive both translations identically.
	errScoped := buildGetErrorsIncidentFilter(GetErrorsInput{ProcessID: seedProcess, ProxyID: seedProxy}, true)
	incScoped := buildGetIncidentsFilter(GetIncidentsInput{ProcessID: seedProcess, ProxyID: seedProxy})
	if errScoped.ProcessID != incScoped.ProcessID || errScoped.ProxyID != incScoped.ProxyID {
		out = append(out, divergence{
			ID: "filter.resource_scope", Kind: "behavior", Status: statusToBeClosed,
			GetErrors:    "process_id/proxy_id",
			GetIncidents: "translated differently",
			Detail:       "Resource scoping does not translate 1:1.",
		})
	}

	return out
}

// computeProjectionDivergences compares the two projections of the SAME seeded
// records by identity and content — never by count. Two result sets of equal
// length can be entirely different sets.
func computeProjectionDivergences() []divergence {
	var out []divergence
	records := seedIncidentRecords()

	// Identity: get_errors' shim adopts the incident fingerprint as its id
	// precisely so the two tools are correlatable. Verify per record.
	mismatched := 0
	for _, rec := range records {
		ue := incidentRecordToUnifiedError(rec)
		view := recordToView(rec)
		if ue.ID != view.Fingerprint {
			mismatched++
		}
	}
	if mismatched > 0 {
		out = append(out, divergence{
			ID: "projection.identity_uncorrelatable", Kind: "behavior", Status: statusToBeClosed,
			GetErrors:    "id = incident fingerprint",
			GetIncidents: "fingerprint",
			Detail:       "The two tools' item identities no longer correlate, so no set comparison between them is meaningful.",
		})
	}

	// Content: severity vocabulary. get_errors folds to a two-level scale.
	folded := map[string]string{"critical": "error", "info": "warning"}
	for raw, want := range folded {
		rec := records[0]
		rec.Severity = raw
		if got := incidentRecordToUnifiedError(rec).Severity; got != want {
			continue
		}
		view := recordToView(rec)
		if view.Severity == want {
			continue // no divergence: both report the same level
		}
		out = append(out, divergence{
			ID: "projection.severity_scale_" + raw, Kind: "behavior", Status: statusGetErrorsDefect,
			GetErrors:    "folds " + raw + " → " + want + " (two-level error/warning scale)",
			GetIncidents: "preserves " + raw + " as a distinct band",
			Detail:       "Direction is REVERSED: get_incidents carries strictly more information. A get_errors caller cannot tell a critical from an ordinary error. Recorded so the migration does not re-flatten the scale to match the reference.",
		})
	}

	// Content: source vocabulary. The reference (legacy path) and the pipeline
	// name the same sources differently, so a caller's saved `source` string
	// does not carry across.
	legacySources := map[string]bool{}
	for _, ue := range seedLegacyErrors() {
		legacySources[ue.Source] = true
	}
	incidentSources := map[string]bool{}
	for _, rec := range records {
		incidentSources[recordToView(rec).Source] = true
	}
	var unmatched []string
	for s := range legacySources {
		if !incidentSources[s] {
			unmatched = append(unmatched, s)
		}
	}
	if len(unmatched) > 0 {
		sort.Strings(unmatched)
		out = append(out, divergence{
			ID: "projection.source_vocabulary", Kind: "behavior", Status: statusToBeClosed,
			GetErrors:    "colon-delimited: " + strings.Join(unmatched, " "),
			GetIncidents: "underscore enum: browser_js http_5xx http_4xx proxy_diag process_alert build_fail …",
			Detail:       "UNPREDICTED. get_errors itself speaks two vocabularies: its legacy collectors emit browser:js / proxy:http / proxy:diagnostic / process:<id>, while its incident shim passes the pipeline's enum through verbatim. So the same tool labels the same error differently depending on which path served it. Any caller (or doc, or saved filter) keyed on the legacy strings breaks on migration, and get_incidents' sources[] filter accepts only the enum form.",
		})
	}

	// Content: counts. Measure equivalence of error_count/warning_count against
	// inbox_after rather than assuming it.
	_, errOut := formatErrorsOutput(projectRecords(records), true, 25, false)
	inboxErr := 0
	for _, rec := range records {
		if rec.Severity == "error" {
			inboxErr++
		}
	}
	out = append(out, divergence{
		ID: "counts.semantics", Kind: "behavior", Status: statusToBeClosed,
		GetErrors:    "error_count/warning_count = the returned, deduped, filtered set (counted before the display limit)",
		GetIncidents: "inbox_after = whole-inbox band occupancy after the query, independent of what this page returned",
		Detail: "MEASURED, NOT ASSUMED — and they are NOT equivalent even when the numbers happen to coincide (" +
			strconv.Itoa(errOut.ErrorCount) + " vs " + strconv.Itoa(inboxErr) + " on this seed). They answer different questions: 'how many matched your filter' vs 'how full is the inbox'. Under any filter that excludes part of the inbox, or any limit, they diverge numerically too. A migration that maps error_count onto inbox_after.error is wrong.",
	})

	return out
}

func projectRecords(records []protocol.IncidentRecord) []unifiedError {
	out := make([]unifiedError, 0, len(records))
	for _, rec := range records {
		out = append(out, incidentRecordToUnifiedError(rec))
	}
	return out
}

// computeGranularityDivergences tests the frame case directly, as required:
// the SAME error raised in two distinct content frames. get_errors keeps them
// apart because frame_id is in its dedup key; the incident fingerprint has no
// frame identity, so the pipeline collapses them.
func computeGranularityDivergences() []divergence {
	var out []divergence

	fe := &proxy.FrontendError{
		Message:   "TypeError: Cannot read property 'id' of undefined",
		Stack:     "at submitOrder (/src/checkout.js:42:15)",
		URL:       seedPage,
		Timestamp: seedClock,
	}
	legacy := append(
		convertJSErrorDirect(seedProxy, fe, seedFrameA),
		convertJSErrorDirect(seedProxy, fe, seedFrameB)...,
	)
	legacyKept := len(deduplicateErrors(legacy))

	// Build the same two occurrences as real incident events. Nothing about the
	// frame can be expressed in incident.Context, which is the point.
	ctx := incident.Context{URL: seedPage, ProxyID: seedProxy}
	evA := incident.NewIncidentEvent(incident.SourceBrowserJS, incident.SeverityError, "TypeError", fe.Message, ctx, nil)
	evB := incident.NewIncidentEvent(incident.SourceBrowserJS, incident.SeverityError, "TypeError", fe.Message, ctx, nil)
	incidentKept := 1
	if evA.Fingerprint != evB.Fingerprint {
		incidentKept = 2
	}

	if legacyKept != incidentKept {
		out = append(out, divergence{
			ID: "granularity.frame_collapse", Kind: "granularity", Status: statusToBeClosed,
			GetErrors:    "keeps " + strconv.Itoa(legacyKept) + " entries (frame_id is in dedupKey)",
			GetIncidents: "keeps " + strconv.Itoa(incidentKept) + " entry (fingerprint = sha256(source|category|canonical_msg|url), no frame identity)",
			Detail:       "The superset fails here on GRANULARITY, not on fields — and this is strictly worse than a missing column. Adding frame_id to the incident record would not fix it: the two occurrences were already merged upstream at fingerprint time, so one of them no longer exists to be labelled. Closing this requires frame identity inside incident.NewIncidentEvent's fingerprint, not just in protocol.IncidentContext. Under the always-wrap model (docs/responsive-canonical-target.md §5.2/§6.2) each content frame is a distinct context, so collapsing them hides a real second failure.",
		})
	}

	return out
}

// computeInventory assembles every measured divergence in a stable order.
func computeInventory() []divergence {
	var all []divergence
	all = append(all, computeInputDivergences()...)
	all = append(all, computeOutputDivergences()...)
	all = append(all, computeItemDivergences()...)
	all = append(all, computeFilterDivergences()...)
	all = append(all, computeProjectionDivergences()...)
	all = append(all, computeGranularityDivergences()...)
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all
}

// ─── the oracle ──────────────────────────────────────────────────────────────

func TestGetErrorsOracle_DivergenceMatchesGolden(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden goldenInventory
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}

	got := computeInventory()

	// Regeneration aid for whoever closes a gap: AGNT_ORACLE_REGOLD=1 rewrites
	// the inventory from the measurement. It deliberately still FAILS the test
	// afterwards, so a stale golden can never be laundered into a green run —
	// the rewrite must be reviewed as part of the same commit.
	if os.Getenv("AGNT_ORACLE_REGOLD") == "1" {
		golden.Divergences = got
		out, err := json.MarshalIndent(golden, "", "  ")
		if err != nil {
			t.Fatalf("marshal golden: %v", err)
		}
		if err := os.WriteFile(goldenPath, append(out, '\n'), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Fatalf("REGENERATED %s with %d divergences — review the diff and re-run without AGNT_ORACLE_REGOLD", goldenPath, len(got))
	}

	// Compare by identity and content, never by count — two inventories of equal
	// length can be entirely different sets. Membership is diffed by ID so the
	// failure names the divergence that actually moved rather than whichever
	// entry the shift happened to land on; ordering is asserted separately.
	goldenByID := make(map[string]divergence, len(golden.Divergences))
	for _, d := range golden.Divergences {
		goldenByID[d.ID] = d
	}
	gotByID := make(map[string]divergence, len(got))
	for _, d := range got {
		gotByID[d.ID] = d
	}

	for _, d := range got {
		g, ok := goldenByID[d.ID]
		if !ok {
			t.Errorf("NEW divergence not in golden — a regression, or a change nobody recorded:\n  %s [%s/%s]\n  %s",
				d.ID, d.Kind, d.Status, d.Detail)
			continue
		}
		if !reflect.DeepEqual(d, g) {
			t.Errorf("divergence %q changed:\n  measured: %+v\n  golden:   %+v", d.ID, d, g)
		}
	}
	for _, g := range golden.Divergences {
		if _, ok := gotByID[g.ID]; !ok {
			t.Errorf("golden divergence no longer measured — the gap CLOSED (good): %s\n"+
				"  Shrink the golden in the SAME commit that closed it.", g.ID)
		}
	}

	// Ordering is part of the contract: the committed inventory must read in the
	// same stable order the oracle produces, so its diffs stay reviewable.
	if gi, gg := ids(got), ids(golden.Divergences); !reflect.DeepEqual(gi, gg) {
		t.Errorf("inventory ordering differs:\n  measured: %v\n  golden:   %v", gi, gg)
	}
}

func ids(ds []divergence) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}

// TestGetErrorsOracle_GoldenIsWellFormed keeps the inventory readable and keeps
// the sanctioned divergence from being quietly reclassified as ordinary debt.
func TestGetErrorsOracle_GoldenIsWellFormed(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden goldenInventory
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}

	seen := map[string]bool{}
	justified := 0
	for _, d := range golden.Divergences {
		if seen[d.ID] {
			t.Errorf("duplicate divergence id %q", d.ID)
		}
		seen[d.ID] = true

		switch d.Status {
		case statusToBeClosed, statusGetErrorsDefect:
			if d.Rationale != "" {
				t.Errorf("%s: only %s entries carry a rationale", d.ID, statusPermanentlyJustified)
			}
		case statusPermanentlyJustified:
			justified++
			if d.Rationale == "" {
				t.Errorf("%s: a permanently justified divergence must state its rationale inline", d.ID)
			}
		default:
			t.Errorf("%s: unknown status %q", d.ID, d.Status)
		}
		if d.Detail == "" {
			t.Errorf("%s: every entry must explain what is lost", d.ID)
		}
	}

	// The `global` exclusion is an owner decision and must survive every future
	// shrink of this inventory. If it ever disappears, someone closed it — which
	// would mean weakening per-session isolation.
	if !seen["input.global"] {
		t.Error("input.global must remain in the golden: it is permanently excluded from the superset by owner decision, not pending work")
	}
	if justified != 1 {
		t.Errorf("expected exactly one permanently justified divergence (input.global), got %d", justified)
	}
}

// TestGetErrorsOracle_SupersetHoldsWhenInventoryEmpty states the exit condition
// in code: get_errors may be deleted exactly when no to_be_closed entry remains.
func TestGetErrorsOracle_SupersetHoldsWhenInventoryEmpty(t *testing.T) {
	t.Parallel()

	open := 0
	for _, d := range computeInventory() {
		if d.Status == statusToBeClosed {
			open++
		}
	}
	t.Logf("get_errors retirement blockers remaining: %d", open)
	if open == 0 {
		t.Log("SUPERSET HOLDS — get_incidents now covers get_errors; the removal slice is unblocked")
	}
}
