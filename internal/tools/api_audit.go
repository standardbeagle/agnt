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
			return errorResult("proxy_id required (or `id` alias)"), APIAuditOutput{}, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), APIAuditOutput{}, nil
		}

		res, summary, raw := dt.runBufferAudit(apiAuditSpec, input.ProxyID, input.Target, input.FrameID, input.Raw)
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
func (dt *DaemonTools) runBufferAudit(spec bufferAuditSpec, proxyID, target, frameID string, raw bool) (*mcp.CallToolResult, string, any) {
	code := buildBufferAuditCode(spec, raw)

	execTarget, err := resolveExecTarget(target, frameID)
	if err != nil {
		return errorResult(err.Error()), "", nil
	}
	result, err := dt.client.ProxyExec(proxyID, code, execTarget)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to execute audit: %v", err)), "", nil
	}

	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		return errorResult(fmt.Sprintf("audit failed: %s", errMsg)), "", nil
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
		return errorResult(errMsg), "", nil
	}

	if raw {
		return nil, getString(parsed, "summary"), parsed
	}
	return nil, formatBufferAuditCompact(spec.headline, parsed), nil
}

// formatBufferAuditCompact builds a short text summary from the AI-optimized
// (non-raw) audit object: score/grade headline, summary line, and a grouped
// list of findings by type.
func formatBufferAuditCompact(headline string, parsed map[string]any) string {
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

	// Deterministic ordering of finding types.
	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)

	for _, t := range types {
		findings, ok := byType[t].([]any)
		if !ok || len(findings) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s (%d)\n", t, len(findings))
		for _, fAny := range findings {
			f, ok := fAny.(map[string]any)
			if !ok {
				continue
			}
			sev := getString(f, "severity")
			msg := getString(f, "message")
			sel := getString(f, "selector")
			if sel != "" {
				fmt.Fprintf(&b, "  [%s] %s — %s\n", sev, sel, msg)
			} else {
				fmt.Fprintf(&b, "  [%s] %s\n", sev, msg)
			}
		}
	}

	return strings.TrimRight(b.String(), "\n")
}
