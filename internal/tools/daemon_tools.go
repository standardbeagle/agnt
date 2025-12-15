package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// DaemonTools wraps a daemon client for MCP tool handlers.
type DaemonTools struct {
	client *daemon.Client
	config daemon.AutoStartConfig
}

// NewDaemonTools creates a new daemon tools wrapper with auto-start.
func NewDaemonTools(config daemon.AutoStartConfig) *DaemonTools {
	return &DaemonTools{
		config: config,
	}
}

// ensureConnected ensures we have a connection to the daemon.
func (dt *DaemonTools) ensureConnected() error {
	if dt.client != nil && dt.client.IsConnected() {
		return nil
	}

	client, err := daemon.EnsureDaemonRunning(dt.config)
	if err != nil {
		return fmt.Errorf("failed to connect to daemon: %w", err)
	}

	dt.client = client
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
  query: Search logs with filters (default)
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

Examples:
  proxylog {proxy_id: "dev", types: ["http"], methods: ["GET"]}
  proxylog {proxy_id: "dev", types: ["error"]}
  proxylog {proxy_id: "dev", types: ["sketch"]}
  proxylog {proxy_id: "dev", types: ["panel_message"]}
  proxylog {proxy_id: "dev", since: "5m", limit: 50}
  proxylog {proxy_id: "dev", action: "stats"}
  proxylog {proxy_id: "dev", action: "clear"}

Each proxy maintains its own separate log storage.`,
	}, dt.makeProxyLogHandler())

	mcp.AddTool(server, &mcp.Tool{
		Name: "currentpage",
		Description: `Get current page sessions with grouped resources and metrics.

Actions:
  list: List all active page sessions (default)
  get: Get detailed information for a specific session
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
  currentpage {proxy_id: "dev", action: "get", session_id: "page-1"}
  currentpage {proxy_id: "dev", action: "clear"}

The list action returns summary counts (interaction_count, mutation_count).
The get action returns full interaction and mutation history for debugging.

This provides a high-level view of active pages and their resources.`,
	}, dt.makeCurrentPageHandler())
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
		path := input.Path
		if path == "" {
			path = "."
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
		path := input.Path
		if path == "" {
			path = "."
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to resolve path: %v", err)), RunOutput{}, nil
		}

		// Build daemon protocol config
		config := protocol.RunConfig{
			ID:         input.ID,
			Path:       absPath,
			ScriptName: input.ScriptName,
			Raw:        input.Raw,
			Command:    input.Command,
			Args:       input.Args,
			Mode:       string(input.Mode),
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
	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get working directory: %v", err)), ProcOutput{}, nil
	}

	// Create directory filter
	dirFilter := protocol.DirectoryFilter{
		Directory: cwd,
		Global:    input.Global,
	}

	result, err := dt.client.ProcList(dirFilter)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	output := ProcOutput{
		Count:     getInt(result, "count"),
		Directory: getString(result, "directory"),
		Global:    getBool(result, "global"),
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
	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return errorResult(fmt.Sprintf("failed to get working directory: %v", err)), ProxyOutput{}, nil
	}

	// Create directory filter
	dirFilter := protocol.DirectoryFilter{
		Directory: cwd,
		Global:    input.Global,
	}

	result, err := dt.client.ProxyList(dirFilter)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	output := ProxyOutput{
		Count:     getInt(result, "count"),
		Directory: getString(result, "directory"),
		Global:    getBool(result, "global"),
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
			output.ChaosStats = &ChaosStatsOutput{
				TotalRequests:   getInt64(stats, "total_requests"),
				AffectedCount:   getInt64(stats, "affected_count"),
				LatencyInjected: getInt64(stats, "latency_injected_ms"),
				ErrorsInjected:  getInt64(stats, "errors_injected"),
				DropsInjected:   getInt64(stats, "drops_injected"),
				TruncatedCount:  getInt64(stats, "truncated_count"),
				ReorderedCount:  getInt64(stats, "reordered_count"),
			}
			if ruleStats, ok := stats["rule_stats"].(map[string]interface{}); ok {
				output.ChaosStats.RuleStats = make(map[string]int64)
				for k, v := range ruleStats {
					if n, ok := v.(float64); ok {
						output.ChaosStats.RuleStats[k] = int64(n)
					}
				}
			}
		}
		if rules, ok := result["rules"].([]interface{}); ok {
			for _, r := range rules {
				if rm, ok := r.(map[string]interface{}); ok {
					output.ChaosRules = append(output.ChaosRules, ChaosRuleOutput{
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
			config.Rules = append(config.Rules, &protocol.ChaosRuleConfig{
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
			})
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
		rule := protocol.ChaosRuleConfig{
			ID:                 input.ChaosRule.ID,
			Name:               input.ChaosRule.Name,
			Type:               input.ChaosRule.Type,
			Enabled:            input.ChaosRule.Enabled,
			URLPattern:         input.ChaosRule.URLPattern,
			Methods:            input.ChaosRule.Methods,
			Probability:        input.ChaosRule.Probability,
			MinLatencyMs:       input.ChaosRule.MinLatencyMs,
			MaxLatencyMs:       input.ChaosRule.MaxLatencyMs,
			JitterMs:           input.ChaosRule.JitterMs,
			BytesPerMs:         input.ChaosRule.BytesPerMs,
			ChunkSize:          input.ChaosRule.ChunkSize,
			DropAfterPercent:   input.ChaosRule.DropAfterPercent,
			DropAfterBytes:     input.ChaosRule.DropAfterBytes,
			ErrorCodes:         input.ChaosRule.ErrorCodes,
			ErrorMessage:       input.ChaosRule.ErrorMessage,
			TruncatePercent:    input.ChaosRule.TruncatePercent,
			ReorderMinRequests: input.ChaosRule.ReorderMinRequests,
			ReorderMaxWaitMs:   input.ChaosRule.ReorderMaxWaitMs,
			StaleDelayMs:       input.ChaosRule.StaleDelayMs,
		}
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
			for _, r := range rules {
				if rm, ok := r.(map[string]interface{}); ok {
					output.ChaosRules = append(output.ChaosRules, ChaosRuleOutput{
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
		}
		return nil, output, nil

	case "stats":
		result, err := dt.client.ChaosStats(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		output := ProxyOutput{}
		if stats, ok := result["stats"].(map[string]interface{}); ok {
			output.ChaosStats = &ChaosStatsOutput{
				TotalRequests:   getInt64(stats, "total_requests"),
				AffectedCount:   getInt64(stats, "affected_count"),
				LatencyInjected: getInt64(stats, "latency_injected_ms"),
				ErrorsInjected:  getInt64(stats, "errors_injected"),
				DropsInjected:   getInt64(stats, "drops_injected"),
				TruncatedCount:  getInt64(stats, "truncated_count"),
				ReorderedCount:  getInt64(stats, "reordered_count"),
			}
			if ruleStats, ok := stats["rule_stats"].(map[string]interface{}); ok {
				output.ChaosStats.RuleStats = make(map[string]int64)
				for k, v := range ruleStats {
					if n, ok := v.(float64); ok {
						output.ChaosStats.RuleStats[k] = int64(n)
					}
				}
			}
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
					entry.Data = data
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
