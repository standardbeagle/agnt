package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/tunnel"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// normalizePath normalizes a path for consistent comparison.
func normalizePath(path string) string {
	if path == "" || path == "." {
		return "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	// On Windows, normalize to lowercase for case-insensitive comparison.
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}

// registerAgntCommands registers agnt-specific commands with the Hub.
// This enables Hub's command dispatch to route these commands to the daemon's handlers.
// Note: Registering a command that Hub already registered will override Hub's handler.
func (d *Daemon) registerAgntCommands() {
	// PROC command - override Hub's to add URL tracking and project filtering
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "PROC",
		SubVerbs:    []string{"STATUS", "OUTPUT", "STOP", "LIST", "CLEANUP-PORT"},
		Description: "Manage running processes",
		Handler:     d.hubHandleProc,
	})

	// DETECT command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "DETECT",
		Description: "Detect project type and available scripts",
		Handler:     d.hubHandleDetect,
	})

	// PROXY command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "PROXY",
		SubVerbs:    []string{"START", "STOP", "STATUS", "LIST", "EXEC", "TOAST"},
		Description: "Manage reverse proxies",
		Handler:     d.hubHandleProxy,
	})

	// PROXYLOG command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "PROXYLOG",
		SubVerbs:    []string{"QUERY", "SUMMARY", "CLEAR", "STATS"},
		Description: "Query proxy traffic logs",
		Handler:     d.hubHandleProxyLog,
	})

	// CURRENTPAGE command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "CURRENTPAGE",
		SubVerbs:    []string{"LIST", "GET", "SUMMARY", "CLEAR"},
		Description: "View active page sessions",
		Handler:     d.hubHandleCurrentPage,
	})

	// OVERLAY command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "OVERLAY",
		SubVerbs:    []string{"SET", "GET", "CLEAR", "ACTIVITY"},
		Description: "Configure overlay endpoint",
		Handler:     d.hubHandleOverlay,
	})

	// TUNNEL command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "TUNNEL",
		SubVerbs:    []string{"START", "STOP", "STATUS", "LIST"},
		Description: "Manage tunnel connections",
		Handler:     d.hubHandleTunnel,
	})

	// CHAOS command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "CHAOS",
		SubVerbs:    []string{"ENABLE", "DISABLE", "STATUS", "PRESET", "SET", "ADD-RULE", "REMOVE-RULE", "LIST-RULES", "STATS", "CLEAR", "LIST-PRESETS"},
		Description: "Configure chaos engineering rules",
		Handler:     d.hubHandleChaos,
	})

	// SESSION command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "SESSION",
		SubVerbs:    []string{"REGISTER", "UNREGISTER", "HEARTBEAT", "LIST", "GET", "SEND", "SCHEDULE", "CANCEL", "TASKS", "FIND", "ATTACH", "URL"},
		Description: "Manage client sessions",
		Handler:     d.hubHandleSession,
	})

	// STATUS command - returns full daemon info (Hub's INFO is minimal)
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "STATUS",
		Description: "Get full daemon status and statistics",
		Handler:     d.hubHandleStatus,
	})

	log.Printf("[DEBUG] Registered %d agnt-specific commands with Hub", 10)
}

