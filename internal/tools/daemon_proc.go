package tools

import (
	"context"

	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/standardbeagle/agnt/internal/daemon"

	"os"
	"path/filepath"
	"strings"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/agnt/internal/protocol"

	"time"
)

// makeRunHandler creates a handler for the run tool.
func (dt *DaemonTools) makeRunHandler() func(context.Context, *mcp.CallToolRequest, RunInput) (*mcp.CallToolResult, RunOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input RunInput) (*mcp.CallToolResult, RunOutput, error) {
		if err := validateRunInput(input); err != nil {
			return errorResult(validationError("run", err)), RunOutput{}, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), RunOutput{}, nil
		}

		path := input.Path
		if path == "" {
			path = getProjectPath()
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to resolve path: %v", err)), RunOutput{}, nil
		}

		// Resolve script name to command if needed
		// The daemon's hub doesn't resolve script names, so we must do it here
		var cmd string
		var args []string
		var id string

		if input.Raw {

			if input.Command == "" {
				return errorResult("raw mode requires command"), RunOutput{}, nil
			}
			cmd = input.Command
			args = input.Args
			id = input.ID
			if id == "" {
				id = fmt.Sprintf("proc-%d", time.Now().UnixNano()%100000)
			}
		} else if input.ScriptName != "" {

			// First check .agnt.kdl for the script
			if resolvedCmd, resolvedArgs, err := resolveKDLScript(absPath, input.ScriptName, input.Args); err == nil {
				cmd = resolvedCmd
				args = resolvedArgs
				id = input.ID
				if id == "" {
					id = input.ScriptName
				}
			} else {
				// Fall back to project detection (package.json scripts, etc.)
				proj, detectErr := project.Detect(absPath)
				if detectErr != nil {
					return errorResult(fmt.Sprintf("failed to detect project: %v", detectErr)), RunOutput{}, nil
				}

				cmdDef := project.GetCommandByName(proj, input.ScriptName)
				if cmdDef == nil {
					available := project.GetCommandNames(proj)
					return errorResult(fmt.Sprintf("unknown script %q. Available: %s", input.ScriptName, strings.Join(available, ", "))), RunOutput{}, nil
				}

				cmd = cmdDef.Command
				args = append(cmdDef.Args, input.Args...)
				id = input.ID
				if id == "" {
					id = input.ScriptName
				}
			}
		} else {
			return errorResult("script_name required (or use raw=true with command)"), RunOutput{}, nil
		}

		mode := string(input.Mode)
		if mode == "" {
			mode = "background"
		}

		config := struct {
			protocol.RunConfig
			NoAutoRestart bool `json:"no_auto_restart,omitempty"`
		}{
			RunConfig: protocol.RunConfig{
				ID:      id,
				Path:    absPath,
				Raw:     true,
				Command: cmd,
				Args:    args,
				Mode:    mode,
				Env:     os.Environ(),
			},
			NoAutoRestart: input.NoAutoRestart,
		}

		result, err := dt.client.Run(config)
		if err != nil {
			return formatDaemonError(err, "run"), RunOutput{}, nil
		}

		processID := getString(result, "process_id")

		output := RunOutput{
			ProcessID: processID,
			PID:       getInt(result, "pid"),
			Command:   getString(result, "command"),
			ExitCode:  getInt(result, "exit_code"),
			State:     getString(result, "state"),
			Runtime:   getString(result, "runtime"),
			Stdout:    getString(result, "stdout"),
			Stderr:    getString(result, "stderr"),
		}

		return nil, output, nil
	}
}

// makeProcHandler creates a handler for the proc tool.
func (dt *DaemonTools) makeProcHandler() func(context.Context, *mcp.CallToolRequest, ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
		if err := validateProcInput(input); err != nil {
			return errorResult(validationError("proc", err)), ProcOutput{}, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), ProcOutput{}, nil
		}

		switch input.Action {
		case "status":
			return dt.handleProcStatus(input)
		case "output":
			return dt.handleProcOutput(input)
		case "stop":
			return dt.handleProcStop(input)
		case "restart":
			return dt.handleProcRestart(input)
		case "list":
			return dt.handleProcList(input)
		case "cleanup_port":
			return dt.handleProcCleanupPort(input)
		case "autorestart":
			return dt.handleProcAutoRestart(input)
		case "scripts":
			return dt.handleScriptList(input)
		case "script_output":
			return dt.handleScriptOutput(input)
		case "script_history":
			return dt.handleScriptHistory(input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q. Use: status, output, stop, restart, list, cleanup_port, autorestart, scripts, script_output, script_history", input.Action)), ProcOutput{}, nil
		}
	}
}

