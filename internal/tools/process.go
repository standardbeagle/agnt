package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/go-cli-server/process"

	"github.com/standardbeagle/go-sdk/mcp"
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
	Path          string   `json:"path,omitempty" jsonschema:"Project directory (defaults to current dir)"`
	ScriptName    string   `json:"script_name,omitempty" jsonschema:"Script name from detect (e.g. test, lint, build)"`
	Raw           bool     `json:"raw,omitempty" jsonschema:"Raw mode: use command and args directly"`
	Command       string   `json:"command,omitempty" jsonschema:"Raw mode: executable to run"`
	Args          []string `json:"args,omitempty" jsonschema:"Extra args (appended in script mode, used directly in raw mode)"`
	ID            string   `json:"id,omitempty" jsonschema:"Process ID (auto-generated if empty)"`
	Mode          RunMode  `json:"mode,omitempty" jsonschema:"Execution mode: background (default), foreground, foreground-raw"`
	NoAutoRestart bool     `json:"no_auto_restart,omitempty" jsonschema:"Disable automatic restart (auto-restart is enabled by default for background processes)"`
	// Dependency-ordered start: hold the process in pending state until
	// each named dependency reaches a ready signal (URL detected or port
	// bound). On dependency timeout the process transitions to failed
	// with reason "dependency_timeout:<name>". Default per-dep timeout
	// is 30s; override via DependsOnTimeout (seconds).
	DependsOn        []string `json:"depends_on,omitempty" jsonschema:"Process IDs this process must wait on before launching"`
	DependsOnTimeout int      `json:"depends_on_timeout,omitempty" jsonschema:"Per-process dep-wait timeout in seconds (default 30, negative for unbounded)"`
}

// GroupProcessInput defines a single process inside a run_group action.
type GroupProcessInput struct {
	ID          string            `json:"id" jsonschema:"Process ID (required, unique within the group)"`
	Run         string            `json:"run,omitempty" jsonschema:"Shell command string (mutually exclusive with command)"`
	Command     string            `json:"command,omitempty" jsonschema:"Executable (mutually exclusive with run)"`
	Args        []string          `json:"args,omitempty" jsonschema:"Args appended to command"`
	Cwd         string            `json:"cwd,omitempty" jsonschema:"Working directory (defaults to project path)"`
	Env         map[string]string `json:"env,omitempty" jsonschema:"Extra env vars merged into the process environment"`
	URLMatchers []string          `json:"url_matchers,omitempty" jsonschema:"Custom URL detection patterns"`
	AutoRestart bool              `json:"auto_restart,omitempty" jsonschema:"Restart automatically on crash"`
	DependsOn   []string          `json:"depends_on,omitempty" jsonschema:"Process IDs this process must wait on before launching"`
}