// hubHandleProc handles the PROC command (overrides Hub's built-in).
// Adds URL tracking and project-based filtering.
func (d *Daemon) hubHandleProc(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	switch cmd.SubVerb {
	case "STATUS":
		return d.hubHandleProcStatus(ctx, conn, cmd)
	case "OUTPUT":
		return d.hubHandleProcOutput(ctx, conn, cmd)
	case "STOP":
		return d.hubHandleProcStop(ctx, conn, cmd)
	case "LIST":
		return d.hubHandleProcList(ctx, conn, cmd)
	case "CLEANUP-PORT":
		return d.hubHandleProcCleanupPort(ctx, conn, cmd)
	case "":
		return conn.WriteStructuredErr(&hubproto.StructuredError{
			Code:         hubproto.ErrMissingParam,
			Message:      "action required",
			Command:      "PROC",
			Param:        "action",
			ValidActions: []string{"STATUS", "OUTPUT", "STOP", "LIST", "CLEANUP-PORT"},
		})
	default:
		return conn.WriteStructuredErr(&hubproto.StructuredError{
			Code:         hubproto.ErrInvalidAction,
			Message:      "unknown action",
			Command:      "PROC",
			Action:       cmd.SubVerb,
			ValidActions: []string{"STATUS", "OUTPUT", "STOP", "LIST", "CLEANUP-PORT"},
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
	switch filter.Stream {
	case "stdout":
		output, _ = proc.Stdout()
	case "stderr":
		output, _ = proc.Stderr()
	default:
		output, _ = proc.CombinedOutput()
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
	if err := conn.WriteChunk([]byte(outputStr)); err != nil {
		return err
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
		return conn.WriteOK(fmt.Sprintf("process %q already stopped", processID))
	}

	if err := d.hub.ProcessManager().Stop(ctx, processID); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to stop: %v", err))
	}

	return conn.WriteOK(fmt.Sprintf("process %q stopped", processID))
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
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", h, m)
}

// hubHandleDetect handles the DETECT command.
func (d *Daemon) hubHandleDetect(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	path := "."
	if len(cmd.Args) > 0 {
		path = cmd.Args[0]
	}

	proj, err := project.Detect(path)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"type":            proj.Type,
		"path":            proj.Path,
		"package_manager": proj.PackageManager,
		"scripts":         project.GetCommandNames(proj),
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	return conn.WriteJSON(data)
}

// hubHandleProxy handles the PROXY command and its sub-verbs.
func (d *Daemon) hubHandleProxy(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	switch cmd.SubVerb {
	case "START":
		return d.hubHandleProxyStart(ctx, conn, cmd)
	case "STOP":
		return d.hubHandleProxyStop(ctx, conn, cmd)
	case "STATUS":
		return d.hubHandleProxyStatus(conn, cmd)
	case "LIST":
		return d.hubHandleProxyList(conn, cmd)
	case "EXEC":
		return d.hubHandleProxyExec(conn, cmd)
	case "TOAST":
		return d.hubHandleProxyToast(conn, cmd)
	default:
		return conn.WriteStructuredErr(&hubproto.StructuredError{
			Code:         hubproto.ErrInvalidArgs,
			Message:      "unknown PROXY sub-command",
			Command:      "PROXY",
			ValidActions: []string{"START", "STOP", "STATUS", "LIST", "EXEC", "TOAST"},
		})
	}
}

// hubHandleProxyStart handles PROXY START command.
// PROXY START <id> <target_url> <port> [max_log_size]
func (d *Daemon) hubHandleProxyStart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 3 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY START requires: <id> <target_url> <port>")
	}

	proxyID := cmd.Args[0]
	targetURL := cmd.Args[1]
	port, err := strconv.Atoi(cmd.Args[2])
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid port")
	}

	maxLogSize := 1000
	if len(cmd.Args) > 3 {
		maxLogSize, _ = strconv.Atoi(cmd.Args[3])
	}

	// Parse extended config from JSON data (optional)
	path := "."
	bindAddress := ""
	publicURL := ""
	verifyTLS := false
	if len(cmd.Data) > 0 {
		var data struct {
			Path        string `json:"path"`
			BindAddress string `json:"bind_address"`
			PublicURL   string `json:"public_url"`
			VerifyTLS   bool   `json:"verify_tls"`
		}
		if err := json.Unmarshal(cmd.Data, &data); err == nil {
			if data.Path != "" {
				path = data.Path
			}
			bindAddress = data.BindAddress
			publicURL = data.PublicURL
			verifyTLS = data.VerifyTLS
		}
	}

	// Create proxy config
	proxyConfig := proxy.ProxyConfig{
		ID:          proxyID,
		TargetURL:   targetURL,
		ListenPort:  port,
		MaxLogSize:  maxLogSize,
		AutoRestart: true,
		Path:        normalizePath(path),
		BindAddress: bindAddress,
		PublicURL:   publicURL,
		VerifyTLS:   verifyTLS,
	}

	proxyServer, err := d.proxym.Create(ctx, proxyConfig)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	// Find session for this project to get session-specific overlay endpoint
	if path != "" {
		if session, ok := d.sessionRegistry.FindByDirectory(normalizePath(path)); ok && session.OverlayPath != "" {
			proxyServer.SetOverlayEndpoint(session.OverlayPath)
			log.Printf("[DEBUG] Set session-specific overlay endpoint for proxy %s: %s", proxyID, session.OverlayPath)
		} else if endpoint := d.OverlayEndpoint(); endpoint != "" {
			// Fallback to global overlay endpoint if no session found
			proxyServer.SetOverlayEndpoint(endpoint)
			log.Printf("[DEBUG] Set global overlay endpoint for proxy %s: %s", proxyID, endpoint)
		}
	} else if endpoint := d.OverlayEndpoint(); endpoint != "" {
		// Fallback to global overlay endpoint if no path specified
		proxyServer.SetOverlayEndpoint(endpoint)
		log.Printf("[DEBUG] Set global overlay endpoint for proxy %s: %s", proxyID, endpoint)
	}

	// Persist proxy config
	if d.stateMgr != nil {
		d.stateMgr.AddProxy(PersistentProxyConfig{
			ID:         proxyID,
			TargetURL:  targetURL,
			Port:       port,
			MaxLogSize: maxLogSize,
			Path:       path,
		})
	}

	resp := map[string]interface{}{
		"id":          proxyServer.ID,
		"listen_addr": proxyServer.ListenAddr,
		"target_url":  proxyServer.TargetURL.String(),
		"status":      "running",
	}
	if proxyServer.BindAddress != "" {
		resp["bind_address"] = proxyServer.BindAddress
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProxyStop handles PROXY STOP command.
func (d *Daemon) hubHandleProxyStop(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY STOP requires: <id>")
	}

	proxyID := cmd.Args[0]

	if err := d.proxym.Stop(ctx, proxyID); err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Remove from persisted state
	if d.stateMgr != nil {
		d.stateMgr.RemoveProxy(proxyID)
	}

	return conn.WriteOK("proxy stopped")
}

// hubHandleProxyStatus handles PROXY STATUS command.
func (d *Daemon) hubHandleProxyStatus(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY STATUS requires: <id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	resp := map[string]interface{}{
		"id":          p.ID,
		"listen_addr": p.ListenAddr,
		"target_url":  p.TargetURL.String(),
		"status":      "running",
		"stats":       p.Stats(),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProxyList handles PROXY LIST command.
func (d *Daemon) hubHandleProxyList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	// Parse filter from command data
	var dirFilter hubproto.DirectoryFilter
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &dirFilter)
	}

	// Resolve filter path from session code or directory
	filterPath := ""
	if !dirFilter.Global {
		if dirFilter.SessionCode != "" {
			// Look up session to get project path
			if session, ok := d.sessionRegistry.Get(dirFilter.SessionCode); ok {
				filterPath = normalizePath(session.ProjectPath)
			}
		} else if dirFilter.Directory != "" {
			filterPath = normalizePath(dirFilter.Directory)
		}
	}

	proxies := d.proxym.List()

	var result []map[string]interface{}
	for _, p := range proxies {
		proxyPath := normalizePath(p.Path)

		// Filter by path if not global and we have a filter path
		if !dirFilter.Global && filterPath != "" && filterPath != "." && proxyPath != filterPath {
			continue
		}

		result = append(result, map[string]interface{}{
			"id":          p.ID,
			"listen_addr": p.ListenAddr,
			"target_url":  p.TargetURL.String(),
			"status":      "running",
			"path":        p.Path,
		})
	}

	data, _ := json.Marshal(map[string]interface{}{
		"proxies": result,
		"count":   len(result),
	})
	return conn.WriteJSON(data)
}