func (dt *DaemonTools) handleProcStatus(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for status"), ProcOutput{}, nil
	}

	result, err := dt.client.ProcStatus(input.ProcessID)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	return nil, ProcOutput{
		ProcessID: getString(result, "process_id"),
		State:     getString(result, "state"),
		Summary:   getString(result, "summary"),
		ExitCode:  getInt(result, "exit_code"),
		Runtime:   getString(result, "runtime"),
	}, nil
}

func (dt *DaemonTools) handleProcOutput(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for output"), ProcOutput{}, nil
	}

	filter := protocol.OutputFilter{
		Stream: input.Stream,
		Tail:   input.Tail,
		Head:   input.Head,
		Grep:   input.Grep,
		GrepV:  input.GrepV,
	}

	output, err := dt.client.ProcOutput(input.ProcessID, filter)
	if err != nil {

		projectPath := getProjectPath()
		if projectPath != "" {
			scriptResult, scriptErr := dt.client.ScriptOutput(input.ProcessID, projectPath, input.Tail)
			if scriptErr == nil {
				lines := getStringSlice(scriptResult, "lines")
				return nil, ProcOutput{
					ProcessID: input.ProcessID,
					Output:    strings.Join(lines, "\n"),
					Lines:     len(lines),
				}, nil
			}
		}
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	return nil, ProcOutput{
		ProcessID: input.ProcessID,
		Output:    output,
	}, nil
}

func (dt *DaemonTools) handleProcStop(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for stop"), ProcOutput{}, nil
	}

	result, err := dt.client.ProcStop(input.ProcessID, input.Force)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	return nil, ProcOutput{
		ProcessID: getString(result, "process_id"),
		State:     getString(result, "state"),
		Success:   getBool(result, "success"),
	}, nil
}

func (dt *DaemonTools) handleProcRestart(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for restart"), ProcOutput{}, nil
	}

	result, err := dt.client.ProcRestart(input.ProcessID)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	return nil, ProcOutput{
		ProcessID: getString(result, "process_id"),
		State:     getString(result, "state"),
		Success:   getBool(result, "success"),
		Message:   getString(result, "message"),
	}, nil
}

func (dt *DaemonTools) handleProcAutoRestart(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for autorestart"), ProcOutput{}, nil
	}

	action := "disable"
	if input.AutoRestartEnable {
		action = "enable"
	}

	// Build config if enabling
	var config *daemon.ProcAutoRestartConfig
	if action == "enable" {
		config = &daemon.ProcAutoRestartConfig{
			MaxRestarts: input.MaxRestarts,
			OnlyOnError: input.OnlyOnError,
		}
	}

	result, err := dt.client.ProcAutoRestart(input.ProcessID, action, config)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	return nil, ProcOutput{
		ProcessID: getString(result, "id"),
		Success:   getBool(result, "auto_restart") == input.AutoRestartEnable,
		Message:   getString(result, "message"),
	}, nil
}

