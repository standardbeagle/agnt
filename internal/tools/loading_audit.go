package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"

	"github.com/standardbeagle/go-sdk/mcp"
)

// LoadingAuditInput defines input for the loading_audit tool.
type LoadingAuditInput struct {
	ProxyID string `json:"proxy_id,omitempty" jsonschema:"Proxy ID to run audit on (preferred)"`
	ID      string `json:"id,omitempty" jsonschema:"Alias for proxy_id"`
	Raw     bool   `json:"raw,omitempty" jsonschema:"Return full JSON instead of compact text"`
}

// LoadingAuditOutput defines output for the loading_audit tool.
type LoadingAuditOutput struct {
	Summary string `json:"summary"`
	Raw     any    `json:"raw,omitempty"`
}

// loadingAuditToolDescription is shared between legacy and daemon mode registrations.
const loadingAuditToolDescription = `Run a loading-UX audit over the recorded spinner/loader timeline.

Analyzes the in-page spinner timeline (window.__devtool_spinners) and flags:
  spinner-cascade:       loaders that fire serially (B appears after A finishes)
                         when they could have loaded in parallel
  spinner-fragmentation: 3+ concurrent sub-loaders under one ancestor that
                         should be consolidated into a single master loader

The audit reads a timeline populated during page load — a fresh page load is
required to capture the loading sequence. If the timeline is empty the audit
returns score 100 with a summary noting "No loading indicators recorded —
reload page then re-run".

Examples:
  loading_audit {proxy_id: "dev"}
  loading_audit {id: "dev"}
  loading_audit {proxy_id: "dev", raw: true}

Output:
  - Default: Compact text summary optimized for AI consumption
  - With raw: true: Full JSON with every finding and selector`

// loadingAuditBackend holds either a daemon client or a direct proxy manager,
// enabling a single registration path for both daemon and legacy modes.
type loadingAuditBackend struct {
	DualBackend[DaemonTools, proxy.ProxyManager]
}

// RegisterLoadingAuditTool registers the loading_audit tool.
// Exactly one of dt or pm must be non-nil.
func RegisterLoadingAuditTool(server *mcp.Server, dt *DaemonTools, pm *proxy.ProxyManager) {
	backend := &loadingAuditBackend{DualBackend[DaemonTools, proxy.ProxyManager]{Daemon: dt, Legacy: pm}}
	addLenientTool(server, &mcp.Tool{
		Name:        "loading_audit",
		Description: loadingAuditToolDescription,
	}, backend.makeHandler())
}

// makeHandler creates a handler that dispatches to daemon or legacy path.
func (b *loadingAuditBackend) makeHandler() func(context.Context, *mcp.CallToolRequest, LoadingAuditInput) (*mcp.CallToolResult, LoadingAuditOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input LoadingAuditInput) (*mcp.CallToolResult, LoadingAuditOutput, error) {
		input.ProxyID = pickProxyID(input.ID, input.ProxyID)
		if input.ProxyID == "" {
			return errorResult("proxy_id required (or `id` alias)"), LoadingAuditOutput{}, nil
		}

		type auditResult struct {
			toolResult *mcp.CallToolResult
			output     LoadingAuditOutput
			err        error
		}
		res, _ := DispatchResult(b.DualBackend,
			func(dt *DaemonTools) (auditResult, error) {
				if err := dt.ensureConnected(); err != nil {
					return auditResult{toolResult: errorResult(err.Error())}, nil
				}
				r, o, e := dt.executeLoadingAuditDaemon(input)
				return auditResult{toolResult: r, output: o, err: e}, nil
			},
			func(pm *proxy.ProxyManager) (auditResult, error) {
				proxyServer, err := pm.Get(input.ProxyID)
				if err != nil {
					return auditResult{toolResult: errorResult(fmt.Sprintf("proxy not found: %s", input.ProxyID))}, nil
				}
				r, o, e := executeLoadingAuditLegacy(proxyServer, input)
				return auditResult{toolResult: r, output: o, err: e}, nil
			},
		)
		return res.toolResult, res.output, res.err
	}
}