// hubHandleProxyExec handles PROXY EXEC command.
func (d *Daemon) hubHandleProxyExec(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY EXEC requires: <id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Code is in the data payload
	if len(cmd.Data) == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY EXEC requires code")
	}

	code := string(cmd.Data)
	execID, resultChan, err := p.ExecuteJavaScript(code)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	// Wait for result with timeout
	timeout := 30 * time.Second
	select {
	case result := <-resultChan:
		if result == nil {
			return conn.WriteErr(hubproto.ErrInternal, "execution channel closed")
		}

		resp := map[string]interface{}{
			"execution_id": execID,
			"success":      result.Error == "",
			"result":       result.Result,
			"error":        result.Error,
			"duration":     result.Duration.String(),
		}

		data, _ := json.Marshal(resp)
		return conn.WriteJSON(data)

	case <-time.After(timeout):
		return conn.WriteErr(hubproto.ErrTimeout, "execution timed out")
	}
}

// hubHandleProxyToast handles PROXY TOAST command.
func (d *Daemon) hubHandleProxyToast(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY TOAST requires: <id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Toast config is in the data payload
	if len(cmd.Data) == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY TOAST requires toast config")
	}

	var toast struct {
		Message  string `json:"toast_message"`
		Type     string `json:"toast_type"`
		Title    string `json:"toast_title"`
		Duration int    `json:"toast_duration"`
	}
	if err := json.Unmarshal(cmd.Data, &toast); err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid toast config: "+err.Error())
	}

	if toast.Type == "" {
		toast.Type = "info"
	}
	if toast.Message == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "toast_message is required")
	}

	sentCount, err := p.BroadcastToast(toast.Type, toast.Title, toast.Message, toast.Duration)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"success":    true,
		"sent_count": sentCount,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProxyLog handles the PROXYLOG command and its sub-verbs.
func (d *Daemon) hubHandleProxyLog(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	switch cmd.SubVerb {
	case "QUERY", "":
		return d.hubHandleProxyLogQuery(conn, cmd)
	case "SUMMARY":
		return d.hubHandleProxyLogSummary(conn, cmd)
	case "CLEAR":
		return d.hubHandleProxyLogClear(conn, cmd)
	case "STATS":
		return d.hubHandleProxyLogStats(conn, cmd)
	default:
		return conn.WriteStructuredErr(&hubproto.StructuredError{
			Code:         hubproto.ErrInvalidArgs,
			Message:      "unknown PROXYLOG sub-command",
			Command:      "PROXYLOG",
			ValidActions: []string{"QUERY", "SUMMARY", "CLEAR", "STATS"},
		})
	}
}

// hubHandleProxyLogQuery handles PROXYLOG QUERY command.
func (d *Daemon) hubHandleProxyLogQuery(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXYLOG requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	var filter proxy.LogFilter
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &filter)
	}

	entries := p.Logger().Query(filter)

	data, _ := json.Marshal(map[string]interface{}{"logs": entries})
	return conn.WriteJSON(data)
}

