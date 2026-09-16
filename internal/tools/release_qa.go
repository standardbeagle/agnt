package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/standardbeagle/agnt/internal/finding"
	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/standardbeagle/go-sdk/mcp"
)

// ReleaseQAFlowStep is one explicit step of the changed flow, executed via the
// existing proxy navigate/exec actions only — release_qa adds no new browser
// automation.
type ReleaseQAFlowStep struct {
	Action   string `json:"action"             jsonschema:"Flow step action: navigate (needs url) or exec (needs selector — clicks the element)"`
	Selector string `json:"selector,omitempty" jsonschema:"Element selector for action=exec"`
	URL      string `json:"url,omitempty"      jsonschema:"Target URL for action=navigate"`
}

// ReleaseQAInput is the input schema for the release_qa tool.
type ReleaseQAInput struct {
	ProxyID string              `json:"proxy_id"          jsonschema:"Proxy ID to run the release gate against"`
	Flow    []ReleaseQAFlowStep `json:"flow,omitempty"    jsonschema:"Explicit changed-flow steps, run via proxy navigate/exec before the audit"`
	Audit   string              `json:"audit,omitempty"   jsonschema:"The ONE broad audit: quality (default, __devtool.auditAll), responsive, api, or loading"`
	Profile string              `json:"profile,omitempty" jsonschema:"Finding projection (default: release)"`
	Raw     bool                `json:"raw,omitempty"     jsonschema:"Return full JSON instead of compact text"`
}

// releaseQAFinding is one finding from the broad audit, carrying whatever the
// drill-down dispatch needs (selector for layout/click, url for api).
type releaseQAFinding struct {
	ID       string `json:"id"`
	Type     string `json:"type,omitempty"`
	Severity string `json:"severity,omitempty"`
	Selector string `json:"selector,omitempty"`
	URL      string `json:"url,omitempty"`
}

// releaseQADrillLine is one drill-down outcome row.
type releaseQADrillLine struct {
	Area   string `json:"area"`
	Target string `json:"target,omitempty"`
	Status string `json:"status"` // ok | failed
	Detail string `json:"detail,omitempty"`
}

// ReleaseQAOutput is the structured output of release_qa.
type ReleaseQAOutput struct {
	Status    string               `json:"status"` // PASS | FAIL
	Header    string               `json:"header"`
	Verify    string               `json:"verify"`
	AuditTool string               `json:"audit_tool"`
	Profile   string               `json:"profile"`
	Findings  []releaseQAFinding   `json:"findings"`
	DrillDown []releaseQADrillLine `json:"drill_down"`
	Warnings  []string             `json:"warnings,omitempty"`
}

// releaseQADeps bundles every side effect runReleaseQA needs so tests drive
// the core with fixtures and the MCP handler wires the real daemon client.
type releaseQADeps struct {
	investigation func(ctx context.Context) (*protocol.Investigation, error)
	merge         func(ctx context.Context, patch protocol.InvestigationPatch) error
	verify        func(ctx context.Context, input VerifyChangeInput) (VerifyChangeOutput, error)
	flowStep      func(ctx context.Context, step ReleaseQAFlowStep) error
	audit         func(ctx context.Context, tool, profile string) ([]releaseQAFinding, error)
	drill         func(ctx context.Context, area string, finding releaseQAFinding) (string, error)
}

// releaseQAAuditTools are the valid values for ReleaseQAInput.Audit.
var releaseQAAuditTools = map[string]bool{
	"quality":    true,
	"responsive": true,
	"api":        true,
	"loading":    true,
}

// releaseDrillAreas maps a broad-audit finding's type (its audit source) to
// the drill-down area that owns it. Only these types drill; quality-content
// findings (missing-alt, duplicate-id, ...) and loading findings have no
// drill-down. Classification never falls back to substrings — a finding not
// in this table passes the drill stage untouched.
var releaseDrillAreas = map[string]string{
	// click-interception (layout.js) -> diagnose action=click
	"click-interception": "click",
	// api findings (audit-api.js) -> api_audit re-run
	"waterfall":      "api",
	"n-plus-one":     "api",
	"duplicate-call": "api",
	"chatty-load":    "api",
	// responsive findings (responsive.js) -> diagnose action=layout
	"layout":                       "responsive",
	"overflow":                     "responsive",
	"exceeds-viewport":             "responsive",
	"extreme-font-size":            "responsive",
	"fixed-width":                  "responsive",
	"large-fixed-element":          "responsive",
	"min-width-too-large":          "responsive",
	"positioned-offscreen-right":   "responsive",
	"small-font":                   "responsive",
	"small-touch-target":           "responsive",
	"table-not-scrollable":         "responsive",
	"unintended-horizontal-scroll": "responsive",
	"wide-table":                   "responsive",
}

