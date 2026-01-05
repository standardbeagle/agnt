package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/go-cli-server/process"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RunMode specifies how the run tool executes and returns results.
type RunMode string

const (
	// RunModeBackground starts process in background, returns process_id for tracking (default)
	RunModeBackground RunMode = "background"
	// RunModeForeground waits for completion, returns exit code (output via proc)
	RunModeForeground RunMode = "foreground"
	// RunModeForegroundRaw waits for completion, returns exit code and full output
	RunModeForegroundRaw RunMode = "foreground-raw"
)

// RunInput defines input for the run tool.
type RunInput struct {
	Path       string   `json:"path,omitempty" jsonschema:"Project directory (defaults to current dir)"`
	ScriptName string   `json:"script_name,omitempty" jsonschema:"Script name from detect (e.g. test, lint, build)"`
	Raw        bool     `json:"raw,omitempty" jsonschema:"Raw mode: use command and args directly"`
	Command    string   `json:"command,omitempty" jsonschema:"Raw mode: executable to run"`
	Args       []string `json:"args,omitempty" jsonschema:"Extra args (appended in script mode, used directly in raw mode)"`
	ID         string   `json:"id,omitempty" jsonschema:"Process ID (auto-generated if empty)"`
	Mode       RunMode  `json:"mode,omitempty" jsonschema:"Execution mode: background (default), foreground, foreground-raw"`
}

// RunOutput defines output for run.
type RunOutput struct {
	ProcessID string `json:"process_id"`
	PID       int    `json:"pid"`
	Command   string `json:"command"`
	// Foreground mode fields
	ExitCode int    `json:"exit_code,omitempty"`
	State    string `json:"state,omitempty"`
	Runtime  string `json:"runtime,omitempty"`
	// Foreground-raw mode fields
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
}

// ProcInput defines input for the proc tool.
type ProcInput struct {
	Action    string `json:"action" jsonschema:"Action: status, output, stop, list, cleanup_port"`
	ProcessID string `json:"process_id,omitempty" jsonschema:"Process ID (required for status/output/stop)"`
	// Output filters
	Stream string `json:"stream,omitempty" jsonschema:"stdout, stderr, or combined (default)"`
	Tail   int    `json:"tail,omitempty" jsonschema:"Last N lines only"`
	Head   int    `json:"head,omitempty" jsonschema:"First N lines only"`
	Grep   string `json:"grep,omitempty" jsonschema:"Filter lines matching regex pattern"`
	GrepV  bool   `json:"grep_v,omitempty" jsonschema:"Invert grep (exclude matching lines)"`
	// Stop options
	Force bool `json:"force,omitempty" jsonschema:"For stop: force kill immediately"`
	// Cleanup options
	Port int `json:"port,omitempty" jsonschema:"Port number (required for cleanup_port)"`
	// Directory filtering
	Global bool `json:"global,omitempty" jsonschema:"For list: include processes from all directories (default: false)"`
}

// ProcOutput defines output for proc.
type ProcOutput struct {
	// For status
	ProcessID string `json:"process_id,omitempty"`
	State     string `json:"state,omitempty"`
	Summary   string `json:"summary,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	Runtime   string `json:"runtime,omitempty"`
	// For output
	Output    string `json:"output,omitempty"`
	Lines     int    `json:"lines,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// For list
	Count       int         `json:"count,omitempty"`
	Processes   []ProcEntry `json:"processes,omitempty"`
	ProjectPath string      `json:"project_path,omitempty"`
	SessionCode string      `json:"session_code,omitempty"`
	Global      bool        `json:"global,omitempty"`
	// For stop
	Success bool `json:"success,omitempty"`
	// For cleanup_port
	KilledPIDs []int  `json:"killed_pids,omitempty"`
	Message    string `json:"message,omitempty"`
}

// ProcEntry is a process in the list.
type ProcEntry struct {
	ID          string `json:"id"`
	Command     string `json:"command"`
	State       string `json:"state"`
	Summary     string `json:"summary"`
	Runtime     string `json:"runtime"`
	ProjectPath string `json:"project_path,omitempty"`
}