// hubHandleProxyLogSummary handles PROXYLOG SUMMARY command.
func (d *Daemon) hubHandleProxyLogSummary(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXYLOG SUMMARY requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// For summary, we return stats plus recent entries
	stats := p.Logger().Stats()

	resp := map[string]interface{}{
		"stats": stats,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProxyLogClear handles PROXYLOG CLEAR command.
func (d *Daemon) hubHandleProxyLogClear(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXYLOG CLEAR requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	p.Logger().Clear()
	return conn.WriteOK("logs cleared")
}

// hubHandleProxyLogStats handles PROXYLOG STATS command.
func (d *Daemon) hubHandleProxyLogStats(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXYLOG STATS requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	stats := p.Logger().Stats()

	data, _ := json.Marshal(stats)
	return conn.WriteJSON(data)
}

// hubHandleCurrentPage handles the CURRENTPAGE command.
func (d *Daemon) hubHandleCurrentPage(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	switch cmd.SubVerb {
	case "LIST", "":
		return d.hubHandleCurrentPageList(conn, cmd)
	case "GET":
		return d.hubHandleCurrentPageGet(conn, cmd)
	case "SUMMARY":
		return d.hubHandleCurrentPageSummary(conn, cmd)
	case "CLEAR":
		return d.hubHandleCurrentPageClear(conn, cmd)
	default:
		return conn.WriteStructuredErr(&hubproto.StructuredError{
			Code:         hubproto.ErrInvalidArgs,
			Message:      "unknown CURRENTPAGE sub-command",
			Command:      "CURRENTPAGE",
			ValidActions: []string{"LIST", "GET", "SUMMARY", "CLEAR"},
		})
	}
}

// hubHandleCurrentPageList handles CURRENTPAGE LIST command.
func (d *Daemon) hubHandleCurrentPageList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CURRENTPAGE LIST requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	sessions := p.PageTracker().GetActiveSessions()

	resp := map[string]interface{}{
		"sessions": sessions,
		"count":    len(sessions),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleCurrentPageGet handles CURRENTPAGE GET command.
func (d *Daemon) hubHandleCurrentPageGet(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 2 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CURRENTPAGE GET requires: <proxy_id> <session_id>")
	}

	proxyID := cmd.Args[0]
	sessionID := cmd.Args[1]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	session, ok := p.PageTracker().GetSession(sessionID)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, "session not found")
	}

	data, _ := json.Marshal(session)
	return conn.WriteJSON(data)
}

// hubHandleCurrentPageSummary handles CURRENTPAGE SUMMARY command.
func (d *Daemon) hubHandleCurrentPageSummary(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 2 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CURRENTPAGE SUMMARY requires: <proxy_id> <session_id>")
	}

	proxyID := cmd.Args[0]
	sessionID := cmd.Args[1]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	session, ok := p.PageTracker().GetSession(sessionID)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, "session not found")
	}

	// Return a summary of the session
	data, _ := json.Marshal(session)
	return conn.WriteJSON(data)
}

// hubHandleCurrentPageClear handles CURRENTPAGE CLEAR command.
func (d *Daemon) hubHandleCurrentPageClear(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CURRENTPAGE CLEAR requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	p.PageTracker().Clear()
	return conn.WriteOK("page sessions cleared")
}

// hubHandleOverlay handles the OVERLAY command.
func (d *Daemon) hubHandleOverlay(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	switch cmd.SubVerb {
	case "SET":
		return d.hubHandleOverlaySet(conn, cmd)
	case "GET", "":
		return d.hubHandleOverlayGet(conn)
	case "CLEAR":
		return d.hubHandleOverlayClear(conn)
	case "ACTIVITY":
		return d.hubHandleOverlayActivity(conn)
	default:
		return conn.WriteStructuredErr(&hubproto.StructuredError{
			Code:         hubproto.ErrInvalidArgs,
			Message:      "unknown OVERLAY sub-command",
			Command:      "OVERLAY",
			ValidActions: []string{"SET", "GET", "CLEAR", "ACTIVITY"},
		})
	}
}

// hubHandleOverlaySet handles OVERLAY SET command.
func (d *Daemon) hubHandleOverlaySet(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	var config struct {
		Endpoint string `json:"endpoint"`
	}

	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &config)
	}

	if config.Endpoint == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "endpoint is required")
	}

	d.SetOverlayEndpoint(config.Endpoint)
	return conn.WriteOK("overlay endpoint set")
}

