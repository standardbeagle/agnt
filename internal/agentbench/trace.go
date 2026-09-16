// Package agentbench scores JSON traces of AGNT debugging sessions against
// the documented-workflow baseline.
//
// A Trace is the ordered sequence of tool calls an agent made while working
// one scenario (see scenarios.go for the pinned catalogue). Score derives
// six efficiency metrics from a trace; LoadScenario loads a pinned baseline
// trace from testdata/baseline/<name>.json.
//
// The baseline traces are the "documented-workflow baseline": they transcribe
// the call sequence today's shipped guidance prescribes (the marketplace
// browser-debug skill and internal/incident/remediation.go route primaries).
// They are NOT live-model recordings — none exist; the agnt hook ring buffer
// (internal/daemon/hub_hook.go) is a transient toast feed, not a persisted
// trace.
package agentbench

import (
	"errors"
	"fmt"
)

// StepKind classifies what a step does.
type StepKind string

const (
	KindState      StepKind = "state"      // currentpage, proxy status, daemon status
	KindIncidents  StepKind = "incidents"  // get_incidents / proxylog error pulls
	KindAudit      StepKind = "audit"      // responsive_audit / api_audit / loading_audit / __devtool.audit
	KindComposite  StepKind = "composite"  // snapshot baseline/compare, replaytest
	KindExec       StepKind = "exec"       // proxy exec (code field set)
	KindScreenshot StepKind = "screenshot" // screenshot capture
	KindOther      StepKind = "other"
)

// validKinds is the closed set a trace may use.
var validKinds = map[StepKind]bool{
	KindState: true, KindIncidents: true, KindAudit: true,
	KindComposite: true, KindExec: true, KindScreenshot: true, KindOther: true,
}

// Step is one tool call in a session trace.
type Step struct {
	Tool          string         `json:"tool"`
	Action        string         `json:"action,omitempty"`
	Args          map[string]any `json:"args,omitempty"`
	Code          string         `json:"code,omitempty"`
	ResponseBytes int            `json:"response_bytes"`
	Kind          StepKind       `json:"kind"`
	// Useful marks the step that produced a real finding toward the
	// scenario's root cause. The first useful step's index is
	// calls_to_first_finding.
	Useful bool `json:"useful,omitempty"`
	// FindingKind optionally classifies a useful step: "layout",
	// "responsive", "visual", "network", "interaction", "a11y", ...
	// layout/responsive/visual findings anchor
	// screenshot_before_visual_hypothesis.
	FindingKind string `json:"finding_kind,omitempty"`
	// Prescribed marks a step whose next action came from an explicitly
	// recorded contract prescription (e.g. a release_qa sweep link the
	// shipped tools do not emit as a pointer), not from a tool response.
	Prescribed bool `json:"prescribed,omitempty"`
}

// Trace is one scenario's recorded session.
type Trace struct {
	Scenario string `json:"scenario"`
	// Provenance names the source file and lines this trace was
	// transcribed from. Required on baseline traces.
	Provenance string `json:"provenance,omitempty"`
	Steps      []Step `json:"steps"`
}

// validate enforces the schema. Malformed traces are rejected wholesale —
// Score never returns a partial report.
func (t *Trace) validate() error {
	if t == nil {
		return errors.New("agentbench: nil trace")
	}
	if len(t.Steps) == 0 {
		return errors.New("agentbench: trace has no steps")
	}
	for i, s := range t.Steps {
		if s.Tool == "" {
			return fmt.Errorf("agentbench: step %d: missing tool", i)
		}
		if s.ResponseBytes < 0 {
			return fmt.Errorf("agentbench: step %d: negative response_bytes (%d)", i, s.ResponseBytes)
		}
		if !validKinds[s.Kind] {
			return fmt.Errorf("agentbench: step %d: unknown kind %q", i, s.Kind)
		}
	}
	return nil
}