// releaseDrillArea classifies one broad-audit finding into its drill-down
// area from the finding's audit source (type). "" means no drill-down.
func releaseDrillArea(f releaseQAFinding) string {
	return releaseDrillAreas[strings.ToLower(f.Type)]
}

// releaseQAFlowCode compiles one explicit flow step to the exact JavaScript
// the existing proxy navigate/exec actions run. Pure: the executor only sends
// the result through client.ProxyExec. Anything else fails fast.
func releaseQAFlowCode(step ReleaseQAFlowStep) (string, error) {
	switch step.Action {
	case "navigate":
		if step.URL == "" {
			return "", fmt.Errorf("flow navigate step requires url")
		}
		return buildNavigateJS("goto", step.URL)
	case "exec":
		if step.Selector == "" {
			return "", fmt.Errorf("flow exec step requires selector")
		}
		selJSON, err := json.Marshal(step.Selector)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(`(function(){
		var d = window.__devtool;
		if (!d || typeof d.clickElement !== 'function') { return JSON.stringify({ error: '__devtool.clickElement not available on this page (old injected bundle — reload the page through the proxy)' }); }
		return JSON.stringify(d.clickElement(%s));
	})()`, string(selJSON)), nil
	default:
		return "", fmt.Errorf("unknown flow action %q (valid: navigate, exec)", step.Action)
	}
}

// parseReleaseFindings walks an audit's raw JSON payload and collects every
// object that looks like a finding (id plus severity/type), capturing the
// fields drill-down dispatches on.
func parseReleaseFindings(raw any) []releaseQAFinding {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []releaseQAFinding
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			id, _ := t["id"].(string)
			_, hasSev := t["severity"]
			_, hasType := t["type"]
			if id != "" && (hasSev || hasType) && !seen[id] {
				seen[id] = true
				f := releaseQAFinding{ID: id}
				f.Type, _ = t["type"].(string)
				f.Severity, _ = t["severity"].(string)
				f.Selector, _ = t["selector"].(string)
				if f.Selector == "" {
					f.Selector, _ = t["element"].(string)
				}
				f.URL, _ = t["url"].(string)
				out = append(out, f)
			}
			for _, v2 := range t {
				walk(v2)
			}
		case []any:
			for _, v2 := range t {
				walk(v2)
			}
		}
	}
	walk(decoded)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// runReleaseQA is the release-gate orchestration core. Order is the contract:
