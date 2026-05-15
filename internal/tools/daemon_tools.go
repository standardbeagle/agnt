package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"

	"github.com/standardbeagle/go-sdk/mcp"
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

	// Channel session management (agnt mcp in channel mode).
	//
	// Iter 18 race fix (Option B, single-owner ctx): the heartbeat goroutine
	// no longer reads a shared stop channel from DaemonTools. Instead,
	// RegisterChannelSession creates a child context and stores its
	// CancelFunc on the returned ChannelSessionHandle. The goroutine selects
	// only on its ticker and its own hbCtx.Done(); Close() cancels that ctx
	// from inside stopOnce.Do so the signal happens exactly once with no
	// shared-field read. channelSessionCode remains here because it is a
	// process-scoped identifier read by ChannelSessionCode(); that access
	// continues to go through sessionMu.
	channelSessionCode string // Session code for the channel session (empty if not registered)

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

// RunAutostart triggers a non-interactive autostart for the given project
// directory via the daemon. Used by the MCP InitializedHandler in channel
// mode. Returns the raw autostart result map from the daemon.
func (dt *DaemonTools) RunAutostart(projectDir string) (map[string]interface{}, error) {
	if err := dt.ensureConnected(); err != nil {
		return nil, err
	}
	return dt.client.Client().AutostartRun(projectDir)
}

// StartChannelSink starts a goroutine that subscribes to daemon StreamEvents
// and forwards matching entries as MCP channel notifications. Returns a cancel
// function to stop the sink. No-op and returns nil if channel is not enabled.
// projectPath scopes the subscription to proxies for that project directory;
// pass "" to receive events from all proxies (legacy behavior).
func (dt *DaemonTools) StartChannelSink(server *mcp.Server, cfg *config.ChannelConfig, projectPath string) context.CancelFunc {
	if cfg == nil || !cfg.IsEnabled() {
		return func() {}
	}

	ctx, cancel := context.WithCancel(context.Background())

	notify := func(ctx context.Context, method string, params any) error {
		for session := range server.Sessions() {
			_ = session.Notify(ctx, method, params)
		}
		return nil
	}

	sink := NewChannelSink(cfg, notify)

	// Build filter from config: include only event types the channel cares about,
	// and scope to the current project's proxies so two simultaneous MCP clients
	// don't cross-contaminate each other's event streams.
	filter := protocol.StreamEventFilter{
		ProjectPath: projectPath,
	}
	if events := cfg.GetEvents(); len(events) > 0 {
		filter.Types = events
	}

	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			// Create a raw client for streaming (dedicated socket per stream).
			client := daemon.NewClient(daemon.WithSocketPath(dt.config.SocketPath))
			err := client.StreamEvents(ctx, filter, func(entry proxy.LogEntry) error {
				sink.HandleEntry(ctx, entry)
				return nil
			})
			if err != nil {
				debug.Log("channel-sink", "stream ended: %v", err)
			}
			// Reconnect after brief pause unless cancelled.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()

	return cancel
}

// ChannelSessionHandle manages the lifecycle of a daemon session registered
// by agnt mcp in channel mode. Call Close to unregister the session and stop
// the heartbeat. The handle owns the heartbeat goroutine's cancel function
// so Close() signals the goroutine via context cancellation — no shared
// stop-channel field on DaemonTools, no Close-vs-Init race.
type ChannelSessionHandle struct {
	dt       *DaemonTools
	cancelHB context.CancelFunc // Cancels the heartbeat goroutine's context.
	stopOnce sync.Once
}

// Close unregisters the channel session from the daemon and stops the
// heartbeat goroutine. stopOnce guarantees the ctx cancel + daemon
// unregister pair fire exactly once, even under concurrent Close calls.
func (h *ChannelSessionHandle) Close() {
	if h == nil || h.dt == nil {
		return
	}
	h.stopOnce.Do(func() {
		if h.cancelHB != nil {
			h.cancelHB()
		}
		h.dt.closeChannelSession()
	})
}