// hubHandleOverlayGet handles OVERLAY GET command.
func (d *Daemon) hubHandleOverlayGet(conn *hubpkg.Connection) error {
	endpoint := d.OverlayEndpoint()

	resp := map[string]interface{}{
		"endpoint": endpoint,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleOverlayClear handles OVERLAY CLEAR command.
func (d *Daemon) hubHandleOverlayClear(conn *hubpkg.Connection) error {
	d.SetOverlayEndpoint("")
	return conn.WriteOK("overlay endpoint cleared")
}

// hubHandleOverlayActivity handles OVERLAY ACTIVITY command (heartbeat).
func (d *Daemon) hubHandleOverlayActivity(conn *hubpkg.Connection) error {
	// This is a heartbeat from the overlay to keep the connection alive
	return conn.WriteOK("activity recorded")
}

// hubHandleTunnel handles the TUNNEL command.
func (d *Daemon) hubHandleTunnel(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	switch cmd.SubVerb {
	case "START":
		return d.hubHandleTunnelStart(ctx, conn, cmd)
	case "STOP":
		return d.hubHandleTunnelStop(ctx, conn, cmd)
	case "STATUS":
		return d.hubHandleTunnelStatus(conn, cmd)
	case "LIST":
		return d.hubHandleTunnelList(conn)
	default:
		return conn.WriteStructuredErr(&hubproto.StructuredError{
			Code:         hubproto.ErrInvalidArgs,
			Message:      "unknown TUNNEL sub-command",
			Command:      "TUNNEL",
			ValidActions: []string{"START", "STOP", "STATUS", "LIST"},
		})
	}
}

// hubHandleTunnelStart handles TUNNEL START command.
func (d *Daemon) hubHandleTunnelStart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "TUNNEL START requires: <id>")
	}

	tunnelID := cmd.Args[0]

	var config struct {
		Provider   string `json:"provider"`
		LocalPort  int    `json:"local_port"`
		LocalHost  string `json:"local_host"`
		ProxyID    string `json:"proxy_id"`
		BinaryPath string `json:"binary_path"`
	}

	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &config)
	}

	if config.Provider == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "provider is required")
	}
	if config.LocalPort == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "local_port is required")
	}

	tunnelConfig := tunnel.Config{
		Provider:   tunnel.Provider(config.Provider),
		LocalPort:  config.LocalPort,
		LocalHost:  config.LocalHost,
		BinaryPath: config.BinaryPath,
	}

	t, err := d.tunnelm.Start(ctx, tunnelID, tunnelConfig)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	// Wait for public URL
	publicURL, err := t.WaitForURL(ctx)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("tunnel started but failed to get URL: %v", err))
	}

	// Update proxy public URL if proxy_id specified
	if config.ProxyID != "" {
		if p, err := d.proxym.Get(config.ProxyID); err == nil {
			p.SetPublicURL(publicURL)
		}
	}

	resp := map[string]interface{}{
		"id":         tunnelID,
		"provider":   config.Provider,
		"local_port": config.LocalPort,
		"public_url": publicURL,
		"status":     "running",
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleTunnelStop handles TUNNEL STOP command.
func (d *Daemon) hubHandleTunnelStop(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "TUNNEL STOP requires: <id>")
	}

	tunnelID := cmd.Args[0]

	if err := d.tunnelm.Stop(ctx, tunnelID); err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	return conn.WriteOK("tunnel stopped")
}

// hubHandleTunnelStatus handles TUNNEL STATUS command.
func (d *Daemon) hubHandleTunnelStatus(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "TUNNEL STATUS requires: <id>")
	}

	tunnelID := cmd.Args[0]

	t, ok := d.tunnelm.Get(tunnelID)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, tunnelID)
	}

	info := t.Info()
	resp := map[string]interface{}{
		"id":         tunnelID,
		"provider":   string(info.Provider),
		"state":      info.State,
		"public_url": info.PublicURL,
		"local_addr": info.LocalAddr,
	}
	if info.Error != "" {
		resp["error"] = info.Error
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleTunnelList handles TUNNEL LIST command.
func (d *Daemon) hubHandleTunnelList(conn *hubpkg.Connection) error {
	infos := d.tunnelm.List()

	entries := make([]map[string]interface{}, len(infos))
	for i, info := range infos {
		entry := map[string]interface{}{
			"provider":   string(info.Provider),
			"state":      info.State,
			"public_url": info.PublicURL,
			"local_addr": info.LocalAddr,
		}
		if info.Error != "" {
			entry["error"] = info.Error
		}
		entries[i] = entry
	}

	data, _ := json.Marshal(map[string]interface{}{"tunnels": entries})
	return conn.WriteJSON(data)
}

// hubHandleChaos handles the CHAOS command.
func (d *Daemon) hubHandleChaos(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	switch cmd.SubVerb {
	case "ENABLE":
		return d.hubHandleChaosEnable(conn, cmd)
	case "DISABLE":
		return d.hubHandleChaosDisable(conn, cmd)
	case "STATUS":
		return d.hubHandleChaosStatus(conn, cmd)
	case "PRESET":
		return d.hubHandleChaosPreset(conn, cmd)
	case "SET":
		return d.hubHandleChaosSet(conn, cmd)
	case "ADD-RULE":
		return d.hubHandleChaosAddRule(conn, cmd)
	case "REMOVE-RULE":
		return d.hubHandleChaosRemoveRule(conn, cmd)
	case "LIST-RULES":
		return d.hubHandleChaosListRules(conn, cmd)
	case "STATS":
		return d.hubHandleChaosStats(conn, cmd)
	case "CLEAR":
		return d.hubHandleChaosClear(conn, cmd)
	case "LIST-PRESETS":
		return d.hubHandleChaosListPresets(conn)
	default:
		return conn.WriteStructuredErr(&hubproto.StructuredError{
			Code:         hubproto.ErrInvalidArgs,
			Message:      "unknown CHAOS sub-command",
			Command:      "CHAOS",
			ValidActions: []string{"ENABLE", "DISABLE", "STATUS", "PRESET", "SET", "ADD-RULE", "REMOVE-RULE", "LIST-RULES", "STATS", "CLEAR", "LIST-PRESETS"},
		})
	}
}

// hubHandleChaosEnable handles CHAOS ENABLE command.
func (d *Daemon) hubHandleChaosEnable(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS ENABLE requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	p.ChaosEngine().Enable()
	return conn.WriteOK("chaos enabled")
}

// hubHandleChaosDisable handles CHAOS DISABLE command.
func (d *Daemon) hubHandleChaosDisable(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS DISABLE requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	p.ChaosEngine().Disable()
	return conn.WriteOK("chaos disabled")
}

// hubHandleChaosStatus handles CHAOS STATUS command.
func (d *Daemon) hubHandleChaosStatus(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS STATUS requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	engine := p.ChaosEngine()
	config := engine.GetConfig()

	resp := map[string]interface{}{
		"enabled": engine.IsEnabled(),
		"config":  config,
		"stats":   engine.GetStats(),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleChaosPreset handles CHAOS PRESET command.
func (d *Daemon) hubHandleChaosPreset(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS PRESET requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	var config struct {
		Preset string `json:"chaos_preset"`
	}
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &config)
	}

	if config.Preset == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "chaos_preset is required")
	}

	presetConfig := proxy.GetPreset(config.Preset)
	if presetConfig == nil {
		availablePresets := proxy.ListPresets()
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("unknown preset %q. Available: %s", config.Preset, strings.Join(availablePresets, ", ")))
	}

	if err := p.ChaosEngine().SetConfig(presetConfig); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	return conn.WriteOK(fmt.Sprintf("preset %s applied", config.Preset))
}

