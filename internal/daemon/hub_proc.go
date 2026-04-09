package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleProc(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "PROC %s: args=%v", cmd.SubVerb, cmd.Args)
	switch cmd.SubVerb {
	case "STATUS":
		return d.hubHandleProcStatus(ctx, conn, cmd)
	case "OUTPUT":
		return d.hubHandleProcOutput(ctx, conn, cmd)
	case "STOP":
		return d.hubHandleProcStop(ctx, conn, cmd)
	case "RESTART":
		return d.hubHandleProcRestart(ctx, conn, cmd)
	case "LIST":
		return d.hubHandleProcList(ctx, conn, cmd)
	case "CLEANUP-PORT":
		return d.hubHandleProcCleanupPort(ctx, conn, cmd)
	case "AUTORESTART":
		return d.hubHandleProcAutoRestart(ctx, conn, cmd)
	case "":
		return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
			Code:         hubproto.ErrMissingParam,
			Message:      "action required",
			Command:      "PROC",
			Param:        "action",
			ValidActions: []string{"STATUS", "OUTPUT", "STOP", "RESTART", "LIST", "CLEANUP-PORT", "AUTORESTART"},
		})
	default:
		return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
			Code:         hubproto.ErrInvalidAction,
			Message:      "unknown action",
			Command:      "PROC",
			Action:       cmd.SubVerb,
			ValidActions: []string{"STATUS", "OUTPUT", "STOP", "RESTART", "LIST", "CLEANUP-PORT", "AUTORESTART"},
		})
	}
}

// hubHandleProcStatus handles PROC STATUS <id>.

func (d *Daemon) hubHandleProcStatus(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "process_id required")
	}

	processID := cmd.Args[0]
	proc, err := d.hub.ProcessManager().Get(processID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("process %q not found", processID))
	}

	resp := map[string]interface{}{
		"id":           proc.ID,
		"command":      proc.Command,
		"args":         proc.Args,
		"state":        proc.State().String(),
		"summary":      proc.Summary(),
		"runtime":      formatDuration(proc.Runtime()),
		"runtime_ms":   proc.Runtime().Milliseconds(),
		"project_path": proc.ProjectPath,
	}

	if pid := proc.PID(); pid > 0 {
		resp["pid"] = pid
	}
	if proc.State().String() == "stopped" || proc.State().String() == "failed" {
		resp["exit_code"] = proc.ExitCode()
	}

	// Add URLs from URL tracker
	if urls := d.urlTracker.GetURLs(processID); len(urls) > 0 {
		resp["urls"] = urls
	}

	// Check for rogue process using the same port
	if rogueInfo := d.detectRogueProcess(ctx, proc); rogueInfo != nil && rogueInfo.HasWarning {
		resp["warning"] = fmt.Sprintf(
			"Port %d is in use by unmanaged process (PID %v). "+
				"Run 'proc {action: \"restart\", process_id: \"%s\"}' to kill the rogue process and restart properly.",
			rogueInfo.Port, rogueInfo.PIDs, proc.ID)
		resp["rogue_process"] = map[string]interface{}{
			"port": rogueInfo.Port,
			"pids": rogueInfo.PIDs,
		}
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProcOutput handles PROC OUTPUT <id> [filter].

func (d *Daemon) hubHandleProcOutput(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "process_id required")
	}

	processID := cmd.Args[0]
	proc, err := d.hub.ProcessManager().Get(processID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("process %q not found", processID))
	}

	// Parse optional filter from JSON data
	var filter hubproto.OutputFilter
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &filter); err != nil {
			return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
		}
	}

	var output []byte
	var truncated bool
	switch filter.Stream {
	case "stdout":
		output, truncated = proc.Stdout()
	case "stderr":
		output, truncated = proc.Stderr()
	default:
		output, truncated = proc.CombinedOutput()
	}
	if truncated {
		debug.Warn("process %q output was truncated (stream=%s)", processID, filter.Stream)
	}

	// Prepend restart event delimiters if this process was auto-restarted
	if d.autoRestarter != nil {
		if events := d.autoRestarter.GetRestartEvents(processID); len(events) > 0 {
			delimiter := FormatRestartDelimiter(events)
			output = append([]byte(delimiter), output...)
		}
	}

	// Apply filters
	lines := strings.Split(string(output), "\n")
	var filtered []string

	for _, line := range lines {
		if filter.Grep != "" {
			match := strings.Contains(line, filter.Grep)
			if filter.GrepV {
				match = !match
			}
			if !match {
				continue
			}
		}
		filtered = append(filtered, line)
	}

	// Apply head/tail limits
	if filter.Head > 0 && len(filtered) > filter.Head {
		filtered = filtered[:filter.Head]
	}
	if filter.Tail > 0 && len(filtered) > filter.Tail {
		filtered = filtered[len(filtered)-filter.Tail:]
	}

	// Return output as chunked response (client expects CHUNK + END for .String())
	outputStr := strings.Join(filtered, "\n")
	if truncated {
		notice := "[WARNING: output was truncated due to buffer size limit]\n"
		if err := conn.WriteChunk([]byte(notice)); err != nil {
			return err
		}
	}
	if len(outputStr) > 0 {
		if err := conn.WriteChunk([]byte(outputStr)); err != nil {
			return err
		}
	}
	return conn.WriteEnd()
}

