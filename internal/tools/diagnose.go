package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/finding"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/go-sdk/mcp"
)

// DiagnoseInput defines input for the diagnose tool. The action enum declares
// both actions now so the schema is stable; layout lands in the next slice.
type DiagnoseInput struct {
	ProxyID  string `json:"proxy_id,omitempty" jsonschema:"Proxy ID to diagnose (preferred)"`
	ID       string `json:"id,omitempty" jsonschema:"Alias for proxy_id"`
	Action   string `json:"action" jsonschema:"Diagnosis to run: click (dead-click triage from recorded interactions) or layout (declared, implemented in a later slice)"`
	Selector string `json:"selector,omitempty" jsonschema:"Diagnose this element instead of the last recorded click (action=click)"`
	Target   string `json:"target,omitempty" jsonschema:"Frame in the always-wrap model: 'inner' (default) = active page content frame; 'outer' = chrome shell"`
	FrameID  string `json:"frame_id,omitempty" jsonschema:"Diagnose a specific content frame by id (default: the active content frame). Rarely needed."`
	Raw      bool   `json:"raw,omitempty" jsonschema:"Return full JSON instead of compact text"`
}

// DiagnoseOutput defines output for the diagnose tool.
type DiagnoseOutput struct {
	Summary string `json:"summary"`
	Verdict string `json:"verdict"`
	Raw     any    `json:"raw,omitempty"`
}

// diagnoseClickResult mirrors the structured object returned by
// __devtool.diagnoseClick (internal/proxy/scripts/diagnostics.js).
type diagnoseClickResult struct {
	Helper          string `json:"helper"`
	Version         int    `json:"version"`
	EvidenceMissing string `json:"evidenceMissing,omitempty"`
	Click           *struct {
		Selector  string `json:"selector"`
		Timestamp *int64 `json:"timestamp"`
		Position  *struct {
			ClientX float64 `json:"client_x"`
			ClientY float64 `json:"client_y"`
		} `json:"position"`
	} `json:"click"`
	Element    map[string]any `json:"element,omitempty"`
	// HitTestPoint records which point the helper hit-tested: "click" (the
	// recorded click position, only when it targets the diagnosed element)
	// or "center" (the element's own center — the named-element path).
	HitTestPoint string `json:"hitTestPoint,omitempty"`
	AtPoint      *struct {
		Selector string `json:"selector"`
	} `json:"atPoint,omitempty"`
	Obstructed bool           `json:"obstructed,omitempty"`
	Stacking   map[string]any `json:"stacking,omitempty"`
	Container  map[string]any `json:"container,omitempty"`
}

const diagnoseToolDescription = `Diagnose a dead click (action=click) in one call.

Composes existing evidence — no new detection: the last recorded click from the
interaction ring buffer, the element and any obstruction at the click point,
the stacking root (__devtool.getStacking), the fixed/containing-block trap
(__devtool.getContainer), and browser_js incidents recorded since the click.

Verdicts:
  handler_missing       click reached its target; nothing intercepted it
  obstructed            another element is hit-testable at the click point
  container_trap        position:fixed/absolute captured by an ancestor
  no_click_recorded     interaction history empty — no cause fabricated
  insufficient_evidence helper absent (old bundle) or evidence incomplete

Examples:
  diagnose {action: "click", proxy_id: "dev"}
  diagnose {action: "click", proxy_id: "dev", selector: ".save-btn"}
  diagnose {action: "click", proxy_id: "dev", raw: true}

action=layout is declared in the schema and lands in a later slice.`

// RegisterDiagnoseTool registers the diagnose MCP tool. Per-session like
// get_incidents: intentionally no `global` flag.
func RegisterDiagnoseTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name:        "diagnose",
		Description: diagnoseToolDescription,
	}, dt.makeDiagnoseHandler())
}