// hubHandleChaosSet handles CHAOS SET command.
func (d *Daemon) hubHandleChaosSet(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS SET requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	var config proxy.ChaosConfig
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &config)
	}

	if err := p.ChaosEngine().SetConfig(&config); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	return conn.WriteOK("chaos config set")
}

// hubHandleChaosAddRule handles CHAOS ADD-RULE command.
func (d *Daemon) hubHandleChaosAddRule(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS ADD-RULE requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	var wrapper struct {
		Rule proxy.ChaosRule `json:"chaos_rule"`
	}
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &wrapper)
	}

	if wrapper.Rule.ID == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "rule id is required")
	}

	if err := p.ChaosEngine().AddRule(&wrapper.Rule); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	return conn.WriteOK("rule added")
}

// hubHandleChaosRemoveRule handles CHAOS REMOVE-RULE command.
func (d *Daemon) hubHandleChaosRemoveRule(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS REMOVE-RULE requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	var config struct {
		RuleID string `json:"chaos_rule_id"`
	}
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &config)
	}

	if config.RuleID == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "chaos_rule_id is required")
	}

	p.ChaosEngine().RemoveRule(config.RuleID)
	return conn.WriteOK("rule removed")
}

// hubHandleChaosListRules handles CHAOS LIST-RULES command.
func (d *Daemon) hubHandleChaosListRules(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS LIST-RULES requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	config := p.ChaosEngine().GetConfig()
	var rules []*proxy.ChaosRule
	if config != nil {
		rules = config.Rules
	}
	if rules == nil {
		rules = []*proxy.ChaosRule{}
	}

	data, _ := json.Marshal(map[string]interface{}{"rules": rules})
	return conn.WriteJSON(data)
}

// hubHandleChaosStats handles CHAOS STATS command.
func (d *Daemon) hubHandleChaosStats(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS STATS requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	stats := p.ChaosEngine().GetStats()

	data, _ := json.Marshal(stats)
	return conn.WriteJSON(data)
}

