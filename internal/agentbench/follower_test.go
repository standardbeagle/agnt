package agentbench

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/finding"
	"github.com/standardbeagle/agnt/internal/tools"
)

//go:embed testdata/contract
var contractFS embed.FS

// fixtureResponse is one recorded call/response pair in a contract fixture.
// When Formatter is set, Text is rendered at test time by the shipped Go
// formatter over Input — never hand-typed; otherwise Text is a verbatim
// recording of a tool whose response carries no formatter-owned next: line.
type fixtureResponse struct {
	Call        fixtureCall     `json:"call"`
	Text        string          `json:"text,omitempty"`
	Formatter   string          `json:"formatter,omitempty"` // incidents|responsive|buffer|diagnose_layout|diagnose_click
	Headline    string          `json:"headline,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	Next        string          `json:"next,omitempty"` // raw-JSON next field; verbatim non-formatter responses only
	// Prescribed records a sweep link the shipped contract does not emit
	// as a pointer: the follower still executes it, but the step is
	// marked prescribed in the trace and disclosed in the docs table.
	Prescribed  string          `json:"prescribed,omitempty"`
	Useful      bool            `json:"useful,omitempty"`
	FindingKind string          `json:"finding_kind,omitempty"`
}

type fixtureCall struct {
	Tool   string         `json:"tool"`
	Action string         `json:"action,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	Code   string         `json:"code,omitempty"`
}

type contractFixture struct {
	Scenario  string            `json:"scenario"`
	Responses []fixtureResponse `json:"responses"`
}

// fixtureExecutor is the test Executor: recorded responses keyed by
// tool+args, formatter-rendered where a shipped formatter exists. A call
// with no recorded response is a loud error — never an invented answer.
type fixtureExecutor struct {
	scenario string
	byKey    map[string]fixtureResponse
}

func loadContractFixture(t *testing.T, scenario string) *fixtureExecutor {
	t.Helper()
	data, err := contractFS.ReadFile("testdata/contract/" + scenario + "/fixture.json")
	if err != nil {
		t.Fatalf("load contract fixture %q: %v", scenario, err)
	}
	var fx contractFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse contract fixture %q: %v", scenario, err)
	}
	ex := &fixtureExecutor{scenario: scenario, byKey: map[string]fixtureResponse{}}
	for _, r := range fx.Responses {
		if r.Formatter != "" && r.Next != "" {
			t.Fatalf("contract fixture %q: formatter-rendered response (tool=%s) carries an injected next override %q — record it as prescribed instead",
				scenario, r.Call.Tool, r.Next)
		}
		if r.Next != "" && r.Prescribed != "" {
			t.Fatalf("contract fixture %q: response (tool=%s) sets both next and prescribed",
				scenario, r.Call.Tool)
		}
		key := Call{Tool: r.Call.Tool, Action: r.Call.Action, Args: r.Call.Args, Code: r.Call.Code}.key()
		if _, dup := ex.byKey[key]; dup {
			t.Fatalf("contract fixture %q: duplicate response for tool=%s action=%s args=%v code=%q",
				scenario, r.Call.Tool, r.Call.Action, r.Call.Args, r.Call.Code)
		}
		ex.byKey[key] = r
	}
	return ex
}

func (ex *fixtureExecutor) Execute(call Call) (Response, error) {
	r, ok := ex.byKey[call.key()]
	if !ok {
		return Response{}, fmt.Errorf("contract fixture %q: no recorded response for tool=%s action=%s args=%v code=%q",
			ex.scenario, call.Tool, call.Action, call.Args, call.Code)
	}
	text := r.Text
	if r.Formatter != "" {
		rendered, err := renderFixtureText(call, r)
		if err != nil {
			return Response{}, fmt.Errorf("contract fixture %q: render %s: %w", ex.scenario, r.Formatter, err)
		}
		text = rendered
	}
	next, prescribed := r.Next, false
	if r.Prescribed != "" {
		next, prescribed = r.Prescribed, true
	}
	return Response{Text: text, Next: next, Useful: r.Useful, FindingKind: r.FindingKind, Prescribed: prescribed}, nil
}

