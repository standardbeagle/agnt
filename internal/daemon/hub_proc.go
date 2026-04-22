package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleProc(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	valid := []string{"RUN", "STATUS", "OUTPUT", "STOP", "RESTART", "LIST", "CLEANUP-PORT", "AUTORESTART"}
	return newCommandRouter("PROC").
		withDefault(func(_ context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
			return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
				Code:         hubproto.ErrInvalidAction,
				Message:      "unknown action",
				Command:      "PROC",
				Action:       cmd.SubVerb,
				ValidActions: valid,
			})
		}).
		dispatch(ctx, conn, cmd, map[string]handlerFn{
			"RUN":          d.hubHandleProcRun,
			"STATUS":       d.hubHandleProcStatus,
			"OUTPUT":       d.hubHandleProcOutput,
			"STOP":         d.hubHandleProcStop,
			"RESTART":      d.hubHandleProcRestart,
			"LIST":         d.hubHandleProcList,
			"CLEANUP-PORT": d.hubHandleProcCleanupPort,
			"AUTORESTART":  d.hubHandleProcAutoRestart,
			"": func(_ context.Context, conn *hubpkg.Connection, _ *hubproto.Command) error {
				return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
					Code:         hubproto.ErrMissingParam,
					Message:      "action required",
					Command:      "PROC",
					Param:        "action",
					ValidActions: valid,
				})
			},
		})
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

	// Surface the last known death record so a simple status call
	// reveals "never started" vs "died at T with code C".
	if info, ok := d.processExitInfo.Get(processID); ok {
		exitInfoToResponse(resp, info)
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
	filter, err := unmarshalCommand[hubproto.OutputFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
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
	dirFilter, err := unmarshalCommand[hubproto.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
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
		// Surface death records inline so proc list shows dead processes
		// at a glance.
		if info, ok := d.processExitInfo.Get(p.ID); ok {
			exitInfoToResponse(entry, info)
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
		// Mark this stop as daemon-initiated so the OutageClassifier
		// treats the upcoming outage as a Rebuild rather than a Crash.
		d.healthTracker.MarkDaemonInitiatedStop(processID)
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
		config := DefaultAutoRestartConfig()
		config.Enabled = true
		if payload, err := unmarshalCommand[struct {
			MaxRestarts    int     `json:"max_restarts"`
			OnlyOnError    bool    `json:"only_on_error"`
			RestartDelayMs float64 `json:"restart_delay_ms"`
		}](cmd); err == nil {
			if payload.MaxRestarts > 0 {
				config.MaxRestarts = payload.MaxRestarts
			}
			config.OnlyOnError = payload.OnlyOnError
			if payload.RestartDelayMs > 0 {
				config.RestartDelay = time.Duration(payload.RestartDelayMs) * time.Millisecond
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

// procRunPayload shapes the JSON body for PROC RUN. Fields mirror
// config.ScriptConfig — the handler builds a *config.ScriptConfig from
// this payload and delegates to StartScriptExplicit. Keeping the wire
// shape separate from config.ScriptConfig so KDL-only fields (e.g.
// AutostartFlag) don't leak into the MCP contract.
type procRunPayload struct {
	Run         string            `json:"run,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	URLMatchers []string          `json:"url_matchers,omitempty"`
	AutoRestart bool              `json:"auto_restart,omitempty"`
	ProjectPath string            `json:"project_path,omitempty"`
}

// hubHandleProcRun handles PROC RUN <name>.
//
// Thin wrapper over StartScriptExplicit — the MCP PROC RUN path and the
// autostart path share the same per-script wire-up (scriptConfigs cache,
// scriptRegistry.Register, command resolution, StartScript + state
// transitions). Before T4 the only MCP tool path that started processes
// was the top-level RUN verb, which bypassed the script registry and
// left MCP-started processes invisible to the overlay admin screen
// (SCRIPT LIST shows registry entries). PROC RUN fills that gap: the
// new process appears as a process-kind entry in SCRIPT LIST exactly
// like an autostart script.
//
// Project path resolution: prefer the JSON payload's ProjectPath; fall
// back to the connection's session ProjectPath. If neither is set the
// handler errors — StartScriptExplicit requires a non-empty projectPath
// to key scriptConfigs / scriptRegistry entries.
func (d *Daemon) hubHandleProcRun(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "PROC RUN requires: <name>")
	}

	name := cmd.Args[0]
	if name == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROC RUN: name must not be empty")
	}

	payload, err := unmarshalCommand[procRunPayload](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid PROC RUN payload: %v", err))
	}

	if payload.Run == "" && payload.Command == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "PROC RUN requires `run` or `command` in payload")
	}

	// Resolve project path: explicit override > session binding.
	projectPath := payload.ProjectPath
	if projectPath == "" {
		if session, ok := d.sessionRegistry.Get(conn.SessionCode()); ok {
			projectPath = session.ProjectPath
		}
	}
	if projectPath == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "PROC RUN: project_path required (or attach session with ProjectPath)")
	}
	projectPath = normalizePath(projectPath)

	scriptCfg := &config.ScriptConfig{
		Run:         payload.Run,
		Command:     payload.Command,
		Args:        payload.Args,
		Cwd:         payload.Cwd,
		Env:         payload.Env,
		URLMatchers: payload.URLMatchers,
		AutoRestart: payload.AutoRestart,
	}

	// Respect caller cancellation before we touch the registry.
	select {
	case <-ctx.Done():
		return conn.WriteErr(hubproto.ErrInternal, ctx.Err().Error())
	default:
	}

	// StartScriptExplicit is synchronous; on return the process is
	// registered and either running or a startup_error entry carries
	// the detail. proxyConfigs=nil — MCP ad-hoc processes have no
	// linked proxy config; port resolution falls back to command-line
	// / PORT env scanning in getExpectedPortsForScript.
	if err := d.StartScriptExplicit(ctx, name, scriptCfg, projectPath, nil); err != nil {
		debug.Warn("daemon", "PROC RUN %q failed: %v", name, err)
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	processID := makeProcessID(projectPath, name)

	resp := map[string]interface{}{
		"name":         name,
		"process_id":   processID,
		"project_path": projectPath,
	}

	// Surface PID when the ProcessManager knows about the new process.
	// StartScriptExplicit delegates to StartScript which registers into
	// ProcessManager synchronously, so the lookup should succeed for a
	// successfully started process. A miss is not fatal — the caller
	// already has process_id and can poll PROC STATUS.
	if proc, getErr := d.hub.ProcessManager().Get(processID); getErr == nil {
		resp["pid"] = proc.PID()
		resp["state"] = proc.State().String()
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProxyRestart handles PROXY RESTART <id>.
// Stops a proxy and restarts it with the same configuration.