// hubHandleProcStop handles PROC STOP <id>.

func (d *Daemon) hubHandleProcStop(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "process_id required")
	}

	processID := cmd.Args[0]
	proc, err := d.hub.ProcessManager().Get(processID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("process %q not found", processID))
	}

	if !proc.IsRunning() {
		resp := map[string]interface{}{
			"process_id": processID,
			"state":      proc.State().String(),
			"success":    true,
			"message":    fmt.Sprintf("process %q already stopped", processID),
		}
		data, _ := json.Marshal(resp)
		return conn.WriteJSON(data)
	}

	// Unregister from auto-restart before stopping to prevent the monitor
	// from restarting the process after an explicit user stop.
	if d.autoRestarter != nil {
		d.autoRestarter.Unregister(processID)
	}

	if err := d.hub.ProcessManager().Stop(ctx, processID); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to stop: %v", err))
	}

	resp := map[string]interface{}{
		"process_id": processID,
		"state":      "stopped",
		"success":    true,
		"message":    fmt.Sprintf("process %q stopped", processID),
	}
	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProcList handles PROC LIST [filter].

func (d *Daemon) hubHandleProcList(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	procs := d.hub.ProcessManager().List()

	// Parse directory filter from JSON data (optional)
	var dirFilter hubproto.DirectoryFilter
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &dirFilter); err != nil {
			return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
		}
	}

	// Resolve the project path for filtering
	var projectPath string
	var sessionCode string
	filteredProcs := procs

	if dirFilter.Global {
		// No filtering - return all processes
	} else if dirFilter.SessionCode != "" {
		sessionCode = dirFilter.SessionCode
		session, ok := d.sessionRegistry.Get(sessionCode)
		if !ok {
			return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session %q not found", sessionCode))
		}
		projectPath = session.ProjectPath
	} else if dirFilter.Directory != "" {
		projectPath = dirFilter.Directory
	} else if connSession := conn.SessionCode(); connSession != "" {
		sessionCode = connSession
		session, ok := d.sessionRegistry.Get(sessionCode)
		if ok {
			projectPath = session.ProjectPath
		}
	}

	// Filter processes by project path
	if !dirFilter.Global && projectPath != "" {
		normalizedDir := normalizePath(projectPath)
		var filtered []*goprocess.ManagedProcess
		for _, p := range procs {
			if normalizePath(p.ProjectPath) == normalizedDir {
				filtered = append(filtered, p)
			}
		}
		filteredProcs = filtered
	}

	entries := make([]map[string]interface{}, len(filteredProcs))
	var warnings []string
	for i, p := range filteredProcs {
		entry := map[string]interface{}{
			"id":           p.ID,
			"command":      p.Command,
			"state":        p.State().String(),
			"summary":      p.Summary(),
			"runtime":      formatDuration(p.Runtime()),
			"runtime_ms":   p.Runtime().Milliseconds(),
			"project_path": p.ProjectPath,
		}
		// Add URLs from URL tracker
		if urls := d.urlTracker.GetURLs(p.ID); len(urls) > 0 {
			entry["urls"] = urls
		}
		// Check for rogue process using the same port
		if rogueInfo := d.detectRogueProcess(ctx, p); rogueInfo != nil && rogueInfo.HasWarning {
			warning := fmt.Sprintf(
				"Process %q shows as %s but port %d is in use by unmanaged process (PID %v). "+
					"Run 'proc {action: \"restart\", process_id: \"%s\"}' to kill the rogue process and restart properly.",
				p.ID, p.State().String(), rogueInfo.Port, rogueInfo.PIDs, p.ID)
			entry["warning"] = warning
			entry["rogue_process"] = map[string]interface{}{
				"port": rogueInfo.Port,
				"pids": rogueInfo.PIDs,
			}
			warnings = append(warnings, warning)
		}
		entries[i] = entry
	}

	resp := map[string]interface{}{
		"count":           len(filteredProcs),
		"processes":       entries,
		"global":          dirFilter.Global,
		"total_in_daemon": len(procs),
	}
	if projectPath != "" {
		resp["project_path"] = normalizePath(projectPath)
	}
	if sessionCode != "" {
		resp["session_code"] = sessionCode
	}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProcCleanupPort handles PROC CLEANUP-PORT <port>.