// RegisterProcessTools adds process-related MCP tools to the server.
func RegisterProcessTools(server *mcp.Server, pm *process.ProcessManager) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "run",
		Description: `Run a project script or raw command.

Modes:
  background (default): Returns process_id immediately for tracking via proc tool
  foreground: Waits for completion, returns exit_code/state/runtime (output via proc)
  foreground-raw: Waits for completion, returns exit_code/state/runtime + stdout/stderr

Restarting: To restart a dev server, use proc stop first, then run again:
  proc {action: "stop", process_id: "dev"}
  run {script_name: "dev"}
Never use pkill or external commands - always use proc stop for clean shutdown.

Examples:
  run {script_name: "test"}
  run {script_name: "test", mode: "foreground"}
  run {script_name: "test", mode: "foreground-raw"}
  run {raw: true, command: "go", args: ["mod", "tidy"], mode: "foreground-raw"}`,
	}, makeRunHandler(pm))

	mcp.AddTool(server, &mcp.Tool{
		Name: "proc",
		Description: `Manage running processes.

Actions:
  list: List all running processes (use global: true for all directories)
  status: Get process status and info
  output: Get process output (tail/grep supported)
  stop: Gracefully stop a process (use force: true for immediate kill)
  cleanup_port: Kill any process using a specific port

Restarting dev servers: Always use stop action, never pkill or external commands.
  proc {action: "stop", process_id: "dev"}
  run {script_name: "dev"}

Examples:
  proc {action: "list"}
  proc {action: "status", process_id: "test"}
  proc {action: "output", process_id: "test", tail: 20}
  proc {action: "output", process_id: "test", grep: "FAIL"}
  proc {action: "stop", process_id: "test"}
  proc {action: "stop", process_id: "test", force: true}
  proc {action: "cleanup_port", port: 3000}`,
	}, makeProcHandler(pm))
}

func makeRunHandler(pm *process.ProcessManager) func(context.Context, *mcp.CallToolRequest, RunInput) (*mcp.CallToolResult, RunOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input RunInput) (*mcp.CallToolResult, RunOutput, error) {
		path := input.Path
		if path == "" {
			path = "."
		}

		var cmd string
		var args []string

		if input.Raw {
			// Raw mode: use command and args directly
			if input.Command == "" {
				return errorResult("raw mode requires command"), RunOutput{}, nil
			}
			cmd = input.Command
			args = input.Args
		} else {
			// Script mode: look up from project
			if input.ScriptName == "" {
				return errorResult("script_name required (or use raw=true with command)"), RunOutput{}, nil
			}

			proj, err := project.Detect(path)
			if err != nil {
				return errorResult(fmt.Sprintf("failed to detect project: %v", err)), RunOutput{}, nil
			}

			cmdDef := project.GetCommandByName(proj, input.ScriptName)
			if cmdDef == nil {
				available := project.GetCommandNames(proj)
				return errorResult(fmt.Sprintf("unknown script %q. Available: %s", input.ScriptName, strings.Join(available, ", "))), RunOutput{}, nil
			}

			cmd = cmdDef.Command
			args = append(cmdDef.Args, input.Args...)
		}

		// Generate ID if not provided
		id := input.ID
		if id == "" {
			if input.ScriptName != "" {
				id = input.ScriptName
			} else {
				id = fmt.Sprintf("proc-%d", time.Now().UnixNano()%100000)
			}
		}

		// Normalize mode (default to background)
		mode := input.Mode
		if mode == "" {
			mode = RunModeBackground
		}

		// Validate mode
		switch mode {
		case RunModeBackground, RunModeForeground, RunModeForegroundRaw:
			// Valid modes
		default:
			return errorResult(fmt.Sprintf("invalid mode %q. Use: background, foreground, foreground-raw", mode)), RunOutput{}, nil
		}

		proc, err := pm.StartCommand(ctx, process.ProcessConfig{
			ID:          id,
			ProjectPath: path,
			Command:     cmd,
			Args:        args,
		})
		if err != nil {
			return errorResult(fmt.Sprintf("failed to start: %v", err)), RunOutput{}, nil
		}

		cmdStr := cmd + " " + strings.Join(args, " ")

		// Background mode: return immediately
		if mode == RunModeBackground {
			return nil, RunOutput{
				ProcessID: proc.ID,
				PID:       proc.PID(),
				Command:   cmdStr,
			}, nil
		}

		// Foreground modes: wait for completion
		select {
		case <-proc.Done():
			// Process completed
		case <-ctx.Done():
			// Context cancelled, stop the process
			pm.StopProcess(ctx, proc)
			return errorResult(fmt.Sprintf("process cancelled: %v", ctx.Err())), RunOutput{}, nil
		}

		output := RunOutput{
			ProcessID: proc.ID,
			PID:       proc.PID(),
			Command:   cmdStr,
			ExitCode:  proc.ExitCode(),
			State:     proc.State().String(),
			Runtime:   formatDuration(proc.Runtime()),
		}

		// Foreground-raw mode: include stdout/stderr
		if mode == RunModeForegroundRaw {
			stdout, _ := proc.Stdout()
			stderr, _ := proc.Stderr()
			output.Stdout = string(stdout)
			output.Stderr = string(stderr)
		}

		return nil, output, nil
	}
}

func makeProcHandler(pm *process.ProcessManager) func(context.Context, *mcp.CallToolRequest, ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
		switch input.Action {
		case "status":
			return handleStatus(pm, input)
		case "output":
			return handleOutput(pm, input)
		case "stop":
			return handleStop(ctx, pm, input)
		case "list":
			return handleList(pm)
		case "cleanup_port":
			return handleCleanupPort(ctx, pm, input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q. Use: status, output, stop, list, cleanup_port", input.Action)), ProcOutput{}, nil
		}
	}
}

