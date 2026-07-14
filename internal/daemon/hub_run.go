package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/standardbeagle/agnt/internal/debug"

	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
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

	// RUN/RUN-JSON commands customize the shared hub's default handlers to add
	// atomic auto-restart registration. Hub's handler returns "id" but tools
	// expect "process_id", and auto-restart must be registered before the
	// response so there's no race with fast-crashing processes.
	if err := replaceDefault("RUN", "Start a process with auto-restart", d.hubHandleRun); err != nil {
		return err
	}
	if err := replaceDefault("RUN-JSON", "Start a process with auto-restart (JSON config)", d.hubHandleRun); err != nil {
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
		Description: "Restart all processes and proxies using .agnt.kdl config",
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

// agntRunConfig extends the hub's RunConfig with agnt-specific fields.
// Extra fields are ignored by the hub's JSON unmarshaling but parsed here.
type agntRunConfig struct {
	hubproto.RunConfig
	NoAutoRestart bool `json:"no_auto_restart,omitempty"`
}

// hubHandleRun handles RUN and RUN-JSON commands (overrides Hub's built-in).
// Adds atomic auto-restart registration for background processes and returns
// "process_id" (matching what daemon_tools.go expects).

func (d *Daemon) hubHandleRun(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	cfg, err := unmarshalCommand[agntRunConfig](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid JSON config")
	}

	if cfg.Command == "" && cfg.ScriptName == "" {
		return conn.WriteMissingParam("RUN", "command", "command or script_name required")
	}

	procCfg := goprocess.ProcessConfig{
		ID:          cfg.ID,
		ProjectPath: cfg.Path,
		Command:     cfg.Command,
		Args:        cfg.Args,
		Env:         cfg.Env,
		EnableStdin: cfg.EnableStdin,
	}

	result, err := d.hub.ProcessManager().StartOrReuse(ctx, procCfg)
	if err != nil {
		return conn.WriteInternalErr(err.Error())
	}

	proc := result.Process

	// Register process-death watcher so proc status / get_errors surface
	// the exit when this process dies. Skip if we reused an existing
	// process — it already has a watcher from the original start.
	if !result.Reused {
		d.watchProcessExit(proc)
	}

	// Auto-restart is only active when explicitly enabled via .agnt.kdl
	// `auto-restart true` or PROC AUTORESTART ENABLE. No longer auto-registered
	// for background processes — users restart manually from the overlay.

	response := map[string]interface{}{
		"id":         proc.ID,
		"process_id": proc.ID,
		"pid":        proc.PID(),
		"state":      proc.State().String(),
		"reused":     result.Reused,
		"cleaned":    result.Cleaned,
		"command":    proc.Command,
	}

	data, _ := json.Marshal(response)
	return conn.WriteJSON(data)
}

// hubHandleProc handles the PROC command (overrides Hub's built-in).
// Adds URL tracking and project-based filtering.
