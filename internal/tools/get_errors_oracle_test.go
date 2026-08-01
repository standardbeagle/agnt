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

// Availability classes for a to_be_closed divergence.
//
// The owner's bar for retiring get_errors is AVAILABILITY, not exactness: the
// information get_errors surfaced must be REACHABLE through get_incidents.
// Identical ids, counts, labels, ordering and dedup granularity are explicitly
// NOT required. So an open divergence blocks the deletion only when the
// information itself is gone, not when it merely arrives in another shape.
//
// This field is a REVIEWED ARTIFACT, NOT A LEVER. Demoting an entry to
// presentation_differs is the one cheap way to open the gate without closing a
// gap, so it must never be a quiet edit: the measured value lives in this file,
// the recorded value lives in the golden, and TestGetErrorsOracle_GoldenIsWellFormed
// additionally pins the distribution. A demotion therefore has to change three
// places at once and shows up as a reviewable diff in both files.
const (
	// availabilityDataMissing marks information that is absent, or destroyed
	// upstream, and unrecoverable by any caller. BLOCKS the deletion.
	availabilityDataMissing = "data_missing"
	// availabilityPresentationDiffers marks information that is reachable, where
	// only the shape, name or accounting differs. Does NOT block the deletion.
	availabilityPresentationDiffers = "presentation_differs"
)

// divergence is one measured difference between the two tools.
type divergence struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	// Availability is the blocking judgement for a to_be_closed entry:
	// availabilityDataMissing or availabilityPresentationDiffers. Empty for
	// every other status, and empty for a to_be_closed divergence nobody has
	// classified yet — which the well-formedness test rejects, so a newly
	// measured gap cannot slip into the golden unclassified and non-blocking.
	Availability string `json:"availability,omitempty"`
	// AvailabilityReason states, in the artifact, WHY the information is or is
	// not reachable. Required whenever Availability is set.
	AvailabilityReason string `json:"availability_reason,omitempty"`
	GetErrors          string `json:"get_errors"`
	GetIncidents       string `json:"get_incidents"`
	Detail             string `json:"detail"`
	Rationale          string `json:"rationale,omitempty"`
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
		// A warning-band entry so matched-set counts and inbox band occupancy
		// genuinely disagree under an exclusion filter — counts.semantics is
		// gated on a real numeric divergence, not appended unconditionally.
		rec("fp-4xx", string(incident.SourceHTTP4xx), "warning", "404 Not Found", "GET /api/missing"),
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

// topLevelJSONNames returns only the immediate json field names of a struct.
// Used on the get_errors reference types, where the exhaustiveness check asks
// "is this field accounted for", not "where did it move to".
func topLevelJSONNames(t reflect.Type) []string {
	var names []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			tag = strings.ToLower(f.Name)
		}
		names = append(names, tag)
	}
	return names
}

// fieldProbe is one get_errors field and the get_incidents names that would
// cover it. A gap exists only when NONE of the candidates is present on the
// incidents side, so adding any of them closes the gap automatically.
//
// Every field of every get_errors reference type must have a probe, or an entry
// in declaredCovered below. TestGetErrorsOracle_EveryReferenceFieldIsAccountedFor
// fails otherwise: the inventory presents itself as the complete picture, so
// discovery must be enumerated by the type system rather than by hand.
type fieldProbe struct {
	field      string // json name on the get_errors side; drives exhaustiveness
	id         string
	kind       string
	candidates []string // checked against the get_incidents name set
	status     string
	errDesc    string
	detail     string
	rationale  string
	// availability / availabilityReason are set only on probes that actually
	// fire today, i.e. that are classified from a measurement. A probe that is
	// currently covered (or a hypothetical guard that has never fired) leaves
	// them empty on purpose: if it ever starts firing, the resulting entry lands
	// unclassified and the well-formedness test demands a human judgement rather
	// than inheriting a guess made before anyone measured it.
	availability       string
	availabilityReason string
}

