package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/standardbeagle/go-sdk/mcp"
)

// APIAuditInput defines input for the api_audit tool.
type APIAuditInput struct {
	ProxyID string `json:"proxy_id,omitempty" jsonschema:"Proxy ID to run audit on (preferred)"`
	ID      string `json:"id,omitempty" jsonschema:"Alias for proxy_id"`
	Target  string `json:"target,omitempty" jsonschema:"Frame in the always-wrap model: 'inner' (default) = active page content frame; 'outer' = chrome shell. Audits normally want inner."`
	FrameID string `json:"frame_id,omitempty" jsonschema:"Audit a specific content frame by id (default: the active content frame). Rarely needed."`
	Raw     bool   `json:"raw,omitempty" jsonschema:"Return full JSON instead of compact text"`
	Profile string `json:"profile,omitempty" jsonschema:"Finding projection: 'bug' (top 5 by severity), 'release', or 'full' (default: full)"`
}

// APIAuditOutput defines output for the api_audit tool.
type APIAuditOutput struct {
	Summary string `json:"summary"`
	Raw     any    `json:"raw,omitempty"`
}

// apiAuditToolDescription describes the api_audit tool.
const apiAuditToolDescription = `Run an API-efficiency audit over the recorded fetch/XHR call buffer.

Analyzes the in-page fetch/XHR call buffer (window.__devtool_api) and flags:
  waterfall:      serial request chains that could run in parallel
  n-plus-one:     a parameterised endpoint hit many times (batch opportunity)
  duplicate-call: identical request repeated within a short window
  chatty-load:    too many calls during the initial page load

The audit reads a buffer populated by browsing — a fresh page load is required
to fill it. If the buffer is empty the audit returns score 100 with a summary
noting "no API calls recorded — reload page then re-run".

Examples:
  api_audit {proxy_id: "dev"}
  api_audit {id: "dev"}
  api_audit {proxy_id: "dev", raw: true}

Output:
  - Default: Compact text summary optimized for AI consumption
  - With raw: true: Full JSON with every finding and selector`

// RegisterAPIAuditTool registers the api_audit tool.
func RegisterAPIAuditTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name:        "api_audit",
		Description: apiAuditToolDescription,
	}, dt.makeAPIAuditHandler())
}

// makeAPIAuditHandler runs the api_audit tool via the daemon.
func (dt *DaemonTools) makeAPIAuditHandler() func(context.Context, *mcp.CallToolRequest, APIAuditInput) (*mcp.CallToolResult, APIAuditOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input APIAuditInput) (*mcp.CallToolResult, APIAuditOutput, error) {
		input.ProxyID = pickProxyID(input.ID, input.ProxyID)
		if input.ProxyID == "" {
			return fail[APIAuditOutput]("proxy_id required (or `id` alias)")
		}

		if err := dt.ensureConnected(); err != nil {
			return fail[APIAuditOutput](err.Error())
		}

		res, summary, raw := dt.runBufferAudit(apiAuditSpec, input.ProxyID, input.Target, input.FrameID, input.Raw, input.Profile)
		if res != nil {
			return res, APIAuditOutput{}, nil
		}
		return nil, APIAuditOutput{Summary: summary, Raw: raw}, nil
	}
}

// apiAuditSpec parameterizes runBufferAudit for the fetch/XHR call buffer.
var apiAuditSpec = bufferAuditSpec{
	globalVar: "__devtool_audit_api",
	fn:        "auditAPIEfficiency",
	module:    "audit-api",
	headline:  "API Efficiency Audit",
}

// bufferAuditSpec parameterizes a buffer-backed audit (api_audit /
// loading_audit): the in-page global + method to invoke, the module name used
// in the not-loaded error, and the compact-summary headline. The two audits
// differ only in these four values; everything else is shared below.
type bufferAuditSpec struct {
	globalVar string
	fn        string
	module    string
	headline  string
}