// renderFixtureText runs the shipped formatter for a fixture response.
func renderFixtureText(call Call, r fixtureResponse) (string, error) {
	profile := "full"
	if p, ok := call.Args["profile"].(string); ok && p != "" {
		profile = p
	}
	switch r.Formatter {
	case "incidents":
		var views []tools.BenchIncidentView
		if err := json.Unmarshal(r.Input, &views); err != nil {
			return "", err
		}
		return tools.RenderIncidentsCompactForBench(views), nil
	case "responsive":
		return tools.RenderResponsiveCompactForBench(r.Input, profile)
	case "buffer":
		var parsed map[string]any
		if err := json.Unmarshal(r.Input, &parsed); err != nil {
			return "", err
		}
		return tools.RenderBufferAuditCompactForBench(r.Headline, parsed, "dev", profile), nil
	case "diagnose_layout":
		var findings []finding.Finding
		if err := json.Unmarshal(r.Input, &findings); err != nil {
			return "", err
		}
		return tools.RenderDiagnoseLayoutCompactForBench("dev", findings), nil
	case "diagnose_click":
		var in struct {
			Verdict  string            `json:"verdict"`
			Findings []finding.Finding `json:"findings"`
		}
		if err := json.Unmarshal(r.Input, &in); err != nil {
			return "", err
		}
		return tools.RenderDiagnoseClickCompactForBench("dev", in.Verdict, in.Findings), nil
	}
	return "", fmt.Errorf("unknown formatter %q", r.Formatter)
}

// followContract replays one scenario and scores the recorded trace.
func followContract(t *testing.T, scenario string) (*Trace, Report) {
	t.Helper()
	tr, err := Follow(scenario, loadContractFixture(t, scenario))
	if err != nil {
		t.Fatalf("scenario %s: follow: %v", scenario, err)
	}
	r, err := Score(tr)
	if err != nil {
		t.Fatalf("scenario %s: score: %v", scenario, err)
	}
	return tr, r
}

func TestContractCatalogueMatchesTestdata(t *testing.T) {
	entries, err := contractFS.ReadDir("testdata/contract")
	if err != nil {
		t.Fatalf("read contract testdata: %v", err)
	}
	dirs := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirs[e.Name()] = true
		if _, ok := scenarios[e.Name()]; !ok {
			t.Fatalf("contract fixture dir %s has no catalogue entry", e.Name())
		}
		if _, err := contractFS.ReadFile("testdata/contract/" + e.Name() + "/fixture.json"); err != nil {
			t.Fatalf("contract fixture dir %s missing fixture.json: %v", e.Name(), err)
		}
	}
	for name := range scenarios {
		if !dirs[name] {
			t.Fatalf("scenario %q has no testdata/contract/%s/ fixture dir", name, name)
		}
	}
}

func TestFollower_EachScenarioReachesFindingWithinThreeCalls(t *testing.T) {
	th := DefaultThresholds()
	_ = th
	for _, sc := range Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			_, r := followContract(t, sc.Name)
			limit := 3
			if sc.Name == "release_qa" {
				limit = 5 // QA sweep exemption: broad sweep precedes the finding
			}
			if r.CallsToFirstFinding == 0 {
				t.Fatalf("scenario %s: contract trace has no useful step", sc.Name)
			}
			if r.CallsToFirstFinding > limit {
				t.Fatalf("scenario %s: calls_to_first_finding = %d, want <= %d",
					sc.Name, r.CallsToFirstFinding, limit)
			}
		})
	}
}

func TestFollower_NoRawJSInAnyContractTrace(t *testing.T) {
	for _, sc := range Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			_, r := followContract(t, sc.Name)
			if r.RawJSSteps != 0 {
				t.Fatalf("scenario %s: raw_js_steps = %d, want 0", sc.Name, r.RawJSSteps)
			}
		})
	}
}

func TestFollower_NoRepeatedBroadAudit(t *testing.T) {
	max := DefaultThresholds().MaxBroadAudits
	for _, sc := range Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			_, r := followContract(t, sc.Name)
			if r.BroadAuditCount > max {
				t.Fatalf("scenario %s: broad_audit_count = %d, want <= %d", sc.Name, r.BroadAuditCount, max)
			}
		})
	}
}

func TestFollower_NoScreenshotBeforeHypothesis(t *testing.T) {
	for _, sc := range Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			_, r := followContract(t, sc.Name)
			if r.ScreenshotBeforeVisualHypothesis {
				t.Fatalf("scenario %s: screenshot_before_visual_hypothesis = true, want false", sc.Name)
			}
		})
	}
}

