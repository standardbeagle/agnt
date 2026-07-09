package tools

import (
	"context"

	"github.com/standardbeagle/go-sdk/mcp"
)

// LoadingAuditInput defines input for the loading_audit tool.
type LoadingAuditInput struct {
	ProxyID string `json:"proxy_id,omitempty" jsonschema:"Proxy ID to run audit on (preferred)"`
	ID      string `json:"id,omitempty" jsonschema:"Alias for proxy_id"`
	Target  string `json:"target,omitempty" jsonschema:"Frame in the always-wrap model: 'inner' (default) = active page content frame; 'outer' = chrome shell. Audits normally want inner."`
	FrameID string `json:"frame_id,omitempty" jsonschema:"Audit a specific content frame by id (default: the active content frame). Rarely needed."`
	Raw     bool   `json:"raw,omitempty" jsonschema:"Return full JSON instead of compact text"`
}

// LoadingAuditOutput defines output for the loading_audit tool.
type LoadingAuditOutput struct {
	Summary string `json:"summary"`
	Raw     any    `json:"raw,omitempty"`
}

// loadingAuditToolDescription describes the loading_audit tool.
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

// RegisterLoadingAuditTool registers the loading_audit tool.
func RegisterLoadingAuditTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name:        "loading_audit",
		Description: loadingAuditToolDescription,
	}, dt.makeLoadingAuditHandler())
}

// makeLoadingAuditHandler runs the loading_audit tool via the daemon.
func (dt *DaemonTools) makeLoadingAuditHandler() func(context.Context, *mcp.CallToolRequest, LoadingAuditInput) (*mcp.CallToolResult, LoadingAuditOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input LoadingAuditInput) (*mcp.CallToolResult, LoadingAuditOutput, error) {
		input.ProxyID = pickProxyID(input.ID, input.ProxyID)
		if input.ProxyID == "" {
			return errorResult("proxy_id required (or `id` alias)"), LoadingAuditOutput{}, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), LoadingAuditOutput{}, nil
		}

		res, summary, raw := dt.runBufferAudit(loadingAuditSpec, input.ProxyID, input.Target, input.FrameID, input.Raw)
		if res != nil {
			return res, LoadingAuditOutput{}, nil
		}
		return nil, LoadingAuditOutput{Summary: summary, Raw: raw}, nil
	}
}

// loadingAuditSpec parameterizes runBufferAudit for the spinner/loader timeline.
var loadingAuditSpec = bufferAuditSpec{
	globalVar: "__devtool_audit_loading",
	fn:        "auditLoading",
	module:    "audit-loading",
	headline:  "Loading UX Audit",
}