// buildLoadingAuditCode constructs the JavaScript that invokes the audit module.
func buildLoadingAuditCode(raw bool) string {
	return fmt.Sprintf(`(function() {
		if (!window.__devtool_audit_loading || !window.__devtool_audit_loading.auditLoading) {
			return JSON.stringify({ error: 'audit-loading module not loaded' });
		}
		return JSON.stringify(window.__devtool_audit_loading.auditLoading({ raw: %t }));
	})()`, raw)
}

// executeLoadingAuditDaemon runs the loading audit using the daemon client.
func (dt *DaemonTools) executeLoadingAuditDaemon(input LoadingAuditInput) (*mcp.CallToolResult, LoadingAuditOutput, error) {
	code := buildLoadingAuditCode(input.Raw)

	result, err := dt.client.ProxyExec(input.ProxyID, code)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to execute audit: %v", err)), LoadingAuditOutput{}, nil
	}

	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		return errorResult(fmt.Sprintf("audit failed: %s", errMsg)), LoadingAuditOutput{}, nil
	}

	resultStr := getString(result, "result")
	if resultStr == "" {
		b, _ := json.Marshal(result)
		resultStr = string(b)
	}

	return parseLoadingAuditResult(resultStr, input.Raw)
}

// executeLoadingAuditLegacy runs the loading audit in legacy (no-daemon) mode.
func executeLoadingAuditLegacy(proxyServer *proxy.ProxyServer, input LoadingAuditInput) (*mcp.CallToolResult, LoadingAuditOutput, error) {
	code := buildLoadingAuditCode(input.Raw)

	execID, resultChan, err := proxyServer.ExecuteJavaScript(code)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to execute audit: %v", err)), LoadingAuditOutput{}, nil
	}

	timeout := time.Duration(30) * time.Second
	select {
	case result := <-resultChan:
		if result == nil {
			return errorResult("execution channel closed without result"), LoadingAuditOutput{}, nil
		}
		if result.Error != "" {
			return errorResult(fmt.Sprintf("audit failed: %s", result.Error)), LoadingAuditOutput{}, nil
		}

		proxyServer.Logger().LogExecution(proxy.ExecutionResult{
			ID:        execID,
			Code:      "loading_audit",
			Result:    result.Result,
			Duration:  result.Duration,
			Timestamp: result.Timestamp,
		})

		return parseLoadingAuditResult(result.Result, input.Raw)

	case <-time.After(timeout):
		return errorResult(fmt.Sprintf("audit timed out after %v", timeout)), LoadingAuditOutput{}, nil
	}
}

// parseLoadingAuditResult decodes the JSON returned by auditLoading and formats
// it for the requested output mode. The audit returns a JSON object in both
// modes; the non-raw object is condensed into a compact text summary.
func parseLoadingAuditResult(resultStr string, raw bool) (*mcp.CallToolResult, LoadingAuditOutput, error) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(resultStr), &parsed); err != nil {
		// Not JSON — surface the raw payload as the summary.
		return nil, LoadingAuditOutput{Summary: resultStr}, nil
	}

	// Module-level error (e.g. audit-loading module not loaded).
	if errMsg, ok := parsed["error"].(string); ok && errMsg != "" {
		return errorResult(errMsg), LoadingAuditOutput{}, nil
	}

	if raw {
		return nil, LoadingAuditOutput{Summary: getString(parsed, "summary"), Raw: parsed}, nil
	}

	return nil, LoadingAuditOutput{Summary: formatLoadingAuditCompact(parsed)}, nil
}

// formatLoadingAuditCompact builds a short text summary from the AI-optimized
// (non-raw) audit object: score/grade headline, summary line, and a grouped
// list of findings by type.
func formatLoadingAuditCompact(parsed map[string]any) string {
	var b strings.Builder

	score := getFloat(parsed, "score")
	grade := getString(parsed, "grade")
	fmt.Fprintf(&b, "=== Loading UX Audit: %s (%d) ===\n", grade, int(score))

	if summary := getString(parsed, "summary"); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n")
	}

	// Empty-timeline case: stats.total == 0, no findings — the summary line above
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
