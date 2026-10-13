package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"

	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
	"github.com/standardbeagle/go-cli-server/script"
)

func (d *Daemon) registerAgntCommands() error {
	registered := 0

	register := func(def hubpkg.CommandDefinition) error {
		if err := d.hub.RegisterCommand(def); err != nil {
			return fmt.Errorf("register %s: %w", def.Verb, err)
		}
		registered++
		return nil
	}
	replaceDefault := func(verb, description string, handler hubpkg.CommandHandler) error {
		if d.hub.HasCommand(verb) {
			if err := d.hub.ReplaceCommandHandler(verb, handler); err != nil {
				return fmt.Errorf("replace %s: %w", verb, err)
			}
			return nil
		}
		return register(hubpkg.CommandDefinition{
			Verb:        verb,
			Description: description,
			Handler:     handler,
		})
	}
	extendOrReplaceProc := func(subVerb string, handler hubpkg.CommandHandler) error {
		if d.hub.HasCommand("PROC") {
			if containsHubSubVerb(d.hub.ValidSubVerbs("PROC"), subVerb) {
				if err := d.hub.ReplaceSubHandler("PROC", subVerb, handler); err != nil {
					return fmt.Errorf("replace PROC %s: %w", subVerb, err)
				}
				return nil
			}
			if err := d.hub.ExtendCommand(hubpkg.CommandDefinition{
				Verb:     "PROC",
				SubVerbs: []string{subVerb},
				Handler:  handler,
			}); err != nil {
				return fmt.Errorf("extend PROC %s: %w", subVerb, err)
			}
			return nil
		}
		return fmt.Errorf("PROC command is not registered")
	}

	// RUN/RUN-JSON commands replace the shared hub's default handlers: agnt
	// starts the process through the same path as PROC RUN's inline mode
	// (see hubHandleRun). RUN never registers auto-restart — that is
	// `auto-restart` in .agnt.kdl or PROC AUTORESTART.
	if err := replaceDefault("RUN", "Start a process", d.hubHandleRun); err != nil {
		return err
	}
	if err := replaceDefault("RUN-JSON", "Start a process (JSON config)", d.hubHandleRun); err != nil {
		return err
	}

	// PROC command: keep the shared hub verb and STDIN action, then replace the
	// subcommands where agnt adds URL tracking/project filtering and extend it
	// with agnt-specific actions.
	procSubVerbs := routerSubVerbs(d.procActions())
	if !d.hub.HasCommand("PROC") {
		if err := register(hubpkg.CommandDefinition{
			Verb:        "PROC",
			SubVerbs:    procSubVerbs,
			Description: "Manage running processes",
			Handler:     d.hubHandleProc,
		}); err != nil {
			return err
		}
	} else {
		for _, subVerb := range procSubVerbs {
			if err := extendOrReplaceProc(subVerb, d.hubHandleProc); err != nil {
				return err
			}
		}
		// Take the default handler too. Every sub-verb above is agnt's own, so
		// what is left for the shared hub's default is the unknown and the
		// missing action, which it answers with a structured error sent as a
		// JSON (success) frame. hubHandleProc refuses both through
		// writeStructuredErr, as an ERR frame the client sees as an error.
		if err := d.hub.ReplaceCommandHandler("PROC", d.hubHandleProc); err != nil {
			return fmt.Errorf("replace PROC default handler: %w", err)
		}
	}

	// SCOPE command
	if err := register(hubpkg.CommandDefinition{
		Verb:        protocol.VerbScope,
		SubVerbs:    []string{protocol.SubVerbResolve},
		Description: "Resolve effective query scope",
		Handler:     d.hubHandleScope,
	}); err != nil {
		return err
	}

	// DETECT command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "DETECT",
		Description: "Detect project type and available scripts",
		Handler:     d.hubHandleDetect,
	}); err != nil {
		return err
	}

	// PROXY command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "PROXY",
		SubVerbs:    routerSubVerbs(d.proxyActions()),
		Description: "Manage reverse proxies",
		Handler:     d.hubHandleProxy,
	}); err != nil {
		return err
	}

	// PROXYLOG command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "PROXYLOG",
		SubVerbs:    routerSubVerbs(d.proxyLogActions()),
		Description: "Query proxy traffic logs",
		Handler:     d.hubHandleProxyLog,
	}); err != nil {
		return err
	}

	// CURRENTPAGE command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "CURRENTPAGE",
		SubVerbs:    routerSubVerbs(d.currentPageActions()),
		Description: "View active page sessions",
		Handler:     d.hubHandleCurrentPage,
	}); err != nil {
		return err
	}

	// OVERLAY command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "OVERLAY",
		SubVerbs:    routerSubVerbs(d.overlayActions()),
		Description: "Configure overlay endpoint",
		Handler:     d.hubHandleOverlay,
	}); err != nil {
		return err
	}

	// TUNNEL command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "TUNNEL",
		SubVerbs:    routerSubVerbs(d.tunnelActions()),
		Description: "Manage tunnel connections",
		Handler:     d.hubHandleTunnel,
	}); err != nil {
		return err
	}

	// BROWSER command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "BROWSER",
		SubVerbs:    routerSubVerbs(d.browserActions()),
		Description: "Manage browser instances",
		Handler:     d.hubHandleBrowser,
	}); err != nil {
		return err
	}

	// AUTOMATION command (chromedp sessions for programmatic browser control)
	if err := register(hubpkg.CommandDefinition{
		Verb:        "AUTOMATION",
		SubVerbs:    routerSubVerbs(d.automationActions()),
		Description: "Control browser automation sessions (chromedp)",
		Handler:     d.hubHandleAutomation,
	}); err != nil {
		return err
	}

	// CHAOS command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "CHAOS",
		SubVerbs:    routerSubVerbs(d.chaosActions()),
		Description: "Configure chaos engineering rules",
		Handler:     d.hubHandleChaos,
	}); err != nil {
		return err
	}

	// SESSION command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "SESSION",
		SubVerbs:    routerSubVerbs(d.sessionActions()),
		Description: "Manage client sessions",
		Handler:     d.hubHandleSession,
	}); err != nil {
		return err
	}

	// SESSION-HOST command - daemon-owned detachable PTY sessions
	if err := register(hubpkg.CommandDefinition{
		Verb:        protocol.VerbSessionHost,
		SubVerbs:    routerSubVerbs(d.sessionHostActions()),
		Description: "Manage daemon-owned detachable PTY sessions",
		Handler:     d.hubHandleSessionHost,
	}); err != nil {
		return err
	}

	// STATUS command - returns full daemon info (Hub's INFO is minimal)
	if err := register(hubpkg.CommandDefinition{
		Verb:        "STATUS",
		Description: "Get full daemon status and statistics",
		Handler:     d.hubHandleStatus,
	}); err != nil {
		return err
	}

	// STORE command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "STORE",
		SubVerbs:    routerSubVerbs(d.storeActions()),
		Description: "Manage persistent key-value storage",
		Handler:     d.hubHandleStore,
	}); err != nil {
		return err
	}

	// PUBLISH command — public walkthrough share-token lifecycle (control plane).
	if err := register(hubpkg.CommandDefinition{
		Verb:        protocol.VerbPublish,
		SubVerbs:    routerSubVerbs(d.publishActions()),
		Description: "Manage public walkthrough shares (create/status/list/revoke/rotate)",
		Handler:     d.hubHandlePublish,
	}); err != nil {
		return err
	}

	// AUTOMATE command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "AUTOMATE",
		SubVerbs:    routerSubVerbs(d.automateActions()),
		Description: "Process automation tasks using AI",
		Handler:     d.hubHandleAutomate,
	}); err != nil {
		return err
	}

	// ALERTS command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "ALERTS",
		SubVerbs:    routerSubVerbs(d.alertsActions()),
		Description: "Process output alert queries",
		Handler:     d.hubHandleAlerts,
	}); err != nil {
		return err
	}

	// INCIDENTS command
	if err := register(hubpkg.CommandDefinition{
		Verb:        protocol.VerbIncidents,
		SubVerbs:    routerSubVerbs(d.incidentsActions()),
		Description: "Query the per-session incident inbox",
		Handler:     d.hubHandleIncidents,
	}); err != nil {
		return err
	}

	// INVESTIGATION command - per-session investigation record (read + merge)
	if err := register(hubpkg.CommandDefinition{
		Verb:        protocol.VerbInvestigation,
		SubVerbs:    routerSubVerbs(d.investigationActions()),
		Description: "Read and merge the per-session investigation record",
		Handler:     d.hubHandleInvestigation,
	}); err != nil {
		return err
	}

	// PORTS command - listening-port inventory + orphan pgid management
	if err := register(hubpkg.CommandDefinition{
		Verb:        protocol.VerbPorts,
		SubVerbs:    routerSubVerbs(d.portsActions()),
		Description: "List listening ports and manage orphaned process groups",
		Handler:     d.hubHandlePorts,
	}); err != nil {
		return err
	}

	// SCRIPT command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "SCRIPT",
		SubVerbs:    routerSubVerbs(d.scriptActions()),
		Description: "Query and control managed scripts",
		Handler:     d.hubHandleScript,
	}); err != nil {
		return err
	}

	// DOCTOR command
	if err := register(hubpkg.CommandDefinition{
		Verb:        protocol.VerbDoctor,
		Description: "Run health checks and return diagnostic report",
		Handler:     d.hubHandleDoctor,
	}); err != nil {
		return err
	}

	// AUTOSTART command
	if err := register(hubpkg.CommandDefinition{
		Verb:        protocol.VerbAutostart,
		SubVerbs:    routerSubVerbs(d.autostartActions()),
		Description: "Resolve port conflicts and resume autostart",
		Handler:     d.hubHandleAutostart,
	}); err != nil {
		return err
	}

	// STOP-ALL command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "STOP-ALL",
		Description: "Stop all running processes, proxies, and tunnels",
		Handler:     d.hubHandleStopAll,
	}); err != nil {
		return err
	}

	// RESTART-ALL command
	if err := register(hubpkg.CommandDefinition{
		Verb:        "RESTART-ALL",
		Description: "Restart running processes and proxies in place, keeping their launch settings",
		Handler:     d.hubHandleRestartAll,
	}); err != nil {
		return err
	}

	// STREAM-EVENTS command
	if err := register(hubpkg.CommandDefinition{
		Verb:        protocol.VerbStreamEvents,
		Description: "Stream proxy events in real-time with optional filters",
		Handler:     d.hubHandleStreamEvents,
	}); err != nil {
		return err
	}

	// HOOK command - Claude Code hook dispatcher enqueue (phase 1 scope).
	// Hot path: push into ring buffer and ack OK. All fan-out happens off
	// the socket goroutine via drainHooks.
	if err := register(hubpkg.CommandDefinition{
		Verb:        protocol.VerbHook,
		Description: "Enqueue a Claude Code hook event for async fan-out",
		Handler:     d.hubHandleHook,
	}); err != nil {
		return err
	}

	// SHIM command - shell shim routing (EXEC) + install bookkeeping
	// (REGISTER). EXEC can block for the duration of a one-shot build/test;
	// it runs on its own connection goroutine like every other handler.
	if err := register(hubpkg.CommandDefinition{
		Verb:        protocol.VerbShim,
		SubVerbs:    routerSubVerbs(d.shimActions()),
		Description: "Route shimmed shell commands through managed processes",
		Handler:     d.hubHandleShim,
	}); err != nil {
		return err
	}

	debug.Log("daemon", "Registered %d agnt-specific commands with Hub", registered)
	return nil
}

