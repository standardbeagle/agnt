package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/process"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/tunnel"
)

// normalizePath returns a cleaned absolute path for consistent comparison.
// This ensures paths from different sources (CLI, MCP) match correctly.
func normalizePath(path string) string {
	if path == "" || path == "." {
		return "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

// handleDetect handles the DETECT command.
func (c *Connection) handleDetect(cmd *protocol.Command) error {
	path := "."
	if len(cmd.Args) > 0 {
		path = cmd.Args[0]
	}

	proj, err := project.Detect(path)
	if err != nil {
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	// Build response
	resp := map[string]interface{}{
		"type":            proj.Type,
		"path":            proj.Path,
		"package_manager": proj.PackageManager,
		"scripts":         project.GetCommandNames(proj),
	}

	data, err := json.Marshal(resp)
	if err != nil {
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	return c.writeJSON(data)
}

// handleRun handles the RUN and RUN-JSON commands.
// Implements idempotent start: if process with same ID+Path is running, returns it;
// if stopped/failed, cleans up and starts new; if port conflict, kills blocker and retries.
func (c *Connection) handleRun(ctx context.Context, cmd *protocol.Command) error {
	var config protocol.RunConfig

	if cmd.Verb == protocol.VerbRunJSON {
		// Parse JSON config from data payload
		if len(cmd.Data) == 0 {
			return c.writeErr(protocol.ErrInvalidArgs, "RUN-JSON requires JSON data")
		}
		if err := json.Unmarshal(cmd.Data, &config); err != nil {
			return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("invalid JSON: %v", err))
		}
	} else {
		// Parse from args: RUN <id> <path> <mode> <command> [args...]
		if len(cmd.Args) < 4 {
			return c.writeErr(protocol.ErrInvalidArgs, "RUN requires: <id> <path> <mode> <command> [args...]")
		}
		config = protocol.RunConfig{
			ID:      cmd.Args[0],
			Path:    cmd.Args[1],
			Mode:    cmd.Args[2],
			Raw:     true,
			Command: cmd.Args[3],
			Args:    cmd.Args[4:],
		}
	}

	// Validate mode
	switch config.Mode {
	case "background", "foreground", "foreground-raw":
		// Valid
	case "":
		config.Mode = "background"
	default:
		return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("invalid mode: %s", config.Mode))
	}

	// Resolve command
	var command string
	var args []string

	if config.Raw {
		if config.Command == "" {
			return c.writeErr(protocol.ErrInvalidArgs, "raw mode requires command")
		}
		command = config.Command
		args = config.Args
	} else {
		// Script mode: look up from project
		if config.ScriptName == "" {
			return c.writeErr(protocol.ErrInvalidArgs, "script_name required (or use raw=true)")
		}

		proj, err := project.Detect(config.Path)
		if err != nil {
			return c.writeErr(protocol.ErrInternal, fmt.Sprintf("project detection failed: %v", err))
		}

		cmdDef := project.GetCommandByName(proj, config.ScriptName)
		if cmdDef == nil {
			available := project.GetCommandNames(proj)
			return c.writeErr(protocol.ErrNotFound, fmt.Sprintf("unknown script %q. Available: %s", config.ScriptName, strings.Join(available, ", ")))
		}

		command = cmdDef.Command
		args = append(cmdDef.Args, config.Args...)
	}

	// Generate ID if not provided
	id := config.ID
	if id == "" {
		if config.ScriptName != "" {
			id = config.ScriptName
		} else {
			id = fmt.Sprintf("proc-%d", time.Now().UnixNano()%100000)
		}
	}

	// Use StartOrReuse for idempotent behavior
	result, err := c.daemon.pm.StartOrReuse(ctx, process.ProcessConfig{
		ID:          id,
		ProjectPath: config.Path,
		Command:     command,
		Args:        args,
	})
	if err != nil {
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	proc := result.Process

	// Background mode: return immediately
	if config.Mode == "background" {
		resp := map[string]interface{}{
			"process_id": proc.ID,
			"pid":        proc.PID(),
			"command":    command + " " + strings.Join(args, " "),
		}
		if result.Reused {
			resp["reused"] = true
			resp["state"] = proc.State().String()
		}
		if result.Cleaned {
			resp["cleaned_previous"] = true
		}
		if result.PortRetried {
			resp["port_conflict_resolved"] = true
			resp["ports_cleared"] = result.PortsCleared
		}
		if result.PortError != "" {
			resp["port_error"] = result.PortError
		}
		data, _ := json.Marshal(resp)
		return c.writeJSON(data)
	}

	// Foreground modes: wait for completion
	// If reusing a running process, just check current state
	if result.Reused && proc.IsDone() {
		// Process already finished
	} else if !result.Reused {
		// Wait for new process to complete
		select {
		case <-proc.Done():
			// Process completed
		case <-ctx.Done():
			// Context cancelled
			c.daemon.pm.StopProcess(ctx, proc)
			return c.writeErr(protocol.ErrTimeout, "process cancelled")
		}
	} else {
		// Reused and still running - wait for it
		select {
		case <-proc.Done():
			// Process completed
		case <-ctx.Done():
			return c.writeErr(protocol.ErrTimeout, "process cancelled")
		}
	}

	resp := map[string]interface{}{
		"process_id": proc.ID,
		"pid":        proc.PID(),
		"command":    command + " " + strings.Join(args, " "),
		"exit_code":  proc.ExitCode(),
		"state":      proc.State().String(),
		"runtime":    formatDuration(proc.Runtime()),
	}
	if result.Reused {
		resp["reused"] = true
	}
	if result.Cleaned {
		resp["cleaned_previous"] = true
	}
	if result.PortRetried {
		resp["port_conflict_resolved"] = true
		resp["ports_cleared"] = result.PortsCleared
	}
	if result.PortError != "" {
		resp["port_error"] = result.PortError
	}

	// Include output for foreground-raw mode
	if config.Mode == "foreground-raw" {
		stdout, _ := proc.Stdout()
		stderr, _ := proc.Stderr()
		resp["stdout"] = string(stdout)
		resp["stderr"] = string(stderr)
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

// Valid actions for each command (for structured error responses)
var (
	validProcActions        = []string{"STATUS", "OUTPUT", "STOP", "LIST", "CLEANUP-PORT"}
	validProxyActions       = []string{"START", "STOP", "STATUS", "LIST", "EXEC", "TOAST"}
	validProxyLogActions    = []string{"QUERY", "CLEAR", "STATS"}
	validCurrentPageActions = []string{"LIST", "GET", "CLEAR"}
)

// handleProc handles the PROC command.
func (c *Connection) handleProc(ctx context.Context, cmd *protocol.Command) error {
	if cmd.SubVerb == "" && len(cmd.Args) > 0 {
		cmd.SubVerb = strings.ToUpper(cmd.Args[0])
		cmd.Args = cmd.Args[1:]
	}

	switch cmd.SubVerb {
	case protocol.SubVerbStatus:
		return c.handleProcStatus(cmd)
	case protocol.SubVerbOutput:
		return c.handleProcOutput(cmd)
	case protocol.SubVerbStop:
		return c.handleProcStop(ctx, cmd)
	case protocol.SubVerbList:
		return c.handleProcList(cmd)
	case protocol.SubVerbCleanupPort:
		return c.handleProcCleanupPort(ctx, cmd)
	case "":
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrMissingParam,
			Message:      "action required",
			Command:      "PROC",
			Param:        "action",
			ValidActions: validProcActions,
		})
	default:
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrInvalidAction,
			Message:      "unknown action",
			Command:      "PROC",
			Action:       cmd.SubVerb,
			ValidActions: validProcActions,
		})
	}
}

func (c *Connection) handleProcStatus(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROC STATUS requires process_id")
	}

	proc, err := c.daemon.pm.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	resp := map[string]interface{}{
		"process_id": proc.ID,
		"state":      proc.State().String(),
		"summary":    proc.Summary(),
		"exit_code":  proc.ExitCode(),
		"runtime":    formatDuration(proc.Runtime()),
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleProcOutput(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROC OUTPUT requires process_id")
	}

	proc, err := c.daemon.pm.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	// Parse optional filters from args
	filter := parseOutputFilter(cmd.Args[1:])

	// Get output
	var data []byte

	switch filter.Stream {
	case "stdout":
		data, _ = proc.Stdout()
	case "stderr":
		data, _ = proc.Stderr()
	default:
		data, _ = proc.CombinedOutput()
	}

	// Apply filters
	output := string(data)
	lines := strings.Split(output, "\n")

	// Grep filter
	if filter.Grep != "" {
		re, err := regexp.Compile(filter.Grep)
		if err != nil {
			return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("invalid grep pattern: %v", err))
		}
		var filtered []string
		for _, line := range lines {
			matches := re.MatchString(line)
			if (matches && !filter.GrepV) || (!matches && filter.GrepV) {
				filtered = append(filtered, line)
			}
		}
		lines = filtered
	}

	// Head filter
	if filter.Head > 0 && len(lines) > filter.Head {
		lines = lines[:filter.Head]
	}

	// Tail filter
	if filter.Tail > 0 && len(lines) > filter.Tail {
		lines = lines[len(lines)-filter.Tail:]
	}

	output = strings.Join(lines, "\n")

	// Return as chunked response for consistency
	if err := c.writeChunk([]byte(output)); err != nil {
		return err
	}
	return c.writeEnd()
}