// hubHandleChaosClear handles CHAOS CLEAR command.
func (d *Daemon) hubHandleChaosClear(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CHAOS CLEAR requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.proxym.Get(proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	p.ChaosEngine().Clear()
	return conn.WriteOK("chaos cleared")
}

// hubHandleChaosListPresets handles CHAOS LIST-PRESETS command.
func (d *Daemon) hubHandleChaosListPresets(conn *hubpkg.Connection) error {
	presets := proxy.ListPresets()

	data, _ := json.Marshal(map[string]interface{}{"presets": presets})
	return conn.WriteJSON(data)
}

// hubHandleSession handles the SESSION command.
func (d *Daemon) hubHandleSession(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	switch cmd.SubVerb {
	case "REGISTER":
		return d.hubHandleSessionRegister(conn, cmd)
	case "UNREGISTER":
		return d.hubHandleSessionUnregister(conn, cmd)
	case "HEARTBEAT":
		return d.hubHandleSessionHeartbeat(conn, cmd)
	case "LIST":
		return d.hubHandleSessionList(conn, cmd)
	case "GET":
		return d.hubHandleSessionGet(conn, cmd)
	case "SEND":
		return d.hubHandleSessionSend(conn, cmd)
	case "SCHEDULE":
		return d.hubHandleSessionSchedule(conn, cmd)
	case "CANCEL":
		return d.hubHandleSessionCancel(conn, cmd)
	case "TASKS":
		return d.hubHandleSessionTasks(conn, cmd)
	case "FIND":
		return d.hubHandleSessionFind(conn, cmd)
	case "ATTACH":
		return d.hubHandleSessionAttach(conn, cmd)
	case "URL":
		return d.hubHandleSessionURL(conn, cmd)
	default:
		return conn.WriteStructuredErr(&hubproto.StructuredError{
			Code:         hubproto.ErrInvalidArgs,
			Message:      "unknown SESSION sub-command",
			Command:      "SESSION",
			ValidActions: []string{"REGISTER", "UNREGISTER", "HEARTBEAT", "LIST", "GET", "SEND", "SCHEDULE", "CANCEL", "TASKS", "FIND", "ATTACH", "URL"},
		})
	}
}

// hubHandleSessionRegister handles SESSION REGISTER command.
// SESSION REGISTER <code> <overlay_path> -- <json_metadata>
func (d *Daemon) hubHandleSessionRegister(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 2 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION REGISTER requires: <code> <overlay_path>")
	}

	code := cmd.Args[0]
	overlayPath := cmd.Args[1]

	// Parse optional metadata from data payload
	var metadata struct {
		ProjectPath string   `json:"project_path"`
		Command     string   `json:"command"`
		Args        []string `json:"args"`
	}
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &metadata)
	}

	// Create session
	session := &Session{
		Code:        code,
		OverlayPath: overlayPath,
		ProjectPath: normalizePath(metadata.ProjectPath),
		Command:     metadata.Command,
		Args:        metadata.Args,
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}

	if err := d.sessionRegistry.Register(session); err != nil {
		return conn.WriteErr(hubproto.ErrAlreadyExists, err.Error())
	}

	// Associate session with this connection for cleanup
	conn.SetSessionCode(code)

	// Run autostart for this project
	autostartResult := d.RunAutostart(context.Background(), metadata.ProjectPath)

	resp := map[string]interface{}{
		"code":      code,
		"autostart": autostartResult,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionUnregister handles SESSION UNREGISTER command.
// SESSION UNREGISTER <code>
func (d *Daemon) hubHandleSessionUnregister(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION UNREGISTER requires: <code>")
	}

	code := cmd.Args[0]

	// Clean up session resources (processes, proxies) before unregistering
	d.CleanupSessionResources(code)

	return conn.WriteOK(fmt.Sprintf("session %s unregistered", code))
}

// hubHandleSessionHeartbeat handles SESSION HEARTBEAT command.
// SESSION HEARTBEAT <code>
func (d *Daemon) hubHandleSessionHeartbeat(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION HEARTBEAT requires: <code>")
	}

	code := cmd.Args[0]

	if err := d.sessionRegistry.Heartbeat(code); err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	return conn.WriteOK("heartbeat received")
}

// hubHandleSessionList handles SESSION LIST command.
// SESSION LIST [-- <directory_filter_json>]
func (d *Daemon) hubHandleSessionList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	var filter struct {
		Directory string `json:"directory"`
		Global    bool   `json:"global"`
	}

	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &filter)
	}

	sessions := d.sessionRegistry.List(normalizePath(filter.Directory), filter.Global)

	// Convert to response format
	sessionList := make([]map[string]interface{}, 0, len(sessions))
	for _, s := range sessions {
		sessionList = append(sessionList, s.ToJSON())
	}

	resp := map[string]interface{}{
		"sessions":  sessionList,
		"count":     len(sessionList),
		"directory": filter.Directory,
		"global":    filter.Global,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionGet handles SESSION GET command.
// SESSION GET <code>
func (d *Daemon) hubHandleSessionGet(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION GET requires: <code>")
	}

	code := cmd.Args[0]

	session, ok := d.sessionRegistry.Get(code)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session %q not found", code))
	}

	data, _ := json.Marshal(session.ToJSON())
	return conn.WriteJSON(data)
}

// hubHandleSessionSend handles SESSION SEND command.
// SESSION SEND <code> -- <message>
func (d *Daemon) hubHandleSessionSend(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION SEND requires: <code>")
	}
	if len(cmd.Data) == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION SEND requires message data")
	}

	code := cmd.Args[0]
	message := string(cmd.Data)

	// Get session
	session, ok := d.sessionRegistry.Get(code)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session %q not found", code))
	}

	if session.GetStatus() != SessionStatusActive {
		return conn.WriteErr(hubproto.ErrInvalidState, fmt.Sprintf("session %q is not active", code))
	}

	// Send message to overlay
	if err := d.sendMessageToOverlay(session.OverlayPath, message); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to send message: %v", err))
	}

	resp := map[string]interface{}{
		"success":      true,
		"session_code": code,
		"message_len":  len(message),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionSchedule handles SESSION SCHEDULE command.
// SESSION SCHEDULE <code> <duration> -- <message>
func (d *Daemon) hubHandleSessionSchedule(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 2 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION SCHEDULE requires: <code> <duration>")
	}
	if len(cmd.Data) == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION SCHEDULE requires message data")
	}

	code := cmd.Args[0]
	durationStr := cmd.Args[1]
	message := string(cmd.Data)

	// Parse duration
	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid duration %q: %v", durationStr, err))
	}

	// Get session to determine project path
	session, ok := d.sessionRegistry.Get(code)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session %q not found", code))
	}

	// Schedule the task
	task, err := d.scheduler.Schedule(code, duration, message, session.ProjectPath)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to schedule: %v", err))
	}

	resp := map[string]interface{}{
		"task_id":      task.ID,
		"session_code": code,
		"deliver_at":   task.DeliverAt.Format(time.RFC3339),
		"message_len":  len(message),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionCancel handles SESSION CANCEL command.
// SESSION CANCEL <task_id>
func (d *Daemon) hubHandleSessionCancel(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION CANCEL requires: <task_id>")
	}

	taskID := cmd.Args[0]

	if err := d.scheduler.Cancel(taskID); err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	return conn.WriteOK(fmt.Sprintf("task %s cancelled", taskID))
}

