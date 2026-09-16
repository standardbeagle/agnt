package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func loadLayoutCompositeFixture(t *testing.T, name string) *diagnoseLayoutCompositeResult {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var res diagnoseLayoutCompositeResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return &res
}

// TestDiagnoseLayout_MobileOverflowFixtureOneCall: the mobile-overflow fixture
// must yield the overflow finding with its container cause AND a
// screenshot_recommended block naming the selector and the exact
// __devtool.screenshot call — in one call.
func TestDiagnoseLayout_MobileOverflowFixtureOneCall(t *testing.T) {
	res := loadLayoutCompositeFixture(t, "diagnose_layout_mobile_overflow.json")
	findings, shot := buildLayoutFindings(res, "")

	var overflow *string
	for i := range findings {
		if findings[i].Visual && strings.Contains(findings[i].Evidence, "horizontal scroll") {
			id := findings[i].ID
			overflow = &id
			if findings[i].ID == "" {
				t.Error("finding id empty — ids must be stable (finding.StableID)")
			}
			if !strings.Contains(findings[i].Evidence, ".page-shell") {
				t.Errorf("overflow finding missing container cause: %q", findings[i].Evidence)
			}
		}
	}
	if overflow == nil {
		t.Fatalf("no visual overflow finding produced: %+v", findings)
	}
	if shot == nil {
		t.Fatal("screenshot_recommended must be present when a visual finding exists")
	}
	if shot.Selector != ".wide-table" {
		t.Errorf("screenshot selector: got %q, want .wide-table", shot.Selector)
	}
	want := "__devtool.screenshot({selector: '.wide-table'"
	if !strings.Contains(shot.Call, want) {
		t.Errorf("screenshot call: got %q, want it to contain %q", shot.Call, want)
	}

	// IDs stable across identical evidence, and differ per finding type.
	again, _ := buildLayoutFindings(loadLayoutCompositeFixture(t, "diagnose_layout_mobile_overflow.json"), "")
	if again[0].ID != findings[0].ID {
		t.Errorf("id not stable: %q vs %q", findings[0].ID, again[0].ID)
	}
}

// TestDiagnoseLayout_NoVisualFindingNoScreenshotRecommendation: a fixture with
// only ineffective-z-index findings must produce NO screenshot_recommended
// block, in compact and raw output.
func TestDiagnoseLayout_NoVisualFindingNoScreenshotRecommendation(t *testing.T) {
	res := loadLayoutCompositeFixture(t, "diagnose_layout_zindex_only.json")
	findings, shot := buildLayoutFindings(res, "")

	if len(findings) != 1 {
		t.Fatalf("findings: got %d, want 1", len(findings))
	}
	if findings[0].Visual {
		t.Errorf("ineffective-zindex must not be Visual: %+v", findings[0])
	}
	if shot != nil {
		t.Fatalf("screenshot_recommended must be absent with no visual finding, got %+v", shot)
	}
	compact := renderDiagnoseLayoutCompact("dev", findings, shot, "")
	if strings.Contains(compact, "screenshot_recommended") {
		t.Errorf("compact output must omit screenshot_recommended:\n%s", compact)
	}
	if !strings.Contains(compact, "ineffective-zindex") && !strings.Contains(compact, "z-index") {
		t.Errorf("compact output must carry the z-index finding:\n%s", compact)
	}
}

// TestDiagnoseLayout_SelectorNarrowsFindings covers PROJECTION ONLY: the
// real subtree narrowing lives in the JS helper (root.contains in
// diagnostics.js, pinned by TestDiagnoseLayoutCompositeJSHelperEmbedded) and
// needs a DOM, so this Go test asserts only that a composite payload which
// already carries just the in-subtree finding projects exactly that finding —
// nothing outside .sidebar may appear.
func TestDiagnoseLayout_SelectorNarrowsFindings(t *testing.T) {
	res := loadLayoutCompositeFixture(t, "diagnose_layout_selector_narrowed.json")
	findings, shot := buildLayoutFindings(res, "")

	if len(findings) != 1 {
		t.Fatalf("findings: got %d, want 1 (only the in-subtree finding)", len(findings))
	}
	// The finding must be the clipped .sidebar .dropdown, and — because the
	// narrowing is by subtree — nothing outside .sidebar may appear.
	for _, f := range findings {
		if !strings.Contains(f.Evidence, ".sidebar") {
			t.Errorf("finding outside the narrowed subtree leaked through: %q", f.Evidence)
		}
	}
	if shot == nil || shot.Selector != ".sidebar .dropdown" {
		t.Errorf("clipped finding is visual: screenshot_recommended must name .sidebar .dropdown, got %+v", shot)
	}
}