// buildBufferAuditCode constructs the JavaScript that invokes the audit module.
func buildBufferAuditCode(spec bufferAuditSpec, raw bool) string {
	return fmt.Sprintf(`(function() {
		if (!window.%[1]s || !window.%[1]s.%[2]s) {
			return JSON.stringify({ error: '%[3]s module not loaded' });
		}
		return JSON.stringify(window.%[1]s.%[2]s({ raw: %[4]t }));
	})()`, spec.globalVar, spec.fn, spec.module, raw)
}

// runBufferAudit executes a buffer-backed audit via the daemon and returns
// either an error result (non-nil first return) or a summary string plus an
// optional raw payload. proxyID must already be resolved.
func (dt *DaemonTools) runBufferAudit(spec bufferAuditSpec, proxyID, target, frameID string, raw bool, profile string) (*mcp.CallToolResult, string, any) {
	if err := validateBufferAuditProfile(profile); err != nil {
		return fail[string](err.Error())
	}
	code := buildBufferAuditCode(spec, raw)

	execTarget, err := resolveExecTarget(target, frameID)
	if err != nil {
		return fail[string](err.Error())
	}
	result, err := dt.client.ProxyExec(proxyID, code, execTarget)
	if err != nil {
		return fail[string](fmt.Sprintf("failed to execute audit: %v", err))
	}

	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		return fail[string](fmt.Sprintf("audit failed: %s", errMsg))
	}

	resultStr := getString(result, "result")
	if resultStr == "" {
		b, _ := json.Marshal(result)
		resultStr = string(b)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(resultStr), &parsed); err != nil {
		// Not JSON — surface the raw payload as the summary.
		return nil, resultStr, nil
	}

	// Module-level error (e.g. audit module not loaded).
	if errMsg, ok := parsed["error"].(string); ok && errMsg != "" {
		return fail[string](errMsg)
	}

	if raw {
		annotateBufferAuditNext(parsed, proxyID)
		return nil, getString(parsed, "summary"), parsed
	}
	return nil, formatBufferAuditCompact(spec.headline, parsed, proxyID, profile), nil
}

// validateBufferAuditProfile rejects unknown profile names.
func validateBufferAuditProfile(profile string) error {
	switch profile {
	case "", AuditProfileBug, AuditProfileRelease, AuditProfileFull:
		return nil
	}
	return fmt.Errorf("invalid profile: %q (valid: bug, release, full)", profile)
}

// bufferAuditURLPattern derives the proxylog query url_pattern for a
// finding. For n-plus-one the finding's template ("GET /api/items/{id}") is
// reduced to the concrete path prefix — method dropped, truncated before the
// first {id} — so the pattern substring-matches every recorded request URL
// in the group ("/api/items/"). Other findings use their URL selector.
func bufferAuditURLPattern(findingType string, f map[string]any) string {
	if findingType == "n-plus-one" {
		if tmpl := getString(f, "template"); tmpl != "" {
			pat := tmpl
			if i := strings.Index(pat, "{id}"); i >= 0 {
				pat = pat[:i]
			}
			if i := strings.Index(pat, " "); i >= 0 {
				pat = pat[i+1:]
			}
			return pat
		}
	}
	return getString(f, "selector")
}

// bufferAuditNextAction returns the exact follow-up tool action for one
// finding: N+1/duplicate/chatty drill into the recorded call buffer via
// proxylog query (url_pattern derived by bufferAuditURLPattern), waterfall
// wants the buffer summary, and loading findings want the page layout view.
// findingType is the resolved type (finding field or group key); S3b may
// retarget the spinner findings to diagnose.
func bufferAuditNextAction(findingType string, f map[string]any, proxyID string) string {
	switch findingType {
	case "n-plus-one", "duplicate-call", "chatty-load":
		return fmt.Sprintf(`proxylog {action:"query", proxy_id:"%s", url_pattern:"%s"}`, proxyID, bufferAuditURLPattern(findingType, f))
	case "waterfall":
		return fmt.Sprintf(`proxylog {action:"summary", proxy_id:"%s"}`, proxyID)
	case "spinner-cascade", "spinner-fragmentation":
		return fmt.Sprintf(`currentpage {action:"layout", proxy_id:"%s"}`, proxyID)
	}
	return ""
}