// makeDiagnoseHandler runs the diagnose tool via the daemon.
func (dt *DaemonTools) makeDiagnoseHandler() func(context.Context, *mcp.CallToolRequest, DiagnoseInput) (*mcp.CallToolResult, DiagnoseOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input DiagnoseInput) (*mcp.CallToolResult, DiagnoseOutput, error) {
		input.ProxyID = pickProxyID(input.ID, input.ProxyID)
		switch input.Action {
		case "click":
		case "layout":
			return fail[DiagnoseOutput]("action=layout is declared but not implemented yet — use action=click")
		default:
			return fail[DiagnoseOutput](validationError("diagnose", fmt.Errorf("action required (valid: click, layout)")))
		}
		if input.ProxyID == "" {
			return fail[DiagnoseOutput]("proxy_id required (or `id` alias)")
		}
		if err := dt.ensureConnected(); err != nil {
			return fail[DiagnoseOutput](err.Error())
		}
		return dt.executeDiagnoseClick(input)
	}
}

// executeDiagnoseClick runs __devtool.diagnoseClick in the page, then pulls
// browser_js incidents since the click from the session inbox.
func (dt *DaemonTools) executeDiagnoseClick(input DiagnoseInput) (*mcp.CallToolResult, DiagnoseOutput, error) {
	opts := map[string]any{}
	if input.Selector != "" {
		opts["selector"] = input.Selector
	}
	optsJSON, err := json.Marshal(opts)
	if err != nil {
		return fail[DiagnoseOutput](fmt.Sprintf("failed to marshal options: %v", err))
	}

	code := fmt.Sprintf(`(function() {
		var d = window.__devtool;
		if (!d || typeof d.diagnoseClick !== 'function') {
			return JSON.stringify({ error: '__devtool.diagnoseClick not available on this page (old injected bundle — reload the page through the proxy)' });
		}
		var r = d.diagnoseClick(%s);
		return typeof r === 'string' ? r : JSON.stringify(r);
	})()`, string(optsJSON))

	execTarget, err := resolveExecTarget(input.Target, input.FrameID)
	if err != nil {
		return fail[DiagnoseOutput](err.Error())
	}
	result, err := dt.client.ProxyExec(input.ProxyID, code, execTarget)
	if err != nil {
		return fail[DiagnoseOutput](fmt.Sprintf("diagnose exec failed: %v", err))
	}
	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		return fail[DiagnoseOutput]("diagnose exec failed: " + errMsg)
	}

	resultStr := getString(result, "result")
	if resultStr == "" {
		b, _ := json.Marshal(result)
		resultStr = string(b)
	}

	// Helper-absent (old bundle) is evidence, not an exec error: the JS layer
	// returns {error: ...} and the verdict must be insufficient_evidence with
	// the exact reason — never a fabricated diagnosis.
	var parsed diagnoseClickResult
	helperErr := ""
	var rawErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(resultStr), &parsed); err != nil {
		helperErr = "diagnoseClick returned unparseable result: " + err.Error()
	} else if err := json.Unmarshal([]byte(resultStr), &rawErr); err == nil && rawErr.Error != "" {
		helperErr = rawErr.Error
	}

	// Incidents since the click, from the session inbox (get_incidents pull
	// surface). Best-effort is not allowed to mask a failure: a query error
	// renders as a warning line in the summary, never silently empty.
	var incidents []incidentView
	incidentWarning := ""
	if parsed.Click != nil && parsed.Click.Timestamp != nil && *parsed.Click.Timestamp > 0 && helperErr == "" {
		since := time.UnixMilli(*parsed.Click.Timestamp).UTC().Format(time.RFC3339)
		res, qerr := dt.client.IncidentQuery(protocol.IncidentQueryFilter{
			Since:   since,
			Sources: []string{"browser_js"},
			ProxyID: input.ProxyID,
			Limit:   20,
			Profile: string(finding.ProfileFull),
		})
		if qerr != nil {
			incidentWarning = "incident query failed: " + qerr.Error()
		} else if res != nil {
			for _, rec := range res.Incidents {
				incidents = append(incidents, recordToView(rec))
			}
		}
	}

	verdict, findings := buildClickFindings(&parsed, helperErr, incidents)

	if input.Raw {
		raw := map[string]any{
			"verdict":   verdict,
			"findings":  findings,
			"evidence":  json.RawMessage(resultStr),
			"incidents": incidents,
		}
		b, _ := json.Marshal(raw)
		return mcpText(string(b)), DiagnoseOutput{Summary: string(b), Verdict: verdict, Raw: raw}, nil
	}

	summary := renderDiagnoseClickCompact(input.ProxyID, verdict, findings, incidents, incidentWarning)
	return mcpText(summary), DiagnoseOutput{Summary: summary, Verdict: verdict}, nil
}