// declaredCovered lists reference fields deliberately NOT probed by name, each
// with the reason. A field may only appear here when a behaviour probe already
// measures it — a name probe would then double-count the same gap.
var declaredCovered = map[string]string{
	"GetErrorsInput.include_warnings": "measured by the filter.severity_default and filter.severity_exclusion_inexpressible behaviour probes. severity[] is the incidents-side spelling, so a name probe would report a false gap; the probes instead assert that the DEFAULTS agree and that the exclusion round-trips — which is the substance of the claim.",
	"GetErrorsOutput.error_count":     "measured by the counts.semantics behaviour probe, which compares the matched-set count against inbox band occupancy numerically. A name probe would double-count the same divergence.",
	"GetErrorsOutput.warning_count":   "measured by the counts.semantics behaviour probe (same reason as error_count).",
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
			ID:                 p.id,
			Kind:               p.kind,
			Status:             p.status,
			Availability:       p.availability,
			AvailabilityReason: p.availabilityReason,
			GetErrors:          p.errDesc,
			GetIncidents:       "(absent)",
			Detail:             p.detail,
			Rationale:          p.rationale,
		})
	}
	return out
}

// inputProbes covers every field of GetErrorsInput. The pass-through fields are
// probed rather than declared so that removing any of them from
// GetIncidentsInput immediately surfaces as a new divergence.
func inputProbes() []fieldProbe {
	passThrough := func(field, note string) fieldProbe {
		return fieldProbe{
			field: field, id: "input." + field, kind: "input_field", status: statusToBeClosed,
			candidates: []string{field}, errDesc: field, detail: note,
		}
	}
	return append([]fieldProbe{
		passThrough("process_id", "Scope-to-process filter; also asserted to translate 1:1 by filter.resource_scope."),
		passThrough("proxy_id", "Scope-to-proxy filter; also asserted to translate 1:1 by filter.resource_scope."),
		passThrough("since", "Recency bound; its translation is separately measured by filter.since_forks_to_legacy."),
		passThrough("limit", "Page bound; its differing scope is separately measured by filter.limit_scope."),
		passThrough("raw", "Full-JSON rendering toggle."),
	}, inputGapProbes()...)
}

func inputGapProbes() []fieldProbe {
	return []fieldProbe{
		{
			field: "action",
			id:    "input.action", kind: "input_field", status: statusToBeClosed,
			candidates:   []string{"action"},
			errDesc:      "action: query|pin|unpin|clear",
			detail:       "get_incidents has no retention verb at all. Without pin/unpin/clear an agent cannot keep an error alive across an auto-clear, so the whole retention feature is unmigrated.",
			availability: availabilityDataMissing,
			availabilityReason: "Retention is the mechanism by which data stays reachable at all: the inbox evicts the oldest entry once a band hits its 100-entry cap (numbered contract 5), " +
				"and with no pin verb a caller has no way to keep a specific incident alive. The entry is destroyed and no reshaping recovers it.",
		},
		{
			field: "error_id",
			id:    "input.error_id", kind: "input_field", status: statusToBeClosed,
			candidates:   []string{"error_id"},
			errDesc:      "error_id: pin/unpin target",
			detail:       "fingerprints[] is the identity analogue on the incidents side, but it only SELECTS records; there is no action to apply to the selection. Closing input.action without this leaves pins untargetable.",
			availability: availabilityDataMissing,
			availabilityReason: "Blocks for the same reason as input.action and must not be demoted separately from it: a retention verb with nothing to address preserves nothing, " +
				"so closing input.action alone would leave the eviction data loss fully open.",
		},
		{
			field: "tag",
			id:    "input.tag", kind: "input_field", status: statusToBeClosed,
			candidates:   []string{"tag"},
			errDesc:      "tag: note stored with a pin",
			detail:       "Agent-authored annotation attached at pin time. No incident-side store for caller-supplied metadata.",
			availability: availabilityDataMissing,
			availabilityReason: "Caller-authored content that exists nowhere else: nothing in the incident pipeline stores caller-supplied metadata, so a tag written through get_errors " +
				"is not reachable through get_incidents in any shape.",
		},
		{
			field: "global",
			id:    "input.global", kind: "input_field", status: statusPermanentlyJustified,
			candidates: []string{"global"},
			errDesc:    "global: cross-project query",
			detail:     "Deliberately excluded from the superset. get_errors keeps it; get_incidents must not gain it.",
			rationale:  "Owner decision: global was a debugging affordance, not a production use case. The incident inbox is per-session hard-isolated (numbered contract 1, .claude/rules/daemon-architecture.md) — a stronger guarantee than project scoping. Closing this gap would mean weakening that isolation, which is expressly forbidden. This entry is permanent and must NOT be removed when the others are.",
		},
	}
}

