package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"devtool-mcp/internal/process"
	"devtool-mcp/internal/project"
	"devtool-mcp/internal/protocol"
	"devtool-mcp/internal/proxy"
)

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
	validProxyActions       = []string{"START", "STOP", "STATUS", "LIST", "EXEC"}
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

	// Filter processes by directory unless global flag is set
	filteredProcs := procs
	if !dirFilter.Global {
		var filtered []*process.ManagedProcess
		for _, p := range procs {
			if p.ProjectPath == directory {
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
		"directory":             directory,
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

	// Parse path from JSON data (optional)
	path := "."
	if len(cmd.Data) > 0 {
		var data struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(cmd.Data, &data); err == nil && data.Path != "" {
			path = data.Path
		}
	}

	config := proxy.ProxyConfig{
		ID:          id,
		TargetURL:   targetURL,
		ListenPort:  port,
		MaxLogSize:  maxLogSize,
		AutoRestart: true,
		Path:        path,
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

	resp := map[string]interface{}{
		"id":          proxyServer.ID,
		"target_url":  proxyServer.TargetURL.String(),
		"listen_addr": proxyServer.ListenAddr,
	}

	data, _ := json.Marshal(resp)
	return c.writeJSON(data)
}

func (c *Connection) handleProxyStop(ctx context.Context, cmd *protocol.Command) error {
	if len(cmd.Args) < 1 {
		return c.writeErr(protocol.ErrInvalidArgs, "PROXY STOP requires id")
	}

	err := c.daemon.proxym.Stop(ctx, cmd.Args[0])
	if err != nil {
		if err == proxy.ErrProxyNotFound {
			return c.writeErr(protocol.ErrNotFound, cmd.Args[0])
		}
		return c.writeErr(protocol.ErrInternal, err.Error())
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

	// Filter proxies by directory unless global flag is set
	filteredProxies := proxies
	if !dirFilter.Global {
		var filtered []*proxy.ProxyServer
		for _, p := range proxies {
			if p.Path == directory {
				filtered = append(filtered, p)
			}
		}
		filteredProxies = filtered
	}

	entries := make([]map[string]interface{}, len(filteredProxies))
	for i, p := range filteredProxies {
		stats := p.Stats()
		entries[i] = map[string]interface{}{
			"id":             stats.ID,
			"target_url":     stats.TargetURL,
			"listen_addr":    stats.ListenAddr,
			"path":           stats.Path,
			"running":        stats.Running,
			"uptime":         formatDuration(stats.Uptime),
			"total_requests": stats.TotalRequests,
		}
	}

	resp := map[string]interface{}{
		"count":                 len(filteredProxies),
		"proxies":               entries,
		"directory":             directory,
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
var validOverlayActions = []string{"SET", "GET", "CLEAR"}

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
