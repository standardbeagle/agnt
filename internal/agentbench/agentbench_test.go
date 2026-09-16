package agentbench

import (
	"fmt"
	"strings"
	"testing"
)

func usefulStep(tool string, kind StepKind, findingKind string) Step {
	return Step{Tool: tool, Kind: kind, ResponseBytes: 800, Useful: true, FindingKind: findingKind}
}

func TestScoreDeterministic(t *testing.T) {
	tr := &Trace{Scenario: "det", Steps: []Step{
		{Tool: "currentpage", Kind: KindState, ResponseBytes: 200},
		{Tool: "proxy", Action: "exec", Kind: KindExec, Code: "document.querySelector('.x')", ResponseBytes: 300},
		usefulStep("proxy", KindExec, "layout"),
	}}
	r1, err := Score(tr)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	r2, err := Score(tr)
	if err != nil {
		t.Fatalf("score again: %v", err)
	}
	if r1 != r2 {
		t.Fatalf("non-deterministic report:\n%+v\n%+v", r1, r2)
	}
}

func TestScoreRejectsMalformedTrace(t *testing.T) {
	good := Step{Tool: "proxy", Action: "status", Kind: KindState, ResponseBytes: 100}
	cases := map[string]Trace{
		"missing tool":              {Steps: []Step{{Kind: KindState, ResponseBytes: 10}}},
		"negative bytes":            {Steps: []Step{{Tool: "proxy", Kind: KindState, ResponseBytes: -1}}},
		"unknown kind":              {Steps: []Step{{Tool: "proxy", Kind: StepKind("bogus"), ResponseBytes: 10}}},
		"no steps":                  {},
		"one good only matters not": {Steps: []Step{good, {Tool: "", Kind: KindState}}},
	}
	for name, tr := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := Score(&tr)
			if err == nil {
				t.Fatalf("expected error, got report %+v", r)
			}
			if r != (Report{}) {
				t.Fatalf("partial report returned on error: %+v", r)
			}
		})
	}
}

func TestRawJSDetection(t *testing.T) {
	raw := Step{Tool: "proxy", Action: "exec", Kind: KindExec, Code: "document.querySelector('.modal').remove()"}
	helper := Step{Tool: "proxy", Action: "exec", Kind: KindExec, Code: "__devtool.getStacking('.dropdown-menu')"}
	windowed := Step{Tool: "proxy", Action: "exec", Kind: KindExec, Code: "window.__devtool.inspect('#x')"}
	awaited := Step{Tool: "proxy", Action: "exec", Kind: KindExec, Code: "await __devtool.screenshot('s')"}
	tr := &Trace{Scenario: "rawjs", Steps: []Step{raw, helper, windowed, awaited}}
	r, err := Score(tr)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if r.RawJSSteps != 1 {
		t.Fatalf("raw_js_steps = %d, want 1 (only document.querySelector step)", r.RawJSSteps)
	}
}

func TestRepeatedStateReads(t *testing.T) {
	tr := &Trace{Scenario: "repeat", Steps: []Step{
		{Tool: "currentpage", Kind: KindState, ResponseBytes: 100},
		{Tool: "currentpage", Kind: KindState, ResponseBytes: 100},
		{Tool: "proxy", Action: "status", Args: map[string]any{"id": "dev"}, Kind: KindState, ResponseBytes: 100},
		{Tool: "proxy", Action: "status", Args: map[string]any{"id": "dev"}, Kind: KindState, ResponseBytes: 100},
		{Tool: "proxy", Action: "status", Args: map[string]any{"id": "other"}, Kind: KindState, ResponseBytes: 100},
	}}
	r, err := Score(tr)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if r.RepeatedStateReads != 2 {
		t.Fatalf("repeated_state_reads = %d, want 2 (dup currentpage + dup proxy status)", r.RepeatedStateReads)
	}
	if r.RepeatedStateReads <= DefaultThresholds().MaxRepeatedStateReads {
		t.Fatalf("fixture must exceed MaxRepeatedStateReads to exercise the threshold")
	}
}

func TestRepeatedBroadAuditDetected(t *testing.T) {
	broad := func() Step {
		return Step{Tool: "responsive_audit", Kind: KindAudit, Args: map[string]any{"profile": "full"}, ResponseBytes: 5000}
	}
	scoped := Step{Tool: "responsive_audit", Kind: KindAudit, Args: map[string]any{"profile": "full", "selector": ".card"}, ResponseBytes: 900}
	tr := &Trace{Scenario: "audits", Steps: []Step{broad(), broad(), scoped}}
	r, err := Score(tr)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if r.BroadAuditCount != 2 {
		t.Fatalf("broad_audit_count = %d, want 2 (scoped audit excluded)", r.BroadAuditCount)
	}
	if r.BroadAuditCount <= DefaultThresholds().MaxBroadAudits {
		t.Fatalf("second full-profile audit not flagged as repeated beyond MaxBroadAudits")
	}
}

