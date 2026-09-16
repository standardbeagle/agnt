package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/finding"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// releaseQAFixture drives runReleaseQA with spies: every side effect is a
// func field the test swaps, and a shared order log proves sequencing.
type releaseQAFixture struct {
	verifyOut      VerifyChangeOutput
	verifyErr      error
	auditFindings  []releaseQAFinding
	auditErr       error
	flowSteps      []ReleaseQAFlowStep
	order          *[]string
	drillErrs      map[string]error  // drill target -> error
	drillSummaries map[string]string // drill target -> handler summary
	mergePatches   []protocol.InvestigationPatch
	merged         *protocol.Investigation
}

func (f *releaseQAFixture) deps() releaseQADeps {
	if f.order == nil {
		f.order = &[]string{}
	}
	f.mergePatches = nil
	inv := &protocol.Investigation{}
	return releaseQADeps{
		investigation: func(ctx context.Context) (*protocol.Investigation, error) {
			return inv, nil
		},
		merge: func(ctx context.Context, patch protocol.InvestigationPatch) error {
			f.mergePatches = append(f.mergePatches, patch)
			f.merged = protocol.MergeInvestigation(inv, patch, testNow())
			return nil
		},
		verify: func(ctx context.Context, in VerifyChangeInput) (VerifyChangeOutput, error) {
			*f.order = append(*f.order, "verify")
			return f.verifyOut, f.verifyErr
		},
		flowStep: func(ctx context.Context, step ReleaseQAFlowStep) error {
			*f.order = append(*f.order, "flow:"+step.Action)
			return nil
		},
		audit: func(ctx context.Context, tool, profile string) ([]releaseQAFinding, error) {
			*f.order = append(*f.order, "audit:"+tool)
			return f.auditFindings, f.auditErr
		},
		drill: func(ctx context.Context, area string, finding releaseQAFinding) (string, error) {
			target := finding.Selector
			if target == "" {
				target = finding.URL
			}
			*f.order = append(*f.order, "drill:"+area+":"+target)
			if f.drillErrs != nil {
				if err, ok := f.drillErrs[target]; ok {
					return "", err
				}
			}
			if f.drillSummaries != nil {
				if s, ok := f.drillSummaries[target]; ok {
					return s, nil
				}
			}
			return "", nil
		},
	}
}

func releaseQAPassVerify() VerifyChangeOutput {
	return VerifyChangeOutput{Status: "PASS", Header: "verify_change: PASS (2 resolved, 0 persist, 0 new)"}
}

func countOrder(order []string, prefix string) int {
	n := 0
	for _, o := range order {
		if strings.HasPrefix(o, prefix) {
			n++
		}
	}
	return n
}

func indexOf(order []string, prefix string) int {
	for i, o := range order {
		if strings.HasPrefix(o, prefix) {
			return i
		}
	}
	return -1
}

func TestReleaseQA_RunsExactlyOneBroadAudit(t *testing.T) {
	// One broad audit per call, before any drill-down — even when drill-down runs.
	fx := &releaseQAFixture{
		verifyOut: releaseQAPassVerify(),
		auditFindings: []releaseQAFinding{
			{ID: "r1", Type: "overflow", Severity: "error", Selector: ".sidebar"},
		},
	}
	out, err := runReleaseQA(context.Background(), ReleaseQAInput{ProxyID: "dev"}, fx.deps())
	if err != nil {
		t.Fatalf("runReleaseQA: %v", err)
	}
	if n := countOrder(*fx.order, "audit:"); n != 1 {
		t.Fatalf("broad audit ran %d times, want exactly 1 (order=%v)", n, *fx.order)
	}
	if countOrder(*fx.order, "drill:") == 0 {
		t.Fatalf("expected drill-down to run (order=%v)", *fx.order)
	}
	if ai, di := indexOf(*fx.order, "audit:"), indexOf(*fx.order, "drill:"); ai == -1 || di == -1 || ai > di {
		t.Fatalf("audit must run before drill-down (order=%v)", *fx.order)
	}
	if out.Header == "" {
		t.Fatal("header empty")
	}
}

