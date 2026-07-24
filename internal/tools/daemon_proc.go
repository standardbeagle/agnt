package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/go-sdk/mcp"

	"os"
	"path/filepath"
	"time"
)

// makeRunHandler creates a handler for the run tool.
func (dt *DaemonTools) makeRunHandler() func(context.Context, *mcp.CallToolRequest, RunInput) (*mcp.CallToolResult, RunOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input RunInput) (*mcp.CallToolResult, RunOutput, error) {
		if err := validateRunInput(input); err != nil {
			return errorResult(validationError("run", err)), RunOutput{}, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), RunOutput{}, nil
		}

		path := input.Path
		if path == "" {
			path = getProjectPath()
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to resolve path: %v", err)), RunOutput{}, nil
		}

		// Resolve script name to command if needed
		// The daemon's hub doesn't resolve script names, so we must do it here
		var cmd string
		var args []string
		var id string

		if input.Raw {

			if input.Command == "" {
				return errorResult("raw mode requires command"), RunOutput{}, nil
			}
			cmd = input.Command
			args = input.Args
			id = input.ID
			if id == "" {
				id = fmt.Sprintf("proc-%d", time.Now().UnixNano()%100000)
			}
		} else if input.ScriptName != "" {

			// First check .agnt.kdl for the script
			if resolvedCmd, resolvedArgs, err := resolveKDLScript(absPath, input.ScriptName, input.Args); err == nil {
				cmd = resolvedCmd
				args = resolvedArgs
				id = input.ID
				if id == "" {
					id = input.ScriptName
				}
			} else {
				// Fall back to project detection (package.json scripts, etc.)
				proj, detectErr := project.Detect(absPath)
				if detectErr != nil {
					return errorResult(fmt.Sprintf("failed to detect project: %v", detectErr)), RunOutput{}, nil
				}

				cmdDef := project.GetCommandByName(proj, input.ScriptName)
				if cmdDef == nil {
					available := project.GetCommandNames(proj)
					return errorResult(fmt.Sprintf("unknown script %q. Available: %s", input.ScriptName, strings.Join(available, ", "))), RunOutput{}, nil
				}

				cmd = cmdDef.Command
				args = append(cmdDef.Args, input.Args...)
				id = input.ID
				if id == "" {
					id = input.ScriptName
				}
			}
		} else {
			return errorResult("script_name required (or use raw=true with command)"), RunOutput{}, nil
		}

		mode := string(input.Mode)
		if mode == "" {
			mode = "background"
		}

		config := struct {
			protocol.RunConfig
			NoAutoRestart bool `json:"no_auto_restart,omitempty"`
		}{
			RunConfig: protocol.RunConfig{
				ID:      id,
				Path:    absPath,
				Raw:     true,
				Command: cmd,
				Args:    args,
				Mode:    mode,
				Env:     os.Environ(),
			},
			NoAutoRestart: input.NoAutoRestart,
		}

		var result map[string]interface{}
		if mode == string(RunModeBackground) {
			// A background run returns as soon as the process is registered, so it
			// holds the shared connection's per-request mutex only briefly. Using
			// that connection keeps its reconnect/retry behavior and avoids a
			// connect/close round trip on the hot path.
			r, err := dt.client.Run(config)
			if err != nil {
				return formatDaemonError(err, "run"), RunOutput{}, nil
			}
			result = r
		} else {
			// Foreground modes block until the process exits (DefaultTimeout:0 =
			// wait forever). This single long blocking IPC must NOT run on the
			// shared ResilientClient connection: its per-request mutex is held for
			// the whole request/response, so a cancelled/timed-out MCP call would
			// strand the read and head-of-line block every other daemon-backed
			// tool for up to the connection's read deadline. Use a DEDICATED
			// connection instead, and close it on ctx cancel to interrupt the
			// blocked read immediately. The buffered channel lets the goroutine
			// finish its send after we've returned.
			socketPath := dt.config.SocketPath
			if socketPath == "" {
				socketPath = daemon.DefaultSocketPath()
			}
			runClient := daemon.NewClientWithPath(socketPath)
			if err := runClient.Connect(); err != nil {
				return formatDaemonError(err, "run"), RunOutput{}, nil
			}

			type runResult struct {
				result map[string]interface{}
				err    error
			}
			ch := make(chan runResult, 1)
			go func() {
				r, e := runClient.Run(config)
				ch <- runResult{result: r, err: e}
			}()

			select {
			case <-ctx.Done():
				// Interrupt the blocked read on the dedicated connection so the
				// goroutine unwinds; nothing else shares this conn.
				runClient.Close()
				return errorResult(fmt.Sprintf("run cancelled: %v", ctx.Err())), RunOutput{}, nil
			case rr := <-ch:
				runClient.Close()
				if rr.err != nil {
					return formatDaemonError(rr.err, "run"), RunOutput{}, nil
				}
				result = rr.result
			}
		}

		processID := getString(result, "process_id")

		output := RunOutput{
			ProcessID: processID,
			PID:       getInt(result, "pid"),
			Command:   getString(result, "command"),
			ExitCode:  getInt(result, "exit_code"),
			State:     getString(result, "state"),
			Runtime:   getString(result, "runtime"),
			Stdout:    getString(result, "stdout"),
			Stderr:    getString(result, "stderr"),
		}
		if output.State != "failed" {
			output.Hint = webServerRunHint(input, output.Command)
		}

		return nil, output, nil
	}
}

