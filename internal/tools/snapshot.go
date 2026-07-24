package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/standardbeagle/agnt/internal/snapshot"

	"github.com/standardbeagle/go-sdk/mcp"
)

// SnapshotInput defines input for the snapshot tool
type SnapshotInput struct {
	Action        string                 `json:"action" jsonschema:"Action: baseline, compare, list, delete, get, screenshot"`
	Name          string                 `json:"name,omitempty" jsonschema:"Baseline or screenshot name (required for baseline/compare/delete/get; optional for screenshot)"`
	Baseline      string                 `json:"baseline,omitempty" jsonschema:"Baseline name to compare against (for compare action)"`
	Pages         []snapshot.PageCapture `json:"pages,omitempty" jsonschema:"Pages to capture (array of {url viewport screenshot_path|screenshot_data}); screenshot_path bridges from the screenshot action"`
	DiffThreshold float64                `json:"diff_threshold,omitempty" jsonschema:"Diff sensitivity threshold 0.0-1.0 (default: 0.01)"`
	// screenshot action — captures the current page of a running proxy
	ProxyID  string `json:"proxy_id,omitempty" jsonschema:"For screenshot: proxy ID whose current page to capture (preferred)"`
	ID       string `json:"id,omitempty" jsonschema:"Alias for proxy_id (for screenshot action)"`
	Target   string `json:"target,omitempty" jsonschema:"For screenshot: frame in the always-wrap model: 'inner' (default) = active page content frame; 'outer' = chrome shell."`
	FrameID  string `json:"frame_id,omitempty" jsonschema:"For screenshot: capture a specific content frame by id (default: the active content frame)."`
	Selector string `json:"selector,omitempty" jsonschema:"For screenshot: CSS selector to capture (default: full viewport)"`
	FullPage bool   `json:"full_page,omitempty" jsonschema:"For screenshot: capture full scrollable page (default: viewport only)"`
}

// SnapshotOutput defines output for the snapshot tool
type SnapshotOutput struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

// RegisterSnapshotTools registers snapshot-related MCP tools. When dt is
// non-nil, the screenshot action is enabled and routed through the daemon's
// proxy exec path.
func RegisterSnapshotTools(server *mcp.Server, manager *snapshot.Manager, dt *DaemonTools) {
	handler := func(ctx context.Context, req *mcp.CallToolRequest, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
		return handleSnapshot(manager, dt, ctx, req, input)
	}

	addLenientTool(server, &mcp.Tool{
		Name: "snapshot",
		Description: `Capture and compare visual snapshots for regression testing.

Actions:
- screenshot: Capture the current page of a running proxy (writes PNG to audit storage; path retrieved via proxylog)
- baseline: Create a new baseline from screenshots
- compare: Compare current screenshots to a baseline
- list: List all available baselines
- delete: Delete a baseline
- get: Get details of a specific baseline

End-to-end flow (screenshot → baseline/compare):
  1. snapshot {action: "screenshot", proxy_id: "dev", name: "home"}
  2. proxylog {proxy_id: "dev", types: ["screenshot"], limit: 1}   // read file_path
  3. snapshot {action: "baseline", name: "v1", pages: [{url: "/", screenshot_path: "<file_path>"}]}
Each page accepts either screenshot_path (the file from step 2) or inline screenshot_data (base64).

Example screenshot:
  snapshot {action: "screenshot", proxy_id: "dev"}
  snapshot {action: "screenshot", proxy_id: "dev", name: "homepage", full_page: true}
  snapshot {action: "screenshot", proxy_id: "dev", selector: ".header"}

Example baseline:
  snapshot {action: "baseline", name: "before-refactor", pages: [{url: "/", viewport: {width: 1920, height: 1080}, screenshot_path: "/…/screenshot-home.png"}]}

Example compare (diff_threshold overrides sensitivity, default 0.01):
  snapshot {action: "compare", baseline: "before-refactor", diff_threshold: 0.02, pages: [{url: "/", screenshot_path: "/…/screenshot-home.png"}]}`,
	}, handler)
}

