package agentbench

import (
	"fmt"
	"sort"
	"strings"
)

// Thresholds holds every numeric policy knob the scorer uses. Tests assert
// against DefaultThresholds(); no numeric literal threshold may appear inside
// a test body.
type Thresholds struct {
	// MaxRepeatedStateReads is how many duplicate state reads (same
	// state tool, same args) are tolerated before the trace is wasteful.
	MaxRepeatedStateReads int
	// MaxRawJSSteps is how many raw (non-__devtool) proxy exec steps are
	// tolerated. Zero: the documented helpers should always suffice.
	MaxRawJSSteps int
	// MaxBroadAudits is how many full-profile, unscoped audits one trace
	// may run before further ones count as repeated waste.
	MaxBroadAudits int
	// BytesPerToken converts returned_bytes into tokens_estimate.
	BytesPerToken int
}

// DefaultThresholds returns the shipped policy.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MaxRepeatedStateReads: 1,
		MaxRawJSSteps:         0,
		MaxBroadAudits:        1,
		BytesPerToken:         4,
	}
}

// Report is the scorer output for one trace.
type Report struct {
	Scenario string `json:"scenario"`
	Steps    int    `json:"steps"`
	// CallsToFirstFinding is the 1-based index of the first step flagged
	// useful=true; 0 when no step is flagged.
	CallsToFirstFinding int `json:"calls_to_first_finding"`
	// ReturnedBytes sums response_bytes across all steps.
	ReturnedBytes int `json:"returned_bytes"`
	// TokensEstimate is ReturnedBytes / Thresholds.BytesPerToken.
	TokensEstimate int `json:"tokens_estimate"`
	// RepeatedStateReads counts state-tool calls (currentpage, proxy
	// status) beyond the first with identical args.
	RepeatedStateReads int `json:"repeated_state_reads"`
	// RawJSSteps counts kind=exec steps whose code does not begin with a
	// __devtool helper prefix.
	RawJSSteps int `json:"raw_js_steps"`
	// BroadAuditCount counts full-profile audits with no selector/area.
	// The second and later occurrences are the repeated waste
	// Thresholds.MaxBroadAudits policies against.
	BroadAuditCount int `json:"broad_audit_count"`
	// ScreenshotBeforeVisualHypothesis is true when a screenshot step
	// precedes the first layout/responsive/visual finding.
	ScreenshotBeforeVisualHypothesis bool `json:"screenshot_before_visual_hypothesis"`
}

// stateTools are the tools whose repeated identical calls count as
// repeated_state_reads.
var stateTools = map[string]bool{"currentpage": true, "proxy": true}

// rawJSAllowedPrefixes mark a proxy exec step as going through the shipped
// __devtool helpers rather than raw page JS.
var rawJSAllowedPrefixes = []string{"__devtool.", "window.__devtool", "await __devtool"}

// visualFindingKinds anchor the screenshot_before_visual_hypothesis metric.
var visualFindingKinds = map[string]bool{"layout": true, "responsive": true, "visual": true}

// Score derives the six efficiency metrics from trace. A malformed trace
// returns an error and no report — never a partial one.
func Score(trace *Trace) (Report, error) {
	return ScoreWithThresholds(trace, DefaultThresholds())
}

// ScoreWithThresholds is Score with caller-supplied thresholds.
func ScoreWithThresholds(trace *Trace, th Thresholds) (Report, error) {
	if err := trace.validate(); err != nil {
		return Report{}, err
	}
	r := Report{Scenario: trace.Scenario, Steps: len(trace.Steps)}

	seenState := map[string]bool{}
	broadAudits := 0
	firstScreenshot := -1
	firstVisualFinding := -1

	for i, s := range trace.Steps {
		r.ReturnedBytes += s.ResponseBytes

		if r.CallsToFirstFinding == 0 && s.Useful {
			r.CallsToFirstFinding = i + 1
		}

		if s.Kind == KindState && isStateToolCall(s) {
			key := s.Tool + "\x00" + s.Action + "\x00" + canonicalArgs(s.Args)
			if seenState[key] {
				r.RepeatedStateReads++
			} else {
				seenState[key] = true
			}
		}

		if s.Kind == KindExec && isRawJS(s.Code) {
			r.RawJSSteps++
		}

		if s.Kind == KindAudit && isBroadAudit(s) {
			broadAudits++
		}

		if s.Kind == KindScreenshot && firstScreenshot < 0 {
			firstScreenshot = i
		}
		if s.Useful && visualFindingKinds[s.FindingKind] && firstVisualFinding < 0 {
			firstVisualFinding = i
		}
	}

	r.TokensEstimate = r.ReturnedBytes / th.BytesPerToken
	r.BroadAuditCount = broadAudits
	r.ScreenshotBeforeVisualHypothesis = firstScreenshot >= 0 &&
		firstVisualFinding >= 0 && firstScreenshot < firstVisualFinding
	return r, nil
}

// isStateToolCall reports whether s is one of the dedup-tracked state reads:
// currentpage (any args) or proxy action=status.
func isStateToolCall(s Step) bool {
	if !stateTools[s.Tool] {
		return false
	}
	if s.Tool == "proxy" && s.Action != "status" {
		return false
	}
	return true
}

// isRawJS reports whether an exec step bypasses the __devtool helpers.
func isRawJS(code string) bool {
	c := strings.TrimSpace(code)
	for _, p := range rawJSAllowedPrefixes {
		if strings.HasPrefix(c, p) {
			return false
		}
	}
	return true
}

// isBroadAudit reports whether an audit step is full-profile and unscoped
// (no selector/area argument narrowing it).
func isBroadAudit(s Step) bool {
	if s.Args != nil {
		if v, ok := s.Args["selector"]; ok && v != nil && v != "" {
			return false
		}
		if v, ok := s.Args["area"]; ok && v != nil && v != "" {
			return false
		}
		if v, ok := s.Args["profile"]; ok {
			if str, ok := v.(string); ok && str != "" && str != "full" {
				return false
			}
		}
	}
	return true
}

// canonicalArgs renders args deterministically for duplicate detection.
func canonicalArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%v;", k, args[k])
	}
	return b.String()
}
