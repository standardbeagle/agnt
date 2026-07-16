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
	"github.com/standardbeagle/agnt/internal/protocol"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) procActions() map[string]handlerFn {
	actions := map[string]handlerFn{
		"RUN":          d.hubHandleProcRun,
		"RUN-GROUP":    d.hubHandleProcRunGroup,
		"STATUS":       d.hubHandleProcStatus,
		"OUTPUT":       d.hubHandleProcOutput,
		"STOP":         d.hubHandleProcStop,
		"RESTART":      d.hubHandleProcRestart,
		"LIST":         d.hubHandleProcList,
		"CLEANUP-PORT": d.hubHandleProcCleanupPort,
		"AUTORESTART":  d.hubHandleProcAutoRestart,
	}
	valid := routerSubVerbs(actions)
	actions[""] = func(_ context.Context, conn *hubpkg.Connection, _ *hubproto.Command) error {
		return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
			Code:         hubproto.ErrMissingParam,
			Message:      "action required",
			Command:      "PROC",
			Param:        "action",
			ValidActions: valid,
		})
	}
	return actions
}

func (d *Daemon) hubHandleProc(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	actions := d.procActions()
	return newCommandRouter("PROC").
		withDefault(func(_ context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
			return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
				Code:         hubproto.ErrInvalidAction,
				Message:      "unknown action",
				Command:      "PROC",
				Action:       cmd.SubVerb,
				ValidActions: routerSubVerbs(actions),
			})
		}).
		dispatch(ctx, conn, cmd, actions)
}

// hubHandleProcStatus handles PROC STATUS <id>.

