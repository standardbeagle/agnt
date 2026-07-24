package tools

import (
	"context"
	"fmt"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/standardbeagle/go-sdk/mcp"
)

// DaemonInput defines input for the daemon management tool.
type DaemonInput struct {
	Action string `json:"action" jsonschema:"Action: status, info, start, stop, restart, stop_all, restart_all, startup_log, doctor"`
	Global *bool  `json:"global,omitempty" jsonschema:"For startup_log/stop_all/restart_all: override project scope; true targets every project, false forces the current project (default: current project)"`
}

// DaemonOutput defines output for daemon management.
type DaemonOutput struct {
	// For status
	Running    bool   `json:"running"`
	SocketPath string `json:"socket_path,omitempty"`

	// For info
	Version     string       `json:"version,omitempty"`
	Uptime      string       `json:"uptime,omitempty"`
	ClientCount int64        `json:"client_count,omitempty"`
	ProcessInfo *ProcessInfo `json:"process_info,omitempty"`
	ProxyInfo   *ProxyInfo   `json:"proxy_info,omitempty"`

	// For stop_all/restart_all
	ProcessesStopped int `json:"processes_stopped,omitempty"`
	ProxiesStopped   int `json:"proxies_stopped,omitempty"`
	ProcessesStarted int `json:"processes_started,omitempty"`
	ProxiesStarted   int `json:"proxies_started,omitempty"`
	ProcessesFailed  int `json:"processes_failed,omitempty"`
	ProxiesFailed    int `json:"proxies_failed,omitempty"`

	// For all actions
	Success bool   `json:"success,omitempty"`
	Message string `json:"message,omitempty"`
}

// ProcessInfo holds process manager statistics.
type ProcessInfo struct {
	Active       int64 `json:"active"`
	TotalStarted int64 `json:"total_started"`
	TotalFailed  int64 `json:"total_failed"`
}

// ProxyInfo holds proxy manager statistics.
type ProxyInfo struct {
	Active       int64 `json:"active"`
	TotalStarted int64 `json:"total_started"`
}

// RegisterDaemonManagementTool adds the daemon management tool to the server.
func RegisterDaemonManagementTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name: "daemon",
		Description: `Manage the devtool daemon service.

The daemon is a background process that maintains persistent state for
processes and proxies across MCP client connections.

Actions:
  status: Check if daemon is running
  info: Get daemon information (version, uptime, statistics)
  start: Start the daemon (auto-starts if needed)
  stop: Stop the daemon gracefully
  restart: Restart the daemon
  stop_all: Stop all processes and proxies (daemon keeps running)
  restart_all: Restart all processes and proxies (stop then start with same config)
  doctor: Run health checks and return diagnostic report

Examples:
  daemon {action: "status"}
  daemon {action: "info"}
  daemon {action: "start"}
  daemon {action: "stop"}
  daemon {action: "restart"}
  daemon {action: "stop_all"}
  daemon {action: "restart_all"}
  daemon {action: "doctor"}

The daemon auto-starts when needed, so manual start is rarely required.
Use stop_all/restart_all to manage running resources without stopping the daemon.`,
	}, makeDaemonHandler(dt))
}

func makeDaemonHandler(dt *DaemonTools) func(context.Context, *mcp.CallToolRequest, DaemonInput) (*mcp.CallToolResult, DaemonOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input DaemonInput) (*mcp.CallToolResult, DaemonOutput, error) {
		if err := validateDaemonInput(input); err != nil {
			return fail[DaemonOutput](validationError("daemon", err))
		}
		switch input.Action {
		case "status":
			return handleDaemonStatus(dt)
		case "info":
			return handleDaemonInfo(dt)
		case "start":
			return handleDaemonStart(dt)
		case "stop":
			return handleDaemonStop(dt)
		case "restart":
			return handleDaemonRestart(dt)
		case "stop_all":
			return handleDaemonStopAll(dt, input.Global)
		case "restart_all":
			return handleDaemonRestartAll(dt, input.Global)
		case "startup_log":
			return handleDaemonStartupLog(dt, input.Global)
		case "doctor":
			return handleDaemonDoctor(dt)
		default:
			return fail[DaemonOutput](fmt.Sprintf("unknown action %q. Use: status, info, start, stop, restart, stop_all, restart_all, startup_log, doctor", input.Action))
		}
	}
}

func handleDaemonStatus(dt *DaemonTools) (*mcp.CallToolResult, DaemonOutput, error) {
	socketPath := dt.config.SocketPath
	if socketPath == "" {
		socketPath = daemonclient.DefaultSocketPath()
	}

	running := daemonclient.IsDaemonRunning(socketPath)

	return nil, DaemonOutput{
		Running:    running,
		SocketPath: socketPath,
		Message:    formatStatusMessage(running),
	}, nil
}

