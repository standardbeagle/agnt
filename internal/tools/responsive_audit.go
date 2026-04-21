package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"

	"github.com/standardbeagle/go-sdk/mcp"
)

// ResponsiveAuditInput defines input for the responsive_audit tool.
type ResponsiveAuditInput struct {
	ProxyID   string          `json:"proxy_id" jsonschema:"Proxy ID to run audit on"`
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

// responsiveAuditToolDescription is shared between legacy and daemon mode registrations.
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

// responsiveAuditBackend holds either a daemon client or a direct proxy manager,
// enabling a single registration path for both daemon and legacy modes.
type responsiveAuditBackend struct {
	DualBackend[DaemonTools, proxy.ProxyManager]
}

// RegisterResponsiveAuditTool registers the responsive_audit tool.
// Exactly one of dt or pm must be non-nil.
func RegisterResponsiveAuditTool(server *mcp.Server, dt *DaemonTools, pm *proxy.ProxyManager) {
	backend := &responsiveAuditBackend{DualBackend[DaemonTools, proxy.ProxyManager]{Daemon: dt, Legacy: pm}}
	addLenientTool(server, &mcp.Tool{
		Name:        "responsive_audit",
		Description: responsiveAuditToolDescription,
	}, backend.makeHandler())
}

// makeHandler creates a handler that dispatches to daemon or legacy path.
func (b *responsiveAuditBackend) makeHandler() func(context.Context, *mcp.CallToolRequest, ResponsiveAuditInput) (*mcp.CallToolResult, ResponsiveAuditOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ResponsiveAuditInput) (*mcp.CallToolResult, ResponsiveAuditOutput, error) {
		if err := validateResponsiveAuditInput(input); err != nil {
			return errorResult(err.Error()), ResponsiveAuditOutput{}, nil
		}

		if input.ProxyID == "" {
			return errorResult("proxy_id required"), ResponsiveAuditOutput{}, nil
		}

		type auditResult struct {
			toolResult *mcp.CallToolResult
			output     ResponsiveAuditOutput
			err        error
		}
		res, _ := DispatchResult(b.DualBackend,
			func(dt *DaemonTools) (auditResult, error) {
				if err := dt.ensureConnected(); err != nil {
					return auditResult{toolResult: errorResult(err.Error())}, nil
				}
				r, o, e := dt.executeResponsiveAuditDaemon(input)
				return auditResult{toolResult: r, output: o, err: e}, nil
			},
			func(pm *proxy.ProxyManager) (auditResult, error) {
				proxyServer, err := pm.Get(input.ProxyID)
				if err != nil {
					return auditResult{toolResult: errorResult(fmt.Sprintf("proxy not found: %s", input.ProxyID))}, nil
				}
				r, o, e := executeResponsiveAuditLegacy(proxyServer, input)
				return auditResult{toolResult: r, output: o, err: e}, nil
			},
		)
		return res.toolResult, res.output, res.err
	}
}

// executeResponsiveAuditDaemon runs the responsive audit using the daemon client.
func (dt *DaemonTools) executeResponsiveAuditDaemon(input ResponsiveAuditInput) (*mcp.CallToolResult, ResponsiveAuditOutput, error) {
	// Build the audit options for the browser
	auditOpts := buildAuditOptions(input)

	// Build JavaScript code to execute
	optsJSON, err := json.Marshal(auditOpts)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal options: %v", err)), ResponsiveAuditOutput{}, nil
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
	result, err := dt.client.ProxyExec(input.ProxyID, code)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to execute audit: %v", err)), ResponsiveAuditOutput{}, nil
	}

	// Check for error
	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		return errorResult(fmt.Sprintf("audit failed: %s", errMsg)), ResponsiveAuditOutput{}, nil
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

// executeResponsiveAuditLegacy runs the responsive audit in legacy mode.
func executeResponsiveAuditLegacy(proxyServer *proxy.ProxyServer, input ResponsiveAuditInput) (*mcp.CallToolResult, ResponsiveAuditOutput, error) {
	// Build the audit options for the browser
	auditOpts := buildAuditOptions(input)

	// Build JavaScript code to execute
	optsJSON, err := json.Marshal(auditOpts)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to marshal options: %v", err)), ResponsiveAuditOutput{}, nil
	}

	code := fmt.Sprintf(`(function() {
		var responsive = window.__devtool_responsive;
		if (!responsive || !responsive.audit) {
			return { error: 'Responsive audit module not loaded' };
		}
		return responsive.audit(%s);
	})()`, string(optsJSON))

	// Execute in browser
	execID, resultChan, err := proxyServer.ExecuteJavaScript(code)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to execute audit: %v", err)), ResponsiveAuditOutput{}, nil
	}

	// Wait for result with timeout (responsive audit can take 3-6 seconds)
	timeout := time.Duration(30) * time.Second
	select {
	case result := <-resultChan:
		if result == nil {
			return errorResult("execution channel closed without result"), ResponsiveAuditOutput{}, nil
		}

		if result.Error != "" {
			return errorResult(fmt.Sprintf("audit failed: %s", result.Error)), ResponsiveAuditOutput{}, nil
		}

		// Parse result
		output := ResponsiveAuditOutput{}

		if input.Raw {
			// Return raw JSON
			var rawResult any
			if err := json.Unmarshal([]byte(result.Result), &rawResult); err != nil {
				output.Summary = result.Result
			} else {
				output.Summary = result.Result
				output.Raw = rawResult
			}
		} else {
			// Result is already formatted as compact text
			output.Summary = result.Result
		}

		// Log the execution
		proxyServer.Logger().LogExecution(proxy.ExecutionResult{
			ID:        execID,
			Code:      "responsive_audit",
			Result:    result.Result,
			Duration:  result.Duration,
			Timestamp: result.Timestamp,
		})

		return nil, output, nil

	case <-time.After(timeout):
		return errorResult(fmt.Sprintf("audit timed out after %v", timeout)), ResponsiveAuditOutput{}, nil
	}
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