// makeProcHandler creates a handler for the proc tool.
func (dt *DaemonTools) makeProcHandler() func(context.Context, *mcp.CallToolRequest, ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
		input.ProcessID = pickProcessID(input.ID, input.ProcessID)
		if err := validateProcInput(input); err != nil {
			return errorResult(validationError("proc", err)), ProcOutput{}, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), ProcOutput{}, nil
		}

		switch input.Action {
		case "status":
			return dt.handleProcStatus(input)
		case "output":
			return dt.handleProcOutput(input)
		case "find":
			return dt.handleProcFind(input)
		case "stop":
			return dt.handleProcStop(input)
		case "restart":
			return dt.handleProcRestart(input)
		case "list":
			return dt.handleProcList(input)
		case "cleanup_port":
			return dt.handleProcCleanupPort(input)
		case "autorestart":
			return dt.handleProcAutoRestart(input)
		case "scripts":
			return dt.handleScriptList(input)
		case "script_output":
			return dt.handleScriptOutput(input)
		case "script_history":
			return dt.handleScriptHistory(input)
		case "snapshot":
			return dt.handleProcSnapshot(input)
		case "run":
			return dt.handleProcRun(input)
		case "run_group":
			return dt.handleProcRunGroup(input)
		case "wait":
			return dt.handleProcWait(ctx, input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q. Use: status, output, find, stop, restart, list, cleanup_port, autorestart, scripts, script_output, script_history, snapshot, run, run_group, wait", input.Action)), ProcOutput{}, nil
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

	out := ProcOutput{
		ProcessID: getString(result, "process_id"),
		State:     getString(result, "state"),
		Summary:   getString(result, "summary"),
		ExitCode:  getInt(result, "exit_code"),
		Runtime:   getString(result, "runtime"),
	}
	// ProcStatus uses "id" in the daemon response; fall back to it when
	// process_id is empty (both fields are populated by the daemon).
	if out.ProcessID == "" {
		out.ProcessID = getString(result, "id")
	}
	// Surface waiting_for for pending processes (or briefly-still-pending
	// ManagedProcess entries that haven't cleared their tracker entry).
	if waitingFor, ok := result["waiting_for"].([]interface{}); ok {
		for _, w := range waitingFor {
			if s, ok := w.(string); ok {
				out.WaitingFor = append(out.WaitingFor, s)
			}
		}
	}
	if reason := getString(result, "failure_reason"); reason != "" {
		out.FailureReason = reason
	}
	populateLastExitFields(&out, result)

	// Classify the process role from its command string.
	cmdFull := getString(result, "command")
	if cmdFull != "" {
		procID := out.ProcessID
		role := classifyProcess(cmdFull)
		out.Role = role.Role
		out.Produces = role.Produces
		out.OutputHint = strings.ReplaceAll(role.OutputHint, "<id>", procID)
	}

	return nil, out, nil
}

func (dt *DaemonTools) handleProcOutput(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	// Multi-stream path: process_ids array beats the singular process_id.
	// Empty arrays still fall through to the single-stream path (so a
	// caller passing process_ids=[] gets the same error as no IDs at all).
	if len(input.ProcessIDs) > 0 {
		return dt.handleProcOutputMulti(input)
	}
	if input.ProcessID == "" {
		return errorResult("process_id (or process_ids) required for output"), ProcOutput{}, nil
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

		projectPath := getProjectPath()
		if projectPath != "" {
			scriptResult, scriptErr := dt.client.ScriptOutput(input.ProcessID, projectPath, input.Tail)
			if scriptErr == nil {
				lines := getStringSlice(scriptResult, "lines")
				// ScriptOutput only honors Tail (applied daemon-side). Apply
				// the remaining Grep/GrepV/Head filters client-side here so
				// they aren't silently dropped on the fallback path; note any
				// filter (Stream) that the script-history buffer can't honor.
				lines, note, ferr := filterScriptFallbackLines(lines, filter)
				if ferr != nil {
					return errorResult(ferr.Error()), ProcOutput{}, nil
				}
				out := ProcOutput{
					ProcessID: input.ProcessID,
					Output:    strings.Join(lines, "\n"),
					Lines:     len(lines),
					Message:   note,
				}
				// Single-stream extract: scan the fetched lines and surface
				// signals at the top level (no MultiStream populated).
				if len(input.Extract) > 0 {
					sig := extractSignals(lines, input.Extract)
					out.Signals = &sig
					out.Output = appendSingleStreamBuildErrors(out.Output, sig.BuildErrors)
				}
				return nil, out, nil
			}
		}
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	out := ProcOutput{
		ProcessID: input.ProcessID,
		Output:    output,
	}
	if len(input.Extract) > 0 {
		sig := extractSignals(strings.Split(output, "\n"), input.Extract)
		out.Signals = &sig
		out.Output = appendSingleStreamBuildErrors(out.Output, sig.BuildErrors)
	}
	return nil, out, nil
}

// handleProcOutputMulti fans out a per-process ProcOutput call for each
// ID in ProcessIDs and assembles the interleaved/NDJSON response.
//
// Failures are non-fatal per process — a missing process_id surfaces as
// "[id] (error: …)" in the compact output, not a tool-level error. This
// matches the snapshot tool's degraded-but-useful posture: one bad ID
// shouldn't blackhole the whole multi-stream pull.
//
// All fetches run in parallel — N goroutines, N being typically 2-3.
// The same per-process filter applies to every stream (stream/tail/head/
// grep/grepv). A future enhancement could let each ID carry its own
// filter, but the spec doesn't require it.
func (dt *DaemonTools) handleProcOutputMulti(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	filter := protocol.OutputFilter{
		Stream: input.Stream,
		Tail:   input.Tail,
		Head:   input.Head,
		Grep:   input.Grep,
		GrepV:  input.GrepV,
	}

	// Pre-allocate the streams slice in input order so the agent gets
	// stable positional output across calls (parallel goroutines write
	// into their own index, no slice-append race).
	streams := make([]processStream, len(input.ProcessIDs))
	var wg sync.WaitGroup
	for i, id := range input.ProcessIDs {
		i, id := i, id // loop-var capture
		streams[i].ProcessID = id
		wg.Add(1)
		go func() {
			defer wg.Done()
			output, err := dt.client.ProcOutput(id, filter)
			if err != nil {
				streams[i].Err = err.Error()
				return
			}
			// Split into lines; drop trailing empty from final newline.
			lines := strings.Split(output, "\n")
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			streams[i].Lines = lines
		}()
	}
	wg.Wait()

	out := assembleMultiStreamOutput(streams, input.Extract, input.Raw)
	return nil, out, nil
}

// handleProcWait handles proc {action:"wait", process_id, signal(s),
// timeout, poll_ms}. Polls the daemon's ProcOutput until any of the
// requested signals appears in the output or the timeout elapses.
//
// Per the spec, timeout is NOT an error — the agent gets a structured
// result with timeout=true and decides what to do. Tool-level errors
// only fire on validation (missing process_id, no signal requested) or
// catastrophic IPC failure that prevents even the first poll.
func (dt *DaemonTools) handleProcWait(ctx context.Context, input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for wait"), ProcOutput{}, nil
	}
	wanted := input.Signals
	if len(wanted) == 0 && input.Signal != "" {
		wanted = []string{input.Signal}
	}
	if len(wanted) == 0 {
		return errorResult("signal (or signals) required for wait"), ProcOutput{}, nil
	}

	// We always pull from the tail of the buffer — long-running processes
	// can have huge logs, scanning everything every poll is wasteful. 200
	// lines is enough to catch the canonical ready/error markers without
	// blowing past the 256KB output buffer.
	filter := protocol.OutputFilter{
		Stream: "combined",
		Tail:   200,
	}
	fetch := func() ([]string, error) {
		out, err := dt.client.ProcOutput(input.ProcessID, filter)
		if err != nil {
			return nil, err
		}
		return strings.Split(out, "\n"), nil
	}

	// waitForSignal blocks (polling the daemon) until a signal appears or
	// the timeout elapses. Run it in a goroutine and honor the handler ctx
	// so a cancelled MCP call returns promptly. The buffered channel lets
	// the poll goroutine finish its send after we've returned; it
	// self-terminates at the wait timeout.
	ch := make(chan WaitResult, 1)
	go func() {
		ch <- waitForSignal(fetch, wanted, input.TimeoutMs, input.PollMs)
	}()

	select {
	case <-ctx.Done():
		return errorResult(fmt.Sprintf("wait cancelled: %v", ctx.Err())), ProcOutput{}, nil
	case res := <-ch:
		return nil, ProcOutput{
			ProcessID: input.ProcessID,
			Wait:      &res,
		}, nil
	}
}

func (dt *DaemonTools) handleProcFind(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for find"), ProcOutput{}, nil
	}
	if input.What == "" {
		return errorResult("what required for find (build-warnings, build-errors, compile-errors, type-errors, test-failures)"), ProcOutput{}, nil
	}

	// First get the process command so we can pick the right pattern.
	statusResult, err := dt.client.ProcStatus(input.ProcessID)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}
	cmdFull := getString(statusResult, "command")

	pattern, patErr := buildWhatPattern(input.What, cmdFull)
	if patErr != nil {
		return errorResult(patErr.Error()), ProcOutput{}, nil
	}

	filter := protocol.OutputFilter{
		Stream: "combined",
		Grep:   pattern,
		Tail:   input.Tail,
	}

	output, err := dt.client.ProcOutput(input.ProcessID, filter)
	if err != nil {
		// Fall back to script_output if the process isn't directly tracked.
		projectPath := getProjectPath()
		if projectPath != "" {
			scriptResult, scriptErr := dt.client.ScriptOutput(input.ProcessID, projectPath, input.Tail)
			if scriptErr == nil {
				lines := getStringSlice(scriptResult, "lines")
				re, reErr := regexp.Compile(pattern)
				if reErr != nil {
					return errorResult(fmt.Sprintf("internal: bad pattern for %q: %v", input.What, reErr)), ProcOutput{}, nil
				}
				var matched []string
				for _, line := range lines {
					if re.MatchString(line) {
						matched = append(matched, line)
					}
				}
				return nil, ProcOutput{
					ProcessID: input.ProcessID,
					Output:    strings.Join(matched, "\n"),
					Lines:     len(matched),
					Message:   fmt.Sprintf("found %d lines matching %q in process %s (via script_output)", len(matched), input.What, input.ProcessID),
				}, nil
			}
		}
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	lines := strings.Split(output, "\n")
	lineCount := 0
	for _, l := range lines {
		if l != "" {
			lineCount++
		}
	}

	return nil, ProcOutput{
		ProcessID: input.ProcessID,
		Output:    output,
		Lines:     lineCount,
		Message:   fmt.Sprintf("found %d lines matching %q in process %s", lineCount, input.What, input.ProcessID),
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

func (dt *DaemonTools) handleProcRestart(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for restart"), ProcOutput{}, nil
	}

	result, err := dt.client.ProcRestart(input.ProcessID)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	return nil, ProcOutput{
		ProcessID: getString(result, "process_id"),
		State:     getString(result, "state"),
		Success:   getBool(result, "success"),
		Message:   getString(result, "message"),
	}, nil
}

func (dt *DaemonTools) handleProcAutoRestart(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ProcessID == "" {
		return errorResult("process_id required for autorestart"), ProcOutput{}, nil
	}

	action := "disable"
	if input.AutoRestartEnable {
		action = "enable"
	}

	// Build config if enabling
	var config *daemon.ProcAutoRestartConfig
	if action == "enable" {
		config = &daemon.ProcAutoRestartConfig{
			MaxRestarts: input.MaxRestarts,
			OnlyOnError: input.OnlyOnError,
		}
	}

	result, err := dt.client.ProcAutoRestart(input.ProcessID, action, config)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	return nil, ProcOutput{
		ProcessID: getString(result, "id"),
		Success:   getBool(result, "auto_restart") == input.AutoRestartEnable,
		Message:   getString(result, "message"),
	}, nil
}

func (dt *DaemonTools) handleProcList(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {

	dirFilter := dt.scopeFilter(input.Global)

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
				id := getString(pm, "id")
				cmdFull := getString(pm, "command")
				entry := ProcEntry{
					ID:          id,
					Command:     cmdFull,
					State:       getString(pm, "state"),
					Summary:     getString(pm, "summary"),
					Runtime:     getString(pm, "runtime"),
					ProjectPath: getString(pm, "project_path"),
				}

				if idx := strings.Index(id, ":"); idx >= 0 {
					entry.ScriptName = id[idx+1:]
				}

				// Classify process role for build output discoverability.
				if cmdFull != "" {
					role := classifyProcess(cmdFull)
					entry.Role = role.Role
					entry.Produces = role.Produces
					entry.OutputHint = strings.ReplaceAll(role.OutputHint, "<id>", id)
				}

				// Surface waiting_for for processes still gated on deps.
				if waitingFor, ok := pm["waiting_for"].([]interface{}); ok {
					for _, w := range waitingFor {
						if s, ok := w.(string); ok {
							entry.WaitingFor = append(entry.WaitingFor, s)
						}
					}
				}

				populateLastExitFieldsEntry(&entry, pm)
				output.Processes = append(output.Processes, entry)
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

// handleProcRun handles proc {action:"run", id:..., command:..., depends_on:[...]}.
// Routes through PROC RUN on the daemon so the new process appears in
// SCRIPT LIST and the overlay admin screen, with optional dependency
// gating via the daemon-side pending tracker.
func (dt *DaemonTools) handleProcRun(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	id := input.ID
	if id == "" {
		id = input.ScriptName
	}
	if id == "" {
		return errorResult("proc run: id required (or pass script_name)"), ProcOutput{}, nil
	}
	if input.Run == "" && input.Command == "" {
		return errorResult("proc run: requires `run` or `command`"), ProcOutput{}, nil
	}

	projectPath := input.Path
	if projectPath == "" {
		projectPath = getProjectPath()
	}
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		return errorResult(fmt.Sprintf("proc run: failed to resolve path: %v", err)), ProcOutput{}, nil
	}

	cfg := daemon.ProcRunConfig{
		Run:              input.Run,
		Command:          input.Command,
		Args:             input.Args,
		Cwd:              input.Cwd,
		Env:              input.Env,
		URLMatchers:      input.URLMatchers,
		AutoRestart:      input.AutoRestart,
		ProjectPath:      absPath,
		DependsOn:        input.DependsOn,
		DependsOnTimeout: input.DependsOnTimeout,
	}

	result, err := dt.client.ProcRun(id, cfg)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	out := ProcOutput{
		ProcessID: getString(result, "process_id"),
		State:     getString(result, "state"),
	}
	if waitingFor, ok := result["waiting_for"].([]interface{}); ok {
		for _, w := range waitingFor {
			if s, ok := w.(string); ok {
				out.WaitingFor = append(out.WaitingFor, s)
			}
		}
	}
	return nil, out, nil
}

// handleProcRunGroup handles proc {action:"run_group", processes:[...]}.
// Routes through PROC RUN-GROUP on the daemon. Cycle detection runs
// before any process launches; if the dep graph has a cycle the call
// returns an error and no process is started.
func (dt *DaemonTools) handleProcRunGroup(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if len(input.Processes) == 0 {
		return errorResult("proc run_group: processes list cannot be empty"), ProcOutput{}, nil
	}

	projectPath := input.Path
	if projectPath == "" {
		projectPath = getProjectPath()
	}
	absPath, err := filepath.Abs(projectPath)
	if err != nil {
		return errorResult(fmt.Sprintf("proc run_group: failed to resolve path: %v", err)), ProcOutput{}, nil
	}

	groupProcs := make([]daemon.GroupProcess, 0, len(input.Processes))
	for i, gp := range input.Processes {
		if gp.ID == "" {
			return errorResult(fmt.Sprintf("proc run_group: process[%d] missing id", i)), ProcOutput{}, nil
		}
		if gp.Run == "" && gp.Command == "" {
			return errorResult(fmt.Sprintf("proc run_group: process %q requires `run` or `command`", gp.ID)), ProcOutput{}, nil
		}
		groupProcs = append(groupProcs, daemon.GroupProcess{
			Name:        gp.ID,
			Run:         gp.Run,
			Command:     gp.Command,
			Args:        gp.Args,
			Cwd:         gp.Cwd,
			Env:         gp.Env,
			URLMatchers: gp.URLMatchers,
			AutoRestart: gp.AutoRestart,
			DependsOn:   gp.DependsOn,
		})
	}

	cfg := daemon.ProcRunGroupConfig{
		ProjectPath:      absPath,
		DependsOnTimeout: input.DependsOnTimeout,
		Processes:        groupProcs,
	}

	result, err := dt.client.ProcRunGroup(cfg)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	out := ProcOutput{
		Count: getInt(result, "count"),
	}
	if procs, ok := result["processes"].([]interface{}); ok {
		for _, p := range procs {
			if pm, ok := p.(map[string]interface{}); ok {
				gpr := GroupProcessResult{
					ProcessID: getString(pm, "process_id"),
					State:     getString(pm, "state"),
					Error:     getString(pm, "error"),
				}
				if waitingFor, ok := pm["waiting_for"].([]interface{}); ok {
					for _, w := range waitingFor {
						if s, ok := w.(string); ok {
							gpr.WaitingFor = append(gpr.WaitingFor, s)
						}
					}
				}
				out.GroupResults = append(out.GroupResults, gpr)
			}
		}
	}
	return nil, out, nil
}

func (dt *DaemonTools) handleScriptList(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	projectPath := getProjectPath()
	if projectPath == "" {
		return errorResult("could not determine project path"), ProcOutput{}, nil
	}

	result, err := dt.client.ScriptList(projectPath)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	scripts, ok := result["scripts"].([]interface{})
	if !ok {
		return nil, ProcOutput{Count: 0}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== Scripts (%d) ===\n", len(scripts)))

	entries := make([]ProcEntry, 0, len(scripts))
	for _, s := range scripts {
		sm, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		name := getString(sm, "name")
		state := getString(sm, "state")
		startCount := getInt(sm, "start_count")
		failCount := getInt(sm, "fail_count")
		lastErr := getString(sm, "last_error")

		sb.WriteString(fmt.Sprintf("\n%s [%s] starts:%d fails:%d", name, state, startCount, failCount))
		if lastErr != "" {
			sb.WriteString(fmt.Sprintf("\n  last_error: %s", lastErr))
		}

		entries = append(entries, ProcEntry{
			ID:         getString(sm, "process_id"),
			State:      state,
			ScriptName: name,
		})
	}

	return nil, ProcOutput{
		Count:     len(entries),
		Processes: entries,
		Output:    sb.String(),
	}, nil
}

func (dt *DaemonTools) handleScriptOutput(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ScriptName == "" {
		return errorResult("script_name required for script_output"), ProcOutput{}, nil
	}

	projectPath := getProjectPath()
	if projectPath == "" {
		return errorResult("could not determine project path"), ProcOutput{}, nil
	}

	result, err := dt.client.ScriptOutput(input.ScriptName, projectPath, input.Tail)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	lines := getStringSlice(result, "lines")
	return nil, ProcOutput{
		Output: strings.Join(lines, "\n"),
		Lines:  len(lines),
	}, nil
}

func (dt *DaemonTools) handleScriptHistory(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	if input.ScriptName == "" {
		return errorResult("script_name required for script_history"), ProcOutput{}, nil
	}

	projectPath := getProjectPath()
	if projectPath == "" {
		return errorResult("could not determine project path"), ProcOutput{}, nil
	}

	result, err := dt.client.ScriptGet(input.ScriptName, projectPath)
	if err != nil {
		return formatDaemonError(err, "proc"), ProcOutput{}, nil
	}

	var sb strings.Builder
	name := getString(result, "name")
	state := getString(result, "state")
	sb.WriteString(fmt.Sprintf("=== %s [%s] ===\n", name, state))

	if history, ok := result["history"].([]interface{}); ok {
		sb.WriteString(fmt.Sprintf("\nTransitions (%d):\n", len(history)))
		for _, h := range history {
			if hm, ok := h.(map[string]interface{}); ok {
				sb.WriteString(fmt.Sprintf("  %s → %s\n", getString(hm, "timestamp"), getString(hm, "state")))
			}
		}
	}

	return nil, ProcOutput{
		Output: sb.String(),
	}, nil
}

// filterScriptFallbackLines applies the Grep/GrepV/Head portions of an
// OutputFilter to lines pulled from the script-history fallback (ScriptOutput
// only honors Tail daemon-side). It returns the filtered lines, a human-
// readable note for any filter it cannot honor (Stream — the script-history
// buffer is combined-only), and an error if a grep pattern is invalid.
func filterScriptFallbackLines(lines []string, filter protocol.OutputFilter) ([]string, string, error) {
	out := lines
	// Grep is the pattern; GrepV inverts the match (grep -v semantics).
	if filter.Grep != "" {
		re, err := regexp.Compile(filter.Grep)
		if err != nil {
			return nil, "", fmt.Errorf("invalid grep pattern %q: %w", filter.Grep, err)
		}
		kept := make([]string, 0, len(out))
		for _, l := range out {
			if re.MatchString(l) != filter.GrepV {
				kept = append(kept, l)
			}
		}
		out = kept
	}
	if filter.Head > 0 && len(out) > filter.Head {
		out = out[:filter.Head]
	}

	var note string
	if filter.Stream != "" && filter.Stream != "combined" {
		note = fmt.Sprintf("stream=%q not applied (script-history fallback is combined-only)", filter.Stream)
	}
	return out, note, nil
}

func resolveKDLScript(projectPath, scriptName string, extraArgs []string) (string, []string, error) {
	agntCfg, err := config.LoadAgntConfig(projectPath)
	if err != nil || agntCfg == nil {
		return "", nil, fmt.Errorf("no .agnt.kdl config")
	}

	scriptCfg, ok := agntCfg.Scripts[scriptName]
	if !ok {
		return "", nil, fmt.Errorf("script %q not in .agnt.kdl", scriptName)
	}

	if scriptCfg.Run != "" {
		shell, shellArgs := scriptCfg.ResolveShell()
		return shell, append(shellArgs, extraArgs...), nil
	}

	if scriptCfg.Command != "" {
		return scriptCfg.Command, append(scriptCfg.Args, extraArgs...), nil
	}

	proj, err := project.Detect(projectPath)
	if err != nil {
		return "", nil, fmt.Errorf("no command/run and project detection failed: %w", err)
	}
	pm := proj.PackageManager
	if pm == "" {
		pm = "npm"
	}
	return pm, append([]string{"run", scriptName}, extraArgs...), nil
}

// populateLastExitFields copies last-exit fields from a daemon response
// map into the shared LastExitFields embedded by ProcOutput/ProcEntry.
// Uses pointer-to-int for LastExitCode so a real zero exit code (clean
// shutdown) is distinguishable from "field absent".
func populateLastExitFields(out *ProcOutput, resp map[string]interface{}) {
	if out == nil {
		return
	}
	populateLastExit(&out.LastExitFields, resp)
}

// populateLastExitFieldsEntry is the per-entry variant for proc list.
func populateLastExitFieldsEntry(entry *ProcEntry, resp map[string]interface{}) {
	if entry == nil {
		return
	}
	populateLastExit(&entry.LastExitFields, resp)
}

func populateLastExit(fields *LastExitFields, resp map[string]interface{}) {
	if resp == nil {
		return
	}
	if at := getString(resp, "last_exit_at"); at != "" {
		fields.LastExitAt = at
	}
	if _, ok := resp["last_exit_code"]; ok {
		code := getInt(resp, "last_exit_code")
		fields.LastExitCode = &code
	}
	if reason := getString(resp, "last_exit_reason"); reason != "" {
		fields.LastExitReason = reason
	}
	if uptime := getString(resp, "last_uptime"); uptime != "" {
		fields.LastUptime = uptime
	}
	if tail := getString(resp, "last_stderr_tail"); tail != "" {
		fields.LastStderrTail = tail
	}
}