// GroupProcessResult is the per-process outcome reported by a run_group.
type GroupProcessResult struct {
	ProcessID  string   `json:"process_id"`
	State      string   `json:"state"` // "starting", "pending", "failed"
	WaitingFor []string `json:"waiting_for,omitempty"`
	Error      string   `json:"error,omitempty"`
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
	Action    string `json:"action" jsonschema:"Action: status, output, find, stop, restart, list, cleanup_port, autorestart, scripts, script_output, script_history, run, run_group, snapshot, wait"`
	ProcessID string `json:"process_id,omitempty" jsonschema:"Process ID (preferred; required for status/output/stop/restart/autorestart/find/wait)"`
	// Multi-stream output: when set on action="output", the handler fans
	// out a per-process fetch and returns interleaved (or NDJSON) lines
	// tagged with process_id. Wins over the singular ProcessID.
	ProcessIDs []string `json:"process_ids,omitempty" jsonschema:"For output: list of process IDs to pull from in one call (interleaved output)"`
	// Extract: signal names to scan for in the output ("url","error",
	// "warning","ready","port"). Returned in MultiStream[*].Signals.
	Extract []string `json:"extract,omitempty" jsonschema:"For output: signal names to extract (url, error, warning, ready, port)"`
	// Wait action: signal name(s) to wait on. `Signal` is a single name
	// for back-compat; `Signals` is the multi-name "first wins" form.
	Signal    string   `json:"signal,omitempty" jsonschema:"For wait: signal name to wait on (ready, error, warning, url, port)"`
	Signals   []string `json:"signals,omitempty" jsonschema:"For wait: signal names to wait on — first match wins"`
	TimeoutMs int      `json:"timeout,omitempty" jsonschema:"For wait: max ms to wait (default 30000)"`
	PollMs    int      `json:"poll_ms,omitempty" jsonschema:"For wait: poll interval in ms (default 200)"`
	// Script actions
	ScriptName string `json:"script_name,omitempty" jsonschema:"Script name (required for script_output/script_history)"`
	// Output filters
	Stream string `json:"stream,omitempty" jsonschema:"stdout, stderr, or combined (default)"`
	Tail   int    `json:"tail,omitempty" jsonschema:"Last N lines only"`
	Head   int    `json:"head,omitempty" jsonschema:"First N lines only"`
	Grep   string `json:"grep,omitempty" jsonschema:"Filter lines matching regex pattern"`
	GrepV  bool   `json:"grep_v,omitempty" jsonschema:"Invert grep (exclude matching lines)"`
	// Find options
	What string `json:"what,omitempty" jsonschema:"For find: intent to search for (build-warnings, build-errors, test-failures, compile-errors, type-errors)"`
	// Stop options
	Force bool `json:"force,omitempty" jsonschema:"For stop: force kill immediately"`
	// Cleanup options
	Port int `json:"port,omitempty" jsonschema:"Port number (required for cleanup_port)"`
	// Directory filtering
	Global bool `json:"global,omitempty" jsonschema:"For list: include processes from all directories (default: false)"`
	// Auto-restart options
	AutoRestartEnable bool `json:"auto_restart_enable,omitempty" jsonschema:"For autorestart: enable (true) or disable (false)"`
	MaxRestarts       int  `json:"max_restarts,omitempty" jsonschema:"For autorestart: max restarts per minute (default: 5, 0=unlimited)"`
	OnlyOnError       bool `json:"only_on_error,omitempty" jsonschema:"For autorestart: only restart on non-zero exit code"`
	// Snapshot options
	Raw bool `json:"raw,omitempty" jsonschema:"For snapshot: return structured JSON only, skip the compact text rendering"`

	// run / run_group action fields. The `run` action launches a single
	// admin-aware process (visible in SCRIPT LIST) with optional
	// dependency gating; `run_group` launches multiple processes with
	// declared deps in topologically sorted order.
	ID               string              `json:"id,omitempty" jsonschema:"For run: process ID (required for run; defaults to script_name when omitted). For other actions: alias for process_id."`
	Run              string              `json:"run,omitempty" jsonschema:"For run: shell command string (mutually exclusive with command)"`
	Command          string              `json:"command,omitempty" jsonschema:"For run: executable (mutually exclusive with run)"`
	Args             []string            `json:"args,omitempty" jsonschema:"For run: command args"`
	Cwd              string              `json:"cwd,omitempty" jsonschema:"For run: working directory"`
	Env              map[string]string   `json:"env,omitempty" jsonschema:"For run: env vars merged into the process"`
	URLMatchers      []string            `json:"url_matchers,omitempty" jsonschema:"For run: custom URL detection patterns"`
	AutoRestart      bool                `json:"auto_restart,omitempty" jsonschema:"For run: restart on crash"`
	Path             string              `json:"path,omitempty" jsonschema:"For run/run_group: project directory (defaults to cwd)"`
	DependsOn        []string            `json:"depends_on,omitempty" jsonschema:"For run: process IDs this process must wait on before launching"`
	DependsOnTimeout int                 `json:"depends_on_timeout,omitempty" jsonschema:"For run/run_group: dep-wait timeout in seconds (default 30, negative for unbounded)"`
	Processes        []GroupProcessInput `json:"processes,omitempty" jsonschema:"For run_group: list of processes to launch with topo-sorted dep ordering"`
}