func handleSnapshot(manager *snapshot.Manager, dt *DaemonTools, ctx context.Context, req *mcp.CallToolRequest, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	input.ProxyID = pickProxyID(input.ID, input.ProxyID)
	if err := validateSnapshotInput(input); err != nil {
		return fail[SnapshotOutput](validationError("snapshot", err))
	}

	switch input.Action {
	case "baseline":
		return handleSnapshotBaseline(manager, input)
	case "compare":
		return handleSnapshotCompare(manager, input)
	case "list":
		return handleSnapshotList(manager, input)
	case "delete":
		return handleSnapshotDelete(manager, input)
	case "get":
		return handleSnapshotGet(manager, input)
	case "screenshot":
		return handleSnapshotScreenshot(dt, input)
	default:
		return fail[SnapshotOutput](fmt.Sprintf("Unknown action: %s. Valid actions: screenshot, baseline, compare, list, delete, get", input.Action))
	}
}

func handleSnapshotBaseline(manager *snapshot.Manager, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	if input.Name == "" {
		return fail[SnapshotOutput]("Missing required parameter: name")
	}

	if len(input.Pages) == 0 {
		return fail[SnapshotOutput]("Missing or empty required parameter: pages")
	}

	// Create baseline
	baseline, err := manager.CreateBaseline(input.Name, input.Pages)
	if err != nil {
		return fail[SnapshotOutput](fmt.Sprintf("Failed to create baseline: %v", err))
	}

	// Format response
	message := fmt.Sprintf("✓ Baseline '%s' created successfully\n\nPages captured: %d\nGit: %s @ %s",
		baseline.Name,
		len(baseline.Pages),
		baseline.GitBranch,
		baseline.GitCommit)

	dataJSON := ""
	if b, err := json.Marshal(baseline); err == nil {
		dataJSON = string(b)
	}

	return nil, SnapshotOutput{
		Success: true,
		Message: message,
		Data:    dataJSON,
	}, nil
}

func handleSnapshotCompare(manager *snapshot.Manager, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	baselineName := input.Baseline
	if baselineName == "" {
		return fail[SnapshotOutput]("Missing required parameter: baseline")
	}

	if len(input.Pages) == 0 {
		return fail[SnapshotOutput]("Missing or empty required parameter: pages")
	}

	// Compare to baseline (input.DiffThreshold <= 0 falls back to the default)
	result, err := manager.CompareToBaseline(baselineName, input.Pages, input.DiffThreshold)
	if err != nil {
		return fail[SnapshotOutput](fmt.Sprintf("Failed to compare: %v", err))
	}

	// Format response
	message := formatCompareResult(result)

	dataJSON := ""
	if b, err := json.Marshal(result); err == nil {
		dataJSON = string(b)
	}

	return nil, SnapshotOutput{
		Success: !result.Summary.HasRegressions,
		Message: message,
		Data:    dataJSON,
	}, nil
}

func handleSnapshotList(manager *snapshot.Manager, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	baselines, err := manager.ListBaselines()
	if err != nil {
		return fail[SnapshotOutput](fmt.Sprintf("Failed to list baselines: %v", err))
	}

	if len(baselines) == 0 {
		return nil, SnapshotOutput{
			Success: true,
			Message: "No baselines found",
			Data:    "[]",
		}, nil
	}

	// Format list
	message := fmt.Sprintf("Available baselines (%d):\n\n", len(baselines))
	for i, b := range baselines {
		gitInfo := ""
		if b.GitBranch != "" {
			gitInfo = fmt.Sprintf(" [%s @ %s]", b.GitBranch, b.GitCommit)
		}
		message += fmt.Sprintf("%d. %s - %d pages - %s%s\n",
			i+1,
			b.Name,
			len(b.Pages),
			b.Timestamp.Format("2006-01-02 15:04:05"),
			gitInfo)
	}

	dataJSON := ""
	if b, err := json.Marshal(baselines); err == nil {
		dataJSON = string(b)
	}

	return nil, SnapshotOutput{
		Success: true,
		Message: message,
		Data:    dataJSON,
	}, nil
}

func handleSnapshotDelete(manager *snapshot.Manager, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	if input.Name == "" {
		return fail[SnapshotOutput]("Missing required parameter: name")
	}

	if err := manager.DeleteBaseline(input.Name); err != nil {
		return fail[SnapshotOutput](fmt.Sprintf("Failed to delete baseline: %v", err))
	}

	return nil, SnapshotOutput{
		Success: true,
		Message: fmt.Sprintf("✓ Baseline '%s' deleted successfully", input.Name),
	}, nil
}

