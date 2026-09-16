package scripts

import (
	"regexp"
	"strings"
	"testing"
)

// TestDiagnoseClickJSHelperEmbedded is the source guard for
// __devtool.diagnoseClick (the browser half of the `diagnose` MCP tool,
// action=click). The scripts package has no embedded JS engine, so this pins
// the contract statically:
//
//   1. diagnostics.js defines and exports diagnoseClick with a @devtool
//      JSDoc block (so `make generate` surfaces it in apidocs_gen.go).
//   2. api.js wires it onto window.__devtool.
//   3. The helper composes EXISTING evidence only — every helper call inside
//      its body must be one of the already-shipped primitives
//      (__devtool_interactions.getLastClick, __devtool.getElementInfo /
//      getStacking / getContainer, document.elementFromPoint, __devtool_utils
//      helpers). No new detection, no querySelectorAll walks.
func TestDiagnoseClickJSHelperEmbedded(t *testing.T) {
	if !strings.Contains(diagnosticsJS, "function diagnoseClick(") {
		t.Fatalf("diagnostics.js missing diagnoseClick definition")
	}
	if !strings.Contains(diagnosticsJS, "diagnoseClick: diagnoseClick") {
		t.Errorf("diagnostics.js does not export diagnoseClick on __devtool_diagnostics")
	}
	if !strings.Contains(diagnosticsJS, "@devtool diagnoseClick") {
		t.Errorf("diagnostics.js diagnoseClick missing @devtool JSDoc tag (apidocs drift)")
	}
	if !strings.Contains(apiJS, "diagnoseClick") {
		t.Errorf("api.js does not wire diagnoseClick onto window.__devtool")
	}

	// Extract the function body and audit its helper calls.
	start := strings.Index(diagnosticsJS, "function diagnoseClick(")
	if start < 0 {
		return
	}
	body := diagnosticsJS[start:]
	// Body ends at the next top-level function or the export block.
	if idx := strings.Index(body[1:], "\n  function "); idx >= 0 {
		body = body[:idx+1]
	}

	allowed := map[string]bool{
		"__devtool_interactions.getLastClick": true,
		"__devtool.getElementInfo":            true,
		"__devtool.getStacking":               true,
		"__devtool.getContainer":              true,
		"document.elementFromPoint":           true,
		"utils.generateSelector":              true,
		"utils.resolveElement":                true,
		"utils.getRect":                       true,
	}
	callRe := regexp.MustCompile(`(window\.)?(__devtool_interactions|__devtool|document|utils)\.([a-zA-Z]+)\(`)
	for _, m := range callRe.FindAllStringSubmatch(body, -1) {
		name := m[2] + "." + m[3]
		if !allowed[name] {
			t.Errorf("diagnoseClick calls non-allowlisted helper %q — it must compose existing evidence only", name)
		}
	}
	if strings.Contains(body, "querySelectorAll") {
		t.Errorf("diagnoseClick must not walk the DOM with querySelectorAll")
	}

	// Full-ring guard: the last click must come from
	// __devtool_interactions.getLastClick (which scans the whole 500-slot
	// ring), never a fixed-window getHistory(N) scan — a click followed by
	// more than N recorded interactions must still diagnose.
	if !strings.Contains(body, "getLastClick") {
		t.Errorf("diagnoseClick must resolve the last click via __devtool_interactions.getLastClick (full ring buffer)")
	}
	if strings.Contains(body, "getHistory(") {
		t.Errorf("diagnoseClick must not scan a fixed getHistory(N) window — use getLastClick so clicks older than N interactions still diagnose")
	}

	// Stale-click guard: with opts.selector, the recorded click's position
	// may drive the hit-test ONLY when the click targeted that same element;
	// otherwise the helper hit-tests the named element's own center and says
	// so via result.hitTestPoint.
	if !strings.Contains(body, "clickSel === selector") {
		t.Errorf("diagnoseClick must gate click.position behind clickSel === selector (stale click points must not drive the named-element hit-test)")
	}
	if !strings.Contains(body, "hitTestPoint") {
		t.Errorf("diagnoseClick must report result.hitTestPoint (\"click\"|\"center\") so the Go side can prove which point was tested")
	}
}