func computeInputDivergences() []divergence {
	return probeFields(inputProbes(), nameSet(jsonFieldNames(reflect.TypeOf(GetIncidentsInput{}), "")))
}

func outputProbes() []fieldProbe {
	return []fieldProbe{
		{
			field: "collection_warnings",
			id:    "output.collection_warnings", kind: "output_field", status: statusToBeClosed,
			candidates:   []string{"collection_warnings", "inbox_after.collection_warnings"},
			errDesc:      "collection_warnings[]: per-source query failures",
			availability: availabilityDataMissing,
			availabilityReason: "No partial-collection signal is produced anywhere in the pipeline, so there is nothing for a caller to reach: a source that failed to answer is " +
				"indistinguishable from a source with nothing to report. This is absence of information, not a different shape of it.",
			detail: "THE ONE THAT MATTERS MOST. Its whole purpose is that a source which failed to answer never presents as a clean '0 errors'. get_incidents distinguishes only 'inbox unavailable' (pipeline_enabled=false) from 'inbox empty' — it cannot say 'the inbox answered but the proxy log query failed'. Retiring get_errors without this reintroduces a Silent Failure Prohibition violation.",
		},
		{
			field: "summary",
			id:    "output.summary", kind: "output_field", status: statusToBeClosed,
			candidates:   []string{"summary"},
			errDesc:      "summary: rendered text in the typed output struct",
			availability: availabilityPresentationDiffers,
			availabilityReason: "The same rendered text is returned, in the CallToolResult content instead of a typed struct field. Every caller receives it; only the transport " +
				"differs, which is the definition of a presentation difference under the availability bar.",
			detail: "Transport difference rather than lost information: get_incidents renders the same compact text into the CallToolResult content instead of a typed output field. Callers that read the struct (not the content) see nothing. Low severity, but it is a real shape change for the migration.",
		},
	}
}

func computeOutputDivergences() []divergence {
	return probeFields(outputProbes(), nameSet(jsonFieldNames(reflect.TypeOf(GetIncidentsOutput{}), "")))
}

// itemProbes covers every field of unifiedError. The carried-over fields are
// probed by name (not declared) so a refutation stays GUARDED: `page` is covered
// today only because incidentView exposes context.url, and if that ever went
// away the divergence would appear rather than the refutation going stale.
func itemProbes() []fieldProbe {
	carried := func(field, altName, note string) fieldProbe {
		cands := []string{field}
		if altName != "" {
			cands = append(cands, altName)
		}
		return fieldProbe{
			field: field, id: "item." + field, kind: "item_field", status: statusToBeClosed,
			candidates: cands, errDesc: field, detail: note,
		}
	}
	return append([]fieldProbe{
		carried("id", "fingerprint", "Item identity; correlation of the two tools' ids is separately measured by projection.identity_uncorrelatable."),
		carried("source", "", "Source label; its differing vocabulary is separately measured by projection.source_vocabulary."),
		carried("severity", "", "Severity level; the two-level vs four-band scale is separately measured by projection.severity_scale_*."),
		carried("category", "", "Finer-grained error class."),
		carried("message", "summary", "Human-readable text; carried as incidentView.summary."),
		carried("page", "context.url", "Page URL. REFUTED as a gap — covered by context.url, and incidentRecordToUnifiedError already reads Page from rec.Context.URL. Probed rather than assumed so the refutation cannot go stale."),
		carried("count", "", "Occurrence count."),
		carried("last_seen", "", "Most recent occurrence timestamp."),
	}, itemGapProbes()...)
}