// TestDiagnoseLayout_ClippedVsOverflowDistinctIDs: a clipped-descendant
// layout finding and an overflow responsive issue on the SAME selector must
// receive different finding IDs.
func TestDiagnoseLayout_ClippedVsOverflowDistinctIDs(t *testing.T) {
	const payload = `{
	  "helper": "diagnoseLayoutComposite",
	  "version": 1,
	  "layout": {"findings": [{
	    "check": "clipped-descendant",
	    "severity": "medium",
	    "selector": ".wide-table",
	    "detail": "clipped by ancestor overflow"
	  }], "count": 1, "scanned": 10, "by_check": {}},
	  "responsive": {"issues": [{
	    "selector": ".wide-table",
	    "issues": [{"type": "unintended-horizontal-scroll", "severity": "error", "message": "overflows horizontally"}]
	  }]}
	}`
	var res diagnoseLayoutCompositeResult
	if err := json.Unmarshal([]byte(payload), &res); err != nil {
		t.Fatalf("parse: %v", err)
	}
	findings, _ := buildLayoutFindings(&res, "")
	if len(findings) != 2 {
		t.Fatalf("findings: got %d, want 2", len(findings))
	}
	if findings[0].ID == findings[1].ID {
		t.Errorf("clipped-descendant and overflow findings on the same selector must have distinct ids, both %q", findings[0].ID)
	}
	for _, f := range findings {
		if f.ID == "" {
			t.Errorf("finding id empty: %+v", f)
		}
	}
}

// TestDiagnoseLayout_ViewportRestoredOnError: when viewport is given, the
// proxy resize is issued before diagnosing and the prior state is restored
// after — even when the composite exec fails. Full-bleed prior state must be
// restored via the __devtool_resize_content reset (0,0); a prior explicit
// resize must be re-applied as px. The prior state is read from the SHELL's
// content-frame style — never via a raw innerWidth exec against the inner
// frame.
func TestDiagnoseLayout_ViewportRestoredOnError(t *testing.T) {
	cases := []struct {
		name        string
		resizeState string // shell-side {width,height} style state
		wantRestore string
	}{
		{"fullbleed prior resets", `{"width":"","height":""}`, "(0,0)"},
		{"explicit prior resize re-applied", `{"width":"1280px","height":"800px"}`, "(1280,800)"},
		{"full height prior resize re-applied as 0", `{"width":"1280px","height":"100%"}`, "(1280,0)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &fakeLayoutExec{
				resizeState:    tc.resizeState,
				failOnContains: "diagnoseLayoutComposite",
			}
			input := DiagnoseInput{
				Action:   "layout",
				ProxyID:  "dev",
				Viewport: &DiagnoseViewport{Width: 375, Height: 667},
			}
			res, _, err := runDiagnoseLayout(exec, input)
			if err != nil {
				t.Fatalf("Go error: %v", err)
			}
			if res == nil || !res.IsError {
				t.Fatal("injected exec failure must surface as tool error")
			}
			var resizes []string
			for _, c := range exec.calls {
				if strings.Contains(c, "innerWidth") {
					t.Errorf("raw innerWidth exec must not be issued: %q", c)
				}
				if strings.Contains(c, "__devtool_resize_content(") {
					resizes = append(resizes, c)
				}
			}
			if len(resizes) != 2 {
				t.Fatalf("resize calls: got %d, want 2 (resize + restore): %v", len(resizes), resizes)
			}
			if !strings.Contains(resizes[0], "(375,667)") {
				t.Errorf("first resize must apply the requested viewport: %q", resizes[0])
			}
			if !strings.Contains(resizes[1], tc.wantRestore) {
				t.Errorf("restore: got %q, want it to contain %q", resizes[1], tc.wantRestore)
			}
		})
	}
}

// TestDiagnoseLayoutCompositeJSHelperEmbedded: the composite helper must ship
// in diagnostics.js, exported for the api.js wiring, call the three composed
// producers, and narrow by selector subtree.
func TestDiagnoseLayoutCompositeJSHelperEmbedded(t *testing.T) {
	src, err := os.ReadFile("../proxy/scripts/diagnostics.js")
	if err != nil {
		t.Fatalf("read diagnostics.js: %v", err)
	}
	s := string(src)
	for _, needle := range []string{
		"function diagnoseLayoutComposite(",
		"diagnoseLayoutComposite: diagnoseLayoutComposite",
		"layoutApi.diagnose()",
		"checkResponsiveRisk",
		"getStacking",
		"getContainer",
		"root.contains",
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("diagnostics.js missing %q", needle)
		}
	}
	apiSrc, err := os.ReadFile("../proxy/scripts/api.js")
	if err != nil {
		t.Fatalf("read api.js: %v", err)
	}
	if !strings.Contains(string(apiSrc), "diagnoseLayoutComposite") {
		t.Error("api.js must expose __devtool.diagnoseLayoutComposite")
	}
}

type fakeLayoutExec struct {
	calls          []string
	resizeState    string // shell-side content-frame style state
	composite      string
	failOnContains string
}

func (f *fakeLayoutExec) ProxyExec(id, code string, frameID ...string) (map[string]interface{}, error) {
	f.calls = append(f.calls, code)
	if f.failOnContains != "" && strings.Contains(code, f.failOnContains) {
		return nil, fmt.Errorf("injected failure")
	}
	if strings.Contains(code, "__devtool_content_frame") && !strings.Contains(code, "__devtool_resize_content(") {
		return map[string]interface{}{"result": f.resizeState}, nil
	}
	return map[string]interface{}{"result": f.composite}, nil
}
