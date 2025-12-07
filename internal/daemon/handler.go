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

	// Start process
	proc, err := c.daemon.pm.StartCommand(ctx, process.ProcessConfig{
		ID:          id,
		ProjectPath: config.Path,
		Command:     command,
		Args:        args,
	})
	if err != nil {
		if err == process.ErrProcessExists {
			return c.writeErr(protocol.ErrAlreadyExists, id)
		}
		return c.writeErr(protocol.ErrInternal, err.Error())
	}

	// Background mode: return immediately
	if config.Mode == "background" {
		resp := map[string]interface{}{
			"process_id": proc.ID,
			"pid":        proc.PID(),
			"command":    command + " " + strings.Join(args, " "),
		}
		data, _ := json.Marshal(resp)
		return c.writeJSON(data)
	}

	// Foreground modes: wait for completion
	select {
	case <-proc.Done():
		// Process completed
	case <-ctx.Done():
		// Context cancelled
		c.daemon.pm.StopProcess(ctx, proc)
		return c.writeErr(protocol.ErrTimeout, "process cancelled")
	}

	resp := map[string]interface{}{
		"process_id": proc.ID,
		"pid":        proc.PID(),
		"command":    command + " " + strings.Join(args, " "),
		"exit_code":  proc.ExitCode(),
		"state":      proc.State().String(),
		"runtime":    formatDuration(proc.Runtime()),
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
	default:
		return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("unknown PROC action: %s", cmd.SubVerb))
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
	default:
		return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("unknown PROXY action: %s", cmd.SubVerb))
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
	default:
		return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("unknown PROXYLOG action: %s", cmd.SubVerb))
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
	default:
		return c.writeErr(protocol.ErrInvalidArgs, fmt.Sprintf("unknown CURRENTPAGE action: %s", cmd.SubVerb))
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
	}

	return result
}

func convertPageSession(session *proxy.PageSession, includeDetails bool) map[string]interface{} {
	result := map[string]interface{}{
		"id":              session.ID,
		"url":             session.URL,
		"page_title":      session.PageTitle,
		"start_time":      session.StartTime,
		"last_activity":   session.LastActivity,
		"active":          session.Active,
		"resource_count":  len(session.Resources),
		"error_count":     len(session.Errors),
		"has_performance": session.Performance != nil,
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
	}

	return result
}
