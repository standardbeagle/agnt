package agentbench

import "errors"

// Thresholds holds every numeric policy knob the scorer uses.
type Thresholds struct {
	MaxRepeatedStateReads int
	MaxRawJSSteps         int
	MaxBroadAudits        int
	BytesPerToken         int
}

// DefaultThresholds returns the shipped policy.
func DefaultThresholds() Thresholds {
	return Thresholds{MaxRepeatedStateReads: 1, MaxRawJSSteps: 0, MaxBroadAudits: 1, BytesPerToken: 4}
}

// Report is the scorer output for one trace.
type Report struct {
	Scenario                         string `json:"scenario"`
	Steps                            int    `json:"steps"`
	CallsToFirstFinding              int    `json:"calls_to_first_finding"`
	ReturnedBytes                    int    `json:"returned_bytes"`
	TokensEstimate                   int    `json:"tokens_estimate"`
	RepeatedStateReads               int    `json:"repeated_state_reads"`
	RawJSSteps                       int    `json:"raw_js_steps"`
	BroadAuditCount                  int    `json:"broad_audit_count"`
	ScreenshotBeforeVisualHypothesis bool   `json:"screenshot_before_visual_hypothesis"`
}

// Score derives the six efficiency metrics from trace.
func Score(trace *Trace) (Report, error) {
	return ScoreWithThresholds(trace, DefaultThresholds())
}

// ScoreWithThresholds is Score with caller-supplied thresholds.
func ScoreWithThresholds(trace *Trace, th Thresholds) (Report, error) {
	return Report{}, errors.New("agentbench: scorer not implemented")
}
