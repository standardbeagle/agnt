package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DaemonTools wraps a daemon client for MCP tool handlers.
type DaemonTools struct {
	client  *daemon.ResilientClient
	config  daemon.AutoStartConfig
	version string // Client version for validation

	// Session management
	sessionCode     string     // Attached session code (empty if not attached)
	sessionMu       sync.Mutex // Protects sessionCode
	noAutoAttach    bool       // If true, skip auto-attach on connect
	attachAttempted bool       // Whether we've attempted auto-attach

	// Alert delivery via MCP notifications
	alertSink daemon.MCPAlertSink
}

// NewDaemonTools creates a new daemon tools wrapper with auto-start and version checking.
// The version parameter should be the current binary version (e.g., "0.6.5").
func NewDaemonTools(config daemon.AutoStartConfig, version string) *DaemonTools {
	return &DaemonTools{
		config:  config,
		version: version,
	}
}

// SetNoAutoAttach disables automatic session attachment on connect.
// Call this before any tool calls if you want to operate globally.
func (dt *DaemonTools) SetNoAutoAttach(noAttach bool) {
	dt.sessionMu.Lock()
	defer dt.sessionMu.Unlock()
	dt.noAutoAttach = noAttach
}

// SetAlertSink sets the MCP alert sink for delivering process output alerts.
func (dt *DaemonTools) SetAlertSink(sink daemon.MCPAlertSink) {
	dt.sessionMu.Lock()
	defer dt.sessionMu.Unlock()
	dt.alertSink = sink
}

// AlertSink returns the current MCP alert sink, if any.
func (dt *DaemonTools) AlertSink() daemon.MCPAlertSink {
	dt.sessionMu.Lock()
	defer dt.sessionMu.Unlock()
	return dt.alertSink
}

// SetSessionCode sets the session code directly (useful for testing or explicit attachment).
func (dt *DaemonTools) SetSessionCode(code string) {
	dt.sessionMu.Lock()
	defer dt.sessionMu.Unlock()
	dt.sessionCode = code
}

// SessionCode returns the current attached session code.
func (dt *DaemonTools) SessionCode() string {
	dt.sessionMu.Lock()
	defer dt.sessionMu.Unlock()
	return dt.sessionCode
}

// tryAutoAttach attempts to attach to a session for the current directory.
// This is called once on first tool use. It's non-fatal if no session is found.
func (dt *DaemonTools) tryAutoAttach() {
	dt.sessionMu.Lock()
	if dt.attachAttempted || dt.noAutoAttach || dt.sessionCode != "" {
		dt.sessionMu.Unlock()
		return
	}
	dt.attachAttempted = true
	dt.sessionMu.Unlock()

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return // Silently fail - auto-attach is best-effort
	}

	// Try to attach via the daemon
	result, err := dt.client.SessionAttach(cwd)
	if err != nil {
		// No session found for this directory - that's OK
		return
	}

	// Successfully attached
	if code, ok := result["session_code"].(string); ok && code != "" {
		dt.sessionMu.Lock()
		dt.sessionCode = code
		dt.sessionMu.Unlock()

		// Log the attachment (to stderr so it doesn't interfere with MCP)
		if projectPath, ok := result["project_path"].(string); ok {
			fmt.Fprintf(os.Stderr, "[agnt] Attached to session %s (project: %s)\n", code, projectPath)
		}
	}
}

// ensureConnected ensures we have a connection to the daemon with automatic version checking and upgrade.
// It also attempts to auto-attach to a session on first connection.
func (dt *DaemonTools) ensureConnected() error {
	if dt.client != nil && dt.client.IsConnected() {
		// Already connected, but try auto-attach if not done yet
		dt.tryAutoAttach()
		return nil
	}

	// Create ResilientClient with version checking and auto-upgrade
	resilientConfig := daemon.DefaultResilientClientConfig()
	resilientConfig.AutoStartConfig = dt.config
	resilientConfig.ClientVersion = dt.version

	// Configure auto-upgrade callback for version mismatches
	resilientConfig.OnVersionMismatch = func(clientVer, daemonVer string) error {
		fmt.Fprintf(os.Stderr, "[agnt] Version mismatch detected: client=%s daemon=%s\n", clientVer, daemonVer)
		fmt.Fprintf(os.Stderr, "[agnt] Triggering automatic daemon upgrade...\n")

		// Create upgrader
		upgrader := daemon.NewDaemonUpgrader(daemon.UpgradeConfig{
			SocketPath:      dt.config.SocketPath,
			Timeout:         30 * time.Second,
			GracefulTimeout: 5 * time.Second,
			Verbose:         false, // Don't spam logs during auto-upgrade
		})

		// Run upgrade with timeout
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := upgrader.Upgrade(ctx); err != nil {
			return fmt.Errorf("auto-upgrade failed: %w", err)
		}

		fmt.Fprintf(os.Stderr, "[agnt] ✓ Daemon upgraded to %s\n", clientVer)
		return nil
	}

	// Create and connect ResilientClient
	client := daemon.NewResilientClient(resilientConfig)
	if err := client.Connect(); err != nil {
		return fmt.Errorf("failed to connect to daemon: %w", err)
	}

	dt.client = client

	// Try to auto-attach to a session for the current directory
	dt.tryAutoAttach()

	return nil
}