func (c *Connection) handleProcStop(ctx context.Context, cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROC STOP requires process_id")
	}

	processID := cmd.Args[0]
	force := len(cmd.Args) > 1 && cmd.Args[1] == "force"

	proc, err := c.daemon.pm.Get(processID)
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, processID)
	}

	stopCtx := ctx
	if force {
		var cancel context.CancelFunc
		stopCtx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
	}

	c.daemon.pm.StopProcess(stopCtx, proc)

	resp := map[string]interface{}{
		"process_id": proc.ID,
		"state":      proc.State().String(),
		"success":    true,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleProcList(cmd *protocol.Command) error {
	procs := c.daemon.pm.List()

	// Parse directory filter from JSON data (optional)
	var dirFilter protocol.DirectoryFilter
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &dirFilter); err != nil {
			return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
		}
	}

	// Default to current directory if not specified
	directory := dirFilter.Directory
	if directory == "" {
		directory = "."
	}

	// Normalize the filter directory for consistent comparison
	normalizedDir := normalizePath(directory)

	// Filter processes by directory unless global flag is set
	filteredProcs := procs
	if !dirFilter.Global {
		var filtered []*process.ManagedProcess
		for _, p := range procs {
			// Normalize process path for comparison
			if normalizePath(p.ProjectPath) == normalizedDir {
				filtered = append(filtered, p)
			}
		}
		filteredProcs = filtered
	}

	entries := make([]map[string]interface{}, len(filteredProcs))
	for i, p := range filteredProcs {
		entries[i] = map[string]interface{}{
			"id":           p.ID,
			"command":      p.Command,
			"state":        p.State().String(),
			"summary":      p.Summary(),
			"runtime":      formatDuration(p.Runtime()),
			"project_path": p.ProjectPath,
		}
	}

	resp := map[string]interface{}{
		"count":                 len(filteredProcs),
		"processes":             entries,
		"directory":             normalizedDir,
		"global":                dirFilter.Global,
		"total_in_daemon":       len(procs),
		"filtered_by_directory": !dirFilter.Global,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleProcCleanupPort(ctx context.Context, cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROC CLEANUP-PORT requires port number")
	}

	port, err := strconv.Atoi(cmd.Args[0])
	if err != nil || port <= 0 || port > 65535 {
		return c.writeErr(protocol.ErrInvalidArgs, "invalid port number")
	}

	pids, err := c.daemon.pm.KillProcessByPort(ctx, port)
	if err != nil {
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"killed_pids": pids,
		"success":     true,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

// handleProxy handles the PROXY command.
func (c *Connection) handleProxy(ctx context.Context, cmd *protocol.Command) error {
	if cmd.SubVerb == "" && len(cmd.Args) > 0 {
		cmd.SubVerb = strings.ToUpper(cmd.Args[0])
		cmd.Args = cmd.Args[1:]
	}

	switch cmd.SubVerb {
	case protocol.SubVerbStart:
		return c.handleProxyStart(ctx, cmd)
	case protocol.SubVerbStop:
		return c.handleProxyStop(ctx, cmd)
	case protocol.SubVerbStatus:
		return c.handleProxyStatus(cmd)
	case protocol.SubVerbList:
		return c.handleProxyList(cmd)
	case protocol.SubVerbExec:
		return c.handleProxyExec(cmd)
	case protocol.SubVerbToast:
		return c.handleProxyToast(cmd)
	case "":
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrMissingParam,
			Message:      "action required",
			Command:      "PROXY",
			Param:        "action",
			ValidActions: validProxyActions,
		})
	default:
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrInvalidAction,
			Message:      "unknown action",
			Command:      "PROXY",
			Action:       cmd.SubVerb,
			ValidActions: validProxyActions,
		})
	}
}