func TestReleaseQA_FlowRunsBeforeVerifyBeforeAuditBeforeDrill(t *testing.T) {
	// Documented order: explicit flow steps -> verify -> broad audit -> drill.
	fx := &releaseQAFixture{
		verifyOut: releaseQAPassVerify(),
		auditFindings: []releaseQAFinding{
			{ID: "r1", Type: "overflow", Severity: "error", Selector: ".sidebar"},
		},
	}
	in := ReleaseQAInput{ProxyID: "dev", Flow: []ReleaseQAFlowStep{
		{Action: "navigate", URL: "http://localhost:3000/checkout"},
		{Action: "exec", Selector: ".pay-btn"},
	}}
	if _, err := runReleaseQA(context.Background(), in, fx.deps()); err != nil {
		t.Fatalf("runReleaseQA: %v", err)
	}
	fi := indexOf(*fx.order, "flow:")
	vi := indexOf(*fx.order, "verify")
	ai := indexOf(*fx.order, "audit:")
	di := indexOf(*fx.order, "drill:")
	if fi == -1 || vi == -1 || ai == -1 || di == -1 {
		t.Fatalf("missing stage(s) (order=%v)", *fx.order)
	}
	if !(fi < vi && vi < ai && ai < di) {
		t.Fatalf("want flow < verify < audit < drill (order=%v)", *fx.order)
	}
}

func TestReleaseQA_DrillsOnlyFailedAreas(t *testing.T) {
	// Fixture: responsive failed, api passed -> diagnose layout called, api_audit not.
	fx := &releaseQAFixture{
		verifyOut: releaseQAPassVerify(),
		auditFindings: []releaseQAFinding{
			{ID: "r1", Type: "overflow", Severity: "error", Selector: ".sidebar"},
		},
	}
	if _, err := runReleaseQA(context.Background(), ReleaseQAInput{ProxyID: "dev"}, fx.deps()); err != nil {
		t.Fatalf("runReleaseQA: %v", err)
	}
	foundLayout := false
	for _, o := range *fx.order {
		if o == "drill:responsive:.sidebar" {
			foundLayout = true
		}
		if strings.HasPrefix(o, "drill:api:") {
			t.Fatalf("drilled into api area that passed (order=%v)", *fx.order)
		}
	}
	if !foundLayout {
		t.Fatalf("diagnose layout drill for .sidebar not called (order=%v)", *fx.order)
	}
}

func TestReleaseQA_NoDrillForQualityContentFindings(t *testing.T) {
	// Drill areas come from the finding's audit source (type), never from
	// substrings: duplicate-id and missing-alt are quality-content findings
	// with no drill-down — no api drill, no layout drill.
	fx := &releaseQAFixture{
		verifyOut: releaseQAPassVerify(),
		auditFindings: []releaseQAFinding{
			{ID: "d1", Type: "duplicate-id", Severity: "error", Selector: "#card"},
			{ID: "a1", Type: "missing-alt", Severity: "warning", Selector: "img.hero"},
		},
	}
	if _, err := runReleaseQA(context.Background(), ReleaseQAInput{ProxyID: "dev"}, fx.deps()); err != nil {
		t.Fatalf("runReleaseQA: %v", err)
	}
	if n := countOrder(*fx.order, "drill:"); n != 0 {
		t.Fatalf("quality-content findings must not drill; got %d drill(s) (order=%v)", n, *fx.order)
	}
}

func TestReleaseQA_DrillLinesCarrySummary(t *testing.T) {
	// Each drill-down line carries the summary the diagnose/api_audit handler
	// returned — not just ok/failed.
	fx := &releaseQAFixture{
		verifyOut: releaseQAPassVerify(),
		auditFindings: []releaseQAFinding{
			{ID: "r1", Type: "overflow", Severity: "error", Selector: ".sidebar"},
		},
		drillSummaries: map[string]string{".sidebar": "layout: 2 overflow cause(s) on .sidebar"},
	}
	out, err := runReleaseQA(context.Background(), ReleaseQAInput{ProxyID: "dev"}, fx.deps())
	if err != nil {
		t.Fatalf("runReleaseQA: %v", err)
	}
	if len(out.DrillDown) != 1 {
		t.Fatalf("drill-down lines = %d, want 1", len(out.DrillDown))
	}
	if !strings.Contains(out.DrillDown[0].Detail, "2 overflow cause(s)") {
		t.Fatalf("drill line must carry the handler summary, got %q", out.DrillDown[0].Detail)
	}
	if !strings.Contains(formatReleaseQACompact(out), "2 overflow cause(s)") {
		t.Fatalf("compact report must render the handler summary:\n%s", formatReleaseQACompact(out))
	}
}