func itemGapProbes() []fieldProbe {
	return []fieldProbe{
		{
			field: "pinned",
			id:    "item.pinned", kind: "item_field", status: statusToBeClosed,
			candidates:   []string{"pinned", "context.pinned"},
			errDesc:      "pinned: entry survives limit and auto-clear",
			availability: availabilityDataMissing,
			availabilityReason: "Read side of retention: with no per-item flag anywhere on the incidents side, which entries were preserved is not observable by any caller, " +
				"so the retention state itself is unreachable rather than differently spelled.",
			detail: "Without a per-item pinned flag, pinning is unobservable even if input.action lands — the agent cannot tell which entries it saved.",
		},
		{
			field: "tag",
			id:    "item.tag", kind: "item_field", status: statusToBeClosed,
			candidates:         []string{"tag", "context.tag"},
			errDesc:            "tag: the note stored at pin time",
			availability:       availabilityDataMissing,
			availabilityReason: "Read side of input.tag: the note is neither stored nor surfaced on the incidents side, so the caller's own annotation cannot be read back at all.",
			detail:             "Read side of input.tag. Same dependency: useless until item.pinned and input.action exist.",
		},
		{
			field: "location",
			id:    "item.location", kind: "item_field", status: statusToBeClosed,
			candidates:   []string{"location", "context.location"},
			errDesc:      "location: file:line:col of the first app stack frame",
			availability: availabilityDataMissing,
			availabilityReason: "The source-level file:line:col never enters the envelope, so it is not present in any record a caller can read — unrecoverable, not reshaped. " +
				"(ctx.URL is folded into the fingerprint as the event's 'location', which is a different fact.)",
			detail: "MEASURED, NOT ASSUMED: protocol.IncidentContext carries process_id/proxy_id/session_id/project_path/url/pid/port and no location. Note the pipeline does not discard it out of ignorance — incident.NewIncidentEvent folds ctx.URL into the fingerprint as its 'location' argument — but the source-level file:line:col never enters the envelope and is therefore unrecoverable from a record.",
		},
		{
			field: "frame_id",
			id:    "item.frame_id", kind: "item_field", status: statusToBeClosed,
			candidates:   []string{"frame_id", "context.frame_id"},
			errDesc:      "frame_id: the emitting content frame (always-wrap model)",
			availability: availabilityDataMissing,
			availabilityReason: "Which content frame raised the error is never carried into incident.Context, so under the always-wrap model (where each content frame is a distinct " +
				"context) the failing surface cannot be identified by any caller.",
			detail: "Absent from protocol.IncidentContext. This is the field gap; its consequence is the separate, worse granularity.frame_collapse divergence below.",
		},
	}
}

