package tools

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/standardbeagle/agnt/internal/finding"
	"github.com/standardbeagle/go-sdk/mcp"
)

// diagnoseLayoutCompositeResult mirrors the structured object returned by
// __devtool.diagnoseLayoutComposite (internal/proxy/scripts/diagnostics.js).
type diagnoseLayoutCompositeResult struct {
	Helper          string `json:"helper"`
	Version         int    `json:"version"`
	EvidenceMissing string `json:"evidenceMissing,omitempty"`
	// Layout is the raw __devtool_layout.diagnose() payload — parsed ONLY via
	// parseLayoutDiagnostics (currentpage_layout.go). Never a second parser.
	Layout     json.RawMessage `json:"layout,omitempty"`
	Responsive struct {
		Error           string `json:"error,omitempty"`
		EvidenceMissing string `json:"evidenceMissing,omitempty"`
		Issues          []struct {
			Selector string `json:"selector"`
			Issues   []struct {
				Type     string `json:"type"`
				Severity string `json:"severity"`
				Message  string `json:"message"`
			} `json:"issues"`
		} `json:"issues,omitempty"`
	} `json:"responsive,omitempty"`
	// Causes maps a visual finding's selector to its stacking/container cause.
	Causes map[string]struct {
		Stacking  map[string]any `json:"stacking,omitempty"`
		Container map[string]any `json:"container,omitempty"`
	} `json:"causes,omitempty"`
}

// resizeStateJS reads the shell's content-frame inline style so the restore
// knows whether a prior explicit resize was active. Run against "@chrome".
// Full-bleed frames have no inline width (frames.js resets cssText), so an
// empty width means "restore via the (0,0) reset".
const resizeStateJS = `(function(){var f=document.getElementById('__devtool_content_frame');` +
	`if(!f){return JSON.stringify({error:'no content frame'});}` +
	`return JSON.stringify({width:String(f.style.width||''),height:String(f.style.height||'')});})()`

// parseResizeState extracts prior px dimensions from the shell's
// content-frame style state. Anything not an explicit px value (empty,
// "100%", unparseable) maps to 0, which buildResizeJS treats as the
// full-bleed/full-height reset.
func parseResizeState(result string) (w, h int) {
	var st struct {
		Width  string `json:"width"`
		Height string `json:"height"`
	}
	if json.Unmarshal([]byte(result), &st) != nil {
		return 0, 0
	}
	return parsePx(st.Width), parsePx(st.Height)
}

