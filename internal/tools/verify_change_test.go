package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/finding"
	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/standardbeagle/go-sdk/mcp"
)

// verifyFixture builds fake deps around a recorded Investigation and a spy
// producer table. ran records every producer invocation (tool name) so tests
// can prove only target producers ran.
type verifyFixture struct {
	inv        *protocol.Investigation
	ran        *[]string
	responses  map[string][]string // tool -> ids the producer returns now
	merged     *protocol.Investigation
	shots      *[]string
	screenshot map[string]string // finding id -> ref
}

func (f *verifyFixture) deps() verifyDeps {
	f.ran = &[]string{}
	f.shots = &[]string{}
	producers := map[string]verifyProducerFunc{}
	for tool, ids := range f.responses {
		ids := ids
		producers[tool] = func(ctx context.Context, args map[string]any) ([]string, error) {
			*f.ran = append(*f.ran, tool)
			return ids, nil
		}
	}
	// A broad-audit trap: any producer name outside the target set must never
	// be invoked; if it is, the run fails loudly rather than silently passing.
	for _, trap := range []string{"__devtool.audit", "broad_audit", "currentpage"} {
		trap := trap
		producers[trap] = func(ctx context.Context, args map[string]any) ([]string, error) {
			*f.ran = append(*f.ran, trap)
			return nil, nil
		}
	}
	return verifyDeps{
		investigation: func(ctx context.Context) (*protocol.Investigation, error) {
			return f.inv, nil
		},
		merge: func(ctx context.Context, patch protocol.InvestigationPatch) error {
			f.merged = protocol.MergeInvestigation(f.inv, patch, testNow())
			f.inv = f.merged
			return nil
		},
		producers: producers,
		screenshot: func(ctx context.Context, proxyID, name string) (string, error) {
			*f.shots = append(*f.shots, name)
			if ref, ok := f.screenshot[name]; ok {
				return ref, nil
			}
			return "/shots/" + name + ".png", nil
		},
	}
}

func testNow() time.Time { return time.Now() }

func ref(id, tool string, visual bool) protocol.FindingRef {
	return protocol.FindingRef{
		Fingerprint: id,
		Source:      tool,
		Visual:      visual,
		Producer:    &finding.Producer{Tool: tool, Args: map[string]any{"proxy_id": "dev"}},
	}
}

func TestVerifyChange_ResolvedFindingReportsPass(t *testing.T) {
	fx := &verifyFixture{
		inv: &protocol.Investigation{Findings: []protocol.FindingRef{
			ref("aaaa1111", "responsive_audit", false),
			ref("bbbb2222", "api_audit", false),
		}},
		responses: map[string][]string{
			"responsive_audit": {},
			"api_audit":        {},
		},
	}
	out, err := runVerifyChange(context.Background(), VerifyChangeInput{}, fx.deps())
	if err != nil {
		t.Fatalf("runVerifyChange: %v", err)
	}
	if out.Header != "verify_change: PASS (2 resolved, 0 persist, 0 new)" {
		t.Fatalf("header = %q", out.Header)
	}
	if out.Status != "PASS" {
		t.Fatalf("status = %q", out.Status)
	}
	if len(out.Resolved) != 2 {
		t.Fatalf("resolved = %v", out.Resolved)
	}
}

func TestVerifyChange_PersistingFindingReportsFail(t *testing.T) {
	// False-PASS guard: while ANY target persists the header must be FAIL —
	// never PASS.
	fx := &verifyFixture{
		inv: &protocol.Investigation{Findings: []protocol.FindingRef{
			ref("aaaa1111", "responsive_audit", false),
			ref("bbbb2222", "api_audit", false),
		}},
		responses: map[string][]string{
			"responsive_audit": {"aaaa1111"}, // still there
			"api_audit":        {},           // resolved
		},
	}
	out, err := runVerifyChange(context.Background(), VerifyChangeInput{}, fx.deps())
	if err != nil {
		t.Fatalf("runVerifyChange: %v", err)
	}
	if out.Status == "PASS" || strings.HasPrefix(out.Header, "verify_change: PASS") {
		t.Fatalf("false PASS while aaaa1111 persists: %q", out.Header)
	}
	if out.Header != "verify_change: FAIL (1 resolved, 1 persist, 0 new)" {
		t.Fatalf("header = %q", out.Header)
	}
}

func TestVerifyChange_RunsOnlyTargetProducers(t *testing.T) {
	fx := &verifyFixture{
		inv: &protocol.Investigation{
			ActivePageSessionID: "page-1",
			Findings:            []protocol.FindingRef{ref("aaaa1111", "responsive_audit", false)},
		},
		responses: map[string][]string{"responsive_audit": {}},
	}
	_, err := runVerifyChange(context.Background(), VerifyChangeInput{}, fx.deps())
	if err != nil {
		t.Fatalf("runVerifyChange: %v", err)
	}
	for _, tool := range *fx.ran {
		if tool != "responsive_audit" {
			t.Fatalf("non-target producer ran: %v", *fx.ran)
		}
	}
	if len(*fx.ran) != 1 {
		t.Fatalf("producer ran %d times, want 1 (dedupe): %v", len(*fx.ran), *fx.ran)
	}
}