func containsHubSubVerb(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// Run modes on the wire (RunConfig.Mode, PROC RUN `mode`). Empty means background.
const (
	runModeBackground    = "background"
	runModeForeground    = "foreground"
	runModeForegroundRaw = "foreground-raw"
)

// validRunMode reports whether mode is one the daemon implements. An unknown
// mode is refused rather than silently run in the background.
func validRunMode(mode string) bool {
	switch mode {
	case "", runModeBackground, runModeForeground, runModeForegroundRaw:
		return true
	}
	return false
}

func isForegroundRunMode(mode string) bool {
	return mode == runModeForeground || mode == runModeForegroundRaw
}

// awaitForegroundRun implements the foreground modes: it blocks until proc
// exits, then adds the completion fields to resp — exit_code, the final state,
// and runtime, plus the captured stdout/stderr for foreground-raw.
//
// The wait ends early only when ctx does (the request was abandoned or the
// daemon is stopping); the process is left running and the error says so. The
// hub's STATUS frames keep the client's idle deadline alive meanwhile.
func awaitForegroundRun(ctx context.Context, proc *goprocess.ManagedProcess, mode string, resp map[string]interface{}) error {
	select {
	case <-proc.Done():
	case <-ctx.Done():
		return fmt.Errorf("stopped waiting for process %q, which is still running: %w", proc.ID, ctx.Err())
	}
	resp["state"] = proc.State().String()
	resp["exit_code"] = proc.ExitCode()
	resp["runtime"] = formatDuration(proc.Runtime())
	if mode == runModeForegroundRaw {
		stdout, _ := proc.Stdout()
		stderr, _ := proc.Stderr()
		resp["stdout"] = string(stdout)
		resp["stderr"] = string(stderr)
	}
	return nil
}

// runLaunch marks an ad-hoc launch: a process started from a command line the
// caller supplied — the top-level RUN verb, PROC RUN in inline mode, PROC
// RUN-GROUP — rather than from a script .agnt.kdl declares. It has a
// script-registry entry like any managed process, but the config has no
// opinion on it, so everything that compares the registry with the config
// asks isAdHocLaunch first:
//
//   - config reconcile does not stop it as "running, no longer declared";
//   - the autostart prune does not delete its entry as "not in config";
//   - a zero exit inside the startup watch is a finished one-shot, not a
//     failed start (StartScript).
//
// A PROC RUN from_config process is declared in config and is never marked.
// Not user-visible. Stored by markAdHocLaunch, retired with scriptConfigs.
type runLaunch struct {
	// stdin is RunConfig.EnableStdin: the process gets a stdin pipe that
	// PROC STDIN writes to.
	stdin bool
}

// markAdHocLaunch records processID as an ad-hoc launch (see runLaunch).
func (d *Daemon) markAdHocLaunch(processID string, stdin bool) {
	d.runLaunches.Store(processID, runLaunch{stdin: stdin})
}

// isAdHocLaunch is the single definition of "this process is an ad-hoc
// command, not a config script" (see runLaunch).
func (d *Daemon) isAdHocLaunch(processID string) bool {
	_, ok := d.runLaunches.Load(processID)
	return ok
}

// runStdinEnabled reports whether processID was started by RUN with a stdin
// pipe requested.
func (d *Daemon) runStdinEnabled(processID string) bool {
	if v, ok := d.runLaunches.Load(processID); ok {
		return v.(runLaunch).stdin
	}
	return false
}

// runProcessName turns the id a RUN caller supplied into the script name the
// process is registered under. The caller normally sends a short name ("dev",
// "proc-12345"); a caller that sends back the derived id an earlier RUN
// returned for this project means the same process, so the project prefix is
// dropped rather than nested. An empty id gets a generated name.
func runProcessName(id, projectPath string) string {
	if id == "" {
		return fmt.Sprintf("proc-%d", time.Now().UnixNano()%100000)
	}
	return strings.TrimPrefix(id, makeProcessID(projectPath, ""))
}

// runReply is the RUN reply for proc. "id" and "process_id" are the process's
// derived id, usable verbatim in every PROC verb; "name" is the short name it
// was started under, which resolves from the same project.
func runReply(proc *goprocess.ManagedProcess, name string, reused, cleaned bool) map[string]interface{} {
	resp := map[string]interface{}{
		"id":         proc.ID,
		"process_id": proc.ID,
		"name":       name,
		"pid":        proc.PID(),
		"state":      proc.State().String(),
		"reused":     reused,
		"cleaned":    cleaned,
		"command":    proc.Command,
	}
	if proc.IsDone() {
		resp["exit_code"] = proc.ExitCode()
	}
	return resp
}

// hubHandleRun handles RUN and RUN-JSON commands (overrides Hub's built-in).
//
// RUN is PROC RUN's inline mode for an ad-hoc command line: the process is
// started through StartProcessWithDeps → StartScriptExplicit → StartScript,
// so it gets a script-registry entry, pre-flight port cleanup and EADDRINUSE
// recovery, URL detection, an incident owner, and session cleanup like every
// other managed process. Its id is therefore the derived
// makeProcessID(project, <id>) form, not the bare id the caller sent.
//
// What RUN keeps of its own:
//
//   - reuse: an id that is already running is returned as-is with
//     reused:true — no second start, startup watch or exit watcher.
//   - background mode replies as soon as the process exists; the startup
//     watch (and any EADDRINUSE retry) continues on the daemon's context.
//     An early exit is reported by the exit watcher, not by this reply.
//   - enable_stdin (see runLaunch).
//   - the foreground modes reply when the process exits, with exit_code,
//     state, runtime and, for foreground-raw, the captured output.
func (d *Daemon) hubHandleRun(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	// Fields this handler does not know (older clients send no_auto_restart;
	// RUN never auto-restarts, so there is nothing for it to turn off) are
	// ignored by the decoder.
	cfg, err := unmarshalCommand[hubproto.RunConfig](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid JSON config")
	}

	// RUN launches a command line. It does not resolve script names — the
	// client does (or uses PROC RUN from_config for a script in .agnt.kdl) —
	// so a script_name with no command has nothing to start.
	if cfg.Command == "" {
		// A plain ERR, not conn.WriteMissingParam: the hub sends that one as a
		// JSON (success) frame, so the caller would read a refused start as a
		// successful one (see writeStructuredErr).
		return conn.WriteErr(hubproto.ErrMissingParam, "RUN: command required (RUN does not resolve script_name; use PROC RUN from_config for a script declared in .agnt.kdl)")
	}
	if !validRunMode(cfg.Mode) {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("unknown mode %q, valid: background, foreground, foreground-raw", cfg.Mode))
	}

	// A managed process belongs to a project: the registry, session cleanup
	// and the id itself are keyed by it. Explicit path, else the caller's
	// session.
	projectPath := cfg.Path
	if projectPath == "" {
		projectPath = d.getSessionProjectPath(conn)
	}
	if projectPath == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "RUN: path required (or send it from a connection bound to a session)")
	}
	projectPath = normalizePath(projectPath)

	name := runProcessName(cfg.ID, projectPath)
	processID := makeProcessID(projectPath, name)
	pm := d.hub.ProcessManager()

	// Reuse: a live process under this id is the answer. Nothing is started,
	// so nothing is watched twice.
	prev, _ := pm.GetByPath(processID, projectPath)
	if prev != nil {
		if state := prev.State(); state == goprocess.StateRunning || state == goprocess.StateStarting {
			data, _ := json.Marshal(runReply(prev, name, true, false))
			return conn.WriteJSON(data)
		}
	}

	// The registry is a cache. An entry still marked running with no live
	// process behind it would make StartScriptExplicit skip the launch as
	// "already running".
	if entry, ok := d.scriptRegistry.Get(name, projectPath); ok {
		if state := entry.State(); state == script.StateRunning || state == script.StateStarting {
			entry.CompareAndSwapState(state, script.StateStopped)
		}
	}

	scriptCfg := &config.ScriptConfig{
		Command: cfg.Command,
		Args:    cfg.Args,
		Env:     envSliceToMap(cfg.Env),
	}
	d.markAdHocLaunch(processID, cfg.EnableStdin)

	// startedProc returns the process this RUN spawned: one registered under
	// the id that is not the stopped instance found above, and that got as far
	// as having a PID. The ProcessManager registers a process before it
	// spawns it, so "registered" alone is too early to reply on, and a spawn
	// that failed (command not found) never gets a PID at all.
	startedProc := func() *goprocess.ManagedProcess {
		cur, getErr := pm.GetByPath(processID, projectPath)
		if getErr != nil || cur == prev || cur.PID() <= 0 {
			return nil
		}
		return cur
	}

	if isForegroundRunMode(cfg.Mode) {
		result := d.StartProcessWithDeps(withAutostartOwner(ctx, conn.SessionCode()), name, scriptCfg, projectPath, nil, 0)
		proc := startedProc()
		// StartScript reports any exit inside its startup window as a failed
		// start. A foreground caller asked for exactly that exit: it gets the
		// process's result, not the startup-window error.
		if proc == nil || (result.Err != nil && !proc.IsDone()) {
			if result.Err != nil {
				return conn.WriteErr(hubproto.ErrInternal, result.Err.Error())
			}
			return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("process %q did not start", processID))
		}
		d.settleRunEntry(name, projectPath, proc)
		resp := runReply(proc, name, false, prev != nil)
		if err := awaitForegroundRun(ctx, proc, cfg.Mode, resp); err != nil {
			return conn.WriteErr(hubproto.ErrInternal, err.Error())
		}
		data, _ := json.Marshal(resp)
		return conn.WriteJSON(data)
	}

	// Background: the start runs on the daemon's context so the startup watch
	// and EADDRINUSE recovery outlive this request, and the reply goes out as
	// soon as the process exists.
	done := make(chan error, 1)
	launchCtx := withAutostartOwner(d.ctx, conn.SessionCode())
	if !d.goTracked(func() {
		result := d.StartProcessWithDeps(launchCtx, name, scriptCfg, projectPath, nil, 0)
		if proc := startedProc(); proc != nil {
			d.settleRunEntry(name, projectPath, proc)
		}
		done <- result.Err
	}) {
		return conn.WriteErr(hubproto.ErrShuttingDown, "daemon is shutting down")
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if proc := startedProc(); proc != nil {
			data, _ := json.Marshal(runReply(proc, name, false, prev != nil))
			return conn.WriteJSON(data)
		}
		select {
		case startErr := <-done:
			// The start finished. A process that exists — even one that has
			// already exited — is a start; its fate is reported by the exit
			// watcher. No process at all is a failed start.
			if proc := startedProc(); proc != nil {
				data, _ := json.Marshal(runReply(proc, name, false, prev != nil))
				return conn.WriteJSON(data)
			}
			if startErr == nil {
				startErr = fmt.Errorf("process %q did not start", processID)
			}
			return conn.WriteErr(hubproto.ErrInternal, startErr.Error())
		case <-ctx.Done():
			return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("stopped waiting for process %q to start: %v", processID, ctx.Err()))
		case <-ticker.C:
		}
	}
}

// settleRunEntry corrects the script-registry entry of a RUN process that ran
// to completion inside the startup watch. StartScriptExplicit marks any such
// exit as a failed start; a one-shot command that exited 0 did not fail, and
// the registry must not say it did.
func (d *Daemon) settleRunEntry(name, projectPath string, proc *goprocess.ManagedProcess) {
	if !proc.IsDone() || proc.ExitCode() != 0 {
		return
	}
	if entry, ok := d.scriptRegistry.Get(name, projectPath); ok {
		entry.CompareAndSwapState(script.StateFailed, script.StateStopped)
	}
}

// hubHandleProc handles the PROC command (overrides Hub's built-in).
// Adds URL tracking and project-based filtering.
