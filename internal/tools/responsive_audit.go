package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/standardbeagle/go-sdk/mcp"
)

// ResponsiveAuditInput defines input for the responsive_audit tool.
type ResponsiveAuditInput struct {
	ProxyID   string          `json:"proxy_id,omitempty" jsonschema:"Proxy ID to run audit on (preferred)"`
	ID        string          `json:"id,omitempty" jsonschema:"Alias for proxy_id"`
	Target    string          `json:"target,omitempty" jsonschema:"Frame in the always-wrap model: 'inner' (default) = active page content frame; 'outer' = chrome shell. Audits normally want inner."`
	FrameID   string          `json:"frame_id,omitempty" jsonschema:"Audit a specific content frame by id (default: the active content frame). Rarely needed."`
	Viewports []ViewportInput `json:"viewports,omitempty" jsonschema:"Custom viewports to test (default: mobile/tablet/desktop)"`
	Checks    []string        `json:"checks,omitempty" jsonschema:"Checks to run: layout, overflow, a11y (default: all)"`
	Timeout   int             `json:"timeout,omitempty" jsonschema:"Load timeout per viewport in ms (default: 10000)"`
	Raw       bool            `json:"raw,omitempty" jsonschema:"Return full JSON instead of compact text"`
}

// ViewportInput defines a viewport for testing.
type ViewportInput struct {
	Name   string `json:"name" jsonschema:"Viewport name (e.g., 'mobile', 'tablet', 'desktop')"`
	Width  int    `json:"width" jsonschema:"Viewport width in pixels"`
	Height int    `json:"height" jsonschema:"Viewport height in pixels"`
}

// ResponsiveAuditOutput defines output for the responsive_audit tool.
type ResponsiveAuditOutput struct {
	Summary string `json:"summary"`
	Raw     any    `json:"raw,omitempty"`
}

// responsiveAuditToolDescription describes the responsive_audit tool.
const responsiveAuditToolDescription = `Run responsive design audits across multiple viewport sizes.

Detects layout issues, content overflows, and viewport-specific accessibility problems
by loading the page in hidden iframes at target sizes.

Default viewports: mobile (375x667), tablet (768x1024), desktop (1440x900)

Checks available:
  layout:  Collapsed content, fixed element coverage, margin/padding squeeze
  overflow: Horizontal scroll, clipped content, truncated text, squeezed images
  a11y:     Touch target size, iOS zoom triggers, readability issues (mobile)

Examples:
  responsive_audit {proxy_id: "dev"}
  responsive_audit {proxy_id: "dev", checks: ["layout", "overflow"]}
  responsive_audit {proxy_id: "dev", viewports: [{name: "xs", width: 320, height: 568}]}
  responsive_audit {proxy_id: "dev", raw: true}

Output:
  - Default: Compact text format optimized for AI consumption
  - With raw: true: Full JSON with all issues and details

The audit uses sequential viewport loading to avoid memory pressure.
Typical duration: 3-6 seconds for 3 viewports.`

// validCheckTypes defines the allowed check type values.
var validCheckTypes = map[string]bool{
	"layout":   true,
	"overflow": true,
	"a11y":     true,
}

// RegisterResponsiveAuditTool registers the responsive_audit tool.
func RegisterResponsiveAuditTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name:        "responsive_audit",
		Description: responsiveAuditToolDescription,
	}, dt.makeResponsiveAuditHandler())
}

// makeResponsiveAuditHandler runs the responsive_audit tool via the daemon.
func (dt *DaemonTools) makeResponsiveAuditHandler() func(context.Context, *mcp.CallToolRequest, ResponsiveAuditInput) (*mcp.CallToolResult, ResponsiveAuditOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ResponsiveAuditInput) (*mcp.CallToolResult, ResponsiveAuditOutput, error) {
		input.ProxyID = pickProxyID(input.ID, input.ProxyID)
		if err := validateResponsiveAuditInputSize(input); err != nil {
			return fail[ResponsiveAuditOutput](validationError("responsive_audit", err))
		}
		if err := validateResponsiveAuditInput(input); err != nil {
			return fail[ResponsiveAuditOutput](err.Error())
		}

		if input.ProxyID == "" {
			return fail[ResponsiveAuditOutput]("proxy_id required (or `id` alias)")
		}

		if err := dt.ensureConnected(); err != nil {
			return fail[ResponsiveAuditOutput](err.Error())
		}
		return dt.executeResponsiveAuditDaemon(input)
	}
}