// Close closes the daemon client connection.
func (dt *DaemonTools) Close() error {
	if dt.client != nil {
		return dt.client.Close()
	}
	return nil
}

// RegisterDaemonTools adds all MCP tools that communicate with the daemon.
func RegisterDaemonTools(server *mcp.Server, dt *DaemonTools) {
	// Project tools
	addLenientTool(server, &mcp.Tool{
		Name: "detect",
		Description: `Detect project type and available scripts.
Example: detect {path: "."} → {type: "go", scripts: ["test", "build", "lint"]}`,
	}, dt.makeDetectHandler())

	// Process tools
	addLenientTool(server, &mcp.Tool{
		Name: "run",
		Description: `Run a project script or raw command.

Modes:
  background (default): Returns process_id immediately for tracking via proc tool
  foreground: Waits for completion, returns exit_code/state/runtime (output via proc)
  foreground-raw: Waits for completion, returns exit_code/state/runtime + stdout/stderr

Auto-restart: Background processes automatically restart on crash (rate-limited to
prevent loops: max 5 restarts/minute). Use no_auto_restart: true to disable.
  run {script_name: "dev"}                    # Auto-restarts by default
  run {script_name: "test", no_auto_restart: true}  # Disable for one-time tasks

Restarting: To restart a dev server, use proc stop first, then run again:
  proc {action: "stop", process_id: "dev"}
  run {script_name: "dev"}
Never use pkill or external commands - always use proc stop for clean shutdown.

Examples:
  run {script_name: "test"}
  run {script_name: "dev"}
  run {script_name: "test", mode: "foreground"}
  run {script_name: "test", mode: "foreground-raw"}
  run {raw: true, command: "go", args: ["mod", "tidy"], mode: "foreground-raw"}`,
	}, dt.makeRunHandler())

	addLenientTool(server, &mcp.Tool{
		Name: "proc",
		Description: `Manage running processes.

Actions:
  list: List all running processes (use global: true for all directories)
  status: Get process status and info
  output: Get process output (tail/grep supported, falls back to script output history)
  stop: Gracefully stop a process (use force: true for immediate kill)
  restart: Restart a running process (stop then start with same config)
  cleanup_port: Kill any process using a specific port
  autorestart: Enable/disable automatic restart when process exits
  scripts: List all registered scripts with state and start/fail counts
  script_output: Get script output history across restarts
  script_history: Get script state transition history

Restarting dev servers: Use restart action or stop then run again.
  proc {action: "restart", process_id: "dev"}
  # Or manually:
  proc {action: "stop", process_id: "dev"}
  run {script_name: "dev"}
Never use pkill or external commands - always use proc stop/restart for clean shutdown.

Auto-restart: Background processes automatically restart on crash by default (rate-limited
to max 5 restarts/minute to prevent loops). Use autorestart action to check or disable:
  proc {action: "autorestart", process_id: "dev"}  # Check status
  proc {action: "autorestart", process_id: "dev", auto_restart_enable: false}  # Disable

Examples:
  proc {action: "list"}
  proc {action: "status", process_id: "test"}
  proc {action: "output", process_id: "test", tail: 20}
  proc {action: "output", process_id: "test", grep: "FAIL"}
  proc {action: "stop", process_id: "test"}
  proc {action: "stop", process_id: "test", force: true}
  proc {action: "restart", process_id: "dev"}
  proc {action: "cleanup_port", port: 3000}
  proc {action: "autorestart", process_id: "dev", auto_restart_enable: false}
  proc {action: "scripts"}
  proc {action: "script_output", script_name: "dev", tail: 50}
  proc {action: "script_history", script_name: "dev"}`,
	}, dt.makeProcHandler())

	// Proxy tools
	addLenientTool(server, &mcp.Tool{
		Name: "proxy",
		Description: `Manage reverse proxy servers with traffic logging and frontend instrumentation.

Actions:
  start: Create and start a reverse proxy
  stop: Stop a running proxy
  restart: Restart a proxy (stop then start with same config)
  status: Get proxy status and statistics
  list: List all running proxies
  exec: Execute JavaScript in connected browser clients
  toast: Send toast notification to connected browsers

Examples:
  proxy {action: "start", id: "dev", target_url: "http://localhost:3000"}
  proxy {action: "status", id: "dev"}
  proxy {action: "list"}
  proxy {action: "exec", id: "dev", code: "document.title"}
  proxy {action: "toast", id: "dev", toast_message: "Build complete!", toast_type: "success"}
  proxy {action: "stop", id: "dev"}

The proxy automatically:
  - Assigns a stable port based on the target URL (same URL always gets same port)
  - Logs all HTTP traffic (requests/responses)
  - Injects JavaScript to capture frontend errors
  - Captures performance metrics (page load, resources)
  - Provides WebSocket endpoint for metrics
  - Injects __devtool API with 50+ diagnostic functions

Port selection:
  - Default: A stable port derived from target URL hash (range 10000-60000)
  - Only specify 'port' if you need a specific port number
  - The assigned port is returned in the response's 'listen_addr' field

Toast notifications:
  proxy {action: "toast", id: "dev", toast_message: "Task complete"}
  proxy {action: "toast", id: "dev", toast_type: "error", toast_title: "Build Failed", toast_message: "See console for details"}
  proxy {action: "toast", id: "dev", toast_type: "warning", toast_message: "Slow network detected", toast_duration: 8000}
  Toast types: success, error, warning, info (default)

__devtool API (injected into browser):
  proxy {action: "exec", help: true}                    # Full API overview
  proxy {action: "exec", describe: "screenshot"}        # Detailed function docs
  proxy {action: "exec", describe: "interactions.getLastClick"}

Common __devtool examples:
  proxy {action: "exec", id: "dev", code: "__devtool.screenshot('homepage')"}
  proxy {action: "exec", id: "dev", code: "__devtool.log('test', 'info', {data: 1})"}
  proxy {action: "exec", id: "dev", code: "__devtool.interactions.getLastClickContext()"}
  proxy {action: "exec", id: "dev", code: "__devtool.mutations.highlightRecent(5000)"}
  proxy {action: "exec", id: "dev", code: "__devtool.inspect('#submit-btn')"}
  proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility()"}

Each proxy has separate log storage and WebSocket connections.`,
	}, dt.makeProxyHandler())

	addLenientTool(server, &mcp.Tool{
		Name: "proxylog",
		Description: `Query and analyze proxy traffic logs.

Actions:
  query: Search logs with filters (default, may be large)
  summary: Get compact aggregated summary (recommended for large logs)
  clear: Clear all logs for a proxy
  stats: Get log statistics

Log Types:
  http: HTTP request/response pairs
  error: Frontend JavaScript errors with stack traces
  performance: Page load and resource timing metrics
  custom: Custom log messages from __devtool.log()
  screenshot: Screenshots captured via __devtool.screenshot()
  execution: Results of executed JavaScript code
  response: JavaScript execution responses
  interaction: User interactions (clicks, keyboard, scroll)
  mutation: DOM mutations (added, removed, modified elements)
  panel_message: Messages sent from the floating indicator panel
  sketch: Sketches/wireframes from sketch mode (includes JSON data and PNG image path)

Summary Action (Recommended for Large Logs):
  The summary action aggregates logs by type and provides:
  - Counts by type (errors, http, performance, etc.)
  - Deduplicated error summaries (top 10 unique errors)
  - HTTP status/method breakdown
  - Average performance metrics
  - Recent entries for each type (last 5)

  Progressive reveal with detail parameter:
  - detail: ["errors"] - include compact error list (truncated stacks)
  - detail: ["http"] - include HTTP request list
  - detail: ["performance"] - include performance metrics
  - detail: ["interactions"] - include user interactions
  - detail: ["mutations"] - include DOM mutations
  - detail: ["other"] - include custom/panel/sketch logs
  - limit: N - max items per detailed section (default: 10, max: 100)

  All data is automatically compacted to prevent token overflow:
  - Error stack traces limited to first 3 lines
  - Messages truncated to 500 chars max
  - URLs truncated to 100 chars
  - Individual stack lines capped at 120 chars

Query Examples:
  proxylog {proxy_id: "dev", types: ["http"], methods: ["GET"]}
  proxylog {proxy_id: "dev", types: ["error"], limit: 5}
  proxylog {proxy_id: "dev", since: "5m", limit: 50}

Summary Examples (Recommended):
  proxylog {proxy_id: "dev", action: "summary"}
  proxylog {proxy_id: "dev", action: "summary", detail: ["errors"]}
  proxylog {proxy_id: "dev", action: "summary", detail: ["errors", "http"], limit: 20}
  proxylog {proxy_id: "dev", action: "summary", types: ["error"]}

Other Actions:
  proxylog {proxy_id: "dev", action: "stats"}
  proxylog {proxy_id: "dev", action: "clear"}

Each proxy maintains its own separate log storage.`,
	}, dt.makeProxyLogHandler())

	addLenientTool(server, &mcp.Tool{
		Name: "currentpage",
		Description: `Get current page sessions with grouped resources and metrics.

Actions:
  list: List all active page sessions (default)
  get: Get detailed information for a specific session (may be large)
  summary: Get a compact summary optimized for long/complex pages (recommended)
  clear: Clear all page sessions

A page session groups together:
  - The initial HTML document request
  - All associated resource requests (JS, CSS, images, etc.)
  - Frontend JavaScript errors from that page
  - Performance metrics (page load time, paint timing, etc.)
  - User interactions (clicks, keyboard, scroll, etc.)
  - DOM mutations (added, removed, modified elements)

Examples:
  currentpage {proxy_id: "dev"}
  currentpage {proxy_id: "dev", action: "summary", session_id: "page-1"}
  currentpage {proxy_id: "dev", action: "summary", session_id: "page-1", detail: ["interactions"], limit: 20}
  currentpage {proxy_id: "dev", action: "summary", session_id: "page-1", detail: ["interactions", "mutations"]}
  currentpage {proxy_id: "dev", action: "get", session_id: "page-1"}
  currentpage {proxy_id: "dev", action: "clear"}

The list action returns summary counts (interaction_count, mutation_count).
The summary action returns aggregated data (errors by type, interactions by type,
  last 5 interactions/mutations) - best for long pages to avoid context overflow.
  Use detail parameter to get full data for specific sections:
  - detail: ["interactions"] - include full interaction list
  - detail: ["mutations"] - include full mutation list
  - detail: ["errors"] - include compact error list (truncated stacks/messages)
  - detail: ["resources"] - include full resource URL list

Error format is automatically compacted to prevent token overflow:
  - Stack traces limited to first 3 lines
  - Messages truncated to 500 chars max
  - Source paths reduced to filename only
  - Individual stack lines capped at 120 chars
  - limit: N - max items per detailed section (default: 5, max: 100)
The get action returns full interaction and mutation history (may be large).

This provides a high-level view of active pages and their resources.`,
	}, dt.makeCurrentPageHandler())

	// Error aggregation tool
	addLenientTool(server, &mcp.Tool{
		Name: "get_errors",
		Description: `Get all current errors across processes and proxies.

Collects errors from: process output (compile errors, panics, exceptions),
browser JavaScript errors, HTTP 4xx/5xx responses, and proxy transport errors.

Default behavior:
  - Deduplicates identical errors (shows count)
  - Reduces stack traces to first application code frame
  - Filters out noise (static asset 404s, redirects)
  - Sorts by severity (errors first) then recency

Examples:
  get_errors {}
  get_errors {proxy_id: "dev"}
  get_errors {process_id: "dev-server", since: "5m"}
  get_errors {include_warnings: false}
  get_errors {raw: true, limit: 50}`,
	}, dt.makeGetErrorsHandler())

	// Session tool - register via separate function for organization
	RegisterSessionTool(server, dt)

	// Store tool - register via separate function for organization
	RegisterStoreTool(server, dt)
}