func (d *Daemon) hubHandleProcStatus(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "process_id required")
	}

	processID := cmd.Args[0]
	proc, err := d.hub.ProcessManager().Get(processID)
	if err != nil {
		// ProcessManager miss might mean the process is still pending
		// on dependencies — check the pending tracker before returning
		// NotFound. A pending process has a registry entry but no
		// ManagedProcess yet (StartScriptExplicit hasn't run).
		if pending, ok := d.pendingProcs.Get(processID); ok {
			resp := pendingStatusResponse(pending)
			data, _ := json.Marshal(resp)
			return conn.WriteJSON(data)
		}
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

	// Surface in-flight `waiting_for` if the process has been deferred
	// pending dep resolution. This handles the brief window where the
	// pending entry hasn't been removed yet (between dep-ready and
	// StartScriptExplicit's ProcessManager.Register call).
	if pending, ok := d.pendingProcs.Get(processID); ok && len(pending.WaitingFor) > 0 {
		resp["waiting_for"] = pending.WaitingFor
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

// pendingStatusResponse formats a PROC STATUS response for a process
// that is still waiting on dependencies (no ManagedProcess yet). The
// response shape mirrors the ManagedProcess response so clients can
// consume both paths uniformly.
//
// State is "pending" while waiting; "failed" once a dep timed out and
// the entry hasn't been GC'd yet. waiting_for lists the unresolved deps
// in alphabetical order.
func pendingStatusResponse(p PendingProcess) map[string]interface{} {
	state := "pending"
	if p.State == PendingFailed {
		state = "failed"
	}
	resp := map[string]interface{}{
		"id":           p.ProcessID,
		"command":      p.Command,
		"state":        state,
		"summary":      "waiting for dependencies",
		"project_path": p.ProjectPath,
	}
	if len(p.WaitingFor) > 0 {
		resp["waiting_for"] = p.WaitingFor
	}
	if p.FailureReason != "" {
		resp["failure_reason"] = p.FailureReason
	}
	return resp
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
		debug.Warn("daemon", "process %q output was truncated (stream=%s)", processID, filter.Stream)
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
		d.retireIncidentProcessOwner(processID)
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
	d.retireIncidentProcessOwner(processID)

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
	dirFilter, err := unmarshalCommand[protocol.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
	}

	// Route through the mandatory session-scope chokepoint: one gate for
	// every non-debug list/query (see resolveProjectScope). A non-global
	// query with no resolvable project fails loud rather than leaking all
	// projects' processes.
	projectPath, global, err := d.resolveProjectScope(dirFilter, conn.SessionCode())
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}
	sessionCode := dirFilter.SessionCode
	if sessionCode == "" {
		sessionCode = conn.SessionCode()
	}
	filteredProcs := procs

	// Filter processes by project path
	if !global && projectPath != "" {
		normalizedDir := normalizePath(projectPath)
		var filtered []*goprocess.ManagedProcess
		for _, p := range procs {
			if normalizePath(p.ProjectPath) == normalizedDir {
				filtered = append(filtered, p)
			}
		}
		filteredProcs = filtered
	}

	entries := make([]map[string]interface{}, 0, len(filteredProcs))
	var warnings []string
	// seenProcessIDs tracks which IDs are present as ManagedProcess entries
	// so we don't duplicate a pending entry for the same ID once it has
	// transitioned out of pending.
	seenProcessIDs := make(map[string]bool, len(filteredProcs))
	for _, p := range filteredProcs {
		seenProcessIDs[p.ID] = true
		entry := map[string]interface{}{
			"id":           p.ID,
			"command":      p.Command,
			"state":        p.State().String(),
			"summary":      p.Summary(),
			"runtime":      formatDuration(p.Runtime()),
			"runtime_ms":   p.Runtime().Milliseconds(),
			"project_path": p.ProjectPath,
		}
		// Surface in-flight waiting_for if the process is briefly still
		// in the pending tracker (between dep-ready and ProcessManager
		// registration).
		if pending, ok := d.pendingProcs.Get(p.ID); ok && len(pending.WaitingFor) > 0 {
			entry["waiting_for"] = pending.WaitingFor
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
		entries = append(entries, entry)
	}

	// Merge in pending processes that don't yet have a ManagedProcess
	// entry. This is the "depends_on gate is still closed" case — the
	// agent needs to see them in PROC LIST so `waiting_for` is visible
	// before any process actually launches.
	var pendingFilter string
	if !global && projectPath != "" {
		pendingFilter = normalizePath(projectPath)
	}
	for _, pending := range d.pendingProcs.ListByProject(pendingFilter) {
		if seenProcessIDs[pending.ProcessID] {
			continue // already represented by a ManagedProcess entry
		}
		entries = append(entries, pendingStatusResponse(pending))
	}

	resp := map[string]interface{}{
		"count":           len(entries),
		"processes":       entries,
		"global":          global,
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

	pids, protected, err := killPortHoldersGuarded(ctx, d.hub.ProcessManager(), port)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}
	if len(protected) > 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs,
			fmt.Sprintf("port %d is held by the daemon or a managed process (PIDs %v); use proc stop for managed processes", port, protected))
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
	env := proc.Env
	projectPath := proc.ProjectPath
	workingDir := proc.WorkingDir

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
			d.recordStartupEntry(processID, "", "warning", "stop_failed",
				fmt.Sprintf("PROC RESTART: failed to stop process: %v", err), 0)
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
	d.retireIncidentProcessOwner(processID)

	// Start with EADDRINUSE recovery (pre-flight cleanup + startup monitoring)
	newProc, startupErr := d.startScriptWithRetry(ctx, processID, projectPath, workingDir, command, args, env, []int{expectedPort}, false)
	if startupErr != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to restart: %v", startupErr))
	}
	d.registerIncidentProcessOwner(processID, conn.SessionCode())

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
	// DependsOn lists script names the process must wait for before
	// launching. Each name is resolved to a daemon process ID via
	// makeProcessID(projectPath, name). Empty (or omitted) means the
	// process starts immediately.
	DependsOn []string `json:"depends_on,omitempty"`
	// DependsOnTimeout is the per-process upper bound on the dep wait
	// in seconds. Zero (or omitted) means use DefaultDependsOnTimeout
	// (30s). Negative means wait indefinitely (parent ctx only).
	DependsOnTimeout int `json:"depends_on_timeout,omitempty"`
}