func TestVerifyChange_ScreenshotOnlyForVisualFindings(t *testing.T) {
	fx := &verifyFixture{
		inv: &protocol.Investigation{
			ActiveProxyID: "dev",
			Findings: []protocol.FindingRef{
				ref("aaaa1111", "responsive_audit", true),  // visual, resolves
				ref("bbbb2222", "responsive_audit", false), // non-visual, resolves
			},
		},
		responses: map[string][]string{"responsive_audit": {}},
	}
	out, err := runVerifyChange(context.Background(), VerifyChangeInput{ProxyID: "dev"}, fx.deps())
	if err != nil {
		t.Fatalf("runVerifyChange: %v", err)
	}
	if len(*fx.shots) != 1 {
		t.Fatalf("screenshots = %v, want exactly 1 (visual finding only)", *fx.shots)
	}
	if !strings.Contains((*fx.shots)[0], "aaaa1111") {
		t.Fatalf("screenshot name %q does not name the visual finding", (*fx.shots)[0])
	}
	if out.VisualBaselineRef == "" {
		t.Fatal("VisualBaselineRef empty after visual screenshot")
	}
	if fx.merged == nil || fx.merged.VisualBaselineRef != out.VisualBaselineRef {
		t.Fatalf("merged VisualBaselineRef = %+v", fx.merged)
	}
}

func TestVerifyChange_StaleIDWarnsPerID(t *testing.T) {
	fx := &verifyFixture{
		inv: &protocol.Investigation{Findings: []protocol.FindingRef{
			ref("aaaa1111", "responsive_audit", false),
		}},
		responses: map[string][]string{"responsive_audit": {}},
	}
	out, err := runVerifyChange(context.Background(), VerifyChangeInput{
		FindingIDs: []string{"dead0000", "aaaa1111"},
	}, fx.deps())
	if err != nil {
		t.Fatalf("runVerifyChange: %v", err)
	}
	var stale int
	for _, w := range out.CollectionWarnings {
		if strings.Contains(w, "stale_finding_id") && strings.Contains(w, "dead0000") {
			stale++
		}
	}
	if stale != 1 {
		t.Fatalf("stale warnings = %d, want 1; warnings=%v", stale, out.CollectionWarnings)
	}
	// The remaining id is still verified despite the stale one.
	if len(out.Resolved) != 1 || out.Resolved[0] != "aaaa1111" {
		t.Fatalf("resolved = %v", out.Resolved)
	}
}

func TestVerifyChange_MergesOutcomeIntoInvestigation(t *testing.T) {
	fx := &verifyFixture{
		inv: &protocol.Investigation{Findings: []protocol.FindingRef{
			ref("aaaa1111", "api_audit", false), // resolves
			ref("bbbb2222", "api_audit", false), // persists
		}},
		responses: map[string][]string{"api_audit": {"bbbb2222", "cccc3333"}},
	}
	out, err := runVerifyChange(context.Background(), VerifyChangeInput{}, fx.deps())
	if err != nil {
		t.Fatalf("runVerifyChange: %v", err)
	}
	if len(out.New) != 1 || out.New[0] != "cccc3333" {
		t.Fatalf("new = %v", out.New)
	}
	// Read back via InvestigationGet (the fake merge applied to fx.inv).
	inv, err := fx.deps().investigation(context.Background())
	if err != nil {
		t.Fatalf("investigation readback: %v", err)
	}
	got := map[string]bool{}
	for _, r := range inv.Findings {
		got[r.Fingerprint] = true
	}
	if got["aaaa1111"] {
		t.Fatal("resolved id aaaa1111 still in investigation")
	}
	if !got["bbbb2222"] || !got["cccc3333"] {
		t.Fatalf("findings after merge = %v", got)
	}
}