// buildClickFindings projects diagnoseClick evidence into a verdict and the
// S0 finding list. Pure: fixtures drive it without a live browser or daemon.
func buildClickFindings(res *diagnoseClickResult, helperErr string, incidents []incidentView) (string, []finding.Finding) {
	producer := finding.Producer{Tool: "diagnose", Args: map[string]any{"action": "click"}}

	// Any evidence gap — helper absent, interactions module missing, an
	// unresolved selector surfacing as an element/stacking/container error,
	// or no hit-test performed — is insufficient_evidence with the exact
	// reason. Never fall through to handler_missing or no_click_recorded.
	insufficient := func(reason string) (string, []finding.Finding) {
		return "insufficient_evidence", []finding.Finding{{
			ID:       finding.StableID("click", "", reason),
			Category: "click",
			Severity: "warning",
			Evidence: reason,
			Next: finding.NextAction{
				Tool:      "proxy",
				Args:      map[string]any{"action": "status"},
				Rationale: reason,
			},
			Producer: producer,
		}}
	}

	if helperErr != "" {
		return insufficient(helperErr)
	}
	if res == nil {
		return insufficient("diagnoseClick returned no result")
	}
	if res.EvidenceMissing != "" {
		return insufficient(res.EvidenceMissing)
	}
	if res.Click == nil {
		// Never fabricate a cause when no click was recorded.
		return "no_click_recorded", []finding.Finding{{
			ID:       finding.StableID("click", "", "no click recorded"),
			Category: "click",
			Severity: "warning",
			Evidence: "interaction ring buffer holds no click event",
			Next: finding.NextAction{
				Tool:      "watch",
				Args:      map[string]any{"events": "interactions"},
				Rationale: "record the failing click, then re-run diagnose",
			},
			Producer: producer,
		}}
	}

	selector := res.Click.Selector
	if selector == "" {
		reason := "click recorded without a resolvable selector"
		return "insufficient_evidence", []finding.Finding{{
			ID:       finding.StableID("click", "", reason),
			Category: "click",
			Severity: "warning",
			Evidence: reason,
			Next: finding.NextAction{
				Tool:      "proxy",
				Args:      map[string]any{"action": "exec", "code": "__devtool.interactions.getLastClick()"},
				Rationale: "inspect the raw click record to find a selector",
			},
			Producer: producer,
		}}
	}

	incidentNote := ""
	if len(incidents) > 0 {
		incidentNote = fmt.Sprintf("; %d browser_js incident(s) since the click (first: %s)", len(incidents), incidents[0].Summary)
	}

	// Helper-level errors on the composed evidence (e.g. an unresolved
	// selector reported by getElementInfo/getStacking/getContainer).
	for _, ev := range []struct {
		name string
		m    map[string]any
	}{
		{"__devtool.getElementInfo", res.Element},
		{"__devtool.getStacking", res.Stacking},
		{"__devtool.getContainer", res.Container},
	} {
		if e, ok := ev.m["error"].(string); ok && e != "" {
			return insufficient(fmt.Sprintf("%s error for %q: %s", ev.name, selector, e))
		}
	}

	// No hit-test performed: without atPoint the helper never checked what
	// is actually clickable at the point — every downstream verdict would
	// be fabricated.
	if res.AtPoint == nil {
		return insufficient(fmt.Sprintf("no hit-test performed for %q (atPoint absent from diagnoseClick result)", selector))
	}

	if res.Obstructed && res.AtPoint.Selector != "" {
		obstructor := res.AtPoint.Selector
		cause := fmt.Sprintf("click on %q blocked by %q", selector, obstructor)
		return "obstructed", []finding.Finding{{
			ID:       finding.StableID("click", selector, cause),
			Category: "click",
			Severity: "error",
			Evidence: fmt.Sprintf("elementFromPoint at the click point resolves to %q, not the target%s", obstructor, incidentNote),
			Next: finding.NextAction{
				Tool:      "proxy",
				Args:      map[string]any{"action": "exec", "code": fmt.Sprintf("__devtool.getStacking('%s')", obstructor)},
				Rationale: "find the obstructor's stacking root and rootTrigger",
			},
			Producer: producer,
		}}
	}

	if trappedBy, ok := res.Container["trappedBy"]; ok && trappedBy != nil {
		trap, _ := trappedBy.(map[string]any)
		trapSel, _ := trap["selector"].(string)
		trapProp, _ := trap["property"].(string)
		cause := fmt.Sprintf("%q trapped by ancestor %q (%s)", selector, trapSel, trapProp)
		return "container_trap", []finding.Finding{{
			ID:       finding.StableID("click", selector, cause),
			Category: "click",
			Severity: "error",
			Evidence: fmt.Sprintf("position:%v element's containing block is %q via %s%s", res.Container["position"], trapSel, trapProp, incidentNote),
			Next: finding.NextAction{
				Tool:      "proxy",
				Args:      map[string]any{"action": "exec", "code": fmt.Sprintf("__devtool.getContainer('%s')", selector)},
				Rationale: "re-inspect the containing-block trap on the target",
			},
			Producer: producer,
		}}
	}

	cause := fmt.Sprintf("click reached %q but produced no effect", selector)
	return "handler_missing", []finding.Finding{{
		ID:       finding.StableID("click", selector, cause),
		Category: "click",
		Severity: "error",
		Evidence: fmt.Sprintf("no obstruction at the click point and no containing-block trap%s", incidentNote),
		Next: finding.NextAction{
			Tool:      "get_incidents",
			Args:      map[string]any{"sources": []string{"browser_js"}},
			Rationale: "check for a JS error in the click handler around the click time",
		},
		Producer: producer,
	}}
}

