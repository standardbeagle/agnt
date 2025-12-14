package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"devtool-mcp/internal/snapshot"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SnapshotInput defines input for the snapshot tool
type SnapshotInput struct {
	Action        string                     `json:"action" jsonschema:"required,enum=baseline,enum=compare,enum=list,enum=delete,enum=get,description=Action to perform"`
	Name          string                     `json:"name,omitempty" jsonschema:"description=Baseline name (required for baseline/compare/delete/get)"`
	Baseline      string                     `json:"baseline,omitempty" jsonschema:"description=Baseline name to compare against (for compare action)"`
	Pages         []snapshot.PageCapture     `json:"pages,omitempty" jsonschema:"description=Pages to capture (array of {url viewport screenshot_data})"`
	DiffThreshold float64                    `json:"diff_threshold,omitempty" jsonschema:"description=Diff sensitivity threshold 0.0-1.0 (default: 0.01)"`
}

// SnapshotOutput defines output for the snapshot tool
type SnapshotOutput struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// RegisterSnapshotTools registers snapshot-related MCP tools
func RegisterSnapshotTools(server *mcp.Server, manager *snapshot.Manager) {
	// Closure to capture manager
	handler := func(ctx context.Context, req *mcp.CallToolRequest, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
		return handleSnapshot(manager, ctx, req, input)
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "snapshot",
		Description: `Capture and compare visual snapshots for regression testing.

Actions:
- baseline: Create a new baseline from screenshots
- compare: Compare current screenshots to a baseline
- list: List all available baselines
- delete: Delete a baseline
- get: Get details of a specific baseline

Example baseline:
  snapshot {action: "baseline", name: "before-refactor", pages: [{url: "/", viewport: {width: 1920, height: 1080}, screenshot_data: "base64..."}]}

Example compare:
  snapshot {action: "compare", baseline: "before-refactor", pages: [{url: "/", viewport: {width: 1920, height: 1080}, screenshot_data: "base64..."}]}`,
	}, handler)
}

func handleSnapshot(manager *snapshot.Manager, ctx context.Context, req *mcp.CallToolRequest, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
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
	default:
		return errorResult(fmt.Sprintf("Unknown action: %s. Valid actions: baseline, compare, list, delete, get", input.Action)), SnapshotOutput{}, nil
	}
}

func handleSnapshotBaseline(manager *snapshot.Manager, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	if input.Name == "" {
		return errorResult("Missing required parameter: name"), SnapshotOutput{}, nil
	}

	if len(input.Pages) == 0 {
		return errorResult("Missing or empty required parameter: pages"), SnapshotOutput{}, nil
	}

	// Create baseline
	baseline, err := manager.CreateBaseline(input.Name, input.Pages)
	if err != nil {
		return errorResult(fmt.Sprintf("Failed to create baseline: %v", err)), SnapshotOutput{}, nil
	}

	// Format response
	message := fmt.Sprintf("✓ Baseline '%s' created successfully\n\nPages captured: %d\nGit: %s @ %s",
		baseline.Name,
		len(baseline.Pages),
		baseline.GitBranch,
		baseline.GitCommit)

	return nil, SnapshotOutput{
		Success: true,
		Message: message,
		Data:    baseline,
	}, nil
}

func handleSnapshotCompare(manager *snapshot.Manager, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	baselineName := input.Baseline
	if baselineName == "" {
		return errorResult("Missing required parameter: baseline"), SnapshotOutput{}, nil
	}

	if len(input.Pages) == 0 {
		return errorResult("Missing or empty required parameter: pages"), SnapshotOutput{}, nil
	}

	// Compare to baseline
	result, err := manager.CompareToBaseline(baselineName, input.Pages)
	if err != nil {
		return errorResult(fmt.Sprintf("Failed to compare: %v", err)), SnapshotOutput{}, nil
	}

	// Format response
	message := formatCompareResult(result)

	return nil, SnapshotOutput{
		Success: !result.Summary.HasRegressions,
		Message: message,
		Data:    result,
	}, nil
}

func handleSnapshotList(manager *snapshot.Manager, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	baselines, err := manager.ListBaselines()
	if err != nil {
		return errorResult(fmt.Sprintf("Failed to list baselines: %v", err)), SnapshotOutput{}, nil
	}

	if len(baselines) == 0 {
		return nil, SnapshotOutput{
			Success: true,
			Message: "No baselines found",
			Data:    baselines,
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

	return nil, SnapshotOutput{
		Success: true,
		Message: message,
		Data:    baselines,
	}, nil
}

func handleSnapshotDelete(manager *snapshot.Manager, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	if input.Name == "" {
		return errorResult("Missing required parameter: name"), SnapshotOutput{}, nil
	}

	if err := manager.DeleteBaseline(input.Name); err != nil {
		return errorResult(fmt.Sprintf("Failed to delete baseline: %v", err)), SnapshotOutput{}, nil
	}

	return nil, SnapshotOutput{
		Success: true,
		Message: fmt.Sprintf("✓ Baseline '%s' deleted successfully", input.Name),
	}, nil
}

func handleSnapshotGet(manager *snapshot.Manager, input SnapshotInput) (*mcp.CallToolResult, SnapshotOutput, error) {
	if input.Name == "" {
		return errorResult("Missing required parameter: name"), SnapshotOutput{}, nil
	}

	baseline, err := manager.GetBaseline(input.Name)
	if err != nil {
		return errorResult(fmt.Sprintf("Failed to get baseline: %v", err)), SnapshotOutput{}, nil
	}

	data, _ := json.MarshalIndent(baseline, "", "  ")
	return nil, SnapshotOutput{
		Success: true,
		Message: string(data),
		Data:    baseline,
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