// RegisterChannelSession registers a daemon session for the MCP process when
// running in channel mode. In channel mode there is no PTY child, so
// SessionPGID is 0 (pgid cleanup is a no-op; managed processes still clean up
// normally via the daemon's ProcessManager). Returns a handle that must be
// closed on shutdown to unregister the session and stop the heartbeat.
// Returns nil if channel mode is disabled.
func (dt *DaemonTools) RegisterChannelSession(ctx context.Context, cfg *config.ChannelConfig, projectPath string) *ChannelSessionHandle {
	if cfg == nil || !cfg.IsEnabled() {
		return nil
	}

	if err := dt.ensureConnected(); err != nil {
		fmt.Fprintf(os.Stderr, "[agnt] channel session: failed to connect to daemon: %v\n", err)
		return nil
	}

	// Generate a session code for this MCP instance.
	sessionCode := fmt.Sprintf("mcp-%d", time.Now().UnixNano()%10000)

	// Register with SessionPGID=0 (no PTY child in channel mode).
	// Overlay path is "-" because there is no overlay socket in channel mode.
	_, err := dt.client.SessionRegisterWithPGID(sessionCode, "-", projectPath, "mcp", nil, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[agnt] channel session: registration failed: %v\n", err)
		return nil
	}

	dt.sessionMu.Lock()
	dt.channelSessionCode = sessionCode
	dt.sessionMu.Unlock()

	// Child context owned by the returned handle. Close() cancels it;
	// the goroutine selects only on ticker + hbCtx.Done() so there is no
	// shared-state read from DaemonTools inside the hot loop.
	hbCtx, cancelHB := context.WithCancel(ctx)

	// Start heartbeat goroutine.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if dt.client != nil && dt.client.IsConnected() {
					_ = dt.client.SessionHeartbeat(sessionCode)
				}
			case <-hbCtx.Done():
				return
			}
		}
	}()

	return &ChannelSessionHandle{dt: dt, cancelHB: cancelHB}
}

// closeChannelSession unregisters the channel session from the daemon.
// The heartbeat goroutine is stopped by ChannelSessionHandle.Close via
// context cancellation before this runs, so there is no stop channel to
// touch here.
func (dt *DaemonTools) closeChannelSession() {
	dt.sessionMu.Lock()
	code := dt.channelSessionCode
	dt.channelSessionCode = ""
	dt.sessionMu.Unlock()

	if code == "" {
		return
	}

	// Unregister from daemon so CleanupSessionResources fires.
	if dt.client != nil && dt.client.IsConnected() {
		_ = dt.client.SessionUnregister(code)
	}
}

// ChannelSessionCode returns the session code for the active channel session,
// or empty string if none is registered.
func (dt *DaemonTools) ChannelSessionCode() string {
	dt.sessionMu.Lock()
	defer dt.sessionMu.Unlock()
	return dt.channelSessionCode
}