// optional explicit flow steps via proxy navigate/exec -> verify the session's
// recorded findings (changed flow) -> exactly ONE broad audit -> drill-down
// ONLY into areas the broad audit failed. Header is PASS only when verify
// passed, the broad audit found nothing, and nothing warned.
func runReleaseQA(ctx context.Context, input ReleaseQAInput, deps releaseQADeps) (ReleaseQAOutput, error) {
	auditTool := input.Audit
	if auditTool == "" {
		auditTool = "quality"
	}
	if !releaseQAAuditTools[auditTool] {
		return ReleaseQAOutput{}, fmt.Errorf("invalid audit %q (valid: quality, responsive, api, loading)", auditTool)
	}
	profile := input.Profile
	if profile == "" {
		profile = AuditProfileRelease
	}

	out := ReleaseQAOutput{
		AuditTool: auditTool,
		Profile:   profile,
		Findings:  []releaseQAFinding{},
		DrillDown: []releaseQADrillLine{},
	}

	// Explicit flow steps run FIRST (documented order): they replay the
	// changed flow so verify and the broad audit see the post-change page.
	for _, step := range input.Flow {
		if err := deps.flowStep(ctx, step); err != nil {
			out.Warnings = append(out.Warnings, fmt.Sprintf("flow step %s failed: %v", step.Action, err))
		}
	}

	verifyPassed := false
	vout, verr := deps.verify(ctx, VerifyChangeInput{ProxyID: input.ProxyID})
	if verr != nil {
		out.Verify = "FAIL (error: " + verr.Error() + ")"
		out.Warnings = append(out.Warnings, "verify_change failed: "+verr.Error())
	} else {
		out.Verify = vout.Header
		verifyPassed = vout.Status == "PASS"
	}

	// The ONE broad audit of the call. Drill-down never re-runs it.
	findings, aerr := deps.audit(ctx, auditTool, profile)
	if aerr != nil {
		out.Warnings = append(out.Warnings, fmt.Sprintf("broad audit %s failed: %v", auditTool, aerr))
	} else {
		out.Findings = findings
	}

	// Drill into failed areas only; an area with no findings is never touched.
	failedAreas := map[string]bool{}
	var failedAreaList []string
	for _, f := range out.Findings {
		area := releaseDrillArea(f)
		if area == "" {
			continue
		}
		if !failedAreas[area] {
			failedAreas[area] = true
			failedAreaList = append(failedAreaList, area)
		}
		target := f.Selector
		if target == "" {
			target = f.URL
		}
		line := releaseQADrillLine{Area: area, Target: target, Status: "ok"}
		summary, err := deps.drill(ctx, area, f)
		if err != nil {
			line.Status = "failed"
			line.Detail = err.Error()
			out.Warnings = append(out.Warnings, fmt.Sprintf("drill-down %s on %q failed: %v", area, target, err))
		} else {
			// The line carries the handler's summary, not just ok/failed.
			line.Detail = summary
		}
		out.DrillDown = append(out.DrillDown, line)
	}
	sort.Strings(failedAreaList)

	// Merge FailedAreas and the new finding ids into the session Investigation.
	// Producer.Args are per-finding (finding_id/type/area/selector/url) so the
	// release_qa verify producer can recheck through the TARGETED drill-down —
	// never the broad audit (verifyProducerFunc contract).
	if deps.merge != nil && (len(failedAreaList) > 0 || len(out.Findings) > 0) {
		patch := protocol.InvestigationPatch{FailedAreas: failedAreaList}
		for _, f := range out.Findings {
			patch.Findings = append(patch.Findings, protocol.FindingRef{
				Fingerprint: f.ID,
				Severity:    f.Severity,
				Source:      auditTool,
				Producer: &finding.Producer{Tool: "release_qa", Args: map[string]any{
					"proxy_id":   input.ProxyID,
					"audit":      auditTool,
					"finding_id": f.ID,
					"type":       f.Type,
					"area":       releaseDrillArea(f),
					"selector":   f.Selector,
					"url":        f.URL,
				}},
			})
		}
		if err := deps.merge(ctx, patch); err != nil {
			out.Warnings = append(out.Warnings, "investigation merge failed: "+err.Error())
		}
	}

	if verifyPassed && aerr == nil && len(out.Findings) == 0 && len(out.Warnings) == 0 {
		out.Status = "PASS"
	} else {
		out.Status = "FAIL"
	}
	out.Header = "release_qa: " + out.Status
	return out, nil
}