// procRunGroupPayload shapes the JSON body for PROC RUN-GROUP. Each
// entry corresponds to a single process; cycle detection runs on the
// `depends_on` graph BEFORE any process is launched.
type procRunGroupPayload struct {
	ProjectPath string `json:"project_path,omitempty"`
	// DependsOnTimeout applies to every process in the group as the
	// per-process default when a process doesn't override it. Zero
	// means use DefaultDependsOnTimeout.
	DependsOnTimeout int            `json:"depends_on_timeout,omitempty"`
	Processes        []GroupProcess `json:"processes"`
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

	// Translate the wire-level int seconds into time.Duration.
	// 0 → DefaultDependsOnTimeout, negative → unbounded.
	var depTimeout time.Duration
	switch {
	case payload.DependsOnTimeout < 0:
		depTimeout = -1 * time.Second // any negative → unbounded path
	case payload.DependsOnTimeout > 0:
		depTimeout = time.Duration(payload.DependsOnTimeout) * time.Second
	default:
		depTimeout = 0 // sentinel for "use default"
	}

	// StartProcessWithDeps handles both fast-path (no deps, synchronous)
	// and dep-gated (async wait + StartScriptExplicit) cases. proxyConfigs
	// is implicitly nil — MCP ad-hoc processes have no linked proxy config;
	// port resolution falls back to command-line / PORT env scanning in
	// getExpectedPortsForScript.
	result := d.StartProcessWithDeps(ctx, name, scriptCfg, projectPath, payload.DependsOn, depTimeout)
	if result.Err != nil {
		debug.Warn("daemon", "PROC RUN %q failed: %v", name, result.Err)
		return conn.WriteErr(hubproto.ErrInternal, result.Err.Error())
	}

	processID := result.ProcessID
	d.registerIncidentProcessOwner(processID, conn.SessionCode())

	resp := map[string]interface{}{
		"name":         name,
		"process_id":   processID,
		"project_path": projectPath,
		"state":        result.State,
	}
	if len(result.WaitingFor) > 0 {
		resp["waiting_for"] = result.WaitingFor
	}

	// Surface PID when the ProcessManager knows about the new process.
	// For pending processes the ProcessManager entry doesn't exist yet
	// (start happens after deps resolve); the caller can poll PROC STATUS
	// once `waiting_for` empties.
	if result.State == "starting" {
		if proc, getErr := d.hub.ProcessManager().Get(processID); getErr == nil {
			resp["pid"] = proc.PID()
			// Override the "starting" hint from result.State with the
			// authoritative ProcessManager state once we have it.
			resp["state"] = proc.State().String()
		}
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProcRunGroup handles PROC RUN-GROUP.
//
// Cycle detection runs BEFORE any process is launched. On detection
// the call returns ErrInvalidArgs with the cycle description; no
// process is started. Per-process kickoff results are returned in
// declaration order; agents poll PROC STATUS to observe individual
// processes transitioning from "pending" → "starting" → "running".
//
// Project path resolution mirrors PROC RUN: explicit payload override
// > session binding. Empty project path is fatal.
func (d *Daemon) hubHandleProcRunGroup(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	payload, err := unmarshalCommand[procRunGroupPayload](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid PROC RUN-GROUP payload: %v", err))
	}

	if len(payload.Processes) == 0 {
		return conn.WriteErr(hubproto.ErrMissingParam, "PROC RUN-GROUP: processes list cannot be empty")
	}

	projectPath := payload.ProjectPath
	if projectPath == "" {
		if session, ok := d.sessionRegistry.Get(conn.SessionCode()); ok {
			projectPath = session.ProjectPath
		}
	}
	if projectPath == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "PROC RUN-GROUP: project_path required (or attach session with ProjectPath)")
	}
	projectPath = normalizePath(projectPath)

	// Translate per-group default timeout (int seconds) to Duration.
	var depTimeout time.Duration
	switch {
	case payload.DependsOnTimeout < 0:
		depTimeout = -1 * time.Second
	case payload.DependsOnTimeout > 0:
		depTimeout = time.Duration(payload.DependsOnTimeout) * time.Second
	default:
		depTimeout = 0
	}

	groupResult := d.StartProcessGroup(ctx, projectPath, payload.Processes, depTimeout)
	if groupResult.Err != nil {
		// Cycle detection / validation failures land here. Use
		// ErrInvalidArgs so clients distinguish "your config is wrong"
		// from "internal failure".
		return conn.WriteErr(hubproto.ErrInvalidArgs, groupResult.Err.Error())
	}

	procs := make([]map[string]interface{}, 0, len(groupResult.Processes))
	for _, r := range groupResult.Processes {
		entry := map[string]interface{}{
			"process_id": r.ProcessID,
			"state":      r.State,
		}
		if len(r.WaitingFor) > 0 {
			entry["waiting_for"] = r.WaitingFor
		}
		if r.Err != nil {
			entry["error"] = r.Err.Error()
		}
		procs = append(procs, entry)
	}

	resp := map[string]interface{}{
		"project_path": projectPath,
		"count":        len(procs),
		"processes":    procs,
	}
	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProxyRestart handles PROXY RESTART <id>.
// Stops a proxy and restarts it with the same configuration.