func TestReleaseQA_DefaultAuditUsesAuditAll(t *testing.T) {
	// The default quality broad audit is the FULL aggregate __devtool.auditAll
	// (dom/css/security/seo/performance/api/loading/accessibility), not the
	// narrower auditPageQuality. auditAll returns a Promise in every mode
	// (Promise.resolve on the sync path), so the exec code MUST resolve via
	// .then and stringify inside the then callback — JSON.stringify(Promise)
	// is "{}", which would fake an empty finding set and a PASS.
	code := releaseQAQualityAuditCode()
	if !strings.Contains(code, "d.auditAll(") {
		t.Fatalf("default quality audit must invoke __devtool.auditAll; got:\n%s", code)
	}
	if strings.Contains(code, "auditPageQuality(") {
		t.Fatalf("default quality audit must not call auditPageQuality (sync SEO-only audit); got:\n%s", code)
	}
	thenIdx := strings.Index(code, ".then(")
	if thenIdx == -1 {
		t.Fatalf("auditAll returns a Promise; exec code must resolve via .then; got:\n%s", code)
	}
	strIdx := strings.Index(code, "JSON.stringify(r)")
	if strIdx == -1 || strIdx < thenIdx {
		t.Fatalf("stringify must happen inside the then callback, after the Promise resolves; got:\n%s", code)
	}
}

func TestReleaseQA_MergedFindingsCarryTargetedProducerArgs(t *testing.T) {
	// Verify_change re-runs producers by recorded Producer.Args. release_qa's
	// verify producer is the TARGETED drill-down (never the broad audit — one
	// broad audit per release_qa call, and standalone verify_change runs
	// none), so each merged finding must carry per-finding args:
	// finding_id, type, area, selector/url.
	fx := &releaseQAFixture{
		verifyOut: releaseQAPassVerify(),
		auditFindings: []releaseQAFinding{
			{ID: "r1", Type: "overflow", Severity: "error", Selector: ".sidebar"},
		},
	}
	if _, err := runReleaseQA(context.Background(), ReleaseQAInput{ProxyID: "dev"}, fx.deps()); err != nil {
		t.Fatalf("runReleaseQA: %v", err)
	}
	if fx.merged == nil {
		t.Fatal("InvestigationMerge not called")
	}
	var ref *protocol.FindingRef
	for i, fr := range fx.merged.Findings {
		if fr.Fingerprint == "r1" {
			ref = &fx.merged.Findings[i]
		}
	}
	if ref == nil || ref.Producer == nil {
		t.Fatalf("merged finding r1 missing producer: %+v", fx.merged.Findings)
	}
	args := ref.Producer.Args
	for _, key := range []string{"proxy_id", "finding_id", "type", "area", "selector"} {
		if _, ok := args[key]; !ok {
			t.Fatalf("producer args missing %q (targeted verify needs it): %v", key, args)
		}
	}
	if args["finding_id"] != "r1" || args["area"] != "responsive" || args["selector"] != ".sidebar" {
		t.Fatalf("producer args wrong: %v", args)
	}
}

func TestReleaseQA_QualityAuditCodeAwaitsPromise(t *testing.T) {
	// auditAll returns a Promise in every mode; JSON.stringify(Promise) is "{}"
	// which would fake a clean audit and a PASS. The exec code must resolve via
	// .then before stringify.
	code := releaseQAQualityAuditCode()
	thenIdx := strings.Index(code, ".then(")
	if thenIdx == -1 {
		t.Fatalf("quality audit code must await the auditPageQuality Promise via .then; got:\n%s", code)
	}
	strIdx := strings.Index(code, "JSON.stringify(r)")
	if strIdx == -1 || strIdx < thenIdx {
		t.Fatalf("stringify must happen inside the then callback, after the Promise resolves; got:\n%s", code)
	}
}