// annotateBufferAuditNext adds a "next" action to every finding in the raw
// JSON payload so raw consumers get the same drill-down as the compact text.
func annotateBufferAuditNext(parsed map[string]any, proxyID string) {
	findings, ok := parsed["findings"].([]any)
	if !ok {
		return
	}
	for _, fAny := range findings {
		f, ok := fAny.(map[string]any)
		if !ok {
			continue
		}
		if next := bufferAuditNextAction(getString(f, "type"), f, proxyID); next != "" {
			f["next"] = next
		}
	}
}

// formatBufferAuditCompact builds a short text summary from the AI-optimized
// (non-raw) audit object: score/grade headline, summary line, and a grouped
// list of findings by type. Every finding line is followed by its stable
// `id:` and one exact `next:` drill-down action. The profile projection
// (ProjectFindingsByProfile) selects which findings render.
func formatBufferAuditCompact(headline string, parsed map[string]any, proxyID, profile string) string {
	var b strings.Builder

	score := getFloat64(parsed, "score")
	grade := getString(parsed, "grade")
	fmt.Fprintf(&b, "=== %s: %s (%d) ===\n", headline, grade, int(score))

	if summary := getString(parsed, "summary"); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n")
	}

	// Empty-buffer case: stats.total == 0, no findings — the summary line above
	// already carries the "reload page then re-run" guidance.
	byType, _ := parsed["findingsByType"].(map[string]any)
	if len(byType) == 0 {
		return strings.TrimRight(b.String(), "\n")
	}

	// Flatten in sorted-type order (deterministic input for the stable bug
	// sort), attach next actions, project by profile, regroup by type.
	flatTypes := make([]string, 0, len(byType))
	for t := range byType {
		flatTypes = append(flatTypes, t)
	}
	sort.Strings(flatTypes)
	flat := make([]AuditFinding, 0, len(byType))
	for _, t := range flatTypes {
		fList := byType[t]
		findings, ok := fList.([]any)
		if !ok {
			continue
		}
		for _, fAny := range findings {
			f, ok := fAny.(map[string]any)
			if !ok {
				continue
			}
			ft := getString(f, "type")
			if ft == "" {
				ft = t
			}
			flat = append(flat, AuditFinding{
				ID:       getString(f, "id"),
				Type:     ft,
				Severity: getString(f, "severity"),
				Selector: getString(f, "selector"),
				Message:  getString(f, "message"),
				Next:     bufferAuditNextAction(ft, f, proxyID),
			})
		}
	}
	projected := ProjectFindingsByProfile(flat, profile)

	grouped := make(map[string][]AuditFinding)
	for _, f := range projected {
		grouped[f.Type] = append(grouped[f.Type], f)
	}

	// Deterministic ordering of finding types.
	types := make([]string, 0, len(grouped))
	for t := range grouped {
		types = append(types, t)
	}
	sort.Strings(types)

	for _, t := range types {
		findings := grouped[t]
		fmt.Fprintf(&b, "\n%s (%d)\n", t, len(findings))
		for _, f := range findings {
			if f.Selector != "" {
				fmt.Fprintf(&b, "  [%s] %s — %s\n", f.Severity, f.Selector, f.Message)
			} else {
				fmt.Fprintf(&b, "  [%s] %s\n", f.Severity, f.Message)
			}
			if f.ID != "" {
				fmt.Fprintf(&b, "  id: %s\n", f.ID)
			}
			if f.Next != "" {
				fmt.Fprintf(&b, "  next: %s\n", f.Next)
			}
		}
	}

	return strings.TrimRight(b.String(), "\n")
}