func handleDaemonInfo(dt *DaemonTools) (*mcp.CallToolResult, DaemonOutput, error) {
	if err := dt.ensureConnected(); err != nil {
		return fail[DaemonOutput](fmt.Sprintf("daemon not running: %v", err))
	}

	info, err := dt.client.Info()
	if err != nil {
		return fail[DaemonOutput](fmt.Sprintf("failed to get info: %v", err))
	}

	output := DaemonOutput{
		Running:     true,
		SocketPath:  info.SocketPath,
		Version:     info.Version,
		Uptime:      formatDuration(info.Uptime),
		ClientCount: info.ClientCount,
		ProcessInfo: &ProcessInfo{
			Active:       info.ProcessInfo.Active,
			TotalStarted: info.ProcessInfo.TotalStarted,
			TotalFailed:  info.ProcessInfo.TotalFailed,
		},
		ProxyInfo: &ProxyInfo{
			Active:       info.ProxyInfo.Active,
			TotalStarted: info.ProxyInfo.TotalStarted,
		},
	}

	return nil, output, nil
}

func handleDaemonStart(dt *DaemonTools) (*mcp.CallToolResult, DaemonOutput, error) {
	socketPath := dt.config.SocketPath
	if socketPath == "" {
		socketPath = daemonclient.DefaultSocketPath()
	}

	// Check if already running
	if daemonclient.IsDaemonRunning(socketPath) {
		return nil, DaemonOutput{
			Running:    true,
			SocketPath: socketPath,
			Success:    true,
			Message:    "Daemon is already running",
		}, nil
	}

	// Try to start and connect
	if err := dt.ensureConnected(); err != nil {
		return fail[DaemonOutput](fmt.Sprintf("failed to start daemon: %v", err))
	}

	return nil, DaemonOutput{
		Running:    true,
		SocketPath: socketPath,
		Success:    true,
		Message:    "Daemon started successfully",
	}, nil
}

func handleDaemonStop(dt *DaemonTools) (*mcp.CallToolResult, DaemonOutput, error) {
	socketPath := dt.config.SocketPath
	if socketPath == "" {
		socketPath = daemonclient.DefaultSocketPath()
	}

	// Check if running
	if !daemonclient.IsDaemonRunning(socketPath) {
		return nil, DaemonOutput{
			Running:    false,
			SocketPath: socketPath,
			Success:    true,
			Message:    "Daemon is not running",
		}, nil
	}

	// Stop the daemon
	if err := daemonclient.StopDaemon(socketPath); err != nil {
		return fail[DaemonOutput](fmt.Sprintf("failed to stop daemon: %v", err))
	}

	// Close our connection
	if dt.client != nil {
		dt.client.Close()
		dt.client = nil
	}

	return nil, DaemonOutput{
		Running:    false,
		SocketPath: socketPath,
		Success:    true,
		Message:    "Daemon stopped successfully",
	}, nil
}

func handleDaemonRestart(dt *DaemonTools) (*mcp.CallToolResult, DaemonOutput, error) {
	socketPath := dt.config.SocketPath
	if socketPath == "" {
		socketPath = daemonclient.DefaultSocketPath()
	}

	// Stop if running
	if daemonclient.IsDaemonRunning(socketPath) {
		if err := daemonclient.StopDaemon(socketPath); err != nil {
			return fail[DaemonOutput](fmt.Sprintf("failed to stop daemon: %v", err))
		}

		// Close our connection
		if dt.client != nil {
			dt.client.Close()
			dt.client = nil
		}
	}

	// Start again
	if err := dt.ensureConnected(); err != nil {
		return fail[DaemonOutput](fmt.Sprintf("failed to start daemon: %v", err))
	}

	return nil, DaemonOutput{
		Running:    true,
		SocketPath: socketPath,
		Success:    true,
		Message:    "Daemon restarted successfully",
	}, nil
}

func formatStatusMessage(running bool) string {
	if running {
		return "Daemon is running"
	}
	return "Daemon is not running"
}

func handleDaemonStopAll(dt *DaemonTools, global *bool) (*mcp.CallToolResult, DaemonOutput, error) {
	if err := dt.ensureConnected(); err != nil {
		return fail[DaemonOutput](fmt.Sprintf("daemon not running: %v", err))
	}

	result, err := dt.client.StopAllScoped(dt.scopeFilter(global))
	if err != nil {
		return fail[DaemonOutput](fmt.Sprintf("failed to stop all: %v", err))
	}

	processesStopped := getInt(result, "processes_stopped")
	proxiesStopped := getInt(result, "proxies_stopped")

	return nil, DaemonOutput{
		Running:          true,
		ProcessesStopped: processesStopped,
		ProxiesStopped:   proxiesStopped,
		Success:          true,
		Message:          fmt.Sprintf("Stopped %d processes and %d proxies", processesStopped, proxiesStopped),
	}, nil
}