// parsePx parses a CSS px value ("375px" -> 375); anything else -> 0.
func parsePx(s string) int {
	s = strings.TrimSuffix(strings.TrimSpace(s), "px")
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// screenshotRecommendation names the element to frame and the exact
// __devtool.screenshot call. Populated ONLY when at least one finding is
// Visual (overflow, clipped, offscreen, overlap).
type screenshotRecommendation struct {
	Selector string `json:"selector"`
	Call     string `json:"call"`
	Reason   string `json:"reason"`
}

// layoutProxyExec is the narrow ProxyExec surface runDiagnoseLayout needs, so
// tests can inject failures without a daemon.
type layoutProxyExec interface {
	ProxyExec(id, code string, frameID ...string) (map[string]interface{}, error)
}

// executeDiagnoseLayout runs the layout composite through the daemon client.
func (dt *DaemonTools) executeDiagnoseLayout(input DiagnoseInput) (*mcp.CallToolResult, DiagnoseOutput, error) {
	return runDiagnoseLayout(dt.client, input)
}

// runDiagnoseLayout executes __devtool.diagnoseLayoutComposite in the page.
// When input.Viewport is set, the viewport is resized first (via the proxy
// resize action's chrome-shell path) and the original size is restored in a
// defer — including when the composite exec fails.
func runDiagnoseLayout(exec layoutProxyExec, input DiagnoseInput) (*mcp.CallToolResult, DiagnoseOutput, error) {
	execTarget, err := resolveExecTarget(input.Target, input.FrameID)
	if err != nil {
		return fail[DiagnoseOutput](err.Error())
	}

	if input.Viewport != nil && input.Viewport.Width > 0 {
		// Prior resize state is read from the SHELL's content-frame inline
		// style — never via a raw innerWidth exec against the inner frame.
		// Full-bleed (no inline width) restores via the (0,0) reset; a prior
		// explicit resize is re-applied as px.
		origW, origH := 0, 0
		stateRes, serr := exec.ProxyExec(input.ProxyID, resizeStateJS, "@chrome")
		if serr == nil {
			origW, origH = parseResizeState(getString(stateRes, "result"))
		}
		if _, rerr := exec.ProxyExec(input.ProxyID, buildResizeJS(input.Viewport.Width, input.Viewport.Height), "@chrome"); rerr != nil {
			return fail[DiagnoseOutput](fmt.Sprintf("viewport resize failed: %v", rerr))
		}
		defer func() {
			_, _ = exec.ProxyExec(input.ProxyID, buildResizeJS(origW, origH), "@chrome")
		}()
	}

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
		if (!d || typeof d.diagnoseLayoutComposite !== 'function') {
			return JSON.stringify({ error: '__devtool.diagnoseLayoutComposite not available on this page (old injected bundle — reload the page through the proxy)' });
		}
		var r = d.diagnoseLayoutComposite(%s);
		return typeof r === 'string' ? r : JSON.stringify(r);
	})()`, string(optsJSON))

	result, err := exec.ProxyExec(input.ProxyID, code, execTarget)
	if err != nil {
		return fail[DiagnoseOutput](fmt.Sprintf("diagnose layout exec failed: %v", err))
	}
	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		return fail[DiagnoseOutput]("diagnose layout exec failed: " + errMsg)
	}

	resultStr := getString(result, "result")
	if resultStr == "" {
		b, _ := json.Marshal(result)
		resultStr = string(b)
	}

	var parsed diagnoseLayoutCompositeResult
	helperErr := ""
	var rawErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(resultStr), &parsed); err != nil {
		helperErr = "diagnoseLayoutComposite returned unparseable result: " + err.Error()
	} else if err := json.Unmarshal([]byte(resultStr), &rawErr); err == nil && rawErr.Error != "" {
		helperErr = rawErr.Error
	}

	findings, shot := buildLayoutFindings(&parsed, helperErr)

	verdict := "no_issues"
	if helperErr != "" || parsed.EvidenceMissing != "" {
		verdict = "insufficient_evidence"
	} else if len(findings) > 0 {
		verdict = "issues_found"
	}

	if input.Raw {
		raw := map[string]any{
			"verdict":  verdict,
			"findings": findings,
			"evidence": json.RawMessage(resultStr),
		}
		if shot != nil {
			raw["screenshot_recommended"] = shot
		}
		b, _ := json.Marshal(raw)
		return mcpText(string(b)), DiagnoseOutput{Summary: string(b), Verdict: verdict, Raw: raw}, nil
	}

	summary := renderDiagnoseLayoutCompact(input.ProxyID, findings, shot, "")
	return mcpText(summary), DiagnoseOutput{Summary: summary, Verdict: verdict}, nil
}

// visualLayoutType reports whether a responsive-risk issue type or a layout
// diagnose check is visual (overflow, clipped, offscreen, overlap) — only
// visual findings populate screenshot_recommended.
func visualLayoutType(kind string) bool {
	switch kind {
	case "clipped-descendant":
		return true
	}
	return strings.Contains(kind, "scroll") ||
		strings.Contains(kind, "offscreen") ||
		strings.Contains(kind, "overflow") ||
		strings.Contains(kind, "overlap")
}

// layoutSeverity maps diagnose() severities onto finding severities.
func layoutSeverity(s string) string {
	switch s {
	case "high", "error":
		return "error"
	default:
		return "warning"
	}
}

// causeText extracts a human cause from the enriched stacking/container
// evidence for a selector, when one was found.
func causeText(res *diagnoseLayoutCompositeResult, selector string) string {
	c, ok := res.Causes[selector]
	if !ok {
		return ""
	}
	if trappedBy, ok := c.Container["trappedBy"].(map[string]any); ok && trappedBy != nil {
		sel, _ := trappedBy["selector"].(string)
		prop, _ := trappedBy["property"].(string)
		if sel != "" {
			return fmt.Sprintf("container cause: trapped by %q (%s)", sel, prop)
		}
	}
	if root, _ := c.Stacking["root"].(string); root != "" {
		trigger, _ := c.Stacking["rootTrigger"].(string)
		return fmt.Sprintf("stacking cause: root %q (%s)", root, trigger)
	}
	return ""
}

// buildLayoutFindings projects the composite evidence into the shared finding
// list plus the single screenshot_recommended block. Pure: fixtures drive it
// without a live browser or daemon. The layout payload is parsed ONLY through
// parseLayoutDiagnostics (currentpage action=layout) — never a second parser.
func buildLayoutFindings(res *diagnoseLayoutCompositeResult, helperErr string) ([]finding.Finding, *screenshotRecommendation) {
	producer := finding.Producer{Tool: "diagnose", Args: map[string]any{"action": "layout"}}

	if helperErr != "" {
		return []finding.Finding{{
			ID:       finding.StableID("layout", "", helperErr),
			Category: "layout",
			Severity: "warning",
			Evidence: helperErr,
			Next: finding.NextAction{
				Tool:      "proxy",
				Args:      map[string]any{"action": "status"},
				Rationale: helperErr,
			},
			Producer: producer,
		}}, nil
	}
	if res == nil {
		return nil, nil
	}
	if res.EvidenceMissing != "" {
		return []finding.Finding{{
			ID:       finding.StableID("layout", "", res.EvidenceMissing),
			Category: "layout",
			Severity: "warning",
			Evidence: res.EvidenceMissing,
			Next: finding.NextAction{
				Tool:      "proxy",
				Args:      map[string]any{"action": "status"},
				Rationale: res.EvidenceMissing,
			},
			Producer: producer,
		}}, nil
	}

	var findings []finding.Finding
	var selectors []string // parallel to findings — the selector each was built from
	add := func(f finding.Finding, sel string) {
		findings = append(findings, f)
		selectors = append(selectors, sel)
	}

	// 1. Layout diagnose() findings (containing-block traps, ineffective
	//    z-index, click interception, clipped descendants).
	if len(res.Layout) > 0 {
		if layout, err := parseLayoutDiagnostics(string(res.Layout)); err == nil {
			for _, lf := range layout.Findings {
				evidence := lf.Detail
				if lf.Cause != "" {
					evidence += fmt.Sprintf(" cause: %q (%s)", lf.Cause, lf.CauseProperty)
				}
				if c := causeText(res, lf.Selector); c != "" {
					evidence += "; " + c
				}
				nextCode := fmt.Sprintf("__devtool.getContainer('%s')", lf.Selector)
				if lf.Check == "ineffective-zindex" {
					nextCode = fmt.Sprintf("__devtool.getStacking('%s')", lf.Selector)
				}
				add(finding.Finding{
					ID:       finding.StableID("layout", lf.Selector, lf.Check),
					Category: "layout",
					Severity: layoutSeverity(lf.Severity),
					Evidence: evidence,
					Visual:   visualLayoutType(lf.Check),
					Next: finding.NextAction{
						Tool:      "proxy",
						Args:      map[string]any{"action": "exec", "code": nextCode},
						Rationale: "inspect the " + lf.Check + " cause on " + lf.Selector,
					},
					Producer: producer,
				}, lf.Selector)
			}
		}
	}

	// 2. Responsive risk findings for the current viewport.
	for _, entry := range res.Responsive.Issues {
		for _, issue := range entry.Issues {
			evidence := issue.Message
			if c := causeText(res, entry.Selector); c != "" {
				evidence += "; " + c
			}
			add(finding.Finding{
				ID:       finding.StableID("layout", entry.Selector, issue.Type),
				Category: "layout",
				Severity: layoutSeverity(issue.Severity),
				Evidence: evidence,
				Visual:   visualLayoutType(issue.Type),
				Next: finding.NextAction{
					Tool:      "proxy",
					Args:      map[string]any{"action": "exec", "code": fmt.Sprintf("__devtool.getContainer('%s')", entry.Selector)},
					Rationale: "inspect the container cause of the " + issue.Type + " on " + entry.Selector,
				},
				Producer: producer,
			}, entry.Selector)
		}
	}

	// screenshot_recommended: only when at least one finding is visual.
	var shot *screenshotRecommendation
	for i, f := range findings {
		if f.Visual {
			sel := selectors[i]
			shot = &screenshotRecommendation{
				Selector: sel,
				Call:     fmt.Sprintf("__devtool.screenshot({selector: '%s', name: 'diagnose_layout'})", sel),
				Reason:   "visual finding present (overflow/clipped/offscreen/overlap) — frame the element to confirm",
			}
			break
		}
	}

	return findings, shot
}

// renderDiagnoseLayoutCompact renders the layout findings in compact text
// form (id: / next: lines) shared with the other audit tools.
func renderDiagnoseLayoutCompact(proxyID string, findings []finding.Finding, shot *screenshotRecommendation, warning string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== diagnose layout (%s) ===\n", proxyID))
	sb.WriteString(fmt.Sprintf("findings: %d\n\n", len(findings)))
	for _, f := range findings {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", f.Severity, f.Evidence))
		sb.WriteString("  id: " + f.ID + "\n")
		sb.WriteString("  next: " + renderDiagnoseNext(f.Next) + "\n")
	}
	if shot != nil {
		sb.WriteString("\nscreenshot_recommended: " + shot.Call + "\n")
		sb.WriteString("  frame: " + shot.Selector + "\n")
	}
	if warning != "" {
		sb.WriteString("\n!! " + warning + "\n")
	}
	return sb.String()
}