func (c *Connection) handleProxyStart(ctx context.Context, cmd *protocol.Command) error {
	// PROXY START <id> <target_url> <port> [max_log_size]
	if len(cmd.Args) < 3 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROXY START requires: <id> <target_url> <port>")
	}

	id := cmd.Args[0]
	targetURL := cmd.Args[1]
	port, err := strconv.Atoi(cmd.Args[2])
	if err != nil {
		return c.writeErr(protocol.ErrInvalidArgs, "invalid port")
	}

	maxLogSize := 1000
	if len(cmd.Args) > 3 {
		maxLogSize, _ = strconv.Atoi(cmd.Args[3])
	}

	// Parse extended config from JSON data (optional)
	path := "."
	bindAddress := ""
	publicURL := ""
	var tunnelConfig *protocol.TunnelConfig
	if len(cmd.Data) > 0 {
		var data struct {
			Path        string                 `json:"path"`
			BindAddress string                 `json:"bind_address"`
			PublicURL   string                 `json:"public_url"`
			Tunnel      *protocol.TunnelConfig `json:"tunnel"`
		}
		if err := json.Unmarshal(cmd.Data, &data); err == nil {
			if data.Path != "" {
				path = data.Path
			}
			bindAddress = data.BindAddress
			publicURL = data.PublicURL
			tunnelConfig = data.Tunnel
		}
	}

	config := proxy.ProxyConfig{
		ID:          id,
		TargetURL:   targetURL,
		ListenPort:  port,
		MaxLogSize:  maxLogSize,
		AutoRestart: true,
		Path:        path,
		BindAddress: bindAddress,
		PublicURL:   publicURL,
		Tunnel:      tunnelConfig,
	}

	proxyServer, err := c.daemon.proxym.Create(context.Background(), config)
	if err != nil {
		if err == proxy.ErrProxyExists {
			return c.writeErr(protocol.ErrAlreadyExists, id)
		}
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	// Configure overlay endpoint for event forwarding
	overlayEndpoint := c.daemon.OverlayEndpoint()
	if overlayEndpoint != "" {
		proxyServer.SetOverlayEndpoint(overlayEndpoint)
	}

	// Persist proxy config for recovery
	if sm := c.daemon.StateManager(); sm != nil {
		sm.AddProxy(PersistentProxyConfig{
			ID:         id,
			TargetURL:  targetURL,
			Port:       port,
			MaxLogSize: maxLogSize,
			Path:       path,
		})
	}

	resp := map[string]interface{}{
		"id":           proxyServer.ID,
		"target_url":   proxyServer.TargetURL.String(),
		"listen_addr":  proxyServer.ListenAddr,
		"bind_address": proxyServer.BindAddress,
	}
	if proxyServer.PublicURL != "" {
		resp["public_url"] = proxyServer.PublicURL
	}

	// Include tunnel URL if available
	if tunnelURL := proxyServer.TunnelURL(); tunnelURL != "" {
		resp["tunnel_url"] = tunnelURL
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleProxyStop(ctx context.Context, cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROXY STOP requires id")
	}

	proxyID := cmd.Args[0]

	err := c.daemon.proxym.Stop(ctx, proxyID)
	if err != nil {
		if err == proxy.ErrProxyNotFound {
			return c.writeErr(protocol.ErrNotFound, proxyID)
		}
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	// Remove from persisted state
	if sm := c.daemon.StateManager(); sm != nil {
		sm.RemoveProxy(proxyID)
	}

	return c.writeOK("stopped")
}

func (c *Connection) handleProxyStatus(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROXY STATUS requires id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	stats := proxyServer.Stats()

	resp := map[string]interface{}{
		"id":             stats.ID,
		"target_url":     stats.TargetURL,
		"listen_addr":    stats.ListenAddr,
		"bind_address":   stats.BindAddress,
		"running":        stats.Running,
		"uptime":         formatDuration(stats.Uptime),
		"total_requests": stats.TotalRequests,
		"log_stats": map[string]interface{}{
			"total_entries":     stats.LoggerStats.TotalEntries,
			"available_entries": stats.LoggerStats.AvailableEntries,
			"max_size":          stats.LoggerStats.MaxSize,
			"dropped":           stats.LoggerStats.Dropped,
		},
	}
	if stats.PublicURL != "" {
		resp["public_url"] = stats.PublicURL
	}

	// Include tunnel information if available
	if proxyServer.HasTunnel() {
		resp["tunnel"] = map[string]interface{}{
			"running": proxyServer.IsTunnelRunning(),
			"url":     proxyServer.TunnelURL(),
		}
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleProxyList(cmd *protocol.Command) error {
	proxies := c.daemon.proxym.List()

	// Parse directory filter from JSON data (optional)
	var dirFilter protocol.DirectoryFilter
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &dirFilter); err != nil {
			return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
		}
	}

	// Default to current directory if not specified
	directory := dirFilter.Directory
	if directory == "" {
		directory = "."
	}

	// Normalize the filter directory for consistent comparison
	normalizedDir := normalizePath(directory)

	// Filter proxies by directory unless global flag is set
	filteredProxies := proxies
	if !dirFilter.Global {
		var filtered []*proxy.ProxyServer
		for _, p := range proxies {
			// Normalize proxy path for comparison
			if normalizePath(p.Path) == normalizedDir {
				filtered = append(filtered, p)
			}
		}
		filteredProxies = filtered
	}

	entries := make([]map[string]interface{}, len(filteredProxies))
	for i, p := range filteredProxies {
		stats := p.Stats()
		entry := map[string]interface{}{
			"id":             stats.ID,
			"target_url":     stats.TargetURL,
			"listen_addr":    stats.ListenAddr,
			"bind_address":   stats.BindAddress,
			"path":           stats.Path,
			"running":        stats.Running,
			"uptime":         formatDuration(stats.Uptime),
			"total_requests": stats.TotalRequests,
		}
		if stats.PublicURL != "" {
			entry["public_url"] = stats.PublicURL
		}
		// Include tunnel info if available
		if p.HasTunnel() {
			entry["tunnel_url"] = p.TunnelURL()
			entry["tunnel_running"] = p.IsTunnelRunning()
		}
		entries[i] = entry
	}

	resp := map[string]interface{}{
		"count":                 len(filteredProxies),
		"proxies":               entries,
		"directory":             normalizedDir,
		"global":                dirFilter.Global,
		"total_in_daemon":       len(proxies),
		"filtered_by_directory": !dirFilter.Global,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleProxyExec(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROXY EXEC requires id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	// Code is in the data payload
	if len(cmd.Data) == 0 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROXY EXEC requires code")
	}

	code := string(cmd.Data)
	execID, resultChan, err := proxyServer.ExecuteJavaScript(code)
	if err != nil {
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	// Wait for result with timeout
	timeout := 30 * time.Second
	select {
	case result := <-resultChan:
		if result == nil {
			return c.writeErr(protocol.ErrInternal, "execution channel closed")
		}

		resp := map[string]interface{}{
			"execution_id": execID,
			"success":      result.Error == "",
			"result":       result.Result,
			"error":        result.Error,
			"duration":     result.Duration.String(),
		}

		data, _ := json.Marshal(resp)
		return c.writeJSON(data)

	case <-time.After(timeout):
		return c.writeErr(protocol.ErrTimeout, "execution timed out")
	}
}

func (c *Connection) handleProxyToast(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROXY TOAST requires id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	// Toast config is in the data payload
	if len(cmd.Data) == 0 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROXY TOAST requires toast config")
	}

	var toast protocol.ToastConfig
	if err := json.Unmarshal(cmd.Data, &toast); err != nil {
		return c.writeErr(protocol.ErrInvalidArgs, "invalid toast config: "+err.Error())
	}

	// Validate toast type
	if toast.Type == "" {
		toast.Type = "info"
	}
	validTypes := map[string]bool{"success": true, "error": true, "warning": true, "info": true}
	if !validTypes[toast.Type] {
		return c.writeErr(protocol.ErrInvalidArgs, "invalid toast type: "+toast.Type)
	}

	// Validate message
	if toast.Message == "" {
		return c.writeErr(protocol.ErrInvalidArgs, "toast message required")
	}

	// Broadcast toast to connected browsers
	sentCount, err := proxyServer.BroadcastToast(toast.Type, toast.Title, toast.Message, toast.Duration)
	if err != nil {
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"success":    true,
		"sent_count": sentCount,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

// handleProxyLog handles the PROXYLOG command.
func (c *Connection) handleProxyLog(cmd *protocol.Command) error {
	if cmd.SubVerb == "" && len(cmd.Args) > 0 {
		cmd.SubVerb = strings.ToUpper(cmd.Args[0])
		cmd.Args = cmd.Args[1:]
	}

	switch cmd.SubVerb {
	case protocol.SubVerbQuery:
		return c.handleProxyLogQuery(cmd)
	case protocol.SubVerbClear:
		return c.handleProxyLogClear(cmd)
	case protocol.SubVerbStats:
		return c.handleProxyLogStats(cmd)
	case "":
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrMissingParam,
			Message:      "action required",
			Command:      "PROXYLOG",
			Param:        "action",
			ValidActions: validProxyLogActions,
		})
	default:
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrInvalidAction,
			Message:      "unknown action",
			Command:      "PROXYLOG",
			Action:       cmd.SubVerb,
			ValidActions: validProxyLogActions,
		})
	}
}

func (c *Connection) handleProxyLogQuery(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROXYLOG QUERY requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	// Parse filter from JSON data
	var queryFilter protocol.LogQueryFilter
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &queryFilter); err != nil {
			return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
		}
	}

	// Convert to proxy.LogFilter
	filter := proxy.LogFilter{
		Methods:     queryFilter.Methods,
		URLPattern:  queryFilter.URLPattern,
		StatusCodes: queryFilter.StatusCodes,
		Limit:       queryFilter.Limit,
	}

	for _, t := range queryFilter.Types {
		filter.Types = append(filter.Types, proxy.LogEntryType(t))
	}

	// Parse time filters
	if queryFilter.Since != "" {
		since, err := parseTimeOrDuration(queryFilter.Since)
		if err != nil {
			return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("invalid since: %v", err))
		}
		filter.Since = &since
	}

	if queryFilter.Until != "" {
		until, err := time.Parse(time.RFC3339, queryFilter.Until)
		if err != nil {
			return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("invalid until: %v", err))
		}
		filter.Until = &until
	}

	if filter.Limit == 0 {
		filter.Limit = 100
	}

	entries := proxyServer.Logger().Query(filter)

	// Convert to JSON-friendly format
	result := make([]map[string]interface{}, 0, len(entries))
	for i := range entries {
		entryMap := convertLogEntry(&entries[i])
		if entryMap != nil {
			result = append(result, entryMap)
		}
	}

	resp := map[string]interface{}{
		"entries": result,
		"count":   len(result),
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleProxyLogClear(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROXYLOG CLEAR requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	proxyServer.Logger().Clear()
	return c.writeOK("cleared")
}

func (c *Connection) handleProxyLogStats(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROXYLOG STATS requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	stats := proxyServer.Logger().Stats()

	resp := map[string]interface{}{
		"total_entries":     stats.TotalEntries,
		"available_entries": stats.AvailableEntries,
		"max_size":          stats.MaxSize,
		"dropped":           stats.Dropped,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

// handleCurrentPage handles the CURRENTPAGE command.
func (c *Connection) handleCurrentPage(cmd *protocol.Command) error {
	if cmd.SubVerb == "" && len(cmd.Args) > 0 {
		cmd.SubVerb = strings.ToUpper(cmd.Args[0])
		cmd.Args = cmd.Args[1:]
	}

	switch cmd.SubVerb {
	case protocol.SubVerbList:
		return c.handleCurrentPageList(cmd)
	case protocol.SubVerbGet:
		return c.handleCurrentPageGet(cmd)
	case protocol.SubVerbClear:
		return c.handleCurrentPageClear(cmd)
	case "":
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrMissingParam,
			Message:      "action required",
			Command:      "CURRENTPAGE",
			Param:        "action",
			ValidActions: validCurrentPageActions,
		})
	default:
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrInvalidAction,
			Message:      "unknown action",
			Command:      "CURRENTPAGE",
			Action:       cmd.SubVerb,
			ValidActions: validCurrentPageActions,
		})
	}
}

