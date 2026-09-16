package tools

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/go-sdk/mcp"
)

func loadDiagnoseFixture(t *testing.T, name string) *diagnoseClickResult {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var res diagnoseClickResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return &res
}

// TestDiagnoseClick_ObstructedFixtureOneCall: the recorded obstructed-click
// fixture must yield verdict=obstructed, the obstructing selector, a stable
// id, and the getStacking next step — in one call, no follow-up probes.
func TestDiagnoseClick_ObstructedFixtureOneCall(t *testing.T) {
	res := loadDiagnoseFixture(t, "diagnose_click_obstructed.json")
	verdict, findings := buildClickFindings(res, "", nil)

	if verdict != "obstructed" {
		t.Fatalf("verdict: got %q, want obstructed", verdict)
	}
	if len(findings) != 1 {
		t.Fatalf("findings: got %d, want 1", len(findings))
	}
	f := findings[0]
	if f.ID == "" {
		t.Error("finding id empty — ids must be stable (finding.StableID)")
	}
	// Stable across identical evidence.
	_, again := buildClickFindings(loadDiagnoseFixture(t, "diagnose_click_obstructed.json"), "", nil)
	if again[0].ID != f.ID {
		t.Errorf("id not stable: %q vs %q", f.ID, again[0].ID)
	}
	code, _ := f.Next.Args["code"].(string)
	if !strings.Contains(code, "__devtool.getStacking('.modal-overlay')") {
		t.Errorf("next code: got %q, want __devtool.getStacking('.modal-overlay')", code)
	}
	compact := renderDiagnoseClickCompact("dev", verdict, findings, nil, "")
	if !strings.Contains(compact, "next: proxy exec __devtool.getStacking('.modal-overlay')") {
		t.Errorf("compact next line wrong:\n%s", compact)
	}
}

// TestDiagnoseClick_ContainerTrapFixture: a fixed element captured by an
// ancestor transform must yield verdict=container_trap with getContainer
// evidence attached.
func TestDiagnoseClick_ContainerTrapFixture(t *testing.T) {
	res := loadDiagnoseFixture(t, "diagnose_click_container_trap.json")
	verdict, findings := buildClickFindings(res, "", nil)

	if verdict != "container_trap" {
		t.Fatalf("verdict: got %q, want container_trap", verdict)
	}
	f := findings[0]
	code, _ := f.Next.Args["code"].(string)
	if !strings.Contains(code, "__devtool.getContainer('.floating-cta')") {
		t.Errorf("next code: got %q, want __devtool.getContainer('.floating-cta')", code)
	}
	if !strings.Contains(f.Evidence, ".transformed-parent") {
		t.Errorf("evidence missing trap ancestor: %q", f.Evidence)
	}
}

// TestDiagnoseClick_NoClickRecordedNeverFabricates: empty interaction history
// → verdict no_click_recorded, next=watch interactions, no cause populated.
func TestDiagnoseClick_NoClickRecordedNeverFabricates(t *testing.T) {
	res := loadDiagnoseFixture(t, "diagnose_click_no_history.json")
	verdict, findings := buildClickFindings(res, "", nil)

	if verdict != "no_click_recorded" {
		t.Fatalf("verdict: got %q, want no_click_recorded", verdict)
	}
	f := findings[0]
	if f.Next.Tool != "watch" || f.Next.Args["events"] != "interactions" {
		t.Errorf("next: got %+v, want watch {events:interactions}", f.Next)
	}
	compact := renderDiagnoseClickCompact("dev", verdict, findings, nil, "")
	if !strings.Contains(compact, `next: watch {events:"interactions"}`) {
		t.Errorf("compact next line wrong:\n%s", compact)
	}
	// No cause fabricated: evidence describes the missing record, not a cause.
	for _, bad := range []string{"obstructed", "container_trap", "handler_missing", "trapped"} {
		if strings.Contains(f.Evidence, bad) {
			t.Errorf("fabricated cause %q in evidence: %q", bad, f.Evidence)
		}
	}
}

// TestDiagnoseClick_InsufficientEvidenceWhenHelperMissing: an old bundle
// without __devtool.diagnoseClick must yield insufficient_evidence carrying
// the exact reason, never a fabricated diagnosis.
func TestDiagnoseClick_InsufficientEvidenceWhenHelperMissing(t *testing.T) {
	reason := "__devtool.diagnoseClick not available on this page (old injected bundle — reload the page through the proxy)"
	verdict, findings := buildClickFindings(&diagnoseClickResult{}, reason, nil)

	if verdict != "insufficient_evidence" {
		t.Fatalf("verdict: got %q, want insufficient_evidence", verdict)
	}
	if findings[0].Evidence != reason {
		t.Errorf("evidence: got %q, want exact reason %q", findings[0].Evidence, reason)
	}
}