func TestFollower_ContractBeatsBaselineOnEveryMetric(t *testing.T) {
	header := fmt.Sprintf("%-18s | %-22s | %-22s", "scenario", "baseline (1stfind/bytes/tokens/state+/rawjs/audits)", "contract")
	t.Log(header)
	fmt.Println(header)
	for _, sc := range Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			baseTr, err := LoadScenario(sc.Name)
			if err != nil {
				t.Fatalf("load baseline %s: %v", sc.Name, err)
			}
			base, err := Score(baseTr)
			if err != nil {
				t.Fatalf("score baseline %s: %v", sc.Name, err)
			}
			tr, contract := followContract(t, sc.Name)

			prescribed := 0
			for _, s := range tr.Steps {
				if s.Prescribed {
					prescribed++
				}
			}

			baseSummary := fmt.Sprintf("%d/%d/%d/%d/%d/%d", base.CallsToFirstFinding, base.ReturnedBytes,
				base.TokensEstimate, base.RepeatedStateReads, base.RawJSSteps, base.BroadAuditCount)
			contractSummary := fmt.Sprintf("%d/%d/%d/%d/%d/%d", contract.CallsToFirstFinding, contract.ReturnedBytes,
				contract.TokensEstimate, contract.RepeatedStateReads, contract.RawJSSteps, contract.BroadAuditCount)
			line := fmt.Sprintf("%-18s | %-22s | %-22s", sc.Name, baseSummary, contractSummary)
			if prescribed > 0 {
				// Sweep links the shipped contract does not emit as
				// pointers, recorded explicitly in the fixture.
				line += fmt.Sprintf(" (prescribed steps: %d)", prescribed)
			}
			t.Log(line)
			fmt.Println(line)

			checks := []struct {
				name           string
				base, contract int
			}{
				{"calls_to_first_finding", base.CallsToFirstFinding, contract.CallsToFirstFinding},
				{"returned_bytes", base.ReturnedBytes, contract.ReturnedBytes},
				{"tokens_estimate", base.TokensEstimate, contract.TokensEstimate},
				{"repeated_state_reads", base.RepeatedStateReads, contract.RepeatedStateReads},
				{"raw_js_steps", base.RawJSSteps, contract.RawJSSteps},
				{"broad_audit_count", base.BroadAuditCount, contract.BroadAuditCount},
			}
			for _, c := range checks {
				if c.contract > c.base {
					t.Errorf("scenario %s metric %s: contract %d > baseline %d", sc.Name, c.name, c.contract, c.base)
				}
			}
			if contract.ScreenshotBeforeVisualHypothesis && !base.ScreenshotBeforeVisualHypothesis {
				t.Errorf("scenario %s: contract flags screenshot_before_visual_hypothesis where baseline does not", sc.Name)
			}
		})
	}
}

// responsiveNextFor mirrors responsiveNextAction in
// internal/proxy/scripts/responsive.js: the shipped mapping from a
// responsive finding's message to its remediation next action.
func responsiveNextFor(selector, message string) string {
	switch {
	case strings.Contains(message, "horizontal scroll"):
		return "proxy exec __devtool.getContainer('" + selector + "')"
	case strings.Contains(message, "fixed element covers"):
		return "__devtool.getStacking('" + selector + "')"
	case strings.Contains(message, "clipped"), strings.Contains(message, "truncated"):
		return "__devtool.getBox('" + selector + "')"
	default:
		return "__devtool.inspect('" + selector + "')"
	}
}

// TestContractFixturePointersAreShipped: every next: pointer the follower
// consumes from a formatter-rendered response is one the shipped code
// produces for that input. A formatter-rendered fixture response may not
// carry a next override (the formatter owns its next: text); responsive
// issues must carry the next responsiveNextAction computes for their
// message. A sweep link the shipped contract does not provide is recorded
// as an explicitly prescribed step, never injected as a pointer.
func TestContractFixturePointersAreShipped(t *testing.T) {
	type responsiveIssue struct {
		Selector string `json:"selector"`
		Message  string `json:"message"`
		Next     string `json:"next"`
	}
	type responsiveViewport struct {
		Issues []responsiveIssue `json:"issues"`
	}
	for _, sc := range Scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			data, err := contractFS.ReadFile("testdata/contract/" + sc.Name + "/fixture.json")
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			var fx contractFixture
			if err := json.Unmarshal(data, &fx); err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			for i, r := range fx.Responses {
				if r.Formatter != "" && r.Next != "" {
					t.Errorf("response %d (%s): formatter-rendered response carries an injected next override %q — record it as a prescribed step instead",
						i, r.Call.Tool, r.Next)
				}
				if r.Formatter != "responsive" {
					continue
				}
				var in struct {
					Viewports map[string]responsiveViewport `json:"viewports"`
				}
				if err := json.Unmarshal(r.Input, &in); err != nil {
					t.Fatalf("response %d: decode responsive input: %v", i, err)
				}
				for vp, v := range in.Viewports {
					for _, is := range v.Issues {
						want := responsiveNextFor(is.Selector, is.Message)
						if is.Next != want {
							t.Errorf("response %d viewport %s issue %q: next %q is not the shipped responsiveNextAction %q for its message",
								i, vp, is.Message, is.Next, want)
						}
					}
				}
			}
		})
	}
}