// hubHandleSessionTasks handles SESSION TASKS command.
// SESSION TASKS [-- <directory_filter_json>]
func (d *Daemon) hubHandleSessionTasks(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	var filter struct {
		Directory string `json:"directory"`
		Global    bool   `json:"global"`
	}

	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &filter)
	}

	tasks := d.scheduler.ListTasks(normalizePath(filter.Directory), filter.Global)

	// Convert to response format
	taskList := make([]map[string]interface{}, 0, len(tasks))
	for _, t := range tasks {
		taskList = append(taskList, t.ToJSON())
	}

	resp := map[string]interface{}{
		"tasks":     taskList,
		"count":     len(taskList),
		"directory": filter.Directory,
		"global":    filter.Global,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionFind handles SESSION FIND command.
// SESSION FIND <directory>
func (d *Daemon) hubHandleSessionFind(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION FIND requires: <directory>")
	}

	directory := cmd.Args[0]

	session, found := d.sessionRegistry.FindByDirectory(directory)
	if !found {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("no active session found for directory %q or its parents", directory))
	}

	data, _ := json.Marshal(session.ToJSON())
	return conn.WriteJSON(data)
}

// hubHandleSessionAttach handles SESSION ATTACH command.
// SESSION ATTACH <directory>
func (d *Daemon) hubHandleSessionAttach(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION ATTACH requires: <directory>")
	}

	directory := cmd.Args[0]

	session, found := d.sessionRegistry.FindByDirectory(directory)
	if !found {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("no active session found for directory %q or its parents", directory))
	}

	// Associate this connection with the session
	conn.SetSessionCode(session.Code)

	resp := map[string]interface{}{
		"attached":     true,
		"session_code": session.Code,
		"project_path": session.ProjectPath,
		"command":      session.Command,
		"started_at":   session.StartedAt.Format(time.RFC3339),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionURL handles SESSION URL command.
// Reports a detected URL from an agnt run session, triggering proxy creation.
// SESSION URL <code> <url> -- {"script": "dev"}
func (d *Daemon) hubHandleSessionURL(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 2 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION URL requires: <code> <url>")
	}

	code := cmd.Args[0]
	detectedURL := cmd.Args[1]

	// Get session
	session, ok := d.sessionRegistry.Get(code)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session %q not found", code))
	}

	// Parse script name from data payload (default to "dev")
	scriptName := "dev"
	if len(cmd.Data) > 0 {
		var data struct {
			Script string `json:"script"`
		}
		if err := json.Unmarshal(cmd.Data, &data); err == nil && data.Script != "" {
			scriptName = data.Script
		}
	}

	// Construct scriptID in the format: {basename}:{scriptName}
	scriptID := makeProcessID(session.ProjectPath, scriptName)

	// Send proxy event to trigger proxy creation
	select {
	case d.proxyEvents <- ProxyEvent{
		Type:     URLDetected,
		ScriptID: scriptID,
		URL:      detectedURL,
		Path:     session.ProjectPath,
	}:
		// Event queued successfully
	default:
		return conn.WriteErr(hubproto.ErrInternal, "proxy event queue full")
	}

	resp := map[string]interface{}{
		"success":      true,
		"session_code": code,
		"url":          detectedURL,
		"script":       scriptName,
		"script_id":    scriptID,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// sendMessageToOverlay sends a message to an overlay socket.
func (d *Daemon) sendMessageToOverlay(socketPath string, message string) error {
	// Create HTTP client that connects via Unix socket
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
	}

	// Build request body
	body := bytes.NewBufferString(message)

	// POST to /inject endpoint
	req, err := http.NewRequest("POST", "http://unix/inject", body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send to overlay: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("overlay returned status %d", resp.StatusCode)
	}

	return nil
}

// hubHandleStatus handles the STATUS command.
// Returns full daemon info (Hub's built-in INFO only returns minimal data).
func (d *Daemon) hubHandleStatus(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	info := d.Info()
	data, err := json.Marshal(info)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}
	return conn.WriteJSON(data)
}