// TestDiagnoseClick_SelectorIgnoresStaleClickPoint: diagnose
// {action:"click", selector} with a recorded last click on a DIFFERENT
// element must not report obstructed against the stale click point. The
// helper hit-tests the named element's own center (hitTestPoint:"center")
// and the verdict reflects the named element, not the stale click.
func TestDiagnoseClick_SelectorIgnoresStaleClickPoint(t *testing.T) {
	res := loadDiagnoseFixture(t, "diagnose_click_selector_center.json")
	if res.HitTestPoint != "center" {
		t.Fatalf("hitTestPoint: got %q, want center (named element hit-tests its own center, not the stale click point)", res.HitTestPoint)
	}
	verdict, findings := buildClickFindings(res, "", nil)
	if verdict == "obstructed" {
		t.Fatalf("verdict obstructed against a stale click point — named-element path must hit-test the element's own center")
	}
	if verdict != "handler_missing" {
		t.Fatalf("verdict: got %q, want handler_missing (hit at the element center is the element itself)", verdict)
	}
	for _, f := range findings {
		if strings.Contains(f.Evidence, ".cancel-btn") {
			t.Errorf("evidence references the stale click target: %q", f.Evidence)
		}
	}
}

// TestDiagnoseClick_InsufficientEvidenceOnHelperGaps: whenever the helper
// reports a gap — evidenceMissing, an element/stacking/container error
// (unresolved selector), or no hit-test performed (atPoint absent) — the
// verdict must be insufficient_evidence with the exact reason, never
// handler_missing or no_click_recorded.
func TestDiagnoseClick_InsufficientEvidenceOnHelperGaps(t *testing.T) {
	cases := []struct {
		name       string
		fixture    string
		mutate     func(*diagnoseClickResult)
		wantReason string
	}{
		{
			name:       "evidenceMissing",
			fixture:    "diagnose_click_evidence_missing.json",
			wantReason: "interactions module not loaded",
		},
		{
			name:       "element error (unresolved selector)",
			fixture:    "diagnose_click_unresolved_selector.json",
			wantReason: "element not found: .ghost-btn",
		},
		{
			name:    "stacking error",
			fixture: "diagnose_click_obstructed.json",
			mutate: func(r *diagnoseClickResult) {
				r.Stacking = map[string]any{"error": "element not found: #save-btn"}
			},
			wantReason: "element not found: #save-btn",
		},
		{
			name:    "container error",
			fixture: "diagnose_click_obstructed.json",
			mutate: func(r *diagnoseClickResult) {
				r.Container = map[string]any{"error": "element not found: #save-btn"}
			},
			wantReason: "element not found: #save-btn",
		},
		{
			name:       "no hit-test performed (atPoint absent)",
			fixture:    "diagnose_click_no_hittest.json",
			wantReason: "no hit-test performed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := loadDiagnoseFixture(t, tc.fixture)
			if tc.mutate != nil {
				tc.mutate(res)
			}
			verdict, findings := buildClickFindings(res, "", nil)
			if verdict != "insufficient_evidence" {
				t.Fatalf("verdict: got %q, want insufficient_evidence (gap: %s)", verdict, tc.wantReason)
			}
			if !strings.Contains(findings[0].Evidence, tc.wantReason) {
				t.Errorf("evidence: got %q, want it to carry the exact reason %q", findings[0].Evidence, tc.wantReason)
			}
		})
	}
}

// TestDiagnoseInputSchemaDeclaresBothActions pins that the Input schema
// declares action enum [click, layout] now, so the layout slice lands without
// a schema change.
func TestDiagnoseInputSchemaDeclaresBothActions(t *testing.T) {
	dt := NewDaemonTools(daemonclient.AutoStartConfig{}, "test")
	server := mcp.NewServer(&mcp.Implementation{Name: "agnt", Version: "test"}, nil)
	RegisterDiagnoseTool(server, dt)
	// Registration without panic exercises the schema; the action field's
	// declared values are pinned by the description contract and validation:
	h := dt.makeDiagnoseHandler()
	for _, action := range []string{"click", "layout"} {
		_, out, err := h(t.Context(), nil, DiagnoseInput{Action: action, ProxyID: ""})
		if err != nil {
			t.Fatalf("action %q returned Go error: %v", action, err)
		}
		_ = out
	}
	// Unknown action must be rejected.
	res, _, err := h(t.Context(), nil, DiagnoseInput{Action: "scroll", ProxyID: "dev"})
	if err != nil {
		t.Fatalf("unknown action returned Go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatal("unknown action must fail validation")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "click") || !strings.Contains(text, "layout") {
		t.Errorf("validation error must name both declared actions, got: %s", text)
	}
}