func (c *Connection) handleCurrentPageList(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "CURRENTPAGE LIST requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	sessions := proxyServer.PageTracker().GetActiveSessions()

	entries := make([]map[string]interface{}, len(sessions))
	for i, s := range sessions {
		entries[i] = convertPageSession(s, false)
	}

	resp := map[string]interface{}{
		"sessions": entries,
		"count":    len(entries),
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleCurrentPageGet(cmd *protocol.Command) error {
	if len(cmd.Args) < 2 {
		return c.writeErr(protocol.ErrInvalidArgs, "CURRENTPAGE GET requires proxy_id and session_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	session, ok := proxyServer.PageTracker().GetSession(cmd.Args[1])
	if !ok {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[1])
	}

	resp := convertPageSession(session, true)
	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleCurrentPageClear(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "CURRENTPAGE CLEAR requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	proxyServer.PageTracker().Clear()
	return c.writeOK("cleared")
}

// Helper functions

func parseOutputFilter(args []string) protocol.OutputFilter {
	filter := protocol.OutputFilter{Stream: "combined"}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "stream=") {
			filter.Stream = strings.TrimPrefix(arg, "stream=")
		} else if strings.HasPrefix(arg, "tail=") {
			filter.Tail, _ = strconv.Atoi(strings.TrimPrefix(arg, "tail="))
		} else if strings.HasPrefix(arg, "head=") {
			filter.Head, _ = strconv.Atoi(strings.TrimPrefix(arg, "head="))
		} else if strings.HasPrefix(arg, "grep=") {
			filter.Grep = strings.TrimPrefix(arg, "grep=")
		} else if arg == "grep_v" {
			filter.GrepV = true
		}
	}

	return filter
}

func parseTimeOrDuration(s string) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Parse(time.RFC3339, s)
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

func convertLogEntry(entry *proxy.LogEntry) map[string]interface{} {
	result := map[string]interface{}{
		"type": string(entry.Type),
	}

	switch entry.Type {
	case proxy.LogTypeHTTP:
		if entry.HTTP != nil {
			result["timestamp"] = entry.HTTP.Timestamp
			result["data"] = map[string]interface{}{
				"id":          entry.HTTP.ID,
				"method":      entry.HTTP.Method,
				"url":         entry.HTTP.URL,
				"status_code": entry.HTTP.StatusCode,
				"duration_ms": entry.HTTP.Duration.Milliseconds(),
			}
		}
	case proxy.LogTypeError:
		if entry.Error != nil {
			result["timestamp"] = entry.Error.Timestamp
			result["data"] = map[string]interface{}{
				"id":      entry.Error.ID,
				"message": entry.Error.Message,
				"source":  entry.Error.Source,
				"lineno":  entry.Error.LineNo,
				"colno":   entry.Error.ColNo,
				"url":     entry.Error.URL,
				"stack":   entry.Error.Stack,
			}
		}
	case proxy.LogTypePerformance:
		if entry.Performance != nil {
			result["timestamp"] = entry.Performance.Timestamp
			result["data"] = map[string]interface{}{
				"id":           entry.Performance.ID,
				"url":          entry.Performance.URL,
				"load_time_ms": entry.Performance.LoadEventEnd,
			}
		}
	case proxy.LogTypeCustom:
		if entry.Custom != nil {
			result["timestamp"] = entry.Custom.Timestamp
			result["data"] = map[string]interface{}{
				"id":      entry.Custom.ID,
				"level":   entry.Custom.Level,
				"message": entry.Custom.Message,
				"url":     entry.Custom.URL,
			}
		}
	case proxy.LogTypeScreenshot:
		if entry.Screenshot != nil {
			result["timestamp"] = entry.Screenshot.Timestamp
			result["data"] = map[string]interface{}{
				"id":        entry.Screenshot.ID,
				"name":      entry.Screenshot.Name,
				"file_path": entry.Screenshot.FilePath,
				"url":       entry.Screenshot.URL,
				"width":     entry.Screenshot.Width,
				"height":    entry.Screenshot.Height,
				"format":    entry.Screenshot.Format,
				"selector":  entry.Screenshot.Selector,
			}
		}
	case proxy.LogTypeExecution:
		if entry.Execution != nil {
			result["timestamp"] = entry.Execution.Timestamp
			result["data"] = map[string]interface{}{
				"id":          entry.Execution.ID,
				"code":        entry.Execution.Code,
				"result":      entry.Execution.Result,
				"error":       entry.Execution.Error,
				"duration_ms": entry.Execution.Duration.Milliseconds(),
				"url":         entry.Execution.URL,
			}
		}
	case proxy.LogTypeResponse:
		if entry.Response != nil {
			result["timestamp"] = entry.Response.Timestamp
			result["data"] = map[string]interface{}{
				"id":          entry.Response.ID,
				"exec_id":     entry.Response.ExecID,
				"success":     entry.Response.Success,
				"result":      entry.Response.Result,
				"error":       entry.Response.Error,
				"duration_ms": entry.Response.Duration.Milliseconds(),
			}
		}
	case proxy.LogTypeInteraction:
		if entry.Interaction != nil {
			result["timestamp"] = entry.Interaction.Timestamp
			data := map[string]interface{}{
				"id":         entry.Interaction.ID,
				"event_type": entry.Interaction.EventType,
				"url":        entry.Interaction.URL,
				"target":     entry.Interaction.Target,
			}
			if entry.Interaction.Position != nil {
				data["position"] = entry.Interaction.Position
			}
			if entry.Interaction.Key != nil {
				data["key"] = entry.Interaction.Key
			}
			if entry.Interaction.Value != "" {
				data["value"] = entry.Interaction.Value
			}
			result["data"] = data
		}
	case proxy.LogTypeMutation:
		if entry.Mutation != nil {
			result["timestamp"] = entry.Mutation.Timestamp
			data := map[string]interface{}{
				"id":            entry.Mutation.ID,
				"mutation_type": entry.Mutation.MutationType,
				"url":           entry.Mutation.URL,
				"target":        entry.Mutation.Target,
			}
			if len(entry.Mutation.Added) > 0 {
				data["added"] = entry.Mutation.Added
			}
			if len(entry.Mutation.Removed) > 0 {
				data["removed"] = entry.Mutation.Removed
			}
			if entry.Mutation.Attribute != nil {
				data["attribute"] = entry.Mutation.Attribute
			}
			result["data"] = data
		}
	case proxy.LogTypePanelMessage:
		if entry.PanelMessage != nil {
			result["timestamp"] = entry.PanelMessage.Timestamp
			data := map[string]interface{}{
				"id":      entry.PanelMessage.ID,
				"message": entry.PanelMessage.Message,
				"url":     entry.PanelMessage.URL,
			}
			if len(entry.PanelMessage.Attachments) > 0 {
				data["attachments"] = entry.PanelMessage.Attachments
			}
			result["data"] = data
		}
	case proxy.LogTypeSketch:
		if entry.Sketch != nil {
			result["timestamp"] = entry.Sketch.Timestamp
			result["data"] = map[string]interface{}{
				"id":            entry.Sketch.ID,
				"url":           entry.Sketch.URL,
				"file_path":     entry.Sketch.FilePath,
				"element_count": entry.Sketch.ElementCount,
			}
		}
	}

	// Ensure data field is never null - MCP schema validation requires an object
	if _, ok := result["data"]; !ok {
		result["data"] = map[string]interface{}{}
	}

	return result
}

func convertPageSession(session *proxy.PageSession, includeDetails bool) map[string]interface{} {
	result := map[string]interface{}{
		"id":                session.ID,
		"url":               session.URL,
		"browser_session":   session.BrowserSession,
		"page_title":        session.PageTitle,
		"start_time":        session.StartTime,
		"last_activity":     session.LastActivity,
		"active":            session.Active,
		"resource_count":    len(session.Resources),
		"error_count":       len(session.Errors),
		"has_performance":   session.Performance != nil,
		"interaction_count": session.InteractionCount,
		"mutation_count":    session.MutationCount,
	}

	if session.Performance != nil {
		result["load_time_ms"] = session.Performance.LoadEventEnd
	}

	if includeDetails {
		resources := make([]string, len(session.Resources))
		for i, res := range session.Resources {
			resources[i] = res.URL
		}
		result["resources"] = resources

		errors := make([]map[string]interface{}, len(session.Errors))
		for i, err := range session.Errors {
			errors[i] = map[string]interface{}{
				"message": err.Message,
				"source":  err.Source,
				"lineno":  err.LineNo,
				"colno":   err.ColNo,
				"stack":   err.Stack,
			}
		}
		result["errors"] = errors

		// Include interaction history
		interactions := make([]map[string]interface{}, len(session.Interactions))
		for i, interaction := range session.Interactions {
			interactionMap := map[string]interface{}{
				"id":         interaction.ID,
				"timestamp":  interaction.Timestamp,
				"event_type": interaction.EventType,
				"url":        interaction.URL,
				"target": map[string]interface{}{
					"selector":   interaction.Target.Selector,
					"tag":        interaction.Target.Tag,
					"id":         interaction.Target.ID,
					"classes":    interaction.Target.Classes,
					"text":       interaction.Target.Text,
					"attributes": interaction.Target.Attributes,
				},
			}
			if interaction.Position != nil {
				interactionMap["position"] = map[string]interface{}{
					"client_x": interaction.Position.ClientX,
					"client_y": interaction.Position.ClientY,
					"page_x":   interaction.Position.PageX,
					"page_y":   interaction.Position.PageY,
				}
			}
			if interaction.Key != nil {
				interactionMap["key"] = map[string]interface{}{
					"key":   interaction.Key.Key,
					"code":  interaction.Key.Code,
					"ctrl":  interaction.Key.Ctrl,
					"alt":   interaction.Key.Alt,
					"shift": interaction.Key.Shift,
					"meta":  interaction.Key.Meta,
				}
			}
			if interaction.Value != "" {
				interactionMap["value"] = interaction.Value
			}
			if interaction.Data != nil {
				interactionMap["data"] = interaction.Data
			}
			interactions[i] = interactionMap
		}
		result["interactions"] = interactions

		// Include mutation history
		mutations := make([]map[string]interface{}, len(session.Mutations))
		for i, mutation := range session.Mutations {
			mutationMap := map[string]interface{}{
				"id":            mutation.ID,
				"timestamp":     mutation.Timestamp,
				"mutation_type": mutation.MutationType,
				"url":           mutation.URL,
				"target": map[string]interface{}{
					"selector": mutation.Target.Selector,
					"tag":      mutation.Target.Tag,
					"id":       mutation.Target.ID,
				},
			}
			if len(mutation.Added) > 0 {
				added := make([]map[string]interface{}, len(mutation.Added))
				for j, node := range mutation.Added {
					added[j] = map[string]interface{}{
						"selector": node.Selector,
						"tag":      node.Tag,
						"id":       node.ID,
						"html":     node.HTML,
					}
				}
				mutationMap["added"] = added
			}
			if len(mutation.Removed) > 0 {
				removed := make([]map[string]interface{}, len(mutation.Removed))
				for j, node := range mutation.Removed {
					removed[j] = map[string]interface{}{
						"selector": node.Selector,
						"tag":      node.Tag,
						"id":       node.ID,
						"html":     node.HTML,
					}
				}
				mutationMap["removed"] = removed
			}
			if mutation.Attribute != nil {
				mutationMap["attribute"] = map[string]interface{}{
					"name":      mutation.Attribute.Name,
					"old_value": mutation.Attribute.OldValue,
					"new_value": mutation.Attribute.NewValue,
				}
			}
			mutations[i] = mutationMap
		}
		result["mutations"] = mutations
	}

	return result
}

// Valid actions for OVERLAY command
var validOverlayActions = []string{"SET", "GET", "CLEAR", "ACTIVITY"}

// handleOverlay handles the OVERLAY command for configuring the agent overlay endpoint.
func (c *Connection) handleOverlay(cmd *protocol.Command) error {
	if cmd.SubVerb == "" && len(cmd.Args) > 0 {
		cmd.SubVerb = strings.ToUpper(cmd.Args[0])
		cmd.Args = cmd.Args[1:]
	}

	switch cmd.SubVerb {
	case protocol.SubVerbSet:
		return c.handleOverlaySet(cmd)
	case protocol.SubVerbGet:
		return c.handleOverlayGet()
	case protocol.SubVerbClear:
		return c.handleOverlayClear()
	case protocol.SubVerbActivity:
		return c.handleOverlayActivity(cmd)
	case "":
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrMissingParam,
			Message:      "action required",
			Command:      "OVERLAY",
			Param:        "action",
			ValidActions: validOverlayActions,
		})
	default:
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrInvalidAction,
			Message:      "unknown action",
			Command:      "OVERLAY",
			Action:       cmd.SubVerb,
			ValidActions: validOverlayActions,
		})
	}
}