// formatReleaseQACompact renders the compact wire form: header, then the
// verify / audit / drill-down / warnings sections — always all four.
func formatReleaseQACompact(out ReleaseQAOutput) string {
	var sb strings.Builder
	sb.WriteString(out.Header + "\n\n")
	sb.WriteString("verify: " + out.Verify + "\n")
	sb.WriteString(fmt.Sprintf("audit: %s profile=%s — %d finding(s)\n", out.AuditTool, out.Profile, len(out.Findings)))
	for _, f := range out.Findings {
		sb.WriteString(fmt.Sprintf("  [%s] %s", f.Severity, f.Type))
		sb.WriteString(" id:" + f.ID)
		if f.Selector != "" {
			sb.WriteString(" selector:" + f.Selector)
		}
		if f.URL != "" {
			sb.WriteString(" url:" + f.URL)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("drill-down:\n")
	for _, d := range out.DrillDown {
		sb.WriteString(fmt.Sprintf("  %s %s %s", d.Area, d.Target, d.Status))
		if d.Detail != "" {
			sb.WriteString(" (" + d.Detail + ")")
		}
		sb.WriteString("\n")
	}
	if len(out.DrillDown) == 0 {
		sb.WriteString("  (none — no failed areas)\n")
	}
	sb.WriteString("warnings:\n")
	for _, w := range out.Warnings {
		sb.WriteString("!! " + w + "\n")
	}
	if len(out.Warnings) == 0 {
		sb.WriteString("  (none)\n")
	}
	return sb.String()
}

// RegisterReleaseQATool registers the release_qa MCP tool. Per-session like
// verify_change: intentionally no `global` flag.
func RegisterReleaseQATool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name: "release_qa",
		Description: `Release gate: verify the changed flow, run exactly ONE broad audit, drill into failures only.

This is the ONE tool where a broad audit is the default — every other tool
stays targeted. Steps, in order:

1. verify: re-runs verify_change in-process over the session Investigation
   (the "changed flow" = the session's FailedAreas + Findings). Optional
   explicit flow steps run first through the existing proxy navigate/exec
   actions only — no new browser automation.
2. audit: exactly ONE broad audit per call — the full aggregate quality audit
   via __devtool.auditAll through proxy exec (default), or the named
   audit tool (responsive/api/loading) — with profile=release.
3. drill-down: ONLY for areas the broad audit failed. responsive findings
   -> diagnose action=layout on the failing selector; click-interception ->
   diagnose action=click with selector; api findings -> api_audit re-run.
   An area that passed is never drilled.

Header: release_qa: PASS|FAIL. PASS only when verify passed and the broad
audit returned no release-profile findings; any persist, finding or warning
fails the gate. FailedAreas and new finding ids merge into the session
Investigation.

Examples:
  release_qa {proxy_id: "dev"}
  release_qa {proxy_id: "dev", flow: [{action: "navigate", url: "http://localhost:3000/checkout"}, {action: "exec", selector: ".pay-btn"}]}
  release_qa {proxy_id: "dev", audit: "responsive", raw: true}`,
	}, makeReleaseQAHandler(dt))
}

func makeReleaseQAHandler(dt *DaemonTools) func(context.Context, *mcp.CallToolRequest, ReleaseQAInput) (*mcp.CallToolResult, ReleaseQAOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ReleaseQAInput) (*mcp.CallToolResult, ReleaseQAOutput, error) {
		if dt == nil {
			return fail[ReleaseQAOutput]("release_qa requires daemon mode")
		}
		if input.ProxyID == "" {
			return fail[ReleaseQAOutput]("proxy_id required")
		}
		if err := dt.ensureConnected(); err != nil {
			return fail[ReleaseQAOutput]("release_qa failed: cannot reach daemon: " + err.Error())
		}
		vdeps := defaultVerifyDeps(dt, "")
		deps := releaseQADeps{
			investigation: vdeps.investigation,
			merge:         vdeps.merge,
			verify: func(ctx context.Context, in VerifyChangeInput) (VerifyChangeOutput, error) {
				out, err := runVerifyChange(ctx, in, vdeps)
				if err != nil && strings.Contains(err.Error(), "no findings to verify") {
					// An empty Investigation means nothing recorded the changed
					// flow — nothing to verify, not a failure.
					return VerifyChangeOutput{Status: "PASS", Header: "verify_change: PASS (no recorded findings)"}, nil
				}
				return out, err
			},
			flowStep: dt.releaseQAFlowStep(input.ProxyID),
			audit:    dt.releaseQABroadAudit(input.ProxyID),
			drill:    dt.releaseQADrill(input.ProxyID),
		}
		out, err := runReleaseQA(ctx, input, deps)
		if err != nil {
			return fail[ReleaseQAOutput]("release_qa failed: " + err.Error())
		}
		if input.Raw {
			b, _ := json.Marshal(out)
			return mcpText(string(b)), out, nil
		}
		return mcpText(formatReleaseQACompact(out)), out, nil
	}
}