func handleStatus(pm *process.ProcessManager, input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for status"), ProcOutput{}, nil
	}

	proc, err := pm.Get(input.ProcessID)
	if err != nil {
		return errorResult(fmt.Sprintf("process not found: %s", input.ProcessID)), ProcOutput{}, nil
	}

	return nil, ProcOutput{
		ProcessID: proc.ID,
		State:     proc.State().String(),
		Summary:   proc.Summary(),
		ExitCode:  proc.ExitCode(),
		Runtime:   formatDuration(proc.Runtime()),
	}, nil
}

func handleOutput(pm *process.ProcessManager, input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for output"), ProcOutput{}, nil
	}

	proc, err := pm.Get(input.ProcessID)
	if err != nil {
		return errorResult(fmt.Sprintf("process not found: %s", input.ProcessID)), ProcOutput{}, nil
	}

	stream := input.Stream
	if stream == "" {
		stream = "combined"
	}

	var data []byte
	var truncated bool

	switch stream {
	case "stdout":
		data, truncated = proc.Stdout()
	case "stderr":
		data, truncated = proc.Stderr()
	case "combined":
		data, truncated = proc.CombinedOutput()
	default:
		return errorResult("stream must be stdout, stderr, or combined"), ProcOutput{}, nil
	}

	// Apply filters
	output := string(data)
	lines := strings.Split(output, "\n")

	// Grep filter
	if input.Grep != "" {
		re, err := regexp.Compile(input.Grep)
		if err != nil {
			return errorResult(fmt.Sprintf("invalid grep pattern: %v", err)), ProcOutput{}, nil
		}
		var filtered []string
		for _, line := range lines {
			matches := re.MatchString(line)
			if (matches && !input.GrepV) || (!matches && input.GrepV) {
				filtered = append(filtered, line)
			}
		}
		lines = filtered
		truncated = true // Indicate filtering applied
	}

	// Head filter (first N lines)
	if input.Head > 0 && len(lines) > input.Head {
		lines = lines[:input.Head]
		truncated = true
	}

	// Tail filter (last N lines)
	if input.Tail > 0 && len(lines) > input.Tail {
		lines = lines[len(lines)-input.Tail:]
		truncated = true
	}

	output = strings.Join(lines, "\n")

	// Count non-empty lines
	lineCount := 0
	for _, l := range lines {
		if l != "" {
			lineCount++
		}
	}

	return nil, ProcOutput{
		ProcessID: proc.ID,
		Output:    output,
		Lines:     lineCount,
		Truncated: truncated,
	}, nil
}

func handleStop(ctx context.Context, pm *process.ProcessManager, input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for stop"), ProcOutput{}, nil
	}

	proc, err := pm.Get(input.ProcessID)
	if err != nil {
		return errorResult(fmt.Sprintf("process not found: %s", input.ProcessID)), ProcOutput{}, nil
	}

	stopCtx := ctx
	if input.Force {
		var cancel context.CancelFunc
		stopCtx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
	}

	err = pm.StopProcess(stopCtx, proc)

	return nil, ProcOutput{
		ProcessID: proc.ID,
		State:     proc.State().String(),
		Success:   err == nil,
	}, nil
}

func handleList(pm *process.ProcessManager) (*mcp.CallToolResult, ProcOutput, error) {
	procs := pm.List()

	entries := make([]ProcEntry, len(procs))
	for i, p := range procs {
		entries[i] = ProcEntry{
			ID:      p.ID,
			Command: p.Command,
			State:   p.State().String(),
			Summary: p.Summary(),
			Runtime: formatDuration(p.Runtime()),
		}
	}

	return nil, ProcOutput{
		Count:     len(procs),
		Processes: entries,
	}, nil
}

func handleCleanupPort(ctx context.Context, pm *process.ProcessManager, input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.Port <= 0 || input.Port > 65535 {
		return errorResult("valid port number required (1-65535)"), ProcOutput{}, nil
	}

	pids, err := pm.KillProcessByPort(ctx, input.Port)
	if err != nil {
		return errorResult(fmt.Sprintf("failed to cleanup port %d: %v", input.Port, err)), ProcOutput{}, nil
	}

	var message string
	if len(pids) == 0 {
		message = fmt.Sprintf("No processes found listening on port %d", input.Port)
	} else {
		message = fmt.Sprintf("Killed %d process(es) on port %d", len(pids), input.Port)
	}

	return nil, ProcOutput{
		KilledPIDs: pids,
		Message:    message,
		Success:    true,
	}, nil
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: msg},
		},
		IsError: true,
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}
