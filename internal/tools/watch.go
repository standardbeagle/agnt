package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/standardbeagle/go-sdk/mcp"
)

// watchIDPattern constrains proxy/process identifiers to a safe charset.
// IDs flow into a shell command string emitted for `agnt monitor`; rejecting
// anything outside this set (in addition to POSIX-quoting every arg) keeps
// junk and shell metacharacters out of the emitted command. Comma is
// disallowed because it is the multi-target join separator.
var watchIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// validateWatchInput bounds and charset-checks the identifier fields.
func validateWatchInput(input WatchInput) error {
	if err := validateArrayLen("proxy_ids", input.ProxyIDs, maxArrayElements); err != nil {
		return err
	}
	if err := validateArrayLen("process_ids", input.ProcessIDs, maxArrayElements); err != nil {
		return err
	}

	ids := []struct{ field, val string }{
		{"proxy_id", input.ProxyID},
		{"process_id", input.ProcessID},
	}
	for i, v := range input.ProxyIDs {
		ids = append(ids, struct{ field, val string }{fmt.Sprintf("proxy_ids[%d]", i), v})
	}
	for i, v := range input.ProcessIDs {
		ids = append(ids, struct{ field, val string }{fmt.Sprintf("process_ids[%d]", i), v})
	}
	for _, id := range ids {
		if id.val == "" {
			continue
		}
		if err := validateStringLen(id.field, id.val, maxIDLength); err != nil {
			return err
		}
		if !watchIDPattern.MatchString(id.val) {
			return fmt.Errorf("%s contains invalid characters (allowed: letters, digits, . _ : -)", id.field)
		}
	}
	return nil
}

// shellQuoteArg renders a single argument safe for a POSIX shell. Args made
// only of safe characters are returned bare; anything else is wrapped in
// single quotes with embedded single quotes escaped as '\” so the emitted
// command is copy-pasteable without injection.
func shellQuoteArg(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			strings.ContainsRune("_/.:=,@%+-", r) {
			continue
		}
		safe = false
		break
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// WatchInput is the input for the watch tool.
type WatchInput struct {
	Target    string `json:"target,omitempty" jsonschema:"What to watch: errors, interactions, process, or all (default: all)"`
	ProxyID   string `json:"proxy_id,omitempty" jsonschema:"Proxy ID to filter (used with errors and interactions targets)"`
	ProcessID string `json:"process_id,omitempty" jsonschema:"Process ID to filter (required for process target unless process_ids is set)"`
	// Multi-target arrays. When set, these win over the singular variants
	// and are passed to `agnt monitor` as comma-joined --process / --proxy
	// values so a single monitor invocation covers the whole set.
	ProxyIDs   []string `json:"proxy_ids,omitempty" jsonschema:"Multiple proxy IDs to filter — emitted as comma-joined --proxy"`
	ProcessIDs []string `json:"process_ids,omitempty" jsonschema:"Multiple process IDs to filter — emitted as comma-joined --process"`
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
			if len(input.ProcessIDs) > 0 {
				return "Process output for " + strings.Join(input.ProcessIDs, ", ")
			}
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

	if config.needsProcess && input.ProcessID == "" && len(input.ProcessIDs) == 0 {
		return "", "", fmt.Errorf("process_id (or process_ids) is required for process target")
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

	// Multi-target arrays win over the singular variants. The singular
	// remains the back-compat path for callers that only ever watch one
	// process/proxy, and is silently dropped when arrays are present.
	switch {
	case len(input.ProxyIDs) > 0:
		args = append(args, "--proxy", strings.Join(input.ProxyIDs, ","))
	case input.ProxyID != "":
		args = append(args, "--proxy", input.ProxyID)
	}

	switch {
	case len(input.ProcessIDs) > 0:
		args = append(args, "--process", strings.Join(input.ProcessIDs, ","))
	case input.ProcessID != "":
		args = append(args, "--process", input.ProcessID)
	}

	args = append(args, "--format", "compact")

	// POSIX-quote every arg so paths with spaces (or any shell-special
	// characters) survive copy-paste into a shell without injection.
	quotedArgs := make([]string, len(args))
	for i, a := range args {
		quotedArgs[i] = shellQuoteArg(a)
	}

	command := strings.Join(quotedArgs, " ")
	description := config.description(input) + " — feed this command to the Monitor tool to stream events."

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

		if err := validateWatchInput(input); err != nil {
			return errorResult(validationError("watch", err)), emptyOutput, nil
		}

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