// makeDetectHandler creates a handler for the detect tool.
func (dt *DaemonTools) makeDetectHandler() func(context.Context, *mcp.CallToolRequest, DetectInput) (*mcp.CallToolResult, DetectOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input DetectInput) (*mcp.CallToolResult, DetectOutput, error) {
		// Create empty output with initialized Scripts to avoid null in JSON schema validation
		emptyOutput := DetectOutput{Scripts: []string{}}

		if err := validateDetectInput(input); err != nil {
			return errorResult(validationError("detect", err)), emptyOutput, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), emptyOutput, nil
		}

		// Resolve path to absolute to ensure daemon uses correct directory
		// Use session project path (from AGNT_PROJECT_PATH) when path is not specified
		path := input.Path
		if path == "" {
			path = getProjectPath()
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to resolve path: %v", err)), emptyOutput, nil
		}

		result, err := dt.client.Detect(absPath)
		if err != nil {
			return formatDaemonError(err, "detect"), emptyOutput, nil
		}

		// Convert to output type
		output := DetectOutput{
			Type:    getString(result, "type"),
			Scripts: []string{}, // Initialize to empty slice to avoid null in JSON
		}

		if scripts, ok := result["scripts"].([]interface{}); ok {
			for _, s := range scripts {
				if str, ok := s.(string); ok {
					output.Scripts = append(output.Scripts, str)
				}
			}
		}

		if pm, ok := result["package_manager"].(string); ok {
			output.PackageManager = pm
		}

		return nil, output, nil
	}
}

// makeRunHandler creates a handler for the run tool.

func (o PageSessionOutput) MarshalJSON() ([]byte, error) {
	type Alias PageSessionOutput
	return json.Marshal(&struct {
		Alias
	}{
		Alias: Alias(o),
	})
}

// normalizeAddr replaces 0.0.0.0 or [::] with localhost for display.