func (d *Daemon) hubHandleProcCleanupPort(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "port required")
	}

	port, err := strconv.Atoi(cmd.Args[0])
	if err != nil || port <= 0 || port > 65535 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid port number")
	}

	pids, err := d.hub.ProcessManager().KillProcessByPort(ctx, port)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"port":         port,
		"killed_count": len(pids),
		"killed_pids":  pids,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// formatDuration formats a duration for display.

func (d *Daemon) hubHandleProcRestart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "process_id required")
	}

	processID := cmd.Args[0]

	// Get the process to capture its config
	proc, err := d.hub.ProcessManager().Get(processID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("process %q not found", processID))
	}

	// Capture config before stopping
	command := proc.Command
	args := proc.Args
	projectPath := proc.ProjectPath

	// Check if process is in a restartable state
	state := proc.State().String()
	if state != "running" && state != "stopped" && state != "failed" {
		return conn.WriteErr(hubproto.ErrInvalidState, fmt.Sprintf("process %q is in state %s, cannot restart", processID, state))
	}

	// Capture auto-restart state before stopping so we can re-register after restart.
	wasAutoRestart := d.autoRestarter != nil && d.autoRestarter.IsRegistered(processID)

	// Unregister from auto-restart before stopping to prevent the monitor from
	// racing with this explicit restart.
	if d.autoRestarter != nil {
		d.autoRestarter.Unregister(processID)
	}

	// Stop the process if running
	if state == "running" {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := d.hub.ProcessManager().Stop(stopCtx, processID); err != nil {
			debug.Warn("daemon", "error stopping process %s: %v", processID, err)
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: processID,
				Level:     "warning",
				EventType: "stop_failed",
				Message:   fmt.Sprintf("PROC RESTART: failed to stop process: %v", err),
				Timestamp: time.Now(),
			})
		}
		// Wait for process to fully stop
		time.Sleep(100 * time.Millisecond)
	}

	// Detect expected port for EADDRINUSE recovery
	expectedPort := d.getExpectedPortForProcess(proc)

	// Clear URL tracker state so new process output is scanned fresh
	d.urlTracker.ClearProcess(processID)

	// Remove the old process registration
	d.hub.ProcessManager().RemoveByPath(processID, projectPath)

	// Start with EADDRINUSE recovery (pre-flight cleanup + startup monitoring)
	// Use projectPath as workingDir since we don't have separate WorkingDir stored
	newProc, startupErr := d.startScriptWithRetry(ctx, processID, projectPath, projectPath, command, args, nil, []int{expectedPort})
	if startupErr != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to restart: %v", startupErr))
	}

	// Re-register auto-restart if it was previously explicitly enabled
	if wasAutoRestart && d.autoRestarter != nil {
		restartConfig := DefaultAutoRestartConfig()
		restartConfig.Enabled = true // Preserve the explicit enable
		d.autoRestarter.Register(processID, restartConfig, command, args, nil, []int{expectedPort}, projectPath, projectPath)
	}

	resp := map[string]interface{}{
		"id":           processID,
		"process_id":   processID,
		"command":      command,
		"args":         args,
		"project_path": projectPath,
		"state":        newProc.State().String(),
		"pid":          newProc.PID(),
		"restarted":    true,
		"success":      true,
		"message":      fmt.Sprintf("Process %q restarted successfully", processID),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProcAutoRestart handles PROC AUTORESTART <id> [enable|disable|status].
// Enables or disables automatic restart for a process when it exits.

func (d *Daemon) hubHandleProcAutoRestart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "process_id required")
	}

	processID := cmd.Args[0]
	action := "enable" // default action
	if len(cmd.Args) > 1 {
		action = cmd.Args[1]
	}

	// Get the process
	proc, err := d.hub.ProcessManager().Get(processID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("process %q not found", processID))
	}

	switch action {
	case "enable":
		// Parse optional config from JSON payload in cmd.Data
		config := DefaultAutoRestartConfig()
		if len(cmd.Data) > 0 {
			var payload map[string]interface{}
			if err := json.Unmarshal(cmd.Data, &payload); err == nil {
				if maxRestarts, ok := payload["max_restarts"].(float64); ok {
					config.MaxRestarts = int(maxRestarts)
				}
				if onlyOnError, ok := payload["only_on_error"].(bool); ok {
					config.OnlyOnError = onlyOnError
				}
				if delayMs, ok := payload["restart_delay_ms"].(float64); ok {
					config.RestartDelay = time.Duration(delayMs) * time.Millisecond
				}
			}
		}

		// Register for auto-restart
		d.autoRestarter.Register(processID, config, proc.Command, proc.Args, nil, nil, proc.ProjectPath, proc.WorkingDir)

		resp := map[string]interface{}{
			"id":            processID,
			"auto_restart":  true,
			"max_restarts":  config.MaxRestarts,
			"only_on_error": config.OnlyOnError,
			"restart_delay": config.RestartDelay.String(),
			"message":       fmt.Sprintf("Auto-restart enabled for process %q", processID),
		}
		data, _ := json.Marshal(resp)
		return conn.WriteJSON(data)

	case "disable":
		d.autoRestarter.Unregister(processID)

		resp := map[string]interface{}{
			"id":           processID,
			"auto_restart": false,
			"message":      fmt.Sprintf("Auto-restart disabled for process %q", processID),
		}
		data, _ := json.Marshal(resp)
		return conn.WriteJSON(data)

	case "status":
		config, enabled := d.autoRestarter.GetConfig(processID)
		resp := map[string]interface{}{
			"id":           processID,
			"auto_restart": enabled,
		}
		if enabled {
			resp["max_restarts"] = config.MaxRestarts
			resp["only_on_error"] = config.OnlyOnError
			resp["restart_delay"] = config.RestartDelay.String()
		}
		data, _ := json.Marshal(resp)
		return conn.WriteJSON(data)

	default:
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("unknown autorestart action %q, valid: enable, disable, status", action))
	}
}

// hubHandleProxyRestart handles PROXY RESTART <id>.
// Stops a proxy and restarts it with the same configuration.