func computeItemDivergences() []divergence {
	return probeFields(itemProbes(), nameSet(jsonFieldNames(reflect.TypeOf(incidentView{}), "")))
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
		// Measure the consequence rather than inferring it: the hub accepts an
		// incident cursor or RFC3339/RFC3339Nano only. A Since it cannot parse is
		// REJECTED, not ignored — so the observable effect is a PATH FORK, and the
		// projection difference below is what quantifies it.
		_, rfcErr := time.Parse(time.RFC3339, errFilter.Since)
		_, nanoErr := time.Parse(time.RFC3339Nano, errFilter.Since)
		if rfcErr != nil && nanoErr != nil {
			rec := seedIncidentRecords()[0]
			shim := incidentRecordToUnifiedError(rec)
			legacy := seedLegacyErrors()[0]
			out = append(out, divergence{
				ID: "filter.since_forks_to_legacy", Kind: "behavior", Status: statusGetErrorsDefect,
				GetErrors:    "forwards since verbatim (e.g. \"5m\"), which the hub cannot parse",
				GetIncidents: "resolves the duration to an absolute RFC3339 timestamp before querying",
				Detail: "Direction is REVERSED: get_incidents is correct, get_errors is not. Nothing is silently dropped — " +
					"incidentQueryFilterToInternal (internal/daemon/hub_incidents.go) explicitly REJECTS an unparseable since rather than " +
					"returning the whole inbox, and the handler maps that to ErrInvalidArgs, so no Silent Failure Prohibition violation ships " +
					"today. The real defect is a PATH FORK: the rejected query makes collectIncidentErrors return ok=false, and get_errors falls " +
					"through to its legacy collectors, which DO parse durations correctly. So `since:\"5m\"` is answered by a different code path " +
					"than the same query without it — different ids (" + legacy.ID + " vs " + shim.ID + "), different source vocabulary (" +
					legacy.Source + " vs " + shim.Source + "), and legacy frame-aware dedup instead of fingerprint dedup. Same input, two code " +
					"paths, two answers. Does not block the superset; recorded so the migration does not port the fork forward, and so nobody " +
					"'fixes' get_incidents to match the reference here.",
			})
		}
	}

	// limit: forwarded to the inbox, or applied after the fact?
	errFilter = buildGetErrorsIncidentFilter(GetErrorsInput{Limit: 5}, true)
	incFilter = buildGetIncidentsFilter(GetIncidentsInput{Limit: 5})
	if errFilter.Limit != incFilter.Limit {
		out = append(out, divergence{
			ID: "filter.limit_scope", Kind: "behavior", Status: statusToBeClosed,
			Availability: availabilityPresentationDiffers,
			AvailabilityReason: "The whole matching set stays reachable: get_incidents is a cursor-based pull, so a caller that wants everything pages for it. " +
				"Only where the truncation happens differs.",
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
			Availability: availabilityPresentationDiffers,
			AvailabilityReason: "Both vocabularies name the same sources and every legacy label has an enum counterpart, so the origin of an error is fully reachable; " +
				"only the spelling differs. It is a migration cost for saved filters and docs, not lost information.",
			GetErrors:    "colon-delimited: " + strings.Join(unmatched, " "),
			GetIncidents: "underscore enum: browser_js http_5xx http_4xx proxy_diag process_alert build_fail …",
			Detail:       "UNPREDICTED. get_errors itself speaks two vocabularies: its legacy collectors emit browser:js / proxy:http / proxy:diagnostic / process:<id>, while its incident shim passes the pipeline's enum through verbatim. So the same tool labels the same error differently depending on which path served it. Any caller (or doc, or saved filter) keyed on the legacy strings breaks on migration, and get_incidents' sources[] filter accepts only the enum form.",
		})
	}

	// Content: counts. This entry MUST be able to close — "no to_be_closed entry
	// remains" is the signal that authorises deleting get_errors, so appending it
	// unconditionally would make the exit condition unreachable by construction.
	// It is therefore gated on two live conditions: a real numeric divergence on
	// the seed, AND the absence of any matched-set count on the incidents side.
	// Adding e.g. matched_count to GetIncidentsOutput closes it.
	//
	// The seed deliberately mixes severities so the two quantities genuinely
	// disagree: with warnings excluded, get_errors reports zero warnings while
	// the inbox still holds a warning-band entry.
	_, errOut := formatErrorsOutput(projectRecords(records), false, 25, false)
	inboxWarn := 0
	for _, rec := range records {
		if rec.Severity == "warning" || rec.Severity == "info" {
			inboxWarn++
		}
	}

	incidentOutNames := nameSet(jsonFieldNames(reflect.TypeOf(GetIncidentsOutput{}), ""))
	hasMatchedCount := false
	for _, cand := range []string{"matched_count", "match_count", "result_count", "returned_count", "error_count", "warning_count"} {
		if incidentOutNames[cand] {
			hasMatchedCount = true
			break
		}
	}

	if errOut.WarningCount != inboxWarn && !hasMatchedCount {
		out = append(out, divergence{
			ID: "counts.semantics", Kind: "behavior", Status: statusToBeClosed,
			Availability: availabilityPresentationDiffers,
			AvailabilityReason: "The matched records themselves are returned, so 'how many matched my filter' is countable by the caller from the response; " +
				"only which count the tool chooses to report differs. No underlying fact is destroyed.",
			GetErrors:    "error_count/warning_count = the returned, deduped, filtered set (counted before the display limit)",
			GetIncidents: "inbox_after = whole-inbox band occupancy after the query, independent of what this page returned",
			Detail: "MEASURED, NOT ASSUMED, and genuinely closeable: this fires only while the two quantities actually disagree on the seed AND " +
				"get_incidents exposes no matched-set count. With warnings excluded get_errors reports warning_count=" +
				strconv.Itoa(errOut.WarningCount) + " while the inbox warning band still holds " + strconv.Itoa(inboxWarn) + ". They answer " +
				"different questions — 'how many matched your filter' vs 'how full is the inbox' — so a migration that maps error_count onto " +
				"inbox_after.error is wrong. Closing this means giving get_incidents a count of the matched set (e.g. matched_count), not " +
				"reusing the band stats.",
		})
	}

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
			Availability: availabilityDataMissing,
			AvailabilityReason: "DELIBERATE BLOCKING CALL, not a default. The availability bar does not decide this one by itself, so the reasoning is recorded here. " +
				"The merged incident still reports HOW MANY times the error fired, so that fact survives; what does not survive is 'it also fired in frame B', " +
				"which is a distinct fact about a distinct failing surface and is destroyed at fingerprint time — no caller-side operation recovers it. " +
				"It blocks INDEPENDENTLY of item.frame_id, and that independence is the point: the cheap fix to that field (adding frame_id to " +
				"protocol.IncidentContext) would satisfy the field probe while the two occurrences stay merged, so if this entry did not block, the cheap " +
				"fix alone would open the gate over an unrecovered loss. Closing it requires frame identity inside incident.NewIncidentEvent's fingerprint. " +
				"This is the most expensive item in the set; if the owner would rather ship the deletion and accept per-frame occurrence loss, that is an " +
				"explicit owner override of this entry, not a reclassification anyone else should make quietly.",
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
	byAvailability := map[string]int{}
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

		// Availability is only meaningful for entries that block, and every
		// blocking-candidate entry must carry a judgement. An unclassified
		// to_be_closed entry fails loudly rather than defaulting to the
		// convenient non-blocking answer.
		if d.Status == statusToBeClosed {
			switch d.Availability {
			case availabilityDataMissing, availabilityPresentationDiffers:
				byAvailability[d.Availability]++
			case "":
				t.Errorf("%s: a %s entry must be classified %s or %s — an unclassified gap must not silently count as non-blocking",
					d.ID, statusToBeClosed, availabilityDataMissing, availabilityPresentationDiffers)
			default:
				t.Errorf("%s: unknown availability %q", d.ID, d.Availability)
			}
			if d.Availability != "" && d.AvailabilityReason == "" {
				t.Errorf("%s: the availability judgement must state its reason in the artifact", d.ID)
			}
		} else if d.Availability != "" || d.AvailabilityReason != "" {
			t.Errorf("%s: availability applies only to %s entries (status %q)", d.ID, statusToBeClosed, d.Status)
		}
	}

	// The distribution is PINNED, because availability is the one field that can
	// open the deletion gate without any gap actually closing. Demoting an entry
	// to presentation_differs must therefore be a three-place, review-visible
	// change — the measurement in this file, the record in the golden, and these
	// numbers — never a quiet edit to one of them.
	//
	// When a gap genuinely closes, drop its entry and decrement the matching
	// number in the SAME commit; that is the ratchet working, not an obstacle.
	const (
		wantDataMissing         = 9
		wantPresentationDiffers = 4
	)
	if got := byAvailability[availabilityDataMissing]; got != wantDataMissing {
		t.Errorf("golden holds %d %s entries, expected %d — if a blocker genuinely closed, shrink the golden and this number together; "+
			"if an entry was reclassified, that needs review, not a number bump",
			got, availabilityDataMissing, wantDataMissing)
	}
	if got := byAvailability[availabilityPresentationDiffers]; got != wantPresentationDiffers {
		t.Errorf("golden holds %d %s entries, expected %d", got, availabilityPresentationDiffers, wantPresentationDiffers)
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

// TestGetErrorsOracle_EveryReferenceFieldIsAccountedFor makes discovery
// exhaustive rather than hand-enumerated. Every json field of every get_errors
// reference type must either carry a probe or an explicit declaredCovered
// reason. A hand-maintained probe list rots silently and understates the gap
// while the inventory presents itself as the complete picture; this converts
// "I listed what I thought of" into "the type system enumerates it".
func TestGetErrorsOracle_EveryReferenceFieldIsAccountedFor(t *testing.T) {
	t.Parallel()

	refs := []struct {
		name   string
		typ    reflect.Type
		probes []fieldProbe
	}{
		{"GetErrorsInput", reflect.TypeOf(GetErrorsInput{}), inputProbes()},
		{"GetErrorsOutput", reflect.TypeOf(GetErrorsOutput{}), outputProbes()},
		{"unifiedError", reflect.TypeOf(unifiedError{}), itemProbes()},
	}

	total := 0
	for _, ref := range refs {
		probed := map[string]bool{}
		for _, p := range ref.probes {
			if p.field == "" {
				t.Errorf("%s: probe %q declares no field, so it cannot prove coverage of anything", ref.name, p.id)
				continue
			}
			if probed[p.field] {
				t.Errorf("%s: field %q probed twice", ref.name, p.field)
			}
			probed[p.field] = true
		}

		fields := topLevelJSONNames(ref.typ)
		for _, f := range fields {
			total++
			if probed[f] || declaredCovered[ref.name+"."+f] != "" {
				continue
			}
			t.Errorf("%s.%s is neither probed nor declared covered — the inventory claims to be complete, "+
				"so add a fieldProbe for it, or an entry in declaredCovered naming the behaviour probe that measures it",
				ref.name, f)
		}

		// A probe naming a field that no longer exists is dead weight that would
		// keep reporting a gap for something the reference no longer offers.
		actual := nameSet(fields)
		for f := range probed {
			if !actual[f] {
				t.Errorf("%s: probe for %q but the type has no such field — remove the stale probe", ref.name, f)
			}
		}
	}

	// declaredCovered must not accumulate entries for fields that vanished.
	for key := range declaredCovered {
		parts := strings.SplitN(key, ".", 2)
		if len(parts) != 2 {
			t.Errorf("declaredCovered key %q must be Type.field", key)
			continue
		}
		found := false
		for _, ref := range refs {
			if ref.name != parts[0] {
				continue
			}
			for _, f := range topLevelJSONNames(ref.typ) {
				if f == parts[1] {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("declaredCovered names %q, which no longer exists", key)
		}
	}

	t.Logf("reference fields accounted for: %d", total)
}

// TestGetErrorsOracle_SupersetHoldsWhenInventoryEmpty states the exit condition
// in code. The owner's bar is AVAILABILITY, not exactness, so the condition
// counts only data_missing entries: get_errors may be deleted exactly when no
// information it surfaced is unreachable through get_incidents. Entries that
// merely reshape the same information (presentation_differs) are migration
// notes, not blockers, and do not hold the deletion.
//
// open == 0 remains NECESSARY, NOT SUFFICIENT: this oracle measures projection
// and schema, not live handler output, so adapter coverage for every source
// must still be confirmed before deleting.
func TestGetErrorsOracle_SupersetHoldsWhenInventoryEmpty(t *testing.T) {
	t.Parallel()

	open, reshaped := 0, 0
	for _, d := range computeInventory() {
		if d.Status != statusToBeClosed {
			continue
		}
		switch d.Availability {
		case availabilityDataMissing:
			open++
		case availabilityPresentationDiffers:
			reshaped++
		default:
			// Fail loud rather than counting an unclassified gap as harmless.
			t.Errorf("%s: unclassified %s entry (availability %q) — classify it before the exit condition can mean anything",
				d.ID, statusToBeClosed, d.Availability)
		}
	}
	t.Logf("get_errors retirement blockers remaining (data_missing): %d; reshaped-but-reachable (presentation_differs): %d", open, reshaped)
	if open == 0 {
		t.Log("SUPERSET HOLDS on availability — every fact get_errors surfaced is reachable through get_incidents; " +
			"the removal slice is unblocked once adapter coverage is confirmed")
	}
}