func TestScreenshotBeforeHypothesisDetected(t *testing.T) {
	shot := Step{Tool: "proxy", Action: "exec", Kind: KindScreenshot, Code: "await __devtool.screenshot('x')", ResponseBytes: 40000}
	nonVisualFinding := usefulStep("get_incidents", KindIncidents, "network")
	visualFinding := usefulStep("proxy", KindExec, "layout")

	withShot := &Trace{Scenario: "s1", Steps: []Step{shot, visualFinding}}
	r, err := Score(withShot)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if !r.ScreenshotBeforeVisualHypothesis {
		t.Fatalf("expected screenshot_before_visual_hypothesis=true")
	}

	without := &Trace{Scenario: "s2", Steps: []Step{nonVisualFinding, visualFinding, shot}}
	r2, err := Score(without)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if r2.ScreenshotBeforeVisualHypothesis {
		t.Fatalf("screenshot after the visual finding must not flag the metric")
	}
}

func TestScenarioCatalogueMatchesTestdata(t *testing.T) {
	entries, err := baselineFS.ReadDir("testdata/baseline")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	files := map[string]bool{}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		files[name] = true
		if _, ok := scenarios[name]; !ok {
			t.Fatalf("testdata file %s has no catalogue entry", e.Name())
		}
	}
	for name := range scenarios {
		if !files[name] {
			t.Fatalf("scenario %q has no testdata/baseline/%s.json", name, name)
		}
	}
}

func TestBaselineTracesLoadAndScore(t *testing.T) {
	for _, sc := range Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			tr, err := LoadScenario(sc.Name)
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if tr.Provenance == "" {
				t.Fatalf("baseline trace %q has empty provenance", sc.Name)
			}
			if !strings.Contains(tr.Provenance, "SKILL.md") && !strings.Contains(tr.Provenance, "remediation.go") {
				t.Fatalf("provenance must name a transcription source, got %q", tr.Provenance)
			}
			r, err := Score(tr)
			if err != nil {
				t.Fatalf("score: %v", err)
			}
			if r.CallsToFirstFinding == 0 {
				t.Fatalf("baseline %q: no step flagged useful", sc.Name)
			}
			if r.ReturnedBytes == 0 {
				t.Fatalf("baseline %q: returned_bytes is zero", sc.Name)
			}
			if r.TokensEstimate == 0 {
				t.Fatalf("baseline %q: tokens_estimate is zero", sc.Name)
			}
			if r.Steps != len(tr.Steps) {
				t.Fatalf("report steps %d != trace steps %d", r.Steps, len(tr.Steps))
			}
		})
	}
}

func TestBaselineScreenshotFlags(t *testing.T) {
	// The SKILL.md quick-start prescribes screenshot as step one; the
	// dead_click and blank_page baselines transcribe that, so they must
	// flag screenshot_before_visual_hypothesis.
	for _, name := range []string{"dead_click", "blank_page"} {
		tr, err := LoadScenario(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		r, err := Score(tr)
		if err != nil {
			t.Fatalf("score %s: %v", name, err)
		}
		if !r.ScreenshotBeforeVisualHypothesis {
			t.Fatalf("baseline %s: expected screenshot_before_visual_hypothesis=true", name)
		}
	}
}

func TestAgentBenchBaselineReport(t *testing.T) {
	th := DefaultThresholds()
	header := fmt.Sprintf("%-18s %6s %8s %8s %8s %8s %6s %6s %5s",
		"scenario", "steps", "1stfind", "bytes", "tokens", "state+", "rawjs", "audits", "shot<")
	t.Log(header)
	fmt.Println(header)
	for _, sc := range Scenarios() {
		tr, err := LoadScenario(sc.Name)
		if err != nil {
			t.Fatalf("load %s: %v", sc.Name, err)
		}
		r, err := ScoreWithThresholds(tr, th)
		if err != nil {
			t.Fatalf("score %s: %v", sc.Name, err)
		}
		line := fmt.Sprintf("%-18s %6d %8d %8d %8d %8d %6d %6d %5t",
			r.Scenario, r.Steps, r.CallsToFirstFinding, r.ReturnedBytes,
			r.TokensEstimate, r.RepeatedStateReads, r.RawJSSteps,
			r.BroadAuditCount, r.ScreenshotBeforeVisualHypothesis)
		t.Log(line)
		fmt.Println(line)
	}
}
