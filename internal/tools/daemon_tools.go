package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"

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
	mcp.AddTool(server, &mcp.Tool{
		Name: "detect",
		Description: `Detect project type and available scripts.
Example: detect {path: "."} → {type: "go", scripts: ["test", "build", "lint"]}`,
	}, dt.makeDetectHandler())

	// Process tools
	mcp.AddTool(server, &mcp.Tool{
		Name: "run",
		Description: `Run a project script or raw command.

Modes:
  background (default): Returns process_id immediately for tracking via proc tool
  foreground: Waits for completion, returns exit_code/state/runtime (output via proc)
  foreground-raw: Waits for completion, returns exit_code/state/runtime + stdout/stderr

Examples:
  run {script_name: "test"}
  run {script_name: "test", mode: "foreground"}
  run {script_name: "test", mode: "foreground-raw"}
  run {raw: true, command: "go", args: ["mod", "tidy"], mode: "foreground-raw"}`,
	}, dt.makeRunHandler())

	mcp.AddTool(server, &mcp.Tool{
		Name: "proc",
		Description: `Manage running processes.
Examples:
  proc {action: "list"}
  proc {action: "status", process_id: "test"}
  proc {action: "output", process_id: "test", tail: 20}
  proc {action: "output", process_id: "test", grep: "FAIL"}
  proc {action: "stop", process_id: "test"}
  proc {action: "cleanup_port", port: 3000}`,
	}, dt.makeProcHandler())

	// Proxy tools
	mcp.AddTool(server, &mcp.Tool{
		Name: "proxy",
		Description: `Manage reverse proxy servers with traffic logging and frontend instrumentation.

Actions:
  start: Create and start a reverse proxy
  stop: Stop a running proxy
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

	mcp.AddTool(server, &mcp.Tool{
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

	mcp.AddTool(server, &mcp.Tool{
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

	// Session tool - register via separate function for organization
	RegisterSessionTool(server, dt)
}

// makeDetectHandler creates a handler for the detect tool.
func (dt *DaemonTools) makeDetectHandler() func(context.Context, *mcp.CallToolRequest, DetectInput) (*mcp.CallToolResult, DetectOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input DetectInput) (*mcp.CallToolResult, DetectOutput, error) {
		// Create empty output with initialized Scripts to avoid null in JSON schema validation
		emptyOutput := DetectOutput{Scripts: []string{}}

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
func (dt *DaemonTools) makeRunHandler() func(context.Context, *mcp.CallToolRequest, RunInput) (*mcp.CallToolResult, RunOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input RunInput) (*mcp.CallToolResult, RunOutput, error) {
		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), RunOutput{}, nil
		}

		// Resolve path to absolute to ensure daemon uses correct directory
		// Use session project path (from AGNT_PROJECT_PATH) when path is not specified
		path := input.Path
		if path == "" {
			path = getProjectPath()
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to resolve path: %v", err)), RunOutput{}, nil
		}

		// Build daemon protocol config
		// Pass client's environment to daemon so spawned processes use correct PATH, etc.
		config := protocol.RunConfig{
			ID:         input.ID,
			Path:       absPath,
			ScriptName: input.ScriptName,
			Raw:        input.Raw,
			Command:    input.Command,
			Args:       input.Args,
			Mode:       string(input.Mode),
			Env:        os.Environ(),
		}

		if config.Mode == "" {
			config.Mode = "background"
		}

		result, err := dt.client.Run(config)
		if err != nil {
			return formatDaemonError(err, "run"), RunOutput{}, nil
		}

		// Convert to output type
		output := RunOutput{
			ProcessID: getString(result, "process_id"),
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
		case "list":
			return dt.handleProcList(input)
		case "cleanup_port":
			return dt.handleProcCleanupPort(input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q", input.Action)), ProcOutput{}, nil
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

func (dt *DaemonTools) handleProcList(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	// Create directory filter with session code if attached
	dirFilter := protocol.DirectoryFilter{
		Global: input.Global,
	}

	// Use session code if attached, otherwise fall back to project path
	if sessionCode := dt.SessionCode(); sessionCode != "" {
		dirFilter.SessionCode = sessionCode
	} else {
		// Legacy fallback: use project path from environment or cwd
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
				output.Processes = append(output.Processes, ProcEntry{
					ID:          getString(pm, "id"),
					Command:     getString(pm, "command"),
					State:       getString(pm, "state"),
					Summary:     getString(pm, "summary"),
					Runtime:     getString(pm, "runtime"),
					ProjectPath: getString(pm, "project_path"),
				})
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

// makeProxyHandler creates a handler for the proxy tool.
func (dt *DaemonTools) makeProxyHandler() func(context.Context, *mcp.CallToolRequest, ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), ProxyOutput{}, nil
		}

		switch input.Action {
		case "start":
			return dt.handleProxyStart(input)
		case "stop":
			return dt.handleProxyStop(input)
		case "status":
			return dt.handleProxyStatus(input)
		case "list":
			return dt.handleProxyList(input)
		case "exec":
			return dt.handleProxyExec(input)
		case "toast":
			return dt.handleProxyToast(input)
		case "chaos":
			return dt.handleProxyChaos(input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q", input.Action)), ProxyOutput{}, nil
		}
	}
}

func (dt *DaemonTools) handleProxyStart(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return errorResult("id required for start"), ProxyOutput{}, nil
	}
	if input.TargetURL == "" {
		return errorResult("target_url required for start"), ProxyOutput{}, nil
	}

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get working directory: %v", err)), ProxyOutput{}, nil
	}

	// Use -1 to signal "use default" (hash-based port), 0 means auto-assign
	port := input.Port
	if port == 0 {
		port = -1 // Trigger hash-based default in daemon
	}

	// Build config with all options
	config := daemon.ProxyStartConfig{
		Path:        cwd,
		BindAddress: input.BindAddress,
		PublicURL:   input.PublicURL,
		VerifyTLS:   input.VerifyTLS,
	}

	// Configure tunnel if specified
	if input.Tunnel != "" {
		config.Tunnel = &protocol.TunnelConfig{
			Provider:  input.Tunnel,
			Command:   input.TunnelCommand,
			Args:      input.TunnelArgs,
			AuthToken: input.TunnelToken,
			Region:    input.TunnelRegion,
		}
	}

	result, err := dt.client.ProxyStartWithConfig(input.ID, input.TargetURL, port, input.MaxLogSize, config)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	listenAddr := getString(result, "listen_addr")
	bindAddress := getString(result, "bind_address")
	publicURL := getString(result, "public_url")
	tunnelURL := getString(result, "tunnel_url")

	// Build access message based on configuration
	accessURL := "http://localhost" + listenAddr
	if tunnelURL != "" {
		accessURL = tunnelURL
	} else if publicURL != "" {
		accessURL = publicURL
	} else if bindAddress == "0.0.0.0" {
		accessURL = fmt.Sprintf("http://<your-ip>%s", listenAddr)
	}

	return nil, ProxyOutput{
		ID:          getString(result, "id"),
		TargetURL:   getString(result, "target_url"),
		ListenAddr:  listenAddr,
		BindAddress: bindAddress,
		PublicURL:   publicURL,
		TunnelURL:   tunnelURL,
		Message:     fmt.Sprintf("Proxy started. Access at %s", accessURL),
	}, nil
}

func (dt *DaemonTools) handleProxyStop(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return errorResult("id required for stop"), ProxyOutput{}, nil
	}

	err := dt.client.ProxyStop(input.ID)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	return nil, ProxyOutput{
		Success: true,
		Message: fmt.Sprintf("Proxy %s stopped", input.ID),
	}, nil
}

func (dt *DaemonTools) handleProxyStatus(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return errorResult("id required for status"), ProxyOutput{}, nil
	}

	result, err := dt.client.ProxyStatus(input.ID)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	output := ProxyOutput{
		ID:            getString(result, "id"),
		TargetURL:     getString(result, "target_url"),
		ListenAddr:    getString(result, "listen_addr"),
		BindAddress:   getString(result, "bind_address"),
		PublicURL:     getString(result, "public_url"),
		Running:       getBool(result, "running"),
		Uptime:        getString(result, "uptime"),
		TotalRequests: getInt64(result, "total_requests"),
	}

	if logStats, ok := result["log_stats"].(map[string]interface{}); ok {
		output.LogStats = &LogStatsOutput{
			TotalEntries:     getInt64(logStats, "total_entries"),
			AvailableEntries: getInt64(logStats, "available_entries"),
			MaxSize:          getInt64(logStats, "max_size"),
			Dropped:          getInt64(logStats, "dropped"),
		}
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleProxyList(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	// Create directory filter with session code if attached
	dirFilter := protocol.DirectoryFilter{
		Global: input.Global,
	}

	// Use session code if attached, otherwise fall back to project path
	if sessionCode := dt.SessionCode(); sessionCode != "" {
		dirFilter.SessionCode = sessionCode
	} else {
		// Legacy fallback: use project path from environment or cwd
		projectPath := getProjectPath()
		if projectPath != "" {
			dirFilter.Directory = projectPath
		}
	}

	result, err := dt.client.ProxyList(dirFilter)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	output := ProxyOutput{
		Count:       getInt(result, "count"),
		ProjectPath: getString(result, "project_path"),
		SessionCode: getString(result, "session_code"),
		Global:      getBool(result, "global"),
	}

	if proxies, ok := result["proxies"].([]interface{}); ok {
		for _, p := range proxies {
			if pm, ok := p.(map[string]interface{}); ok {
				output.Proxies = append(output.Proxies, ProxyEntry{
					ID:            getString(pm, "id"),
					TargetURL:     getString(pm, "target_url"),
					ListenAddr:    getString(pm, "listen_addr"),
					BindAddress:   getString(pm, "bind_address"),
					PublicURL:     getString(pm, "public_url"),
					Path:          getString(pm, "path"),
					Running:       getBool(pm, "running"),
					Uptime:        getString(pm, "uptime"),
					TotalRequests: getInt64(pm, "total_requests"),
				})
			}
		}
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleProxyExec(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	// Handle help request - no proxy ID required
	if input.Help {
		return nil, ProxyOutput{
			Success: true,
			Message: GetAPIOverview(),
		}, nil
	}

	// Handle describe request - no proxy ID required
	if input.Describe != "" {
		doc, found := GetFunctionDescription(input.Describe)
		if !found {
			// List available functions
			names := ListFunctionNames()
			return nil, ProxyOutput{
				Success: false,
				Message: fmt.Sprintf("Function %q not found.\n\nAvailable functions:\n%v", input.Describe, names),
			}, nil
		}
		return nil, ProxyOutput{
			Success: true,
			Message: doc,
		}, nil
	}

	if input.ID == "" {
		return errorResult("id required for exec"), ProxyOutput{}, nil
	}
	if input.Code == "" {
		return errorResult("code required for exec"), ProxyOutput{}, nil
	}

	result, err := dt.client.ProxyExec(input.ID, input.Code)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	success := getBool(result, "success")
	execID := getString(result, "execution_id")

	if !success {
		errorMsg := getString(result, "error")
		return nil, ProxyOutput{
			Success:     false,
			ExecutionID: execID,
			Message:     fmt.Sprintf("JavaScript execution failed: %s", errorMsg),
		}, nil
	}

	resultVal := getString(result, "result")
	duration := getString(result, "duration")

	return nil, ProxyOutput{
		Success:     true,
		ExecutionID: execID,
		Message:     fmt.Sprintf("JavaScript executed successfully.\nResult: %s\nDuration: %s", resultVal, duration),
	}, nil
}

func (dt *DaemonTools) handleProxyToast(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return errorResult("id required for toast"), ProxyOutput{}, nil
	}
	if input.ToastMessage == "" {
		return errorResult("toast_message required for toast"), ProxyOutput{}, nil
	}

	// Build toast config
	toastConfig := protocol.ToastConfig{
		Type:     input.ToastType,
		Title:    input.ToastTitle,
		Message:  input.ToastMessage,
		Duration: input.ToastDuration,
	}

	// Default type to "info" if not specified
	if toastConfig.Type == "" {
		toastConfig.Type = "info"
	}

	result, err := dt.client.ProxyToast(input.ID, toastConfig)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	sentCount := getInt(result, "sent_count")

	return nil, ProxyOutput{
		Success: getBool(result, "success"),
		Message: fmt.Sprintf("Toast sent to %d connected client(s)", sentCount),
	}, nil
}

// parseChaosStats extracts ChaosStatsOutput from a map result.
func parseChaosStats(stats map[string]interface{}) *ChaosStatsOutput {
	output := &ChaosStatsOutput{
		TotalRequests:   getInt64(stats, "total_requests"),
		AffectedCount:   getInt64(stats, "affected_count"),
		LatencyInjected: getInt64(stats, "latency_injected_ms"),
		ErrorsInjected:  getInt64(stats, "errors_injected"),
		DropsInjected:   getInt64(stats, "drops_injected"),
		TruncatedCount:  getInt64(stats, "truncated_count"),
		ReorderedCount:  getInt64(stats, "reordered_count"),
	}
	if ruleStats, ok := stats["rule_stats"].(map[string]interface{}); ok {
		output.RuleStats = make(map[string]int64)
		for k, v := range ruleStats {
			if n, ok := v.(float64); ok {
				output.RuleStats[k] = int64(n)
			}
		}
	}
	return output
}

// parseChaosRules extracts a slice of ChaosRuleOutput from a rules interface.
func parseChaosRules(rules []interface{}) []ChaosRuleOutput {
	var result []ChaosRuleOutput
	for _, r := range rules {
		if rm, ok := r.(map[string]interface{}); ok {
			result = append(result, ChaosRuleOutput{
				ID:           getString(rm, "id"),
				Name:         getString(rm, "name"),
				Type:         getString(rm, "type"),
				Enabled:      getBool(rm, "enabled"),
				URLPattern:   getString(rm, "url_pattern"),
				Probability:  getFloat64(rm, "probability"),
				TimesApplied: getInt64(rm, "times_applied"),
			})
		}
	}
	return result
}

// inputRuleToProtocol converts a ChaosRuleInput to protocol.ChaosRuleConfig.
func inputRuleToProtocol(r ChaosRuleInput) protocol.ChaosRuleConfig {
	return protocol.ChaosRuleConfig{
		ID:                 r.ID,
		Name:               r.Name,
		Type:               r.Type,
		Enabled:            r.Enabled,
		URLPattern:         r.URLPattern,
		Methods:            r.Methods,
		Probability:        r.Probability,
		MinLatencyMs:       r.MinLatencyMs,
		MaxLatencyMs:       r.MaxLatencyMs,
		JitterMs:           r.JitterMs,
		BytesPerMs:         r.BytesPerMs,
		ChunkSize:          r.ChunkSize,
		DropAfterPercent:   r.DropAfterPercent,
		DropAfterBytes:     r.DropAfterBytes,
		ErrorCodes:         r.ErrorCodes,
		ErrorMessage:       r.ErrorMessage,
		TruncatePercent:    r.TruncatePercent,
		ReorderMinRequests: r.ReorderMinRequests,
		ReorderMaxWaitMs:   r.ReorderMaxWaitMs,
		StaleDelayMs:       r.StaleDelayMs,
	}
}

func (dt *DaemonTools) handleProxyChaos(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return errorResult("id required for chaos"), ProxyOutput{}, nil
	}

	operation := input.ChaosOperation
	if operation == "" {
		operation = "status"
	}

	switch operation {
	case "enable":
		result, err := dt.client.ChaosEnable(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success:      true,
			ChaosEnabled: getBool(result, "enabled"),
			Message:      "Chaos injection enabled",
		}, nil

	case "disable":
		result, err := dt.client.ChaosDisable(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success:      true,
			ChaosEnabled: getBool(result, "enabled"),
			Message:      "Chaos injection disabled",
		}, nil

	case "status":
		result, err := dt.client.ChaosStatus(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		output := ProxyOutput{
			ChaosEnabled: getBool(result, "enabled"),
		}
		if stats, ok := result["stats"].(map[string]interface{}); ok {
			output.ChaosStats = parseChaosStats(stats)
		}
		if rules, ok := result["rules"].([]interface{}); ok {
			output.ChaosRules = parseChaosRules(rules)
		}
		return nil, output, nil

	case "preset":
		if input.ChaosPreset == "" {
			// List available presets
			result, err := dt.client.ChaosListPresets()
			if err != nil {
				return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
			}
			if presets, ok := result["presets"].([]interface{}); ok {
				output := ProxyOutput{ChaosPresets: make([]string, 0, len(presets))}
				for _, p := range presets {
					if s, ok := p.(string); ok {
						output.ChaosPresets = append(output.ChaosPresets, s)
					}
				}
				return nil, output, nil
			}
			return nil, ProxyOutput{}, nil
		}
		// Apply preset
		result, err := dt.client.ChaosPreset(input.ID, input.ChaosPreset)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success:      true,
			ChaosEnabled: getBool(result, "enabled"),
			Message:      fmt.Sprintf("Chaos preset %q applied", input.ChaosPreset),
		}, nil

	case "set":
		if input.ChaosConfig == nil {
			return errorResult("chaos_config required for set operation"), ProxyOutput{}, nil
		}
		config := protocol.ChaosConfigPayload{
			Enabled:     input.ChaosConfig.Enabled,
			GlobalOdds:  input.ChaosConfig.GlobalOdds,
			Seed:        input.ChaosConfig.Seed,
			LoggingMode: input.ChaosConfig.LoggingMode,
		}
		for _, r := range input.ChaosConfig.Rules {
			rule := inputRuleToProtocol(r)
			config.Rules = append(config.Rules, &rule)
		}
		result, err := dt.client.ChaosSet(input.ID, config)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success:      true,
			ChaosEnabled: getBool(result, "enabled"),
			Message:      "Chaos configuration applied",
		}, nil

	case "add_rule":
		if input.ChaosRule == nil {
			return errorResult("chaos_rule required for add_rule operation"), ProxyOutput{}, nil
		}
		rule := inputRuleToProtocol(*input.ChaosRule)
		result, err := dt.client.ChaosAddRule(input.ID, rule)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success: true,
			Message: fmt.Sprintf("Rule %q added", getString(result, "rule_id")),
		}, nil

	case "remove_rule":
		if input.ChaosRuleID == "" {
			return errorResult("chaos_rule_id required for remove_rule operation"), ProxyOutput{}, nil
		}
		_, err := dt.client.ChaosRemoveRule(input.ID, input.ChaosRuleID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success: true,
			Message: fmt.Sprintf("Rule %q removed", input.ChaosRuleID),
		}, nil

	case "list_rules":
		result, err := dt.client.ChaosListRules(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		output := ProxyOutput{}
		if rules, ok := result["rules"].([]interface{}); ok {
			output.ChaosRules = parseChaosRules(rules)
		}
		return nil, output, nil

	case "stats":
		result, err := dt.client.ChaosStats(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		output := ProxyOutput{}
		if stats, ok := result["stats"].(map[string]interface{}); ok {
			output.ChaosStats = parseChaosStats(stats)
		}
		return nil, output, nil

	case "clear":
		_, err := dt.client.ChaosClear(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success:      true,
			ChaosEnabled: false,
			Message:      "Chaos configuration cleared",
		}, nil

	default:
		return errorResult(fmt.Sprintf("unknown chaos operation %q. Use: enable, disable, status, preset, set, add_rule, remove_rule, list_rules, stats, clear", operation)), ProxyOutput{}, nil
	}
}

// makeProxyLogHandler creates a handler for the proxylog tool.
func (dt *DaemonTools) makeProxyLogHandler() func(context.Context, *mcp.CallToolRequest, ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), ProxyLogOutput{}, nil
		}

		if input.ProxyID == "" {
			return errorResult("proxy_id required"), ProxyLogOutput{}, nil
		}

		action := input.Action
		if action == "" {
			action = "query"
		}

		switch action {
		case "query":
			return dt.handleProxyLogQuery(input)
		case "summary":
			return dt.handleProxyLogSummary(input)
		case "clear":
			return dt.handleProxyLogClear(input)
		case "stats":
			return dt.handleProxyLogStats(input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q", action)), ProxyLogOutput{}, nil
		}
	}
}

func (dt *DaemonTools) handleProxyLogQuery(input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	filter := protocol.LogQueryFilter{
		Types:       input.Types,
		Methods:     input.Methods,
		URLPattern:  input.URLPattern,
		StatusCodes: input.StatusCodes,
		Since:       input.Since,
		Until:       input.Until,
		Limit:       input.Limit,
	}

	result, err := dt.client.ProxyLogQuery(input.ProxyID, filter)
	if err != nil {
		return formatDaemonError(err, "proxylog"), ProxyLogOutput{}, nil
	}

	output := ProxyLogOutput{
		Count: getInt(result, "count"),
	}

	if entries, ok := result["entries"].([]interface{}); ok {
		for _, e := range entries {
			if em, ok := e.(map[string]interface{}); ok {
				entry := LogEntryOutput{
					Type: getString(em, "type"),
				}
				if data, ok := em["data"].(map[string]interface{}); ok {
					if b, err := json.Marshal(data); err == nil {
						entry.Data = string(b)
					} else {
						entry.Data = "{}"
					}
				}
				if ts, ok := em["timestamp"].(string); ok {
					if t, err := time.Parse(time.RFC3339, ts); err == nil {
						entry.Timestamp = t
					}
				}
				output.Entries = append(output.Entries, entry)
			}
		}
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleProxyLogSummary(input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	// Query all logs (up to a reasonable limit for aggregation)
	filter := protocol.LogQueryFilter{
		Types:       input.Types,
		Methods:     input.Methods,
		URLPattern:  input.URLPattern,
		StatusCodes: input.StatusCodes,
		Since:       input.Since,
		Until:       input.Until,
		Limit:       0, // Get all entries for aggregation (limited by log buffer size)
	}

	result, err := dt.client.ProxyLogQuery(input.ProxyID, filter)
	if err != nil {
		return formatDaemonError(err, "proxylog"), ProxyLogOutput{}, nil
	}

	entries, ok := result["entries"].([]interface{})
	if !ok {
		entries = []interface{}{}
	}

	// Build detail set for quick lookup
	detailSet := make(map[string]bool)
	for _, d := range input.Detail {
		detailSet[d] = true
	}

	// Default limit is 10, max is 100
	limit := input.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	summary := buildProxyLogSummary(entries, detailSet, limit)

	return nil, ProxyLogOutput{
		Summary: &summary,
	}, nil
}

func (dt *DaemonTools) handleProxyLogClear(input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	err := dt.client.ProxyLogClear(input.ProxyID)
	if err != nil {
		return formatDaemonError(err, "proxylog"), ProxyLogOutput{}, nil
	}

	return nil, ProxyLogOutput{
		Success: true,
		Message: fmt.Sprintf("Logs cleared for proxy %s", input.ProxyID),
	}, nil
}

func (dt *DaemonTools) handleProxyLogStats(input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	result, err := dt.client.ProxyLogStats(input.ProxyID)
	if err != nil {
		return formatDaemonError(err, "proxylog"), ProxyLogOutput{}, nil
	}

	return nil, ProxyLogOutput{
		Stats: &LogStatsOutput{
			TotalEntries:     getInt64(result, "total_entries"),
			AvailableEntries: getInt64(result, "available_entries"),
			MaxSize:          getInt64(result, "max_size"),
			Dropped:          getInt64(result, "dropped"),
		},
	}, nil
}

// makeCurrentPageHandler creates a handler for the currentpage tool.
func (dt *DaemonTools) makeCurrentPageHandler() func(context.Context, *mcp.CallToolRequest, CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), CurrentPageOutput{}, nil
		}

		if input.ProxyID == "" {
			return errorResult("proxy_id required"), CurrentPageOutput{}, nil
		}

		action := input.Action
		if action == "" {
			action = "list"
		}

		switch action {
		case "list":
			return dt.handleCurrentPageList(input)
		case "get":
			return dt.handleCurrentPageGet(input)
		case "summary":
			return dt.handleCurrentPageSummary(input)
		case "clear":
			return dt.handleCurrentPageClear(input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q", action)), CurrentPageOutput{}, nil
		}
	}
}

func (dt *DaemonTools) handleCurrentPageList(input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	result, err := dt.client.CurrentPageList(input.ProxyID)
	if err != nil {
		return formatDaemonError(err, "currentpage"), CurrentPageOutput{}, nil
	}

	output := CurrentPageOutput{
		Count: getInt(result, "count"),
	}

	if sessions, ok := result["sessions"].([]interface{}); ok {
		for _, s := range sessions {
			if sm, ok := s.(map[string]interface{}); ok {
				output.Sessions = append(output.Sessions, convertToPageSessionOutput(sm))
			}
		}
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleCurrentPageGet(input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	if input.SessionID == "" {
		return errorResult("session_id required for get"), CurrentPageOutput{}, nil
	}

	result, err := dt.client.CurrentPageGet(input.ProxyID, input.SessionID)
	if err != nil {
		return formatDaemonError(err, "currentpage"), CurrentPageOutput{}, nil
	}

	session := convertToPageSessionOutput(result)
	return nil, CurrentPageOutput{
		Session: &session,
	}, nil
}

func (dt *DaemonTools) handleCurrentPageSummary(input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	if input.SessionID == "" {
		return errorResult("session_id required for summary"), CurrentPageOutput{}, nil
	}

	result, err := dt.client.CurrentPageGet(input.ProxyID, input.SessionID)
	if err != nil {
		return formatDaemonError(err, "currentpage"), CurrentPageOutput{}, nil
	}

	// Build detail set for quick lookup
	detailSet := make(map[string]bool)
	for _, d := range input.Detail {
		detailSet[d] = true
	}

	// Default limit is 5, max is 100
	limit := input.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 100 {
		limit = 100
	}

	summary := convertToPageSummary(result, detailSet, limit)
	return nil, CurrentPageOutput{
		Summary: &summary,
	}, nil
}

// convertToPageSummary creates a compact summary from page session data.
// detailSet specifies which sections to include full details for (interactions, mutations, errors, resources).
// limit specifies the max items for detailed sections (default 5).
func convertToPageSummary(m map[string]interface{}, detailSet map[string]bool, limit int) PageSummaryOutput {
	summary := PageSummaryOutput{
		ID:               getString(m, "id"),
		URL:              getString(m, "url"),
		PageTitle:        getString(m, "page_title"),
		StartTime:        getTime(m, "start_time"),
		LastActivity:     getTime(m, "last_activity"),
		Active:           getBool(m, "active"),
		ResourceCount:    getInt(m, "resource_count"),
		ErrorCount:       getInt(m, "error_count"),
		LoadTimeMs:       getInt64(m, "load_time_ms"),
		InteractionCount: getInt(m, "interaction_count"),
		MutationCount:    getInt(m, "mutation_count"),
		DetailLimit:      limit,
	}

	// Track which sections have full detail
	var detailSections []string

	// Aggregate resources by type
	if resources, ok := m["resources"].([]interface{}); ok {
		summary.ResourcesByType = make(map[string]int)
		for _, r := range resources {
			if url, ok := r.(string); ok {
				resType := categorizeResource(url)
				summary.ResourcesByType[resType]++
			}
		}

		// Include full resource list if requested
		if detailSet["resources"] {
			detailSections = append(detailSections, "resources")
			maxResources := limit
			if len(resources) < maxResources {
				maxResources = len(resources)
			}
			for i := 0; i < maxResources; i++ {
				if url, ok := resources[i].(string); ok {
					summary.Resources = append(summary.Resources, url)
				}
			}
		}
	}

	// Aggregate errors and deduplicate
	if errors, ok := m["errors"].([]interface{}); ok {
		summary.ErrorsByType = make(map[string]int)
		errorCounts := make(map[string]*ErrorSummary) // key: message

		for _, e := range errors {
			if em, ok := e.(map[string]interface{}); ok {
				msg := getString(em, "message")
				errType := getString(em, "type")
				if errType == "" {
					errType = "Error"
				}
				summary.ErrorsByType[errType]++

				// Truncate long messages for deduplication
				key := msg
				if len(key) > 100 {
					key = key[:100]
				}
				if existing, ok := errorCounts[key]; ok {
					existing.Count++
				} else {
					errorCounts[key] = &ErrorSummary{
						Message: msg,
						Type:    errType,
						Count:   1,
					}
				}
			}
		}

		// Convert to slice, limited to top 5 unique errors
		for _, es := range errorCounts {
			summary.UniqueErrors = append(summary.UniqueErrors, *es)
			if len(summary.UniqueErrors) >= 5 {
				break
			}
		}

		// Include compact error list if requested
		if detailSet["errors"] {
			detailSections = append(detailSections, "errors")
			maxErrors := limit
			if len(errors) < maxErrors {
				maxErrors = len(errors)
			}
			for i := 0; i < maxErrors; i++ {
				if em, ok := errors[i].(map[string]interface{}); ok {
					compactErr := convertToCompactError(em)
					summary.Errors = append(summary.Errors, compactErr)
				}
			}
		}
	}

	// Aggregate interactions by type and get recent
	if interactions, ok := m["interactions"].([]interface{}); ok {
		summary.InteractionsByType = make(map[string]int)
		for _, i := range interactions {
			if im, ok := i.(map[string]interface{}); ok {
				iType := getString(im, "type")
				if iType == "" {
					iType = "unknown"
				}
				summary.InteractionsByType[iType]++
			}
		}

		// Include full interaction list if requested
		if detailSet["interactions"] {
			detailSections = append(detailSections, "interactions")
			maxInteractions := limit
			if len(interactions) < maxInteractions {
				maxInteractions = len(interactions)
			}
			// Get the last N interactions (most recent)
			start := len(interactions) - maxInteractions
			if start < 0 {
				start = 0
			}
			for i := start; i < len(interactions); i++ {
				if im, ok := interactions[i].(map[string]interface{}); ok {
					summary.Interactions = append(summary.Interactions, im)
				}
			}
		} else {
			// Get last 5 interactions for recent preview
			recentLimit := 5
			if limit < recentLimit {
				recentLimit = limit
			}
			start := len(interactions) - recentLimit
			if start < 0 {
				start = 0
			}
			for i := start; i < len(interactions); i++ {
				if im, ok := interactions[i].(map[string]interface{}); ok {
					summary.RecentInteractions = append(summary.RecentInteractions, im)
				}
			}
		}
	}

	// Aggregate mutations by type and get recent
	if mutations, ok := m["mutations"].([]interface{}); ok {
		summary.MutationsByType = make(map[string]int)
		for _, mut := range mutations {
			if mm, ok := mut.(map[string]interface{}); ok {
				mType := getString(mm, "type")
				if mType == "" {
					mType = "unknown"
				}
				summary.MutationsByType[mType]++
			}
		}

		// Include full mutation list if requested
		if detailSet["mutations"] {
			detailSections = append(detailSections, "mutations")
			maxMutations := limit
			if len(mutations) < maxMutations {
				maxMutations = len(mutations)
			}
			// Get the last N mutations (most recent)
			start := len(mutations) - maxMutations
			if start < 0 {
				start = 0
			}
			for i := start; i < len(mutations); i++ {
				if mm, ok := mutations[i].(map[string]interface{}); ok {
					summary.Mutations = append(summary.Mutations, mm)
				}
			}
		} else {
			// Get last 5 mutations for recent preview
			recentLimit := 5
			if limit < recentLimit {
				recentLimit = limit
			}
			start := len(mutations) - recentLimit
			if start < 0 {
				start = 0
			}
			for i := start; i < len(mutations); i++ {
				if mm, ok := mutations[i].(map[string]interface{}); ok {
					summary.RecentMutations = append(summary.RecentMutations, mm)
				}
			}
		}
	}

	// Extract page dimensions if available (from performance data)
	if perf, ok := m["performance"].(map[string]interface{}); ok {
		summary.FirstPaintMs = getInt64(perf, "first_paint_ms")
		summary.DOMContentLoaded = getInt64(perf, "dom_content_loaded_ms")
		summary.PageHeight = getInt(perf, "page_height")
		summary.PageWidth = getInt(perf, "page_width")
		summary.ViewportHeight = getInt(perf, "viewport_height")
		summary.ViewportWidth = getInt(perf, "viewport_width")
	}

	if len(detailSections) > 0 {
		summary.DetailSections = detailSections
	}

	return summary
}

// categorizeResource determines the type of resource from its URL.
func categorizeResource(url string) string {
	lower := strings.ToLower(url)

	// Check common extensions
	if strings.HasSuffix(lower, ".js") || strings.Contains(lower, ".js?") {
		return "js"
	}
	if strings.HasSuffix(lower, ".css") || strings.Contains(lower, ".css?") {
		return "css"
	}
	if strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") ||
		strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".gif") ||
		strings.HasSuffix(lower, ".webp") || strings.HasSuffix(lower, ".svg") ||
		strings.HasSuffix(lower, ".ico") {
		return "image"
	}
	if strings.HasSuffix(lower, ".woff") || strings.HasSuffix(lower, ".woff2") ||
		strings.HasSuffix(lower, ".ttf") || strings.HasSuffix(lower, ".eot") {
		return "font"
	}
	if strings.HasSuffix(lower, ".json") || strings.Contains(lower, "/api/") {
		return "api"
	}
	if strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, "/") {
		return "html"
	}

	return "other"
}

// convertToCompactError converts a raw error map to a CompactError with truncated fields.
// This prevents token overflow when returning error details for pages with many errors.
func convertToCompactError(em map[string]interface{}) CompactError {
	compact := CompactError{
		Message: getString(em, "message"),
		Type:    getString(em, "type"),
		URL:     getString(em, "url"),
	}

	// If type is empty, default to "Error"
	if compact.Type == "" {
		compact.Type = "Error"
	}

	// Build location string from source/lineno/colno
	source := getString(em, "source")
	lineno := getInt(em, "lineno")
	colno := getInt(em, "colno")

	if source != "" {
		// Extract just the filename from the full source path
		parts := strings.Split(source, "/")
		filename := parts[len(parts)-1]

		if lineno > 0 {
			if colno > 0 {
				compact.Location = fmt.Sprintf("%s:%d:%d", filename, lineno, colno)
			} else {
				compact.Location = fmt.Sprintf("%s:%d", filename, lineno)
			}
		} else {
			compact.Location = filename
		}
	}

	// Truncate stack trace to first 3 lines (most relevant)
	stack := getString(em, "stack")
	if stack != "" {
		lines := strings.Split(stack, "\n")
		maxLines := 3
		if len(lines) < maxLines {
			maxLines = len(lines)
		}

		var preview []string
		for i := 0; i < maxLines; i++ {
			line := strings.TrimSpace(lines[i])
			// Truncate individual lines if they're too long
			if len(line) > 120 {
				line = line[:117] + "..."
			}
			preview = append(preview, line)
		}
		compact.StackPreview = strings.Join(preview, "\n")

		// If there are more lines, indicate truncation
		if len(lines) > maxLines {
			compact.StackPreview += fmt.Sprintf("\n... (%d more lines)", len(lines)-maxLines)
		}
	}

	// Add timestamp if available
	if ts, ok := em["timestamp"].(string); ok {
		compact.Timestamp = ts
	}

	// Truncate message if it's extremely long (over 500 chars)
	if len(compact.Message) > 500 {
		compact.Message = compact.Message[:497] + "..."
	}

	return compact
}

// buildProxyLogSummary aggregates log entries into a compact summary.
func buildProxyLogSummary(entries []interface{}, detailSet map[string]bool, limit int) ProxyLogSummary {
	summary := ProxyLogSummary{
		TotalEntries:       len(entries),
		EntriesByType:      make(map[string]int),
		ErrorsByType:       make(map[string]int),
		HTTPByStatus:       make(map[string]int),
		HTTPByMethod:       make(map[string]int),
		InteractionsByType: make(map[string]int),
		MutationsByType:    make(map[string]int),
		OtherTypes:         make(map[string]int),
		DetailLimit:        limit,
	}

	var detailSections []string
	var errors []map[string]interface{}
	var httpRequests []map[string]interface{}
	var performance []map[string]interface{}
	var interactions []map[string]interface{}
	var mutations []map[string]interface{}
	var other []map[string]interface{}

	var firstTime, lastTime time.Time
	errorCounts := make(map[string]*ErrorSummary) // For deduplication
	var totalLoadTime int64
	var perfCount int

	// First pass: categorize and aggregate
	for _, e := range entries {
		em, ok := e.(map[string]interface{})
		if !ok {
			continue
		}

		logType := getString(em, "type")
		summary.EntriesByType[logType]++

		// Track time range
		if ts, ok := em["timestamp"].(string); ok {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				if firstTime.IsZero() || t.Before(firstTime) {
					firstTime = t
				}
				if lastTime.IsZero() || t.After(lastTime) {
					lastTime = t
				}
			}
		}

		// Get data payload
		data, _ := em["data"].(map[string]interface{})

		switch logType {
		case "error":
			summary.ErrorCount++
			if data != nil {
				errors = append(errors, data)

				errType := getString(data, "type")
				if errType == "" {
					errType = "Error"
				}
				summary.ErrorsByType[errType]++

				// Deduplicate errors
				msg := getString(data, "message")
				key := msg
				if len(key) > 100 {
					key = key[:100]
				}
				if existing, ok := errorCounts[key]; ok {
					existing.Count++
				} else {
					errorCounts[key] = &ErrorSummary{
						Message: msg,
						Type:    errType,
						Count:   1,
					}
				}
			}

		case "http":
			summary.HTTPCount++
			if data != nil {
				httpRequests = append(httpRequests, data)

				// Aggregate by status code
				statusCode := getInt(data, "status_code")
				statusGroup := fmt.Sprintf("%dxx", statusCode/100)
				summary.HTTPByStatus[statusGroup]++

				// Aggregate by method
				method := getString(data, "method")
				if method != "" {
					summary.HTTPByMethod[method]++
				}
			}

		case "performance":
			summary.PerformanceCount++
			if data != nil {
				performance = append(performance, data)

				loadTime := getInt64(data, "load_event_end")
				if loadTime > 0 {
					totalLoadTime += loadTime
					perfCount++
				}
			}

		case "interaction":
			summary.InteractionCount++
			if data != nil {
				interactions = append(interactions, data)

				iType := getString(data, "event_type")
				if iType == "" {
					iType = getString(data, "type")
				}
				if iType != "" {
					summary.InteractionsByType[iType]++
				}
			}

		case "mutation":
			summary.MutationCount++
			if data != nil {
				mutations = append(mutations, data)

				mType := getString(data, "type")
				if mType != "" {
					summary.MutationsByType[mType]++
				}
			}

		default:
			summary.OtherCount++
			summary.OtherTypes[logType]++
			if data != nil {
				other = append(other, data)
			}
		}
	}

	// Set time range
	if !firstTime.IsZero() && !lastTime.IsZero() {
		summary.TimeRange = TimeRange{Start: firstTime, End: lastTime}
	}

	// Calculate average load time
	if perfCount > 0 {
		summary.AvgLoadTime = totalLoadTime / int64(perfCount)
	}

	// Build unique errors (top 10)
	for _, es := range errorCounts {
		summary.UniqueErrors = append(summary.UniqueErrors, *es)
		if len(summary.UniqueErrors) >= 10 {
			break
		}
	}

	// Process errors
	if detailSet["errors"] {
		detailSections = append(detailSections, "errors")
		maxErrors := limit
		if len(errors) < maxErrors {
			maxErrors = len(errors)
		}
		// Get most recent errors
		start := len(errors) - maxErrors
		if start < 0 {
			start = 0
		}
		for i := start; i < len(errors); i++ {
			summary.Errors = append(summary.Errors, convertToCompactError(errors[i]))
		}
	} else if summary.ErrorCount > 0 {
		// Include last 5 errors as preview
		recentLimit := 5
		if limit < recentLimit {
			recentLimit = limit
		}
		start := len(errors) - recentLimit
		if start < 0 {
			start = 0
		}
		for i := start; i < len(errors); i++ {
			summary.RecentErrors = append(summary.RecentErrors, convertToCompactError(errors[i]))
		}
	}

	// Process HTTP requests
	if detailSet["http"] {
		detailSections = append(detailSections, "http")
		maxHTTP := limit
		if len(httpRequests) < maxHTTP {
			maxHTTP = len(httpRequests)
		}
		start := len(httpRequests) - maxHTTP
		if start < 0 {
			start = 0
		}
		for i := start; i < len(httpRequests); i++ {
			summary.HTTPRequests = append(summary.HTTPRequests, convertToCompactHTTP(httpRequests[i]))
		}
	} else if summary.HTTPCount > 0 {
		recentLimit := 5
		if limit < recentLimit {
			recentLimit = limit
		}
		start := len(httpRequests) - recentLimit
		if start < 0 {
			start = 0
		}
		for i := start; i < len(httpRequests); i++ {
			summary.RecentHTTP = append(summary.RecentHTTP, convertToCompactHTTP(httpRequests[i]))
		}
	}

	// Process performance
	if detailSet["performance"] {
		detailSections = append(detailSections, "performance")
		maxPerf := limit
		if len(performance) < maxPerf {
			maxPerf = len(performance)
		}
		start := len(performance) - maxPerf
		if start < 0 {
			start = 0
		}
		for i := start; i < len(performance); i++ {
			summary.Performance = append(summary.Performance, convertToCompactPerformance(performance[i]))
		}
	} else if summary.PerformanceCount > 0 {
		recentLimit := 5
		if limit < recentLimit {
			recentLimit = limit
		}
		start := len(performance) - recentLimit
		if start < 0 {
			start = 0
		}
		for i := start; i < len(performance); i++ {
			summary.RecentPerformance = append(summary.RecentPerformance, convertToCompactPerformance(performance[i]))
		}
	}

	// Process interactions
	if detailSet["interactions"] {
		detailSections = append(detailSections, "interactions")
		maxInt := limit
		if len(interactions) < maxInt {
			maxInt = len(interactions)
		}
		start := len(interactions) - maxInt
		if start < 0 {
			start = 0
		}
		for i := start; i < len(interactions); i++ {
			summary.Interactions = append(summary.Interactions, convertToCompactInteraction(interactions[i]))
		}
	} else if summary.InteractionCount > 0 {
		recentLimit := 5
		if limit < recentLimit {
			recentLimit = limit
		}
		start := len(interactions) - recentLimit
		if start < 0 {
			start = 0
		}
		for i := start; i < len(interactions); i++ {
			summary.RecentInteractions = append(summary.RecentInteractions, convertToCompactInteraction(interactions[i]))
		}
	}

	// Process mutations
	if detailSet["mutations"] {
		detailSections = append(detailSections, "mutations")
		maxMut := limit
		if len(mutations) < maxMut {
			maxMut = len(mutations)
		}
		start := len(mutations) - maxMut
		if start < 0 {
			start = 0
		}
		for i := start; i < len(mutations); i++ {
			summary.Mutations = append(summary.Mutations, convertToCompactMutation(mutations[i]))
		}
	} else if summary.MutationCount > 0 {
		recentLimit := 5
		if limit < recentLimit {
			recentLimit = limit
		}
		start := len(mutations) - recentLimit
		if start < 0 {
			start = 0
		}
		for i := start; i < len(mutations); i++ {
			summary.RecentMutations = append(summary.RecentMutations, convertToCompactMutation(mutations[i]))
		}
	}

	// Process other log types
	if detailSet["other"] && summary.OtherCount > 0 {
		detailSections = append(detailSections, "other")
		maxOther := limit
		if len(other) < maxOther {
			maxOther = len(other)
		}
		start := len(other) - maxOther
		if start < 0 {
			start = 0
		}
		for i := start; i < len(other); i++ {
			summary.Other = append(summary.Other, convertToCompactLogEntry(other[i]))
		}
	}

	if len(detailSections) > 0 {
		summary.DetailSections = detailSections
	}

	return summary
}

// Helper converters for compact log types

func convertToCompactHTTP(data map[string]interface{}) CompactHTTPRequest {
	compact := CompactHTTPRequest{
		Method:     getString(data, "method"),
		URL:        getString(data, "url"),
		StatusCode: getInt(data, "status_code"),
		Duration:   getInt64(data, "duration") / 1000000, // Convert ns to ms
		Error:      getString(data, "error"),
	}

	if ts, ok := data["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			compact.Timestamp = t
		}
	}

	// Truncate URL if too long
	if len(compact.URL) > 100 {
		compact.URL = compact.URL[:97] + "..."
	}

	return compact
}

func convertToCompactPerformance(data map[string]interface{}) CompactPerformance {
	compact := CompactPerformance{
		URL:              getString(data, "url"),
		LoadTimeMs:       getInt64(data, "load_event_end"),
		FirstPaintMs:     getInt64(data, "first_paint"),
		DOMContentLoaded: getInt64(data, "dom_content_loaded"),
	}

	if ts, ok := data["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			compact.Timestamp = t
		}
	}

	// Truncate URL if too long
	if len(compact.URL) > 100 {
		compact.URL = compact.URL[:97] + "..."
	}

	return compact
}

func convertToCompactInteraction(data map[string]interface{}) CompactInteraction {
	compact := CompactInteraction{
		Type: getString(data, "event_type"),
	}

	if compact.Type == "" {
		compact.Type = getString(data, "type")
	}

	// Extract target info
	if target, ok := data["target"].(map[string]interface{}); ok {
		selector := getString(target, "selector")
		if selector != "" {
			compact.Target = selector
		} else {
			tag := getString(target, "tag_name")
			id := getString(target, "id")
			if id != "" {
				compact.Target = fmt.Sprintf("%s#%s", tag, id)
			} else {
				compact.Target = tag
			}
		}
	}

	if ts, ok := data["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			compact.Timestamp = t
		}
	}

	// Truncate target if too long
	if len(compact.Target) > 80 {
		compact.Target = compact.Target[:77] + "..."
	}

	return compact
}

func convertToCompactMutation(data map[string]interface{}) CompactMutation {
	compact := CompactMutation{
		Type: getString(data, "type"),
	}

	// Extract target info
	if target, ok := data["target"].(map[string]interface{}); ok {
		selector := getString(target, "selector")
		if selector != "" {
			compact.Target = selector
		} else {
			tag := getString(target, "tag_name")
			id := getString(target, "id")
			if id != "" {
				compact.Target = fmt.Sprintf("%s#%s", tag, id)
			} else {
				compact.Target = tag
			}
		}
	}

	// Count nodes if available
	if nodes, ok := data["nodes"].([]interface{}); ok {
		compact.Count = len(nodes)
	}

	if ts, ok := data["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			compact.Timestamp = t
		}
	}

	// Truncate target if too long
	if len(compact.Target) > 80 {
		compact.Target = compact.Target[:77] + "..."
	}

	return compact
}

func convertToCompactLogEntry(data map[string]interface{}) CompactLogEntry {
	compact := CompactLogEntry{
		Type:    getString(data, "type"),
		Message: getString(data, "message"),
	}

	if compact.Message == "" {
		compact.Message = getString(data, "level")
	}

	if ts, ok := data["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			compact.Timestamp = t
		}
	}

	// Truncate message if too long
	if len(compact.Message) > 200 {
		compact.Message = compact.Message[:197] + "..."
	}

	return compact
}

func (dt *DaemonTools) handleCurrentPageClear(input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	err := dt.client.CurrentPageClear(input.ProxyID)
	if err != nil {
		return formatDaemonError(err, "currentpage"), CurrentPageOutput{}, nil
	}

	return nil, CurrentPageOutput{
		Success: true,
		Message: fmt.Sprintf("Page sessions cleared for proxy %s", input.ProxyID),
	}, nil
}

// Helper functions

// getProjectPath returns the project path for filtering processes/proxies.
// It first checks for AGNT_PROJECT_PATH environment variable (set by agnt run),
// then falls back to the current working directory.
// On Windows, paths are normalized to lowercase for case-insensitive comparison.
func getProjectPath() string {
	var path string

	// Check for project path set by agnt run
	if envPath := os.Getenv("AGNT_PROJECT_PATH"); envPath != "" {
		// Convert to absolute path if needed
		absPath, err := filepath.Abs(envPath)
		if err == nil {
			path = absPath
		} else {
			path = envPath
		}
	} else {
		// Fall back to current working directory
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		path = cwd
	}

	// On Windows, normalize to lowercase for case-insensitive comparison
	// This matches the normalization in daemon/handler.go
	if isWindows() {
		path = strings.ToLower(path)
	}
	return path
}

// isWindows returns true if running on Windows.
func isWindows() bool {
	return os.PathSeparator == '\\'
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func getInt(m map[string]interface{}, key string) int {
	if v, ok := m[key].(float64); ok {
		return int(v)
	}
	return 0
}

func getInt64(m map[string]interface{}, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

func getFloat64(m map[string]interface{}, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

func getBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func getTime(m map[string]interface{}, key string) time.Time {
	if v, ok := m[key].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

// formatDaemonError parses structured errors from daemon and creates helpful LLM-friendly messages.
func formatDaemonError(err error, toolName string) *mcp.CallToolResult {
	errStr := err.Error()

	// Try to extract and parse structured JSON error from daemon response
	// Format: "daemon error: [code] {json}"
	if idx := strings.Index(errStr, "] {"); idx != -1 {
		jsonStart := idx + 2 // skip "] "
		jsonStr := errStr[jsonStart:]

		var structErr protocol.StructuredError
		if json.Unmarshal([]byte(jsonStr), &structErr) == nil {
			return formatStructuredError(&structErr, toolName)
		}
	}

	// Fallback to original error message
	return errorResult(fmt.Sprintf("%s failed: %v", toolName, err))
}

// formatStructuredError creates a helpful LLM-friendly message from a structured error.
func formatStructuredError(err *protocol.StructuredError, toolName string) *mcp.CallToolResult {
	var msg strings.Builder

	switch err.Code {
	case protocol.ErrInvalidAction:
		msg.WriteString(fmt.Sprintf("%s: unknown action %q", toolName, err.Action))
		if len(err.ValidActions) > 0 {
			msg.WriteString(fmt.Sprintf("\n\nValid actions: %s", strings.Join(err.ValidActions, ", ")))
			msg.WriteString("\n\nExamples:\n")
			for _, action := range err.ValidActions {
				msg.WriteString(fmt.Sprintf("  %s {action: %q, ...}\n", toolName, strings.ToLower(action)))
			}
		}

	case protocol.ErrMissingParam:
		msg.WriteString(fmt.Sprintf("%s: %s is required", toolName, err.Param))
		if len(err.ValidActions) > 0 {
			msg.WriteString(fmt.Sprintf("\n\nValid values for %s: %s", err.Param, strings.Join(err.ValidActions, ", ")))
		}
		if len(err.ValidParams) > 0 {
			msg.WriteString(fmt.Sprintf("\n\nValid values: %s", strings.Join(err.ValidParams, ", ")))
		}

	case protocol.ErrInvalidCommand:
		msg.WriteString(fmt.Sprintf("unknown command %q", err.Command))
		if len(err.ValidActions) > 0 {
			msg.WriteString(fmt.Sprintf("\n\nValid commands: %s", strings.Join(err.ValidActions, ", ")))
		}

	case protocol.ErrNotFound:
		msg.WriteString(fmt.Sprintf("%s: not found - %s", toolName, err.Message))

	default:
		msg.WriteString(fmt.Sprintf("%s: %s", toolName, err.Message))
	}

	return errorResult(msg.String())
}

func convertToPageSessionOutput(m map[string]interface{}) PageSessionOutput {
	output := PageSessionOutput{
		ID:               getString(m, "id"),
		URL:              getString(m, "url"),
		PageTitle:        getString(m, "page_title"),
		Active:           getBool(m, "active"),
		ResourceCount:    getInt(m, "resource_count"),
		ErrorCount:       getInt(m, "error_count"),
		HasPerformance:   getBool(m, "has_performance"),
		LoadTime:         getInt64(m, "load_time_ms"),
		InteractionCount: getInt(m, "interaction_count"),
		MutationCount:    getInt(m, "mutation_count"),
	}

	// Parse timestamps
	if ts, ok := m["start_time"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			output.StartTime = t
		}
	}
	if ts, ok := m["last_activity"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			output.LastActivity = t
		}
	}

	// Parse resources
	if resources, ok := m["resources"].([]interface{}); ok {
		for _, r := range resources {
			if rs, ok := r.(string); ok {
				output.Resources = append(output.Resources, rs)
			}
		}
	}

	// Parse errors
	if errors, ok := m["errors"].([]interface{}); ok {
		for _, e := range errors {
			if em, ok := e.(map[string]interface{}); ok {
				// Convert to map[string]interface{} for compatibility
				errMap := make(map[string]interface{})
				for k, v := range em {
					errMap[k] = v
				}
				output.Errors = append(output.Errors, errMap)
			}
		}
	}

	// Parse interactions (for detailed view)
	if interactions, ok := m["interactions"].([]interface{}); ok {
		for _, i := range interactions {
			if im, ok := i.(map[string]interface{}); ok {
				interactionMap := make(map[string]interface{})
				for k, v := range im {
					interactionMap[k] = v
				}
				output.Interactions = append(output.Interactions, interactionMap)
			}
		}
	}

	// Parse mutations (for detailed view)
	if mutations, ok := m["mutations"].([]interface{}); ok {
		for _, m := range mutations {
			if mm, ok := m.(map[string]interface{}); ok {
				mutationMap := make(map[string]interface{})
				for k, v := range mm {
					mutationMap[k] = v
				}
				output.Mutations = append(output.Mutations, mutationMap)
			}
		}
	}

	return output
}

// MarshalJSON custom marshaler for proper JSON serialization
func (o PageSessionOutput) MarshalJSON() ([]byte, error) {
	type Alias PageSessionOutput
	return json.Marshal(&struct {
		Alias
	}{
		Alias: Alias(o),
	})
}