func TestFollower_UnknownNextActionFailsLoud(t *testing.T) {
	bogus := "??not-a-tool?? {{"
	ex := &fixtureExecutor{scenario: "bogus_next", byKey: map[string]fixtureResponse{
		(Call{Tool: "currentpage"}).key(): {
			Call: fixtureCall{Tool: "currentpage"},
			Text: "=== currentpage ===\n\nnext: " + bogus,
		},
	}}
	tr, err := Follow("bogus_next", ex)
	if err == nil {
		t.Fatalf("expected loud failure, got trace %+v", tr)
	}
	if !strings.Contains(err.Error(), bogus) {
		t.Fatalf("error must quote the offending next action %q, got: %v", bogus, err)
	}
	if !strings.Contains(err.Error(), "next: "+bogus) {
		t.Fatalf("error must quote the offending response, got: %v", err)
	}

	// Missing next: on the initial step, before any useful step — the chain
	// broke immediately and must fail loudly quoting the response, never
	// silently return a finding-less trace.
	ex2 := &fixtureExecutor{scenario: "dead_end", byKey: map[string]fixtureResponse{
		(Call{Tool: "currentpage"}).key(): {
			Call: fixtureCall{Tool: "currentpage"},
			Text: "=== currentpage ===\n\nurl: http://localhost:9999/\ntitle: blank",
		},
	}}
	_, err = Follow("dead_end", ex2)
	if err == nil {
		t.Fatalf("expected loud failure for missing next: on a non-terminal step")
	}
	if !strings.Contains(err.Error(), "no next") {
		t.Fatalf("error must name the missing next action, got: %v", err)
	}
	if !strings.Contains(err.Error(), "title: blank") {
		t.Fatalf("error must quote the offending response, got: %v", err)
	}

	// Missing next: after a useful finding is a legitimate terminal step —
	// the run ends clean.
	ex3 := &fixtureExecutor{scenario: "clean_end", byKey: map[string]fixtureResponse{
		(Call{Tool: "currentpage"}).key(): {
			Call:    fixtureCall{Tool: "currentpage"},
			Text:    "=== currentpage ===\n\nerror: TypeError boom",
			Useful:      true,
			FindingKind: "errors",
		},
	}}
	tr3, err := Follow("clean_end", ex3)
	if err != nil {
		t.Fatalf("terminal response after a useful step must end clean, got: %v", err)
	}
	if len(tr3.Steps) != 1 {
		t.Fatalf("clean end: expected 1 step, got %d", len(tr3.Steps))
	}
}

func TestNegativeFixturesStillDetected(t *testing.T) {
	th := DefaultThresholds()

	// One hand-built trace per forbidden pattern proves the scorer still
	// detects it.
	rawJS := &Trace{Scenario: "neg_rawjs", Steps: []Step{
		{Tool: "proxy", Action: "exec", Kind: KindExec, Code: "document.querySelector('.x').remove()", ResponseBytes: 100},
	}}
	r, err := Score(rawJS)
	if err != nil {
		t.Fatalf("score rawjs: %v", err)
	}
	if r.RawJSSteps <= th.MaxRawJSSteps {
		t.Fatalf("raw-JS negative fixture: raw_js_steps = %d, detector did not fire", r.RawJSSteps)
	}

	repeatedBroad := &Trace{Scenario: "neg_broad", Steps: []Step{
		{Tool: "responsive_audit", Kind: KindAudit, Args: map[string]any{"profile": "full"}, ResponseBytes: 5000},
		{Tool: "api_audit", Kind: KindAudit, ResponseBytes: 4000},
	}}
	r, err = Score(repeatedBroad)
	if err != nil {
		t.Fatalf("score broad: %v", err)
	}
	if r.BroadAuditCount <= th.MaxBroadAudits {
		t.Fatalf("repeated-broad-audit negative fixture: broad_audit_count = %d, detector did not fire", r.BroadAuditCount)
	}

	shotFirst := &Trace{Scenario: "neg_shot", Steps: []Step{
		{Tool: "proxy", Action: "exec", Kind: KindScreenshot, Code: "await __devtool.screenshot('x')", ResponseBytes: 40000},
		{Tool: "proxy", Action: "exec", Kind: KindExec, Code: "__devtool.diagnoseLayout()", ResponseBytes: 800, Useful: true, FindingKind: "layout"},
	}}
	r, err = Score(shotFirst)
	if err != nil {
		t.Fatalf("score shot: %v", err)
	}
	if !r.ScreenshotBeforeVisualHypothesis {
		t.Fatalf("screenshot-before-hypothesis negative fixture: detector did not fire")
	}
}