// renderDiagnoseClickCompact renders the verdict + findings in the compact
// text format (id: / next: lines) shared with the other audit tools.
func renderDiagnoseClickCompact(proxyID, verdict string, findings []finding.Finding, incidents []incidentView, incidentWarning string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== diagnose click (%s) ===\n", proxyID))
	sb.WriteString("verdict: " + verdict + "\n\n")
	for _, f := range findings {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", f.Severity, f.Evidence))
		sb.WriteString("  id: " + f.ID + "\n")
		sb.WriteString("  next: " + renderDiagnoseNext(f.Next) + "\n")
	}
	if len(incidents) > 0 {
		sb.WriteString("\nincidents since click (browser_js):\n")
		for _, iv := range incidents {
			sb.WriteString(fmt.Sprintf("  - [%s] %s %s\n", iv.Severity, iv.Category, iv.Summary))
		}
	}
	if incidentWarning != "" {
		sb.WriteString("\n!! " + incidentWarning + "\n")
	}
	return sb.String()
}

// renderDiagnoseNext renders one exact, copy-pastable next step per finding.
func renderDiagnoseNext(n finding.NextAction) string {
	switch {
	case n.Tool == "proxy":
		if code, ok := n.Args["code"].(string); ok {
			return "proxy exec " + code
		}
		return "proxy " + formatArgs(n.Args)
	case n.Tool == "watch":
		if ev, ok := n.Args["events"].(string); ok {
			return fmt.Sprintf("watch {events:%q}", ev)
		}
		return "watch" + formatArgs(n.Args)
	default:
		return n.Tool + formatArgs(n.Args)
	}
}