func TestReleaseQA_MergedFindingsDispatchInVerifyChange(t *testing.T) {
	// Findings merged by release_qa name producer "release_qa"; that producer
	// must be in the verify_change dispatch table, and a verify_change run
	// over the merged patch must not warn stale_finding_id.
	producers := defaultVerifyProducers(&DaemonTools{}, "")
	if _, ok := producers["release_qa"]; !ok {
		t.Fatalf("defaultVerifyProducers must dispatch release_qa; keys=%v", producerKeys(producers))
	}
	inv := &protocol.Investigation{Findings: []protocol.FindingRef{{
		Fingerprint: "r1",
		Severity:    "error",
		Source:      "quality",
		Producer: &finding.Producer{Tool: "release_qa", Args: map[string]any{
			"proxy_id": "dev",
			"audit":    "quality",
		}},
	}}}
	out, err := runVerifyChange(context.Background(), VerifyChangeInput{}, verifyDeps{
		investigation: func(ctx context.Context) (*protocol.Investigation, error) { return inv, nil },
		merge:         func(ctx context.Context, patch protocol.InvestigationPatch) error { return nil },
		producers: map[string]verifyProducerFunc{
			"release_qa": func(ctx context.Context, args map[string]any) ([]string, error) { return nil, nil },
		},
		screenshot: func(ctx context.Context, proxyID, name string) (string, error) { return "", nil },
	})
	if err != nil {
		t.Fatalf("runVerifyChange: %v", err)
	}
	for _, w := range out.CollectionWarnings {
		if strings.Contains(w, "stale_finding_id") {
			t.Fatalf("merged release_qa finding must not warn stale_finding_id: %v", out.CollectionWarnings)
		}
	}
}

func TestReleaseQA_OneBroadAuditWithPriorReleaseQAFindings(t *testing.T) {
	// The Investigation already holds a release_qa-produced finding. The call
	// must STILL run exactly one broad audit (its own): the in-process verify
	// step rechecks the prior finding through the targeted producer with
	// per-finding args — never a broad audit. Standalone verify_change is
	// covered by the same producer contract.
	inv := &protocol.Investigation{Findings: []protocol.FindingRef{{
		Fingerprint: "r1",
		Severity:    "error",
		Source:      "quality",
		Producer: &finding.Producer{Tool: "release_qa", Args: map[string]any{
			"proxy_id": "dev", "audit": "quality", "finding_id": "r1",
			"type": "overflow", "area": "responsive", "selector": ".sidebar",
		}},
	}}}
	broadAudits := 0
	producerCalls := 0
	fx := &releaseQAFixture{verifyOut: releaseQAPassVerify()}
	deps := fx.deps()
	deps.verify = func(ctx context.Context, in VerifyChangeInput) (VerifyChangeOutput, error) {
		// Real runVerifyChange over the pre-loaded Investigation, with the
		// release_qa producer spied: it must be invoked with the per-finding
		// targeted args, and it is a drill-level producer — no audit runs here.
		return runVerifyChange(ctx, in, verifyDeps{
			investigation: func(ctx context.Context) (*protocol.Investigation, error) { return inv, nil },
			merge:         func(ctx context.Context, patch protocol.InvestigationPatch) error { return nil },
			producers: map[string]verifyProducerFunc{
				"release_qa": func(ctx context.Context, args map[string]any) ([]string, error) {
					producerCalls++
					if args["finding_id"] != "r1" || args["area"] != "responsive" || args["selector"] != ".sidebar" {
						t.Errorf("release_qa verify producer must get per-finding targeted args; got %v", args)
					}
					return nil, nil // recheck: no longer present
				},
			},
			screenshot: func(ctx context.Context, proxyID, name string) (string, error) { return "", nil },
		})
	}
	deps.audit = func(ctx context.Context, tool, profile string) ([]releaseQAFinding, error) {
		broadAudits++
		return nil, nil
	}
	out, err := runReleaseQA(context.Background(), ReleaseQAInput{ProxyID: "dev"}, deps)
	if err != nil {
		t.Fatalf("runReleaseQA: %v", err)
	}
	if broadAudits != 1 {
		t.Fatalf("broad audit ran %d times, want exactly 1 (prior release_qa findings must not trigger another)", broadAudits)
	}
	if producerCalls != 1 {
		t.Fatalf("release_qa verify producer ran %d times, want 1", producerCalls)
	}
	if out.Status != "PASS" {
		t.Fatalf("status=%q, want PASS (prior finding rechecked resolved, clean audit)", out.Status)
	}
}

func producerKeys(m map[string]verifyProducerFunc) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestReleaseQA_PassWhenAuditCleanAndVerifyPass(t *testing.T) {
	fx := &releaseQAFixture{verifyOut: releaseQAPassVerify()}
	out, err := runReleaseQA(context.Background(), ReleaseQAInput{ProxyID: "dev"}, fx.deps())
	if err != nil {
		t.Fatalf("runReleaseQA: %v", err)
	}
	if out.Status != "PASS" || out.Header != "release_qa: PASS" {
		t.Fatalf("status=%q header=%q", out.Status, out.Header)
	}
}