func handleDaemonRestartAll(dt *DaemonTools, global *bool) (*mcp.CallToolResult, DaemonOutput, error) {
	if err := dt.ensureConnected(); err != nil {
		return fail[DaemonOutput](fmt.Sprintf("daemon not running: %v", err))
	}

	result, err := dt.client.RestartAllScoped(dt.scopeFilter(global))
	if err != nil {
		return fail[DaemonOutput](fmt.Sprintf("failed to restart all: %v", err))
	}

	processesRestarted := getInt(result, "processes_restarted")
	proxiesRestarted := getInt(result, "proxies_restarted")
	processesFailed := getInt(result, "processes_failed")
	proxiesFailed := getInt(result, "proxies_failed")

	return nil, DaemonOutput{
		Running:          true,
		ProcessesStarted: processesRestarted,
		ProxiesStarted:   proxiesRestarted,
		ProcessesFailed:  processesFailed,
		ProxiesFailed:    proxiesFailed,
		Success:          processesFailed == 0 && proxiesFailed == 0,
		Message:          fmt.Sprintf("Restarted %d processes, %d proxies", processesRestarted, proxiesRestarted),
	}, nil
}

func handleDaemonDoctor(dt *DaemonTools) (*mcp.CallToolResult, DaemonOutput, error) {
	if err := dt.ensureConnected(); err != nil {
		return fail[DaemonOutput](fmt.Sprintf("daemon not running: %v", err))
	}

	result, err := dt.client.Doctor(getProjectPath())
	if err != nil {
		return fail[DaemonOutput](fmt.Sprintf("failed to run doctor: %v", err))
	}

	status, _ := result["status"].(string)
	checks, _ := result["checks"].([]interface{})

	var text string
	text = fmt.Sprintf("=== Doctor Report: %s ===\n\n", status)
	for _, c := range checks {
		check, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := check["name"].(string)
		st, _ := check["status"].(string)
		msg, _ := check["message"].(string)
		fix, _ := check["fix"].(string)

		icon := "  "
		switch st {
		case "error":
			icon = "X "
		case "warning":
			icon = "! "
		case "ok":
			icon = "  "
		}

		text += fmt.Sprintf("%s[%s] %s: %s\n", icon, st, name, msg)
		if fix != "" {
			text += "  fix: " + fix + "\n"
		}
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}, DaemonOutput{Success: status != "error", Message: text}, nil
}

func handleDaemonStartupLog(dt *DaemonTools, global *bool) (*mcp.CallToolResult, DaemonOutput, error) {
	if err := dt.ensureConnected(); err != nil {
		return fail[DaemonOutput](fmt.Sprintf("daemon not running: %v", err))
	}

	// Scope through the session-scope chokepoint: the MCP daemon connection
	// is not session-bound, so a non-global call names the project explicitly
	// (mirrors collectProcessAlerts). global:true bypasses the scope; a
	// non-global contextless call is rejected daemon-side rather than leaking
	// every project's startup events.
	dirFilter := dt.scopeFilter(global)
	result, err := dt.client.StartupLog(50, dirFilter)
	if err != nil {
		return fail[DaemonOutput](fmt.Sprintf("failed to query startup log: %v", err))
	}

	entries, _ := result["entries"].([]interface{})
	count := len(entries)

	// Format as readable text
	var text string
	if count == 0 {
		text = "No startup log entries."
	} else {
		text = fmt.Sprintf("=== Startup Log (%d entries) ===\n\n", count)
		for _, e := range entries {
			entry, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			level, _ := entry["level"].(string)
			eventType, _ := entry["event_type"].(string)
			scriptName, _ := entry["script_name"].(string)
			message, _ := entry["message"].(string)
			timestamp, _ := entry["timestamp"].(string)
			output, _ := entry["output"].(string)

			icon := "  "
			switch level {
			case "error":
				icon = "✗ "
			case "warning":
				icon = "⚠ "
			case "info":
				icon = "  "
			}

			text += fmt.Sprintf("%s[%s] %s: %s (%s)\n", icon, eventType, scriptName, message, timestamp)
			if output != "" {
				text += "  output: " + output + "\n"
			}
		}
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}, DaemonOutput{Success: true, Message: text}, nil
}