func handleSnapshotGet(manager *snapshot.Manager, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	if input.Name == "" {
		return fail[SnapshotOutput]("Missing required parameter: name")
	}

	baseline, err := manager.GetBaseline(input.Name)
	if err != nil {
		return fail[SnapshotOutput](fmt.Sprintf("Failed to get baseline: %v", err))
	}

	data, _ := json.MarshalIndent(baseline, "", "  ")
	dataJSON := ""
	if b, err := json.Marshal(baseline); err == nil {
		dataJSON = string(b)
	}

	return nil, SnapshotOutput{
		Success: true,
		Message: string(data),
		Data:    dataJSON,
	}, nil
}

// handleSnapshotScreenshot triggers a screenshot via the proxy's __devtool API.
// The browser overlay writes the PNG to the audit storage; the file path is
// returned in the next proxylog screenshot entry. This handler executes the
// JS, then polls proxylog briefly for the saved path.
func handleSnapshotScreenshot(dt *DaemonTools, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	if dt == nil {
		return fail[SnapshotOutput]("screenshot action requires daemon mode")
	}
	if input.ProxyID == "" {
		return fail[SnapshotOutput]("proxy_id required (or `id` alias)")
	}
	if err := dt.ensureConnected(); err != nil {
		return fail[SnapshotOutput](err.Error())
	}

	name := input.Name
	if name == "" {
		name = "screenshot"
	}

	// Build __devtool.screenshot(opts) invocation. Browser overlay drops the
	// PNG into audit storage and logs a `screenshot` entry with file_path.
	opts := map[string]interface{}{"name": name}
	if input.FullPage {
		opts["fullPage"] = true
	}
	if input.Selector != "" {
		opts["selector"] = input.Selector
	}
	optsJSON, _ := json.Marshal(opts)
	code := fmt.Sprintf("await __devtool.screenshot(%s)", optsJSON)

	execTarget, err := resolveExecTarget(input.Target, input.FrameID)
	if err != nil {
		return fail[SnapshotOutput](err.Error())
	}
	result, err := dt.client.ProxyExec(input.ProxyID, code, execTarget)
	if err != nil {
		return fail[SnapshotOutput](fmt.Sprintf("proxy exec failed: %v", err))
	}
	// ProxyExec reports browser-side JS failures via result["success"]=false +
	// result["error"] (transport err is nil in that case), so a transport-only
	// check would report a phantom success. Surface the JS error explicitly.
	if !getBool(result, "success") {
		return fail[SnapshotOutput](fmt.Sprintf("screenshot failed: %s", getString(result, "error")))
	}

	message := fmt.Sprintf("✓ Screenshot triggered on proxy %q (name=%q). "+
		"Retrieve file path with: proxylog {proxy_id: %q, types: [\"screenshot\"], limit: 1}",
		input.ProxyID, name, input.ProxyID)

	return nil, SnapshotOutput{
		Success: true,
		Message: message,
	}, nil
}

// Helper functions

func formatCompareResult(result *snapshot.CompareResult) string {
	message := fmt.Sprintf("Visual Regression Report: %s → current\n", result.BaselineName)
	message += "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n\n"

	// Summary
	if result.Summary.HasRegressions {
		message += fmt.Sprintf("⚠️  %d of %d pages changed (%.1f%% avg diff)\n\n",
			result.Summary.PagesChanged,
			result.Summary.TotalPages,
			result.Summary.AverageDiff)
	} else {
		message += fmt.Sprintf("✓ No visual changes detected (%d pages)\n\n", result.Summary.TotalPages)
	}

	// Page details
	for _, page := range result.Pages {
		icon := "✓"
		if page.HasChanges {
			icon = "❌"
		}

		message += fmt.Sprintf("%s %s\n", icon, page.URL)
		if page.HasChanges {
			message += fmt.Sprintf("   %s\n", page.Description)
			if page.DiffImagePath != "" {
				message += fmt.Sprintf("   Diff: %s\n", page.DiffImagePath)
			}
		}
		message += "\n"
	}

	message += "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"

	if result.Summary.HasRegressions {
		message += fmt.Sprintf("Summary: %d unexpected changes found\n", result.Summary.PagesChanged)
	} else {
		message += "Summary: All pages match baseline\n"
	}

	return message
}
