package daemon

import (
	"context"
	"encoding/json"

	"github.com/standardbeagle/agnt/internal/debug"

	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) registerAgntCommands() {
	// RUN/RUN-JSON commands - override Hub's to add atomic auto-restart registration.
	// Hub's handler returns "id" but tools expect "process_id", and auto-restart
	// must be registered before the response so there's no race with fast-crashing processes.
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "RUN",
		Description: "Start a process with auto-restart",
		Handler:     d.hubHandleRun,
	})
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "RUN-JSON",
		Description: "Start a process with auto-restart (JSON config)",
		Handler:     d.hubHandleRun,
	})

	// PROC command - override Hub's to add URL tracking and project filtering
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "PROC",
		SubVerbs:    []string{"STATUS", "OUTPUT", "STOP", "RESTART", "LIST", "CLEANUP-PORT", "AUTORESTART"},
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
		SubVerbs:    []string{"START", "STOP", "RESTART", "STATUS", "LIST", "EXEC", "TOAST"},
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
		SubVerbs:    []string{"SET", "GET", "CLEAR", "ACTIVITY", "OUTPUT-PREVIEW"},
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

	// BROWSER command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "BROWSER",
		SubVerbs:    []string{"START", "STOP", "STATUS", "LIST"},
		Description: "Manage browser instances",
		Handler:     d.hubHandleBrowser,
	})

	// AUTOMATION command (chromedp sessions for programmatic browser control)
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "AUTOMATION",
		SubVerbs:    []string{"START", "STOP", "STATUS", "LIST", "SCREENSHOT", "NAVIGATE", "EVALUATE"},
		Description: "Control browser automation sessions (chromedp)",
		Handler:     d.hubHandleAutomation,
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

	// STORE command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "STORE",
		SubVerbs:    []string{"GET", "SET", "DELETE", "LIST", "CLEAR", "GET-ALL"},
		Description: "Manage persistent key-value storage",
		Handler:     d.hubHandleStore,
	})

	// AUTOMATE command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "AUTOMATE",
		SubVerbs:    []string{"PROCESS", "BATCH"},
		Description: "Process automation tasks using AI",
		Handler:     d.hubHandleAutomate,
	})

	// ALERTS command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "ALERTS",
		SubVerbs:    []string{"REPORT", "QUERY", "CLEAR", "STARTUP-LOG"},
		Description: "Process output alert queries",
		Handler:     d.hubHandleAlerts,
	})

	// SCRIPT command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "SCRIPT",
		SubVerbs:    []string{"LIST", "GET", "OUTPUT", "RESTART", "STOP"},
		Description: "Query and control managed scripts",
		Handler:     d.hubHandleScript,
	})

	// DOCTOR command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        protocol.VerbDoctor,
		Description: "Run health checks and return diagnostic report",
		Handler:     d.hubHandleDoctor,
	})

	// AUTOSTART command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        protocol.VerbAutostart,
		SubVerbs:    []string{protocol.SubVerbClearPorts, protocol.SubVerbContinue, protocol.SubVerbAutostartRun},
		Description: "Resolve port conflicts and resume autostart",
		Handler:     d.hubHandleAutostart,
	})

	// STOP-ALL command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "STOP-ALL",
		Description: "Stop all running processes, proxies, and tunnels",
		Handler:     d.hubHandleStopAll,
	})

	// RESTART-ALL command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        "RESTART-ALL",
		Description: "Restart all processes and proxies using .agnt.kdl config",
		Handler:     d.hubHandleRestartAll,
	})

	// STREAM-EVENTS command
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        protocol.VerbStreamEvents,
		Description: "Stream proxy events in real-time with optional filters",
		Handler:     d.hubHandleStreamEvents,
	})

	// HOOK command - Claude Code hook dispatcher enqueue (phase 1 scope).
	// Hot path: push into ring buffer and ack OK. All fan-out happens off
	// the socket goroutine via drainHooks.
	d.hub.RegisterCommand(hubpkg.CommandDefinition{
		Verb:        protocol.VerbHook,
		Description: "Enqueue a Claude Code hook event for async fan-out",
		Handler:     d.hubHandleHook,
	})

	debug.Log("daemon", "Registered %d agnt-specific commands with Hub", 24)
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
	var cfg agntRunConfig
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &cfg); err != nil {
			return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid JSON config")
		}
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