// RegisterDaemonTools adds all MCP tools that communicate with the daemon.
func RegisterDaemonTools(server *mcp.Server, dt *DaemonTools) {
	// Project tools
	addLenientTool(server, &mcp.Tool{
		Name: "detect",
		Description: `Detect project type and available scripts.
Returns ready-to-paste proc run / proc wait invocations and likely_signals
heuristics for each script, so agents can chain detect → proc run → proc wait
without hand-constructing commands.

Examples:
  detect {}
  detect {path: "."}
  detect {raw: true}   // skip compact text, return JSON only`,
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

Restarting: Use proc restart (preferred) or stop then run again:
  proc {action: "restart", process_id: "dev"}
Never use pkill or external commands - always use proc stop/restart for clean shutdown.

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
  restart: Restart a running process (stop then start with same config). For .agnt.kdl autostart scripts, process_id = script name.
  cleanup_port: Kill any process using a specific port
  autorestart: Enable/disable automatic restart when process exits
  scripts: List all registered scripts with state and start/fail counts
  script_output: Get script output history across restarts
  script_history: Get script state transition history
  snapshot: Unified dev-environment status (processes + proxies + recent errors + URLs + suggested next actions) in a single call. Use raw:true for structured JSON.
  run: Start a single admin-aware process; visible in proc list and SCRIPT LIST. Optionally gate on depends_on:[...] (default 30s timeout). Process state is "pending" while waiting; "starting" once deps clear; "failed" on dependency_timeout.
  run_group: Start a multi-process stack in one call. Cycle detection runs before any process launches; on cycle the call returns an error and no process is started. Each process may declare depends_on referencing other processes in the group; topo order is enforced via the readiness signaler.
  wait: Block until a named signal appears in a process's output. Polls (default 200ms, override with poll_ms) until match or timeout (default 30000ms). Returns {signal, matched_line, elapsed_ms} on hit, {timeout: true} on timeout — never errors. Pass signals:[...] to wait for whichever comes first.

Multi-stream output and signals:
  proc {action:"output", process_ids:["build","server","test"]}
  Pulls output from N processes in one call. Each line tagged "[id]" in compact output, NDJSON in raw mode.
  Add extract:["url","error","warning","ready","port"] to scan for structured signals — surfaced per-process under multi_stream[*].signals (or top-level signals when single process_id is used).
  Coordinates with proc snapshot: snapshot uses the get_errors classification pipeline (framework-aware), extract scans raw line buffers (regex, framework-agnostic).

Restarting dev servers:
  proc {action: "restart", process_id: "dev"}
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
  proc {action: "script_history", script_name: "dev"}
  proc {action: "snapshot"}                  # Compact dev environment summary
  proc {action: "snapshot", raw: true}       # Full JSON of processes, proxies, errors, urls, suggested_next
  proc {action: "output", process_ids: ["build","server"], extract: ["error","ready"]}  # Multi-stream + signals
  proc {action: "wait", process_id: "server", signal: "ready", timeout: 30000}          # Block until ready
  proc {action: "wait", process_id: "build", signals: ["ready", "error"], timeout: 60000}  # First wins
  proc {action: "run", id: "api", run: "go run ./cmd/api", depends_on: ["db"]}
  proc {action: "run_group", processes: [
    {id: "db",  run: "docker compose up db"},
    {id: "api", run: "go run ./cmd/api", depends_on: ["db"]},
    {id: "web", run: "npm run dev", depends_on: ["api"]}
  ]}`,
	}, dt.makeProcHandler())

	// Proxy tools
	addLenientTool(server, &mcp.Tool{
		Name:        "proxy",
		Description: ProxyToolDescription,
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
autostart failures (script start failures, port conflicts, proxy errors),
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

	// Watch tool - returns monitor command for Claude's Monitor tool
	addLenientTool(server, &mcp.Tool{
		Name: "watch",
		Description: `Get the shell command for streaming agnt events via Monitor.

Returns a command string that can be passed to Claude's Monitor tool.
Bridges the gap between the MCP client (which knows the daemon socket) and Monitor.

Targets:
  errors: Browser JS errors, HTTP errors, diagnostics
  interactions: Panel messages, clicks, keyboard, sketch events
  process: Process output for a specific process
  all: All agnt events (default)

Examples:
  watch {target: "errors", proxy_id: "dev"}
  watch {target: "interactions", proxy_id: "dev"}
  watch {target: "process", process_id: "app"}
  watch {target: "all"}
  watch {}`,
	}, dt.makeWatchHandler())

	// Session tool - register via separate function for organization
	RegisterSessionTool(server, dt)

	// Store tool - register via separate function for organization
	RegisterStoreTool(server, dt)

	// External error queue tool
	RegisterErrorQueueTool(server, dt)
}

// makeDetectHandler creates a handler for the detect tool.
//
// Daemon mode uses a hybrid path: the daemon roundtrip confirms project type
// and package manager (so the daemon's view stays authoritative for any
// daemon-side state), but the script enrichment (CommandDef → DetectScript
// with proc_run / proc_wait / likely_signals) is built locally from the same
// `project.Detect` call. This avoids a protocol bump while keeping the
// daemon-mode output identical to the legacy-mode output.
func (dt *DaemonTools) makeDetectHandler() func(context.Context, *mcp.CallToolRequest, DetectInput) (*mcp.CallToolResult, DetectOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input DetectInput) (*mcp.CallToolResult, DetectOutput, error) {
		// Empty output with non-nil Scripts to avoid null in JSON schema validation.
		emptyOutput := DetectOutput{Scripts: []DetectScript{}, ScriptNames: []string{}}

		if err := validateDetectInput(input); err != nil {
			return errorResult(validationError("detect", err)), emptyOutput, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), emptyOutput, nil
		}

		// Resolve path to absolute to ensure daemon uses correct directory.
		// Use session project path (from AGNT_PROJECT_PATH) when path is not specified.
		path := input.Path
		if path == "" {
			path = getProjectPath()
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to resolve path: %v", err)), emptyOutput, nil
		}

		// Daemon roundtrip — keeps the daemon authoritative for type/package_manager
		// and lets it surface any daemon-side errors (permission, missing path, ...).
		if _, err := dt.client.Detect(absPath); err != nil {
			return formatDaemonError(err, "detect"), emptyOutput, nil
		}

		// Re-detect locally to get the full CommandDef list for enrichment.
		// `project.Detect` is a pure filesystem read — same call the daemon
		// just made — so this stays consistent without a protocol bump.
		proj, err := project.Detect(absPath)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to detect: %v", err)), emptyOutput, nil
		}

		scripts := buildDetectScripts(proj.Commands)
		scriptNames := make([]string, len(proj.Commands))
		for i, cmd := range proj.Commands {
			scriptNames[i] = cmd.Name
		}

		output := DetectOutput{
			Type:           string(proj.Type),
			Name:           proj.Name,
			Framework:      proj.Metadata["framework"],
			Scripts:        scripts,
			ScriptNames:    scriptNames,
			PackageManager: proj.PackageManager,
			Metadata:       proj.Metadata,
		}

		if input.Raw {
			return nil, output, nil
		}

		summary := formatDetectCompact(output)
		output.Summary = summary
		return mcpText(summary), output, nil
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