// releaseQAFlowStep executes one explicit flow step through the existing
// proxy navigate/exec code path only (client.ProxyExec).
func (dt *DaemonTools) releaseQAFlowStep(proxyID string) func(ctx context.Context, step ReleaseQAFlowStep) error {
	return func(ctx context.Context, step ReleaseQAFlowStep) error {
		code, err := releaseQAFlowCode(step)
		if err != nil {
			return err
		}
		result, err := dt.client.ProxyExec(proxyID, code)
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok && errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		if !getBool(result, "success") {
			if e := getString(result, "error"); e != "" {
				return fmt.Errorf("%s", e)
			}
		}
		// A page-side {error: ...} payload (e.g. selector matched nothing).
		if r := getString(result, "result"); strings.Contains(r, `"error"`) {
			var payload struct {
				Error string `json:"error"`
			}
			if json.Unmarshal([]byte(r), &payload) == nil && payload.Error != "" {
				return fmt.Errorf("%s", payload.Error)
			}
		}
		return nil
	}
}

// releaseQAQualityAuditCode is the proxy-exec code for the default broad
// quality audit: the FULL aggregate __devtool.auditAll (dom/css/security/
// seo/performance/api/loading/accessibility), not the narrower synchronous
// auditPageQuality. auditAll returns a Promise in every mode (Promise.resolve
// on the sync path, an accessibility-chained Promise otherwise), so the
// result MUST be resolved via .then before stringify — JSON.stringify(Promise)
// is "{}", which would fake a clean audit and a PASS. The page-side exec
// handler (core.js) resolves returned Promises, so returning the .then chain
// is sufficient.
func releaseQAQualityAuditCode() string {
	return `(function() {
	var d = window.__devtool;
	if (!d || typeof d.auditAll !== 'function') {
		return JSON.stringify({ error: '__devtool.auditAll not available on this page (old injected bundle — reload the page through the proxy)' });
	}
	return d.auditAll({ raw: true, detailLevel: 'full' }).then(function(r) {
		return typeof r === 'string' ? r : JSON.stringify(r);
	});
})()`
}

// releaseQABroadAudit runs the call's ONE broad audit. quality goes through
// the aggregate __devtool.auditAll via proxy exec; the named audits run their
// own handlers in-process with Raw so finding ids/selectors come back.
func (dt *DaemonTools) releaseQABroadAudit(proxyID string) func(ctx context.Context, tool, profile string) ([]releaseQAFinding, error) {
	return func(ctx context.Context, tool, profile string) ([]releaseQAFinding, error) {
		switch tool {
		case "quality":
			code := releaseQAQualityAuditCode()
			result, err := dt.client.ProxyExec(proxyID, code)
			if err != nil {
				return nil, fmt.Errorf("audit exec failed: %v", err)
			}
			if errMsg, ok := result["error"].(string); ok && errMsg != "" {
				return nil, fmt.Errorf("audit exec failed: %s", errMsg)
			}
			resultStr := getString(result, "result")
			if resultStr == "" {
				b, _ := json.Marshal(result)
				resultStr = string(b)
			}
			var rawErr struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal([]byte(resultStr), &rawErr); err == nil && rawErr.Error != "" {
				return nil, fmt.Errorf("%s", rawErr.Error)
			}
			var raw any
			if err := json.Unmarshal([]byte(resultStr), &raw); err != nil {
				return nil, fmt.Errorf("audit returned unparseable result: %v", err)
			}
			return parseReleaseFindings(raw), nil
		case "responsive":
			res, out, err := dt.makeResponsiveAuditHandler()(ctx, nil, ResponsiveAuditInput{ProxyID: proxyID, Raw: true, Profile: profile})
			if err != nil {
				return nil, err
			}
			if res != nil && res.IsError {
				return nil, fmt.Errorf("responsive_audit failed: %s", callToolResultErrorText(res))
			}
			return parseReleaseFindings(out.Raw), nil
		case "api":
			res, out, err := dt.makeAPIAuditHandler()(ctx, nil, APIAuditInput{ProxyID: proxyID, Raw: true, Profile: profile})
			if err != nil {
				return nil, err
			}
			if res != nil && res.IsError {
				return nil, fmt.Errorf("api_audit failed: %s", callToolResultErrorText(res))
			}
			return parseReleaseFindings(out.Raw), nil
		case "loading":
			res, out, err := dt.makeLoadingAuditHandler()(ctx, nil, LoadingAuditInput{ProxyID: proxyID, Raw: true, Profile: profile})
			if err != nil {
				return nil, err
			}
			if res != nil && res.IsError {
				return nil, fmt.Errorf("loading_audit failed: %s", callToolResultErrorText(res))
			}
			return parseReleaseFindings(out.Raw), nil
		default:
			return nil, fmt.Errorf("invalid audit %q (valid: quality, responsive, api, loading)", tool)
		}
	}
}