func TestReleaseQA_FailPropagatesFromVerify(t *testing.T) {
	fx := &releaseQAFixture{
		verifyOut: VerifyChangeOutput{Status: "FAIL", Header: "verify_change: FAIL (0 resolved, 1 persist, 0 new)"},
	}
	out, err := runReleaseQA(context.Background(), ReleaseQAInput{ProxyID: "dev"}, fx.deps())
	if err != nil {
		t.Fatalf("runReleaseQA: %v", err)
	}
	if out.Status != "FAIL" || out.Header != "release_qa: FAIL" {
		t.Fatalf("status=%q header=%q", out.Status, out.Header)
	}
}

func TestReleaseQA_PartialFailureStillReportsAllSections(t *testing.T) {
	// Drill-down exec error -> warning line, header FAIL, all four sections render, no panic.
	fx := &releaseQAFixture{
		verifyOut: releaseQAPassVerify(),
		auditFindings: []releaseQAFinding{
			{ID: "r1", Type: "overflow", Severity: "error", Selector: ".sidebar"},
		},
		drillErrs: map[string]error{".sidebar": fmt.Errorf("exec failed: boom")},
	}
	out, err := runReleaseQA(context.Background(), ReleaseQAInput{ProxyID: "dev"}, fx.deps())
	if err != nil {
		t.Fatalf("runReleaseQA: %v", err)
	}
	if out.Status != "FAIL" || out.Header != "release_qa: FAIL" {
		t.Fatalf("status=%q header=%q", out.Status, out.Header)
	}
	if len(out.Warnings) == 0 {
		t.Fatal("expected a warning for the failed drill-down")
	}
	compact := formatReleaseQACompact(out)
	for _, section := range []string{"verify", "audit", "drill-down", "warnings"} {
		if !strings.Contains(compact, section) {
			t.Fatalf("compact report missing section %q:\n%s", section, compact)
		}
	}
}

func TestReleaseQA_MergesFailedAreas(t *testing.T) {
	fx := &releaseQAFixture{
		verifyOut: releaseQAPassVerify(),
		auditFindings: []releaseQAFinding{
			{ID: "r1", Type: "overflow", Severity: "error", Selector: ".sidebar"},
		},
	}
	if _, err := runReleaseQA(context.Background(), ReleaseQAInput{ProxyID: "dev"}, fx.deps()); err != nil {
		t.Fatalf("runReleaseQA: %v", err)
	}
	if fx.merged == nil {
		t.Fatal("InvestigationMerge not called")
	}
	foundArea := false
	for _, a := range fx.merged.FailedAreas {
		if a == "responsive" {
			foundArea = true
		}
	}
	if !foundArea {
		t.Fatalf("FailedAreas = %v, want responsive", fx.merged.FailedAreas)
	}
	foundID := false
	for _, fr := range fx.merged.Findings {
		if fr.Fingerprint == "r1" {
			foundID = true
		}
	}
	if !foundID {
		t.Fatalf("merged Findings missing new id r1: %+v", fx.merged.Findings)
	}
}

func TestReleaseQA_FlowStepCodeUsesOnlyNavigateExec(t *testing.T) {
	// Explicit flow steps compile to the existing proxy navigate/exec code
	// paths only — no new browser automation. The exec step clicks through
	// the __devtool.clickElement helper, never a raw querySelector().click().
	navCode, err := releaseQAFlowCode(ReleaseQAFlowStep{Action: "navigate", URL: "http://localhost:3000/x"})
	if err != nil {
		t.Fatalf("navigate: %v", err)
	}
	wantNav, err := buildNavigateJS("goto", "http://localhost:3000/x")
	if err != nil {
		t.Fatalf("buildNavigateJS: %v", err)
	}
	if navCode != wantNav {
		t.Fatalf("navigate flow step must reuse buildNavigateJS; got %q want %q", navCode, wantNav)
	}
	clickCode, err := releaseQAFlowCode(ReleaseQAFlowStep{Action: "exec", Selector: ".save"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(clickCode, "__devtool.clickElement") {
		t.Fatalf("exec flow step must click through __devtool.clickElement; got %q", clickCode)
	}
	if strings.Contains(clickCode, "querySelector(") {
		t.Fatalf("exec flow step must not use raw querySelector().click(); got %q", clickCode)
	}
	if _, err := releaseQAFlowCode(ReleaseQAFlowStep{Action: "screenshot"}); err == nil {
		t.Fatal("unknown flow action must fail fast")
	}
	if _, err := releaseQAFlowCode(ReleaseQAFlowStep{Action: "navigate"}); err == nil {
		t.Fatal("navigate without url must fail fast")
	}
}
