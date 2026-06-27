package tools

import (
	"sort"
	"time"
)

// PageTriageOutput is the at-a-glance triage view of a single page session: the
// top few signals across every category, plus a correlation of what happened
// AFTER the user's last action. It answers "the thing I just did on this screen
// isn't working — what's going on?" in one call, then points at the deeper
// tools for remediation.
type PageTriageOutput struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	PageTitle  string `json:"page_title,omitempty"`
	Active     bool   `json:"active"`
	LoadTimeMs int64  `json:"load_time_ms,omitempty"`

	ErrorCount int            `json:"error_count"`
	TopErrors  []ErrorSummary `json:"top_errors,omitempty"`

	// FrameworkDiagnostics lifts recognized React/Next/Vue/Svelte/Solid runtime
	// messages out of the raw error stream and names the bug class with the
	// correct fix — the runtime-only signals an agent is otherwise blind to.
	FrameworkDiagnostics []FrameworkDiagnostic `json:"framework_diagnostics,omitempty"`

	InteractionCount int            `json:"interaction_count"`
	LastActions      []TriageAction `json:"last_actions,omitempty"`

	MutationCount   int            `json:"mutation_count"`
	MutationsByType map[string]int `json:"mutations_by_type,omitempty"`

	ResourceCount   int              `json:"resource_count"`
	FailedResources []TriageResource `json:"failed_resources,omitempty"`

	Performance *TriagePerf `json:"performance,omitempty"`

	// AfterLastAction correlates signals that occurred after the most recent
	// interaction — the "did my click break something" surface.
	AfterLastAction *TriageConsequence `json:"after_last_action,omitempty"`

	NextTools []string `json:"next_tools,omitempty"`
	Hint      string   `json:"hint,omitempty"`
}

// TriageAction is one recent user interaction, newest first.
type TriageAction struct {
	EventType string `json:"event_type"`
	Selector  string `json:"selector,omitempty"`
	Value     string `json:"value,omitempty"`
}

// TriageResource is a failed sub-resource (HTTP status >= 400).
type TriageResource struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
}

// TriagePerf is the headline performance numbers.
type TriagePerf struct {
	LoadMs int64 `json:"load_ms,omitempty"`
	FCPMs  int64 `json:"fcp_ms,omitempty"`
	DCLMs  int64 `json:"dcl_ms,omitempty"`
}

// TriageConsequence summarizes what happened after the user's last action.
type TriageConsequence struct {
	Action         string `json:"action"`          // e.g. "click #submit"
	ErrorsSince    int    `json:"errors_since"`    // errors after that action
	MutationsSince int    `json:"mutations_since"` // DOM mutations after that action
	SampleError    string `json:"sample_error,omitempty"`
}

const triageTopN = 3

// convertToPageTriage projects the compact wire session onto the triage view.
func convertToPageTriage(m map[string]interface{}) PageTriageOutput {
	out := PageTriageOutput{
		ID:               getString(m, "id"),
		URL:              getString(m, "url"),
		PageTitle:        getString(m, "page_title"),
		Active:           getBool(m, "active"),
		LoadTimeMs:       getInt64(m, "load_time_ms"),
		ErrorCount:       getInt(m, "error_count"),
		InteractionCount: getInt(m, "interaction_count"),
		MutationCount:    getInt(m, "mutation_count"),
		ResourceCount:    getInt(m, "resource_count"),
	}

	errs := asMaps(m["errors"])
	out.TopErrors = topErrors(errs, triageTopN)
	if out.ErrorCount == 0 {
		out.ErrorCount = len(errs)
	}
	out.FrameworkDiagnostics = classifyDiagnostics(errs)

	interactions := asMaps(m["interactions"])
	out.LastActions, _ = lastActions(interactions, triageTopN)

	mutations := asMaps(m["mutations"])
	out.MutationsByType = countBy(mutations, "mutation_type")

	for _, fr := range asMaps(m["failed_resources"]) {
		out.FailedResources = append(out.FailedResources, TriageResource{
			URL:    getString(fr, "url"),
			Status: getInt(fr, "status"),
		})
		if len(out.FailedResources) >= triageTopN {
			break
		}
	}

	if perf, ok := m["performance"].(map[string]interface{}); ok {
		out.Performance = &TriagePerf{
			LoadMs: getInt64(perf, "load_event_end"),
			FCPMs:  getInt64(perf, "first_contentful_paint"),
			DCLMs:  getInt64(perf, "dom_content_loaded"),
		}
	}

	out.AfterLastAction = correlateAfterLastAction(interactions, errs, mutations)

	// Point at the tools that actually remediate; triage is orientation only.
	out.NextTools = []string{
		"get_incidents — full, deduped, prioritized errors with remediation hints",
		"proxylog — network requests/responses for this page",
		"currentpage {action:\"summary\"} — by-type rollups; {action:\"get\", raw:true} — full detail",
	}
	if len(out.FrameworkDiagnostics) > 0 {
		out.Hint = "Recognized framework runtime diagnostics — see framework_diagnostics for the bug class, the correct fix, and the common wrong fix to avoid before editing source."
	} else if out.ErrorCount == 0 && out.InteractionCount == 0 && len(out.FailedResources) == 0 {
		out.Hint = "No errors, failed resources, or interactions recorded on this page yet."
	}

	return out
}

