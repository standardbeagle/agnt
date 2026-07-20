package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/go-cli-server/script"
)

// ReconcileProjectConfig brings the running scripts for projectPath in line
// with the freshly loaded `.agnt.kdl`, WITHOUT restarting the daemon or the AI
// session. This is the live-edit path: setup (or a hand edit) writes config,
// and the dev servers it declares come up — or torn-down ones go away, or
// changed ones relaunch — in place.
//
// Division of labor: the reconcile-specific work is only the STOPS (removed and
// changed scripts). Starting is delegated to the heavily-tested RunAutostart
// path, which re-registers every declared script with its new config, prunes
// stale registry entries, starts adds + the just-stopped changed scripts in
// dependency order, skips still-running unchanged scripts, and materializes
// declared proxies. Piece C hardened the stop→start primitives this relies on.
//
// Returns the computed plan (what it decided to do) so callers can report it.
func (d *Daemon) ReconcileProjectConfig(ctx context.Context, projectPath string) (ReconcilePlan, error) {
	if projectPath == "" {
		return ReconcilePlan{}, nil
	}
	projectPath = normalizePath(projectPath)

	cfg, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		return ReconcilePlan{}, err
	}
	if cfg == nil {
		cfg = config.DefaultAgntConfig()
	}

	// Desired = scripts the config says should be running, by launch signature.
	desired := make(map[string]string)
	for name, sc := range cfg.GetAutostartScripts() {
		desired[name] = scriptSignature(sc)
	}

	// Running = scripts currently materialized for this project, keyed by name,
	// signed from the config snapshot captured when each was last started.
	running := make(map[string]string)
	for _, entry := range d.scriptRegistry.List(projectPath) {
		switch entry.State() {
		case script.StateRunning, script.StateStarting:
		default:
			continue
		}
		if sc, ok := d.scriptConfigs.Load(entry.ProcessID); ok {
			running[entry.Name] = scriptSignature(sc.(*config.ScriptConfig))
		} else {
			// Running but no stored config — force a restart so it picks up
			// whatever the config now declares rather than lingering unknown.
			running[entry.Name] = "\x00unknown"
		}
	}

	plan := computeReconcile(desired, running, nil, nil)

	log := d.startupLog(projectPath)
	if plan.IsEmpty() {
		log.Info("", "reconcile", "config reconcile: no changes")
		return plan, nil
	}
	log.Info("", "reconcile", fmt.Sprintf("config reconcile: start=%v stop=%v restart=%v",
		plan.StartScripts, plan.StopScripts, plan.RestartScripts))

	// Stop removed scripts (prune their registry/config), then stop changed
	// scripts (leave the entry so RunAutostart restarts them with new config).
	for _, name := range plan.StopScripts {
		d.stopReconcileScript(ctx, name, projectPath, true)
	}
	for _, name := range plan.RestartScripts {
		d.stopReconcileScript(ctx, name, projectPath, false)
	}

	// Delegate all starts (adds + just-stopped changed) and proxy
	// materialization to the autostart path. Idempotent for unchanged running
	// scripts (StartScriptExplicit skips them).
	d.RunAutostart(ctx, projectPath)
	return plan, nil
}

// stopReconcileScript stops the managed process for a script and clears its
// runtime state. When prune is true the script is gone from config, so its
// registry + config-cache + auto-restart entries are removed entirely. When
// prune is false the script merely changed: the process is stopped and the
// registry entry is marked Stopped so the subsequent RunAutostart relaunches
// it with the new config.
func (d *Daemon) stopReconcileScript(ctx context.Context, name, projectPath string, prune bool) {
	processID := makeProcessID(projectPath, name)

	// Mark daemon-initiated so the outage classifier treats this as a
	// rebuild, not a crash.
	d.healthTracker.MarkDaemonInitiatedStop(processID)

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := d.hub.ProcessManager().Stop(stopCtx, processID); err != nil {
		debug.Warn("daemon", "reconcile: stop %s: %v", processID, err)
	}
	d.hub.ProcessManager().RemoveByPath(processID, projectPath)
	d.retireIncidentProcessOwner(processID)
	d.urlTracker.ClearProcess(processID)
	if d.autoRestarter != nil {
		d.autoRestarter.Unregister(processID)
	}

	if prune {
		d.scriptRegistry.Remove(name, projectPath)
		d.scriptConfigs.Delete(processID)
		return
	}
	if entry, ok := d.scriptRegistry.Get(name, projectPath); ok {
		entry.SetState(script.StateStopped)
	}
}