// TestVerifyChange_DispatcherRequestsRawAndReportsPersist drives the same
// dispatch constructors defaultVerifyProducers uses, backed by handler-faithful
// stubs: audit/diagnose handlers only fill Raw when the caller asks for it.
// The dispatcher must request raw output, see the known id, and report the
// target as persist with header FAIL — reading it as resolved is a false PASS.
func TestVerifyChange_DispatcherRequestsRawAndReportsPersist(t *testing.T) {
	rawPayload := map[string]any{"findings": []any{
		map[string]any{"id": "ffff0001", "severity": "high", "type": "duplicate-call"},
	}}
	dispatchers := map[string]func(raw *bool) verifyProducerFunc{
		"api_audit": func(raw *bool) verifyProducerFunc {
			return dispatchAPIAudit(func(_ context.Context, _ *mcp.CallToolRequest, in APIAuditInput) (*mcp.CallToolResult, APIAuditOutput, error) {
				*raw = *raw || in.Raw
				if !in.Raw {
					return nil, APIAuditOutput{Summary: "compact"}, nil
				}
				return nil, APIAuditOutput{Raw: rawPayload}, nil
			})
		},
		"loading_audit": func(raw *bool) verifyProducerFunc {
			return dispatchLoadingAudit(func(_ context.Context, _ *mcp.CallToolRequest, in LoadingAuditInput) (*mcp.CallToolResult, LoadingAuditOutput, error) {
				*raw = *raw || in.Raw
				if !in.Raw {
					return nil, LoadingAuditOutput{Summary: "compact"}, nil
				}
				return nil, LoadingAuditOutput{Raw: rawPayload}, nil
			})
		},
		"responsive_audit": func(raw *bool) verifyProducerFunc {
			return dispatchResponsiveAudit(func(_ context.Context, _ *mcp.CallToolRequest, in ResponsiveAuditInput) (*mcp.CallToolResult, ResponsiveAuditOutput, error) {
				*raw = *raw || in.Raw
				if !in.Raw {
					return nil, ResponsiveAuditOutput{Summary: "compact"}, nil
				}
				return nil, ResponsiveAuditOutput{Raw: rawPayload}, nil
			})
		},
		"diagnose": func(raw *bool) verifyProducerFunc {
			return dispatchDiagnose(func(_ context.Context, _ *mcp.CallToolRequest, in DiagnoseInput) (*mcp.CallToolResult, DiagnoseOutput, error) {
				*raw = *raw || in.Raw
				if !in.Raw {
					return nil, DiagnoseOutput{Summary: "compact"}, nil
				}
				return nil, DiagnoseOutput{Raw: rawPayload}, nil
			})
		},
	}
	for tool, mk := range dispatchers {
		t.Run(tool, func(t *testing.T) {
			var rawRequested bool
			fx := &verifyFixture{
				inv: &protocol.Investigation{Findings: []protocol.FindingRef{
					ref("ffff0001", tool, false),
				}},
			}
			deps := fx.deps()
			deps.producers = map[string]verifyProducerFunc{tool: mk(&rawRequested)}
			out, err := runVerifyChange(context.Background(), VerifyChangeInput{}, deps)
			if err != nil {
				t.Fatalf("runVerifyChange: %v", err)
			}
			if !rawRequested {
				t.Errorf("%s dispatcher must request raw output so the handler returns finding ids", tool)
			}
			if out.Status == "PASS" || strings.HasPrefix(out.Header, "verify_change: PASS") {
				t.Fatalf("false PASS while ffff0001 persists: %q", out.Header)
			}
			if len(out.Persisting) != 1 || out.Persisting[0] != "ffff0001" {
				t.Fatalf("persisting = %v, want [ffff0001]", out.Persisting)
			}
		})
	}
}

// TestVerifyChange_ProducerIsErrorResultPersists: a producer handler that
// fails via fail[...] (IsError CallToolResult, nil Go error) must make its
// targets PERSIST with header FAIL — never resolved, never PASS.
func TestVerifyChange_ProducerIsErrorResultPersists(t *testing.T) {
	stub := func(_ context.Context, _ *mcp.CallToolRequest, in APIAuditInput) (*mcp.CallToolResult, APIAuditOutput, error) {
		return fail[APIAuditOutput]("audit-api module not loaded")
	}
	fx := &verifyFixture{
		inv: &protocol.Investigation{Findings: []protocol.FindingRef{
			ref("aaaa1111", "api_audit", false),
		}},
	}
	deps := fx.deps()
	deps.producers = map[string]verifyProducerFunc{"api_audit": dispatchAPIAudit(stub)}
	out, err := runVerifyChange(context.Background(), VerifyChangeInput{}, deps)
	if err != nil {
		t.Fatalf("runVerifyChange: %v", err)
	}
	if out.Status == "PASS" || strings.HasPrefix(out.Header, "verify_change: PASS") {
		t.Fatalf("false PASS on IsError producer: %q", out.Header)
	}
	if len(out.Persisting) != 1 || out.Persisting[0] != "aaaa1111" {
		t.Fatalf("persisting = %v, want [aaaa1111]", out.Persisting)
	}
	var warned bool
	for _, w := range out.CollectionWarnings {
		if strings.Contains(w, "producer") && strings.Contains(w, "aaaa1111") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("expected a producer-failure warning, got %v", out.CollectionWarnings)
	}
}