// executeResponsiveAuditDaemon runs the responsive audit using the daemon client.
func (dt *DaemonTools) executeResponsiveAuditDaemon(input ResponsiveAuditInput) (*mcp.CallToolResult, ResponsiveAuditOutput, error) {
	// Build the audit options for the browser
	auditOpts := buildAuditOptions(input)

	// Build JavaScript code to execute
	optsJSON, err := json.Marshal(auditOpts)
	if err != nil {
		return fail[ResponsiveAuditOutput](fmt.Sprintf("failed to marshal options: %v", err))
	}

	code := fmt.Sprintf(`(function() {
		var responsive = window.__devtool_responsive;
		if (!responsive || !responsive.audit) {
			return JSON.stringify({ error: 'Responsive audit module not loaded' });
		}
		var result = responsive.audit(%s);
		if (result && typeof result.then === 'function') {
			return result.then(function(r) {
				return typeof r === 'string' ? r : JSON.stringify(r);
			});
		}
		return typeof result === 'string' ? result : JSON.stringify(result);
	})()`, string(optsJSON))

	// Execute via daemon
	execTarget, err := resolveExecTarget(input.Target, input.FrameID)
	if err != nil {
		return fail[ResponsiveAuditOutput](err.Error())
	}
	result, err := dt.client.ProxyExec(input.ProxyID, code, execTarget)
	if err != nil {
		return fail[ResponsiveAuditOutput](fmt.Sprintf("failed to execute audit: %v", err))
	}

	// Check for error
	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		return fail[ResponsiveAuditOutput](fmt.Sprintf("audit failed: %s", errMsg))
	}

	// Get result string
	resultStr := getString(result, "result")
	if resultStr == "" {
		// Try to marshal the whole result
		b, _ := json.Marshal(result)
		resultStr = string(b)
	}

	// Parse result
	output := ResponsiveAuditOutput{}

	if input.Raw {
		// Return raw JSON
		var rawResult any
		if err := json.Unmarshal([]byte(resultStr), &rawResult); err != nil {
			output.Summary = resultStr
		} else {
			output.Summary = resultStr
			output.Raw = rawResult
		}
	} else {
		// Result is already formatted as compact text
		output.Summary = resultStr
	}

	return nil, output, nil
}

// buildAuditOptions constructs the audit options map from input.
func buildAuditOptions(input ResponsiveAuditInput) map[string]any {
	auditOpts := make(map[string]any)

	// Set viewports (filter out invalid ones)
	if len(input.Viewports) > 0 {
		viewports := make([]map[string]any, 0, len(input.Viewports))
		for _, vp := range input.Viewports {
			// Skip invalid viewports (negative or zero dimensions)
			if vp.Width > 0 && vp.Height > 0 {
				name := vp.Name
				if name == "" {
					name = fmt.Sprintf("%dx%d", vp.Width, vp.Height)
				}
				viewports = append(viewports, map[string]any{
					"name":   name,
					"width":  vp.Width,
					"height": vp.Height,
				})
			}
		}
		if len(viewports) > 0 {
			auditOpts["viewports"] = viewports
		}
	}

	// Set checks (filter invalid ones)
	if len(input.Checks) > 0 {
		validChecks := make([]string, 0, len(input.Checks))
		for _, check := range input.Checks {
			if validCheckTypes[check] {
				validChecks = append(validChecks, check)
			}
		}
		if len(validChecks) > 0 {
			auditOpts["checks"] = validChecks
		}
	}

	// Set timeout
	if input.Timeout > 0 {
		auditOpts["timeout"] = input.Timeout
	}

	// Set raw mode
	auditOpts["raw"] = input.Raw

	return auditOpts
}

// validateResponsiveAuditInput validates the input parameters.
func validateResponsiveAuditInput(input ResponsiveAuditInput) error {
	// Validate checks
	for _, check := range input.Checks {
		if !validCheckTypes[check] {
			return fmt.Errorf("invalid check type: %q (valid: layout, overflow, a11y)", check)
		}
	}

	// Validate viewports
	for _, vp := range input.Viewports {
		if vp.Width <= 0 || vp.Height <= 0 {
			return fmt.Errorf("invalid viewport dimensions: width=%d, height=%d (must be positive)", vp.Width, vp.Height)
		}
	}

	// Validate timeout
	if input.Timeout < 0 {
		return fmt.Errorf("invalid timeout: %d (must be non-negative)", input.Timeout)
	}

	return nil
}