// ProcOutput defines output for proc.
type ProcOutput struct {
	// For status
	ProcessID string `json:"process_id,omitempty"`
	State     string `json:"state,omitempty"`
	Summary   string `json:"summary,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	Runtime   string `json:"runtime,omitempty"`
	// Role classification — what kind of process this is and what it produces.
	// Role values: "build-watch", "dev-server", "test-runner", "script", "unknown"
	// Produces values: "build-output", "test-results", "logs", "hot-reload"
	Role       string   `json:"role,omitempty"`
	Produces   []string `json:"produces,omitempty"`
	OutputHint string   `json:"output_hint,omitempty"` // advisory: how to query this process output
	// Last known death record — populated when the process has exited
	// (cleanly or via crash/signal) within the retention window. Lets the
	// agent tell "never started" from "started and died at T".
	LastExitAt     string `json:"last_exit_at,omitempty"`
	LastExitCode   *int   `json:"last_exit_code,omitempty"`
	LastExitReason string `json:"last_exit_reason,omitempty"` // "stopped" | "crash" | "signal"
	LastUptime     string `json:"last_uptime,omitempty"`
	LastStderrTail string `json:"last_stderr_tail,omitempty"`
	// For output
	Output    string `json:"output,omitempty"`
	Lines     int    `json:"lines,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	// For list
	Count       int         `json:"count"`
	Processes   []ProcEntry `json:"processes,omitempty"`
	ProjectPath string      `json:"project_path,omitempty"`
	SessionCode string      `json:"session_code,omitempty"`
	Global      bool        `json:"global,omitempty"`
	// For stop
	Success bool `json:"success,omitempty"`
	// For cleanup_port
	KilledPIDs []int  `json:"killed_pids,omitempty"`
	Message    string `json:"message,omitempty"`
	// For snapshot — unified dev-environment status. Populated only by
	// proc {action: "snapshot"}; nil for every other action.
	Snapshot *SnapshotData `json:"snapshot,omitempty"`
	// For multi-stream `proc output` (when process_ids is set) — per-process
	// rows with extracted signals. Each row holds the line slice for that
	// process and an optional signals payload (when `extract` was requested).
	// nil for the single-process variant of `proc output`.
	MultiStream []processStream `json:"multi_stream,omitempty"`
	// For single-stream `proc output` with extract — top-level signals
	// scanned from the fetched output. nil when extract not requested.
	Signals *SignalSet `json:"signals,omitempty"`
	// For `proc wait` — populated when action="wait". nil otherwise.
	Wait *WaitResult `json:"wait,omitempty"`
	// For run / run_group: dependency-gated launch state. WaitingFor lists
	// the unresolved dep names while the process is in pending state.
	WaitingFor    []string             `json:"waiting_for,omitempty"`
	GroupResults  []GroupProcessResult `json:"group_results,omitempty"`
	FailureReason string               `json:"failure_reason,omitempty"` // e.g. "dependency_timeout:db"
}

// ProcEntry is a process in the list.
type ProcEntry struct {
	ID          string `json:"id"`
	Command     string `json:"command"`
	State       string `json:"state"`
	Summary     string `json:"summary"`
	Runtime     string `json:"runtime"`
	ProjectPath string `json:"project_path,omitempty"`
	ScriptName  string `json:"script_name,omitempty"`
	// Role classification — what kind of process this is and what it produces.
	Role       string   `json:"role,omitempty"`
	Produces   []string `json:"produces,omitempty"`
	OutputHint string   `json:"output_hint,omitempty"`
	// Last known death record — see ProcOutput for field semantics.
	LastExitAt     string `json:"last_exit_at,omitempty"`
	LastExitCode   *int   `json:"last_exit_code,omitempty"`
	LastExitReason string `json:"last_exit_reason,omitempty"`
	LastUptime     string `json:"last_uptime,omitempty"`
	LastStderrTail string `json:"last_stderr_tail,omitempty"`
	// WaitingFor is populated for processes still gated on declared deps.
	WaitingFor []string `json:"waiting_for,omitempty"`
}