// releaseQADrillFindings runs the same targeted drill-down handlers as
// releaseQADrill but returns the parsed findings (Raw) instead of the
// summary. The release_qa verify producer uses it to recheck one recorded
// finding without ever running the broad audit.
func (dt *DaemonTools) releaseQADrillFindings(proxyID string) func(ctx context.Context, area string, f releaseQAFinding) ([]releaseQAFinding, error) {
	return func(ctx context.Context, area string, f releaseQAFinding) ([]releaseQAFinding, error) {
		switch area {
		case "responsive":
			res, out, err := dt.makeDiagnoseHandler()(ctx, nil, DiagnoseInput{ProxyID: proxyID, Action: "layout", Selector: f.Selector, Raw: true})
			if err != nil {
				return nil, err
			}
			if res != nil && res.IsError {
				return nil, fmt.Errorf("diagnose layout failed: %s", callToolResultErrorText(res))
			}
			return parseReleaseFindings(out.Raw), nil
		case "click":
			res, out, err := dt.makeDiagnoseHandler()(ctx, nil, DiagnoseInput{ProxyID: proxyID, Action: "click", Selector: f.Selector, Raw: true})
			if err != nil {
				return nil, err
			}
			if res != nil && res.IsError {
				return nil, fmt.Errorf("diagnose click failed: %s", callToolResultErrorText(res))
			}
			return parseReleaseFindings(out.Raw), nil
		case "api":
			res, out, err := dt.makeAPIAuditHandler()(ctx, nil, APIAuditInput{ProxyID: proxyID, Raw: true, Profile: AuditProfileRelease})
			if err != nil {
				return nil, err
			}
			if res != nil && res.IsError {
				return nil, fmt.Errorf("api_audit failed: %s", callToolResultErrorText(res))
			}
			return parseReleaseFindings(out.Raw), nil
		default:
			return nil, fmt.Errorf("no drill-down for area %q", area)
		}
	}
}

// releaseQADrill dispatches the conditional drill-down for one failed-area
// finding: responsive -> diagnose layout, click -> diagnose click, api ->
// api_audit re-run. All in-process via the tools' own handlers.
func (dt *DaemonTools) releaseQADrill(proxyID string) func(ctx context.Context, area string, f releaseQAFinding) (string, error) {
	return func(ctx context.Context, area string, f releaseQAFinding) (string, error) {
		switch area {
		case "responsive":
			res, out, err := dt.makeDiagnoseHandler()(ctx, nil, DiagnoseInput{ProxyID: proxyID, Action: "layout", Selector: f.Selector, Raw: true})
			if err != nil {
				return "", err
			}
			if res != nil && res.IsError {
				return "", fmt.Errorf("diagnose layout failed: %s", callToolResultErrorText(res))
			}
			return out.Summary, nil
		case "click":
			res, out, err := dt.makeDiagnoseHandler()(ctx, nil, DiagnoseInput{ProxyID: proxyID, Action: "click", Selector: f.Selector, Raw: true})
			if err != nil {
				return "", err
			}
			if res != nil && res.IsError {
				return "", fmt.Errorf("diagnose click failed: %s", callToolResultErrorText(res))
			}
			return out.Summary, nil
		case "api":
			// api_audit analyzes the recorded fetch/XHR buffer; the failing
			// url pattern scopes the agent's read of the output.
			res, out, err := dt.makeAPIAuditHandler()(ctx, nil, APIAuditInput{ProxyID: proxyID, Raw: true, Profile: AuditProfileRelease})
			if err != nil {
				return "", err
			}
			if res != nil && res.IsError {
				return "", fmt.Errorf("api_audit failed: %s", callToolResultErrorText(res))
			}
			return out.Summary, nil
		default:
			return "", fmt.Errorf("no drill-down for area %q", area)
		}
	}
}
