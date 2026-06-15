package tools

import (
	"context"

	"github.com/standardbeagle/agnt/internal/license"
	"github.com/standardbeagle/agnt/internal/replaytest"
	"github.com/standardbeagle/go-sdk/mcp"
)

// ReplaytestInput drives the replaytest MCP tool. Action selects the operation;
// the remaining fields supply per-action parameters.
type ReplaytestInput struct {
	Action        string `json:"action" jsonschema:"Operation to perform: record, stop, refine, replay, explore, list, or show."`
	Name          string `json:"name,omitempty" jsonschema:"Scenario name to record, show, refine, or replay."`
	ProxyID       string `json:"proxy_id,omitempty" jsonschema:"Proxy id the scenario records or replays against."`
	Preset        string `json:"preset,omitempty" jsonschema:"Optional preset that tunes recording or exploration behavior."`
	ExploreAgents int    `json:"explore_agents,omitempty" jsonschema:"Number of exploration agents to run for the explore action."`
	Directory     string `json:"directory,omitempty" jsonschema:"Project directory whose scenario store to use. Defaults to the caller's session project."`
	Global        bool   `json:"global,omitempty" jsonschema:"Return cross-project results instead of scoping to the caller's session project."`
}

// ReplaytestOutput is the structured result of a replaytest operation.
type ReplaytestOutput struct {
	Scenarios []string `json:"scenarios,omitempty"`
	Report    string   `json:"report,omitempty"`
	Message   string   `json:"message,omitempty"`
	Success   bool     `json:"success"`
}

// replaytestHandler holds the dependencies for the replaytest MCP tool. Gated
// actions consult the license manager before doing any work.
type replaytestHandler struct {
	lic *license.Manager
}

func newReplaytestHandler(lic *license.Manager) *replaytestHandler {
	return &replaytestHandler{lic: lic}
}

var replaytestGatedActions = map[string]bool{
	"record":  true,
	"stop":    true,
	"refine":  true,
	"replay":  true,
	"explore": true,
}

var replaytestFreeActions = map[string]bool{
	"list": true,
	"show": true,
}

func (h *replaytestHandler) handle(ctx context.Context, in ReplaytestInput) (*mcp.CallToolResult, ReplaytestOutput, error) {
	if !replaytestGatedActions[in.Action] && !replaytestFreeActions[in.Action] {
		return errorResult("unknown action: " + in.Action), ReplaytestOutput{}, nil
	}

	if replaytestGatedActions[in.Action] {
		if _, err := h.lic.Check(license.CapAdvancedTesting); err != nil {
			return errorResult("advanced_testing requires a Pro license — run `agnt activate <key>` to enable replaytest"), ReplaytestOutput{}, nil
		}
		// Action bodies wired in Task 12; stubbed for now.
		out := ReplaytestOutput{Message: in.Action + " not yet implemented", Success: false}
		return replaytestOK(out.Message), out, nil
	}

	switch in.Action {
	case "list":
		names, err := replaytest.NewStore(in.Directory).List()
		if err != nil {
			return errorResult("failed to list scenarios: " + err.Error()), ReplaytestOutput{}, nil
		}
		out := ReplaytestOutput{Scenarios: names, Success: true}
		return replaytestOK("listed scenarios"), out, nil
	case "show":
		if in.Name == "" {
			out := ReplaytestOutput{Message: "provide a scenario name to show", Success: false}
			return replaytestOK(out.Message), out, nil
		}
		sc, err := replaytest.NewStore(in.Directory).LoadScenario(in.Name)
		if err != nil {
			return errorResult("failed to load scenario: " + err.Error()), ReplaytestOutput{}, nil
		}
		data, err := sc.MarshalJSON()
		if err != nil {
			return errorResult("failed to encode scenario: " + err.Error()), ReplaytestOutput{}, nil
		}
		out := ReplaytestOutput{Report: string(data), Success: true}
		return replaytestOK("loaded scenario " + in.Name), out, nil
	}

	return errorResult("unknown action: " + in.Action), ReplaytestOutput{}, nil
}

func replaytestOK(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}
