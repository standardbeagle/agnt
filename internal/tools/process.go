package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"

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
	// Hint surfaces a follow-up suggestion (e.g. start a proxy for browser
	// debugging when a web/dev server was launched). Empty when not applicable.
	Hint string `json:"hint,omitempty"`
}

// webServerRunHint returns a proxy/browser-debug nudge when a background run
// looks like it started a web/dev server, so the browser-debug flow is
// discoverable from `run` alone. Empty for non-server or non-background runs.
func webServerRunHint(input RunInput, command string) string {
	mode := input.Mode
	if mode == "" {
		mode = RunModeBackground
	}
	if mode != RunModeBackground {
		return ""
	}
	hay := strings.ToLower(input.ScriptName + " " + command)
	// Dev-server script names and common server runtimes/commands.
	needles := []string{
		"dev", "serve", "server", "preview", "start",
		"vite", "next", "nuxt", "webpack", "http-server", "live-server",
		"rails s", "manage.py runserver", "flask run", "uvicorn", "gunicorn",
		"dotnet watch", "dotnet run", "astro", "remix", "ng serve",
	}
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return "Looks like a web server. Start a proxy for browser debugging: " +
				"proxy {action:\"start\", id:\"dev\", target_url:\"http://localhost:<port>\"} " +
				"(or /agnt:setup-project to auto-start). See the agnt-process-proxy skill."
		}
	}
	return ""
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
	// Partial-failure notices from a multi-source collection (currently
	// snapshot). A snapshot assembled while one of its sources errored is
	// incomplete, and a raw consumer that only reads Snapshot would otherwise
	// read the gap as "nothing wrong". Mirrored into Output for text callers.
	Warnings []string `json:"warnings,omitempty"`
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
