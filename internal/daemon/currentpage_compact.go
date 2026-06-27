package daemon

import (
	"encoding/json"
	"strings"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// compactPageSession projects a *proxy.PageSession onto the lean,
// converter-aligned wire schema used by CURRENTPAGE GET/SUMMARY.
//
// The raw PageSession carries Navigations and Resources as full HTTPLogEntry
// values — each with the complete response body and request/response headers.
// Marshaling it whole made a single GET ~17 KB for a trivial page (and scales
// with the page's asset bodies), while the tool-side converters cannot even
// read it: they expect resources as URL strings and per-kind counts that the
// raw session never emitted. This projection fixes both:
//
//   - Resources collapse to their URLs (bodies/headers dropped).
//   - Navigations / DocumentRequest (pure body bloat, surfaced by no output
//     struct) are omitted entirely.
//   - Explicit resource_count / error_count / load_time_ms / has_performance
//     are emitted so compact GET and SUMMARY report real numbers.
//   - Errors gain a derived `type` (e.g. "ReferenceError") for SUMMARY's
//     ErrorsByType / dedup. Interactions and mutations pass through with their
//     real `event_type` / `mutation_type` tags intact (they hold no bodies).
func compactPageSession(s *proxy.PageSession) map[string]interface{} {
	out := map[string]interface{}{
		"id":                s.ID,
		"url":               s.URL,
		"page_title":        s.PageTitle,
		"start_time":        s.StartTime,
		"last_activity":     s.LastActivity,
		"active":            s.Active,
		"resource_count":    len(s.Resources),
		"error_count":       len(s.Errors),
		"interaction_count": s.InteractionCount,
		"mutation_count":    s.MutationCount,
		"has_performance":   s.Performance != nil,
	}

	// Resources → URL strings only. Failed resources (status >= 400) are also
	// surfaced as {url,status} for triage ("what on this page is broken").
	urls := make([]string, 0, len(s.Resources))
	failed := make([]map[string]interface{}, 0)
	for _, r := range s.Resources {
		urls = append(urls, r.URL)
		if r.StatusCode >= 400 {
			failed = append(failed, map[string]interface{}{"url": r.URL, "status": r.StatusCode})
		}
	}
	out["resources"] = urls
	out["failed_resources"] = failed

	// Errors → body-free maps with a derived type for grouping.
	errs := make([]map[string]interface{}, 0, len(s.Errors))
	for _, e := range s.Errors {
		errs = append(errs, map[string]interface{}{
			"message":   e.Message,
			"type":      errorType(e),
			"error":     e.Error,
			"source":    e.Source,
			"lineno":    e.LineNo,
			"colno":     e.ColNo,
			"stack":     e.Stack,
			"url":       e.URL,
			"timestamp": e.Timestamp,
		})
	}
	out["errors"] = errs

	// Interactions / mutations pass through verbatim (no bodies; their real
	// event_type / mutation_type tags drive SUMMARY's by-type rollups).
	out["interactions"] = toMaps(s.Interactions)
	out["mutations"] = toMaps(s.Mutations)

	if s.Performance != nil {
		p := s.Performance
		out["load_time_ms"] = p.LoadEventEnd
		out["performance"] = map[string]interface{}{
			"navigation_start":       p.NavigationStart,
			"load_event_end":         p.LoadEventEnd,
			"dom_content_loaded":     p.DOMContentLoaded,
			"first_paint":            p.FirstPaint,
			"first_contentful_paint": p.FirstContentfulPaint,
			"page_width":             p.PageWidth,
			"page_height":            p.PageHeight,
			"viewport_width":         p.ViewportWidth,
			"viewport_height":        p.ViewportHeight,
		}
	} else {
		out["load_time_ms"] = int64(0)
	}

	return out
}

// errorType derives a short error class for grouping, e.g. "ReferenceError"
// from "ReferenceError: x is not defined" (or the "Uncaught …" message form).
func errorType(e proxy.FrontendError) string {
	for _, s := range []string{e.Error, strings.TrimPrefix(e.Message, "Uncaught ")} {
		s = strings.TrimSpace(s)
		if i := strings.IndexByte(s, ':'); i > 0 {
			if t := strings.TrimSpace(s[:i]); t != "" && !strings.ContainsAny(t, " ") {
				return t
			}
		}
	}
	return "Error"
}

// toMaps round-trips a slice of structs through JSON into []map so the wire
// payload keeps the domain JSON tags (event_type, mutation_type, target, …).
func toMaps[T any](items []T) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		b, err := json.Marshal(it)
		if err != nil {
			// Best-effort: domain structs are JSON-serialisable by construction,
			// so a marshal failure drops a single telemetry item rather than
			// failing the whole payload. Log it so a regression is diagnosable.
			debug.Log("currentpage", "toMaps marshal dropped item: %v", err)
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			debug.Log("currentpage", "toMaps unmarshal dropped item: %v", err)
			continue
		}
		out = append(out, m)
	}
	return out
}