func (dt *DaemonTools) handleProcList(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {

	dirFilter := protocol.DirectoryFilter{
		Global: input.Global,
	}

	if sessionCode := dt.SessionCode(); sessionCode != "" {
		dirFilter.SessionCode = sessionCode
	} else {

		projectPath := getProjectPath()
		if projectPath != "" {
			dirFilter.Directory = projectPath
		}
	}

	result, err := dt.client.ProcList(dirFilter)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	output := ProcOutput{
		Count:       getInt(result, "count"),
		ProjectPath: getString(result, "project_path"),
		SessionCode: getString(result, "session_code"),
		Global:      getBool(result, "global"),
	}

	if processes, ok := result["processes"].([]interface{}); ok {
		for _, p := range processes {
			if pm, ok := p.(map[string]interface{}); ok {
				id := getString(pm, "id")
				entry := ProcEntry{
					ID:          id,
					Command:     getString(pm, "command"),
					State:       getString(pm, "state"),
					Summary:     getString(pm, "summary"),
					Runtime:     getString(pm, "runtime"),
					ProjectPath: getString(pm, "project_path"),
				}

				if idx := strings.Index(id, ":"); idx >= 0 {
					entry.ScriptName = id[idx+1:]
				}
				output.Processes = append(output.Processes, entry)
			}
		}
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleProcCleanupPort(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.Port <= 0 || input.Port > 65535 {
		return errorResult("valid port number required (1-65535)"), ProcOutput{}, nil
	}

	result, err := dt.client.ProcCleanupPort(input.Port)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	output := ProcOutput{
		Success: getBool(result, "success"),
	}

	if pids, ok := result["killed_pids"].([]interface{}); ok {
		for _, pid := range pids {
			if p, ok := pid.(float64); ok {
				output.KilledPIDs = append(output.KilledPIDs, int(p))
			}
		}
	}

	if len(output.KilledPIDs) == 0 {
		output.Message = fmt.Sprintf("No processes found listening on port %d", input.Port)
	} else {
		output.Message = fmt.Sprintf("Killed %d process(es) on port %d", len(output.KilledPIDs), input.Port)
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleScriptList(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	projectPath := getProjectPath()
	if projectPath == "" {
		return errorResult("could not determine project path"), ProcOutput{}, nil
	}

	result, err := dt.client.ScriptList(projectPath)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	scripts, ok := result["scripts"].([]interface{})
	if !ok {
		return nil, ProcOutput{Count: 0}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== Scripts (%d) ===\n", len(scripts)))

	entries := make([]ProcEntry, 0, len(scripts))
	for _, s := range scripts {
		sm, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		name := getString(sm, "name")
		state := getString(sm, "state")
		startCount := getInt(sm, "start_count")
		failCount := getInt(sm, "fail_count")
		lastErr := getString(sm, "last_error")

		sb.WriteString(fmt.Sprintf("\n%s [%s] starts:%d fails:%d", name, state, startCount, failCount))
		if lastErr != "" {
			sb.WriteString(fmt.Sprintf("\n  last_error: %s", lastErr))
		}

		entries = append(entries, ProcEntry{
			ID:         getString(sm, "process_id"),
			State:      state,
			ScriptName: name,
		})
	}

	return nil, ProcOutput{
		Count:     len(entries),
		Processes: entries,
		Output:    sb.String(),
	}, nil
}

func (dt *DaemonTools) handleScriptOutput(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ScriptName == "" {
		return errorResult("script_name required for script_output"), ProcOutput{}, nil
	}

	projectPath := getProjectPath()
	if projectPath == "" {
		return errorResult("could not determine project path"), ProcOutput{}, nil
	}

	result, err := dt.client.ScriptOutput(input.ScriptName, projectPath, input.Tail)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	lines := getStringSlice(result, "lines")
	return nil, ProcOutput{
		Output: strings.Join(lines, "\n"),
		Lines:  len(lines),
	}, nil
}

func (dt *DaemonTools) handleScriptHistory(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ScriptName == "" {
		return errorResult("script_name required for script_history"), ProcOutput{}, nil
	}

	projectPath := getProjectPath()
	if projectPath == "" {
		return errorResult("could not determine project path"), ProcOutput{}, nil
	}

	result, err := dt.client.ScriptGet(input.ScriptName, projectPath)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	var sb strings.Builder
	name := getString(result, "name")
	state := getString(result, "state")
	sb.WriteString(fmt.Sprintf("=== %s [%s] ===\n", name, state))

	if history, ok := result["history"].([]interface{}); ok {
		sb.WriteString(fmt.Sprintf("\nTransitions (%d):\n", len(history)))
		for _, h := range history {
			if hm, ok := h.(map[string]interface{}); ok {
				sb.WriteString(fmt.Sprintf("  %s → %s\n", getString(hm, "timestamp"), getString(hm, "state")))
			}
		}
	}

	return nil, ProcOutput{
		Output: sb.String(),
	}, nil
}

func resolveKDLScript(projectPath, scriptName string, extraArgs []string) (string, []string, error) {
	agntCfg, err := config.LoadAgntConfig(projectPath)
	if err != nil || agntCfg == nil {
		return "", nil, fmt.Errorf("no .agnt.kdl config")
	}

	scriptCfg, ok := agntCfg.Scripts[scriptName]
	if !ok {
		return "", nil, fmt.Errorf("script %q not in .agnt.kdl", scriptName)
	}

	if scriptCfg.Run != "" {
		shell, shellArgs := scriptCfg.ResolveShell()
		return shell, append(shellArgs, extraArgs...), nil
	}

	if scriptCfg.Command != "" {
		return scriptCfg.Command, append(scriptCfg.Args, extraArgs...), nil
	}

	proj, err := project.Detect(projectPath)
	if err != nil {
		return "", nil, fmt.Errorf("no command/run and project detection failed: %w", err)
	}
	pm := proj.PackageManager
	if pm == "" {
		pm = "npm"
	}
	return pm, append([]string{"run", scriptName}, extraArgs...), nil
}
