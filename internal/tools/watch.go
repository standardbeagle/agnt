package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/standardbeagle/go-sdk/mcp"
)

// WatchInput is the input for the watch tool.
type WatchInput struct {
	Target    string `json:"target,omitempty" jsonschema:"What to watch: errors, interactions, process, or all (default: all)"`
	ProxyID   string `json:"proxy_id,omitempty" jsonschema:"Proxy ID to filter (used with errors and interactions targets)"`
	ProcessID string `json:"process_id,omitempty" jsonschema:"Process ID to filter (required for process target)"`
}

// WatchOutput is the output for the watch tool.
type WatchOutput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// watchTargetConfig maps target names to their monitor flags and descriptions.
type watchTargetConfig struct {
	types        string
	needsProxy   bool
	needsProcess bool
	description  func(WatchInput) string
}

var watchTargets = map[string]watchTargetConfig{
	"errors": {
		types:      "error,diagnostic",
		needsProxy: false,
		description: func(input WatchInput) string {
			if input.ProxyID != "" {
				return fmt.Sprintf("Errors on proxy %s", input.ProxyID)
			}
			return "All errors"
		},
	},
	"interactions": {
		types:      "panel_message,interaction,sketch",
		needsProxy: false,
		description: func(input WatchInput) string {
			if input.ProxyID != "" {
				return fmt.Sprintf("User interactions on proxy %s", input.ProxyID)
			}
			return "All user interactions"
		},
	},
	"process": {
		types:        "process",
		needsProcess: true,
		description: func(input WatchInput) string {
			return fmt.Sprintf("Process output for %s", input.ProcessID)
		},
	},
	"all": {
		types: "",
		description: func(input WatchInput) string {
			return "All agnt events"
		},
	},
}

// buildWatchCommand constructs the agnt monitor command string.
func buildWatchCommand(dt *DaemonTools, input WatchInput) (string, string, error) {
	target := input.Target
	if target == "" {
		target = "all"
	}

	config, ok := watchTargets[target]
	if !ok {
		return "", "", fmt.Errorf("invalid target %q: must be one of errors, interactions, process, all", target)
	}

	if config.needsProcess && input.ProcessID == "" {
		return "", "", fmt.Errorf("process_id is required for process target")
	}

	binaryPath, err := resolveAgntBinary()
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve agnt binary: %w", err)
	}

	var args []string
	args = append(args, binaryPath, "monitor")
	args = append(args, "--socket", dt.config.SocketPath)

	if config.types != "" {
		args = append(args, "--types", config.types)
	}

	if input.ProxyID != "" {
		args = append(args, "--proxy", input.ProxyID)
	}

	if input.ProcessID != "" {
		args = append(args, "--process", input.ProcessID)
	}

	args = append(args, "--format", "compact")

	// Shell-escape any args containing spaces
	quotedArgs := make([]string, len(args))
	for i, a := range args {
		if strings.Contains(a, " ") {
			quotedArgs[i] = fmt.Sprintf("'%s'", a)
		} else {
			quotedArgs[i] = a
		}
	}

	command := strings.Join(quotedArgs, " ")
	description := config.description(input)

	return command, description, nil
}

// resolveAgntBinary returns the path to the current agnt binary.
func resolveAgntBinary() (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Resolve symlinks
	resolved, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		return execPath, nil
	}
	return resolved, nil
}

// makeWatchHandler creates a handler for the watch tool.
func (dt *DaemonTools) makeWatchHandler() func(context.Context, *mcp.CallToolRequest, WatchInput) (*mcp.CallToolResult, WatchOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input WatchInput) (*mcp.CallToolResult, WatchOutput, error) {
		emptyOutput := WatchOutput{}

		command, description, err := buildWatchCommand(dt, input)
		if err != nil {
			return errorResult(err.Error()), emptyOutput, nil
		}

		return nil, WatchOutput{
			Command:     command,
			Description: description,
		}, nil
	}
}