// RegisterProcessTools adds process-related MCP tools to the server.
func RegisterProcessTools(server *mcp.Server, pm *process.ProcessManager) {
	addLenientTool(server, &mcp.Tool{
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

	addLenientTool(server, &mcp.Tool{
		Name: "proc",
		Description: `Manage running processes.

Actions:
  list: List all running processes with role/produces classification
  status: Get process status, role, and what it produces
  output: Get process output (tail/grep supported)
  find: Intent-based search — extract build-warnings, build-errors, test-failures, etc.
  stop: Gracefully stop a process (use force: true for immediate kill)
  cleanup_port: Kill any process using a specific port

Build output discoverability:
  Running watch processes (dotnet watch, vite, webpack, tsc --watch) are queryable logs.
  Use proc {action:"find"} to extract structured output without knowing grep patterns:
    proc {action: "find", process_id: "backend", what: "build-warnings"}
    proc {action: "find", process_id: "frontend", what: "type-errors"}
  Use proc {action: "status"} to see the process role and what it produces.
  The "produces" field tells you what kind of output is available.

Restarting dev servers: Always use stop action, never pkill or external commands.
  proc {action: "stop", process_id: "dev"}
  run {script_name: "dev"}

Examples:
  proc {action: "list"}
  proc {action: "status", process_id: "backend"}
  proc {action: "output", process_id: "test", tail: 20}
  proc {action: "output", process_id: "test", grep: "FAIL"}
  proc {action: "find", process_id: "backend", what: "build-warnings"}
  proc {action: "find", process_id: "frontend", what: "compile-errors"}
  proc {action: "stop", process_id: "test"}
  proc {action: "cleanup_port", port: 3000}`,
	}, makeProcHandler(pm))
}

// ProcessRole describes the role of a running process.
type ProcessRole struct {
	Role       string   // "build-watch", "dev-server", "test-runner", "script", "unknown"
	Produces   []string // "build-output", "test-results", "logs", "hot-reload"
	OutputHint string   // advisory message for querying output
}

// classifyProcess inspects a command string to determine process role and output characteristics.
// This enables intent-based querying and build-output discoverability.
func classifyProcess(command string) ProcessRole {
	cmd := strings.ToLower(command)

	// dotnet watch — emits build output on every file change
	if matchesAny(cmd, "dotnet watch", "dotnet-watch") {
		return ProcessRole{
			Role:       "build-watch",
			Produces:   []string{"build-output", "logs"},
			OutputHint: `proc {action:"find", process_id:"<id>", what:"build-warnings"} or proc {action:"output", grep:"warning|error"}`,
		}
	}

	// TypeScript compiler watch
	if matchesAny(cmd, "tsc --watch", "tsc -w ", "tsc -w\"", "typescript") && strings.Contains(cmd, "watch") {
		return ProcessRole{
			Role:       "build-watch",
			Produces:   []string{"build-output", "type-errors"},
			OutputHint: `proc {action:"find", process_id:"<id>", what:"type-errors"} or proc {action:"output", grep:"error TS"}`,
		}
	}

	// Webpack watch / webpack-dev-server
	if matchesAny(cmd, "webpack", "webpack-dev-server", "webpack serve") {
		if strings.Contains(cmd, "watch") || strings.Contains(cmd, "serve") || strings.Contains(cmd, "dev-server") {
			return ProcessRole{
				Role:       "build-watch",
				Produces:   []string{"build-output", "hot-reload", "logs"},
				OutputHint: `proc {action:"find", process_id:"<id>", what:"build-errors"} or proc {action:"output", grep:"ERROR|WARNING"}`,
			}
		}
		return ProcessRole{
			Role:       "script",
			Produces:   []string{"build-output"},
			OutputHint: `proc {action:"output", process_id:"<id>"}`,
		}
	}

	// Vite dev server
	if matchesAny(cmd, "vite", "vite dev", "vite serve") {
		return ProcessRole{
			Role:       "dev-server",
			Produces:   []string{"build-output", "hot-reload", "logs"},
			OutputHint: `proc {action:"find", process_id:"<id>", what:"build-errors"} or proc {action:"output", grep:"error|warn"}`,
		}
	}

	// Go test watch (via gotestsum --watch or similar)
	if matchesAny(cmd, "gotestsum", "go test") && strings.Contains(cmd, "watch") {
		return ProcessRole{
			Role:       "test-runner",
			Produces:   []string{"test-results", "logs"},
			OutputHint: `proc {action:"find", process_id:"<id>", what:"test-failures"} or proc {action:"output", grep:"FAIL|PASS"}`,
		}
	}

	// Cargo watch (Rust)
	if matchesAny(cmd, "cargo watch", "cargo-watch") {
		return ProcessRole{
			Role:       "build-watch",
			Produces:   []string{"build-output", "logs"},
			OutputHint: `proc {action:"find", process_id:"<id>", what:"build-errors"} or proc {action:"output", grep:"error|warning"}`,
		}
	}

	// Generic dev server patterns
	if matchesAny(cmd, "next dev", "nuxt dev", "remix dev", "astro dev", "svelte-kit dev") {
		return ProcessRole{
			Role:       "dev-server",
			Produces:   []string{"build-output", "hot-reload", "logs"},
			OutputHint: `proc {action:"find", process_id:"<id>", what:"build-errors"} or proc {action:"output", grep:"error|warn"}`,
		}
	}

	// Generic watch patterns
	if strings.Contains(cmd, " watch") || strings.Contains(cmd, "--watch") || strings.Contains(cmd, "-w ") {
		return ProcessRole{
			Role:       "build-watch",
			Produces:   []string{"build-output", "logs"},
			OutputHint: `proc {action:"find", process_id:"<id>", what:"build-warnings"} or proc {action:"output", grep:"warning|error"}`,
		}
	}

	return ProcessRole{
		Role:       "unknown",
		Produces:   []string{"logs"},
		OutputHint: `proc {action:"output", process_id:"<id>"}`,
	}
}

// matchesAny returns true if s contains any of the given substrings.
func matchesAny(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// buildWhatPattern returns a grep pattern for a given intent string.
// Intent values: "build-warnings", "build-errors", "test-failures", "compile-errors", "type-errors"
func buildWhatPattern(what, command string) (string, error) {
	cmd := strings.ToLower(command)
	isDotnet := matchesAny(cmd, "dotnet", "msbuild")
	isTypeScript := matchesAny(cmd, "tsc", "typescript")
	isRust := matchesAny(cmd, "cargo", "rustc")

	switch what {
	case "build-warnings":
		if isDotnet {
			return `(?i)\bwarning\b`, nil
		}
		return `(?i)\bwarn(ing)?\b`, nil

	case "build-errors", "compile-errors":
		if isDotnet {
			return `(?i)\berror\b`, nil
		}
		if isRust {
			// Rust emits both `error[E0382]: ...` (coded) and `error: ...`
			// (uncoded). Match the leading `error` followed by `[` or `:`.
			return `^error(\[|:)`, nil
		}
		return `(?i)\berror\b`, nil

	case "type-errors":
		if isTypeScript {
			return `error TS\d+`, nil
		}
		if isDotnet {
			return `(?i)\berror\b`, nil
		}
		return `(?i)type.?error|TS\d+`, nil

	case "test-failures":
		return `(?i)\b(FAIL|FAILED|FAILURE|✗|✕|× )\b`, nil

	default:
		return "", fmt.Errorf("unknown what %q: use build-warnings, build-errors, compile-errors, type-errors, test-failures", what)
	}
}

func makeRunHandler(pm *process.ProcessManager) func(context.Context, *mcp.CallToolRequest, RunInput) (*mcp.CallToolResult, RunOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input RunInput) (*mcp.CallToolResult, RunOutput, error) {
		if err := validateRunInput(input); err != nil {
			return errorResult(validationError("run", err)), RunOutput{}, nil
		}

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
			// Script mode: check .agnt.kdl first, then project detection
			if input.ScriptName == "" {
				return errorResult("script_name required (or use raw=true with command)"), RunOutput{}, nil
			}

			if resolvedCmd, resolvedArgs, err := resolveKDLScript(path, input.ScriptName, input.Args); err == nil {
				cmd = resolvedCmd
				args = resolvedArgs
			} else {
				proj, detectErr := project.Detect(path)
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
			}
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
		input.ProcessID = pickProcessID(input.ID, input.ProcessID)
		if err := validateProcInput(input); err != nil {
			return errorResult(validationError("proc", err)), ProcOutput{}, nil
		}

		switch input.Action {
		case "status":
			return handleStatus(pm, input)
		case "output":
			return handleOutput(pm, input)
		case "find":
			return handleFind(pm, input)
		case "stop":
			return handleStop(ctx, pm, input)
		case "list":
			return handleList(pm)
		case "cleanup_port":
			return handleCleanupPort(ctx, pm, input)
		case "snapshot", "scripts", "script_output", "script_history", "run", "run_group", "wait":
			return errorResult(fmt.Sprintf("%s action requires daemon mode", input.Action)), ProcOutput{}, nil
		default:
			return errorResult(fmt.Sprintf("unknown action %q. Use: status, output, find, stop, list, cleanup_port, snapshot, run, run_group, wait", input.Action)), ProcOutput{}, nil
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

	role := classifyProcess(proc.Command + " " + strings.Join(proc.Args, " "))
	hint := strings.ReplaceAll(role.OutputHint, "<id>", proc.ID)

	return nil, ProcOutput{
		ProcessID:  proc.ID,
		State:      proc.State().String(),
		Summary:    proc.Summary(),
		ExitCode:   proc.ExitCode(),
		Runtime:    formatDuration(proc.Runtime()),
		Role:       role.Role,
		Produces:   role.Produces,
		OutputHint: hint,
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

func handleFind(pm *process.ProcessManager, input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for find"), ProcOutput{}, nil
	}
	if input.What == "" {
		return errorResult("what required for find (build-warnings, build-errors, compile-errors, type-errors, test-failures)"), ProcOutput{}, nil
	}

	proc, err := pm.Get(input.ProcessID)
	if err != nil {
		return errorResult(fmt.Sprintf("process not found: %s", input.ProcessID)), ProcOutput{}, nil
	}

	cmdFull := proc.Command + " " + strings.Join(proc.Args, " ")
	pattern, err := buildWhatPattern(input.What, cmdFull)
	if err != nil {
		return errorResult(err.Error()), ProcOutput{}, nil
	}

	data, truncated := proc.CombinedOutput()
	lines := strings.Split(string(data), "\n")

	re, err := regexp.Compile(pattern)
	if err != nil {
		return errorResult(fmt.Sprintf("internal: bad pattern for %q: %v", input.What, err)), ProcOutput{}, nil
	}

	var matched []string
	for _, line := range lines {
		if re.MatchString(line) {
			matched = append(matched, line)
		}
	}

	output := strings.Join(matched, "\n")
	lineCount := len(matched)

	return nil, ProcOutput{
		ProcessID: proc.ID,
		Output:    output,
		Lines:     lineCount,
		Truncated: truncated,
		Message:   fmt.Sprintf("found %d lines matching %q in process %s", lineCount, input.What, input.ProcessID),
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
		role := classifyProcess(p.Command + " " + strings.Join(p.Args, " "))
		hint := strings.ReplaceAll(role.OutputHint, "<id>", p.ID)
		entries[i] = ProcEntry{
			ID:         p.ID,
			Command:    p.Command,
			State:      p.State().String(),
			Summary:    p.Summary(),
			Runtime:    formatDuration(p.Runtime()),
			Role:       role.Role,
			Produces:   role.Produces,
			OutputHint: hint,
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
	debug.Log("tools", "error: %s", msg)
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