// topErrors dedupes by message and returns the n highest-count groups.
func topErrors(errs []map[string]interface{}, n int) []ErrorSummary {
	byMsg := map[string]*ErrorSummary{}
	var order []string
	for _, e := range errs {
		msg := getString(e, "message")
		key := msg
		if len(key) > 100 {
			key = key[:100]
		}
		if es, ok := byMsg[key]; ok {
			es.Count++
		} else {
			t := getString(e, "type")
			if t == "" {
				t = "Error"
			}
			byMsg[key] = &ErrorSummary{Message: msg, Type: t, Count: 1}
			order = append(order, key)
		}
	}
	groups := make([]ErrorSummary, 0, len(order))
	for _, k := range order {
		groups = append(groups, *byMsg[k])
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Count > groups[j].Count })
	if len(groups) > n {
		groups = groups[:n]
	}
	return groups
}

// lastActions returns the last n interactions newest-first, plus the timestamp
// of the most recent one.
func lastActions(interactions []map[string]interface{}, n int) ([]TriageAction, time.Time) {
	var last time.Time
	if len(interactions) > 0 {
		last = eventTime(interactions[len(interactions)-1])
	}
	start := len(interactions) - n
	if start < 0 {
		start = 0
	}
	var actions []TriageAction
	for i := len(interactions) - 1; i >= start; i-- {
		im := interactions[i]
		actions = append(actions, TriageAction{
			EventType: getString(im, "event_type"),
			Selector:  targetSelector(im),
			Value:     getString(im, "value"),
		})
	}
	return actions, last
}

// correlateAfterLastAction counts errors/mutations that occurred strictly after
// the most recent interaction — the high-signal "my click broke it" surface.
func correlateAfterLastAction(interactions, errs, mutations []map[string]interface{}) *TriageConsequence {
	if len(interactions) == 0 {
		return nil
	}
	lastIM := interactions[len(interactions)-1]
	t := eventTime(lastIM)
	if t.IsZero() {
		return nil
	}
	label := getString(lastIM, "event_type")
	if sel := targetSelector(lastIM); sel != "" {
		label += " " + sel
	}

	c := &TriageConsequence{Action: label}
	for _, e := range errs {
		if eventTime(e).After(t) {
			c.ErrorsSince++
			if c.SampleError == "" {
				c.SampleError = getString(e, "message")
			}
		}
	}
	for _, mut := range mutations {
		if eventTime(mut).After(t) {
			c.MutationsSince++
		}
	}
	if c.ErrorsSince == 0 && c.MutationsSince == 0 {
		return nil // nothing happened after the action — no signal to report
	}
	return c
}

// --- helpers -----------------------------------------------------------------

func asMaps(v interface{}) []map[string]interface{} {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(arr))
	for _, it := range arr {
		if m, ok := it.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

func countBy(items []map[string]interface{}, key string) map[string]int {
	if len(items) == 0 {
		return nil
	}
	out := map[string]int{}
	for _, it := range items {
		k := getString(it, key)
		if k == "" {
			k = "unknown"
		}
		out[k]++
	}
	return out
}

func targetSelector(im map[string]interface{}) string {
	if tgt, ok := im["target"].(map[string]interface{}); ok {
		return getString(tgt, "selector")
	}
	return ""
}

func eventTime(m map[string]interface{}) time.Time {
	s := getString(m, "timestamp")
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}