func (c *Connection) handleOverlaySet(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "OVERLAY SET requires socket path")
	}

	endpoint := cmd.Args[0]

	// Validate endpoint format: Unix socket path or Windows named pipe
	isUnixSocket := strings.HasPrefix(endpoint, "/")
	isWindowsPipe := strings.HasPrefix(endpoint, `\\.\pipe\`)
	if !isUnixSocket && !isWindowsPipe {
		return c.writeErr(protocol.ErrInvalidArgs, "endpoint must be a Unix socket path (starting with /) or Windows named pipe (starting with \\\\.\\pipe\\)")
	}

	c.daemon.SetOverlayEndpoint(endpoint)

	resp := map[string]interface{}{
		"socket_path":     endpoint,
		"proxies_updated": len(c.daemon.proxym.List()),
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleOverlayGet() error {
	socketPath := c.daemon.OverlayEndpoint()

	resp := map[string]interface{}{
		"socket_path": socketPath,
		"enabled":     socketPath != "",
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleOverlayClear() error {
	c.daemon.SetOverlayEndpoint("")
	return c.writeOK("overlay endpoint cleared")
}

func (c *Connection) handleOverlayActivity(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "OVERLAY ACTIVITY requires: true|false [proxy_ids...]")
	}

	active := cmd.Args[0] == "true"
	proxyIDs := cmd.Args[1:] // Optional proxy IDs to target

	sentCount := 0

	if len(proxyIDs) > 0 {
		// Broadcast only to specified proxies
		for _, proxyID := range proxyIDs {
			server, err := c.daemon.proxym.Get(proxyID)
			if err != nil {
				continue // Skip non-existent proxies
			}
			sentCount += server.BroadcastActivityState(active)
		}
	} else {
		// No proxy IDs specified - broadcast to all (backward compatibility)
		for _, server := range c.daemon.proxym.List() {
			sentCount += server.BroadcastActivityState(active)
		}
	}

	resp := map[string]interface{}{
		"success":    true,
		"active":     active,
		"sent_count": sentCount,
		"proxy_ids":  proxyIDs,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

// Valid actions for TUNNEL command
var validTunnelActions = []string{"START", "STOP", "STATUS", "LIST"}

// handleTunnel handles the TUNNEL command.
func (c *Connection) handleTunnel(ctx context.Context, cmd *protocol.Command) error {
	if cmd.SubVerb == "" && len(cmd.Args) > 0 {
		cmd.SubVerb = strings.ToUpper(cmd.Args[0])
		cmd.Args = cmd.Args[1:]
	}

	switch cmd.SubVerb {
	case protocol.SubVerbStart:
		return c.handleTunnelStart(ctx, cmd)
	case protocol.SubVerbStop:
		return c.handleTunnelStop(ctx, cmd)
	case protocol.SubVerbStatus:
		return c.handleTunnelStatus(cmd)
	case protocol.SubVerbList:
		return c.handleTunnelList(cmd)
	case "":
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrMissingParam,
			Message:      "action required",
			Command:      "TUNNEL",
			Param:        "action",
			ValidActions: validTunnelActions,
		})
	default:
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrInvalidAction,
			Message:      "unknown action",
			Command:      "TUNNEL",
			Action:       cmd.SubVerb,
			ValidActions: validTunnelActions,
		})
	}
}

func (c *Connection) handleTunnelStart(ctx context.Context, cmd *protocol.Command) error {
	// Parse config from JSON data
	if len(cmd.Data) == 0 {
		return c.writeErr(protocol.ErrInvalidArgs, "TUNNEL START requires JSON config")
	}

	var config protocol.TunnelStartConfig
	if err := json.Unmarshal(cmd.Data, &config); err != nil {
		return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("invalid JSON: %v", err))
	}

	// Validate required fields
	if config.ID == "" {
		return c.writeErr(protocol.ErrInvalidArgs, "tunnel id required")
	}
	if config.Provider == "" {
		return c.writeErr(protocol.ErrInvalidArgs, "provider required (cloudflare or ngrok)")
	}
	if config.LocalPort <= 0 {
		return c.writeErr(protocol.ErrInvalidArgs, "local_port required")
	}

	// Map provider string to tunnel.Provider
	var provider tunnel.Provider
	switch strings.ToLower(config.Provider) {
	case "cloudflare":
		provider = tunnel.ProviderCloudflare
	case "ngrok":
		provider = tunnel.ProviderNgrok
	default:
		return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("unsupported provider: %s (use cloudflare or ngrok)", config.Provider))
	}

	tunnelConfig := tunnel.Config{
		Provider:   provider,
		LocalPort:  config.LocalPort,
		LocalHost:  config.LocalHost,
		BinaryPath: config.BinaryPath,
	}

	t, err := c.daemon.tunnelm.Start(ctx, config.ID, tunnelConfig)
	if err != nil {
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	// If proxy_id is specified, set up auto-configuration of public_url
	if config.ProxyID != "" {
		t.OnURL(func(url string) {
			proxyServer, err := c.daemon.proxym.Get(config.ProxyID)
			if err == nil {
				proxyServer.SetPublicURL(url)
			}
		})
	}

	// Wait for URL with timeout
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	publicURL, err := t.WaitForURL(waitCtx)
	if err != nil {
		// Tunnel started but URL not yet available
		info := t.Info()
		resp := map[string]interface{}{
			"id":         config.ID,
			"provider":   string(info.Provider),
			"state":      info.State,
			"local_addr": info.LocalAddr,
			"error":      err.Error(),
		}
		data, _ := json.Marshal(resp)
		return c.writeJSON(data)
	}

	// If proxy_id was specified and URL is available, update the proxy
	if config.ProxyID != "" && publicURL != "" {
		proxyServer, err := c.daemon.proxym.Get(config.ProxyID)
		if err == nil {
			proxyServer.SetPublicURL(publicURL)
		}
	}

	info := t.Info()
	resp := map[string]interface{}{
		"id":         config.ID,
		"provider":   string(info.Provider),
		"state":      info.State,
		"public_url": publicURL,
		"local_addr": info.LocalAddr,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleTunnelStop(ctx context.Context, cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "TUNNEL STOP requires id")
	}

	tunnelID := cmd.Args[0]

	if err := c.daemon.tunnelm.Stop(ctx, tunnelID); err != nil {
		return c.writeErr(protocol.ErrNotFound, tunnelID)
	}

	return c.writeOK("stopped")
}

func (c *Connection) handleTunnelStatus(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "TUNNEL STATUS requires id")
	}

	t, ok := c.daemon.tunnelm.Get(cmd.Args[0])
	if !ok {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	info := t.Info()
	resp := map[string]interface{}{
		"id":         cmd.Args[0],
		"provider":   string(info.Provider),
		"state":      info.State,
		"public_url": info.PublicURL,
		"local_addr": info.LocalAddr,
	}
	if info.Error != "" {
		resp["error"] = info.Error
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleTunnelList(cmd *protocol.Command) error {
	infos := c.daemon.tunnelm.List()

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

	resp := map[string]interface{}{
		"count":   len(entries),
		"tunnels": entries,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

// Valid actions for CHAOS command
var validChaosActions = []string{"ENABLE", "DISABLE", "STATUS", "PRESET", "SET", "ADD-RULE", "REMOVE-RULE", "LIST-RULES", "STATS", "CLEAR", "LIST-PRESETS"}

// handleChaos handles the CHAOS command.
func (c *Connection) handleChaos(cmd *protocol.Command) error {
	if cmd.SubVerb == "" && len(cmd.Args) > 0 {
		cmd.SubVerb = strings.ToUpper(cmd.Args[0])
		cmd.Args = cmd.Args[1:]
	}

	switch cmd.SubVerb {
	case protocol.SubVerbEnable:
		return c.handleChaosEnable(cmd)
	case protocol.SubVerbDisable:
		return c.handleChaosDisable(cmd)
	case protocol.SubVerbStatus:
		return c.handleChaosStatus(cmd)
	case protocol.SubVerbPreset:
		return c.handleChaosPreset(cmd)
	case protocol.SubVerbSet:
		return c.handleChaosSet(cmd)
	case protocol.SubVerbAddRule:
		return c.handleChaosAddRule(cmd)
	case protocol.SubVerbRemoveRule:
		return c.handleChaosRemoveRule(cmd)
	case protocol.SubVerbListRules:
		return c.handleChaosListRules(cmd)
	case protocol.SubVerbStats:
		return c.handleChaosStats(cmd)
	case protocol.SubVerbClear:
		return c.handleChaosClear(cmd)
	case "LIST-PRESETS":
		return c.handleChaosListPresets()
	case "":
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrMissingParam,
			Message:      "action required",
			Command:      "CHAOS",
			Param:        "action",
			ValidActions: validChaosActions,
		})
	default:
		return c.writeStructuredErr(&protocol.StructuredError{
			Code:         protocol.ErrInvalidAction,
			Message:      "unknown action",
			Command:      "CHAOS",
			Action:       cmd.SubVerb,
			ValidActions: validChaosActions,
		})
	}
}

func (c *Connection) handleChaosEnable(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "CHAOS ENABLE requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	engine := proxyServer.ChaosEngine()
	engine.Enable()

	resp := map[string]interface{}{
		"success": true,
		"enabled": engine.IsEnabled(),
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleChaosDisable(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "CHAOS DISABLE requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	engine := proxyServer.ChaosEngine()
	engine.Disable()

	resp := map[string]interface{}{
		"success": true,
		"enabled": engine.IsEnabled(),
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleChaosStatus(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "CHAOS STATUS requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	engine := proxyServer.ChaosEngine()
	config := engine.GetConfig()
	stats := engine.GetStats()

	ruleCount := 0
	if config != nil {
		ruleCount = len(config.Rules)
	}

	resp := map[string]interface{}{
		"enabled":      engine.IsEnabled(),
		"logging_mode": int(engine.GetLoggingMode()),
		"rule_count":   ruleCount,
		"stats": map[string]interface{}{
			"total_requests":      stats.TotalRequests,
			"affected_count":      stats.AffectedCount,
			"latency_injected_ms": stats.LatencyInjected,
			"errors_injected":     stats.ErrorsInjected,
			"drops_injected":      stats.DropsInjected,
			"truncated_count":     stats.TruncatedCount,
			"reordered_count":     stats.ReorderedCount,
		},
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleChaosPreset(cmd *protocol.Command) error {
	if len(cmd.Args) < 2 {
		return c.writeErr(protocol.ErrInvalidArgs, "CHAOS PRESET requires proxy_id and preset name")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	presetName := cmd.Args[1]
	presetConfig := proxy.GetPreset(presetName)
	if presetConfig == nil {
		availablePresets := proxy.ListPresets()
		return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("unknown preset %q. Available: %s", presetName, strings.Join(availablePresets, ", ")))
	}

	engine := proxyServer.ChaosEngine()
	if err := engine.SetConfig(presetConfig); err != nil {
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"success":    true,
		"preset":     presetName,
		"enabled":    engine.IsEnabled(),
		"rule_count": len(presetConfig.Rules),
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleChaosSet(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "CHAOS SET requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	if len(cmd.Data) == 0 {
		return c.writeErr(protocol.ErrInvalidArgs, "CHAOS SET requires config JSON")
	}

	var configPayload protocol.ChaosConfigPayload
	if err := json.Unmarshal(cmd.Data, &configPayload); err != nil {
		return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("invalid config JSON: %v", err))
	}

	// Convert protocol config to proxy config
	config := &proxy.ChaosConfig{
		Enabled:     configPayload.Enabled,
		GlobalOdds:  configPayload.GlobalOdds,
		Seed:        configPayload.Seed,
		LoggingMode: proxy.LoggingMode(configPayload.LoggingMode),
	}

	for _, ruleConfig := range configPayload.Rules {
		rule := convertProtocolRuleToProxy(ruleConfig)
		config.Rules = append(config.Rules, rule)
	}

	engine := proxyServer.ChaosEngine()
	if err := engine.SetConfig(config); err != nil {
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"success":    true,
		"enabled":    engine.IsEnabled(),
		"rule_count": len(config.Rules),
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleChaosAddRule(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "CHAOS ADD-RULE requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	if len(cmd.Data) == 0 {
		return c.writeErr(protocol.ErrInvalidArgs, "CHAOS ADD-RULE requires rule JSON")
	}

	var ruleConfig protocol.ChaosRuleConfig
	if err := json.Unmarshal(cmd.Data, &ruleConfig); err != nil {
		return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("invalid rule JSON: %v", err))
	}

	rule := convertProtocolRuleToProxy(&ruleConfig)

	engine := proxyServer.ChaosEngine()
	if err := engine.AddRule(rule); err != nil {
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"success": true,
		"rule_id": rule.ID,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleChaosRemoveRule(cmd *protocol.Command) error {
	if len(cmd.Args) < 2 {
		return c.writeErr(protocol.ErrInvalidArgs, "CHAOS REMOVE-RULE requires proxy_id and rule_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	ruleID := cmd.Args[1]
	engine := proxyServer.ChaosEngine()
	removed := engine.RemoveRule(ruleID)

	resp := map[string]interface{}{
		"success": removed,
		"rule_id": ruleID,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleChaosListRules(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "CHAOS LIST-RULES requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	engine := proxyServer.ChaosEngine()
	config := engine.GetConfig()

	rules := make([]map[string]interface{}, 0)
	if config != nil {
		for _, rule := range config.Rules {
			ruleMap := map[string]interface{}{
				"id":          rule.ID,
				"name":        rule.Name,
				"type":        string(rule.Type),
				"enabled":     rule.Enabled,
				"probability": rule.Probability,
			}
			if rule.URLPattern != "" {
				ruleMap["url_pattern"] = rule.URLPattern
			}
			if len(rule.Methods) > 0 {
				ruleMap["methods"] = rule.Methods
			}
			rules = append(rules, ruleMap)
		}
	}

	resp := map[string]interface{}{
		"rules": rules,
		"count": len(rules),
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleChaosStats(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "CHAOS STATS requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	engine := proxyServer.ChaosEngine()
	stats := engine.GetStats()

	resp := map[string]interface{}{
		"total_requests":      stats.TotalRequests,
		"affected_count":      stats.AffectedCount,
		"latency_injected_ms": stats.LatencyInjected,
		"errors_injected":     stats.ErrorsInjected,
		"drops_injected":      stats.DropsInjected,
		"truncated_count":     stats.TruncatedCount,
		"reordered_count":     stats.ReorderedCount,
		"rule_stats":          stats.RuleStats,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleChaosClear(cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "CHAOS CLEAR requires proxy_id")
	}

	proxyServer, err := c.daemon.proxym.Get(cmd.Args[0])
	if err != nil {
		return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
	}

	engine := proxyServer.ChaosEngine()
	engine.Clear()

	resp := map[string]interface{}{
		"success": true,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleChaosListPresets() error {
	presets := proxy.ListPresets()

	resp := map[string]interface{}{
		"presets": presets,
		"count":   len(presets),
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

// convertProtocolRuleToProxy converts a protocol.ChaosRuleConfig to proxy.ChaosRule
func convertProtocolRuleToProxy(cfg *protocol.ChaosRuleConfig) *proxy.ChaosRule {
	return &proxy.ChaosRule{
		ID:                 cfg.ID,
		Name:               cfg.Name,
		Type:               proxy.ChaosType(cfg.Type),
		Enabled:            cfg.Enabled,
		URLPattern:         cfg.URLPattern,
		Methods:            cfg.Methods,
		Probability:        cfg.Probability,
		MinLatencyMs:       cfg.MinLatencyMs,
		MaxLatencyMs:       cfg.MaxLatencyMs,
		JitterMs:           cfg.JitterMs,
		BytesPerMs:         cfg.BytesPerMs,
		ChunkSize:          cfg.ChunkSize,
		DropAfterPercent:   cfg.DropAfterPercent,
		DropAfterBytes:     cfg.DropAfterBytes,
		ErrorCodes:         cfg.ErrorCodes,
		ErrorMessage:       cfg.ErrorMessage,
		TruncatePercent:    cfg.TruncatePercent,
		ReorderMinRequests: cfg.ReorderMinRequests,
		ReorderMaxWaitMs:   cfg.ReorderMaxWaitMs,
		StaleDelayMs:       cfg.StaleDelayMs,
	}
}
