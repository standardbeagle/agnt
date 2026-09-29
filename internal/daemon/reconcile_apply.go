package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/scope"
	"github.com/standardbeagle/go-cli-server/script"
)

// configuredProxy identifies the .agnt.kdl node that created a live proxy.
// Manual proxies have no snapshot and are not stopped by config reconciliation.
type configuredProxy struct {
	name      string
	signature string
}

// ReconcileProjectConfig applies .agnt.kdl changes to configured scripts and
// proxies. Manual proxies retain their independent lifecycle. Autostart handles
// process dependencies; cached URLs let existing scripts create new proxies
// without printing their startup output again.
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
	//
	// Only scripts declared in the current config are eligible, regardless of
	// their autostart flag: an entry with no matching key in cfg.Scripts was
	// started by hand (MCP PROC RUN under an arbitrary name, not an .agnt.kdl
	// scripts{} key). Reconcile owns only config-declared scripts; a manually
	// started process must never be diffed as "running, not desired" and
	// stopped out from under the agent that started it.
	running := make(map[string]string)
	for _, entry := range d.scriptRegistry.List(projectPath) {
		switch entry.State() {
		case script.StateRunning, script.StateStarting:
		default:
			continue
		}
		if _, declared := cfg.Scripts[entry.Name]; !declared {
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

	desiredProxies := make(map[string]string)
	for name, pc := range cfg.Proxies {
		if pc.ShouldAutostart() || pc.Script != "" {
			desiredProxies[name] = proxySignature(pc)
		}
	}
	runningProxies := make(map[string]string)
	for _, p := range d.proxym.ListScoped(scope.Project(projectPath)) {
		if value, ok := d.proxyConfigs.Load(p.ID); ok {
			snapshot := value.(configuredProxy)
			// Several detected URLs can belong to one config node. Any stale
			// instance requires replacement of that node's proxies.
			if runningProxies[snapshot.name] != "\x00changed" {
				if want, ok := desiredProxies[snapshot.name]; ok && want != snapshot.signature {
					runningProxies[snapshot.name] = "\x00changed"
				} else {
					runningProxies[snapshot.name] = snapshot.signature
				}
			}
		}
	}
	plan := computeReconcile(desired, running, desiredProxies, runningProxies)
	// Applied unconditionally, ahead of the plan.IsEmpty() early return below:
	// alerts config and automation.max-sessions are not part of the
	// script/proxy signature diff, so an edit to only those fields produces
	// an empty plan. RunAutostart (the plan's apply path, further down) used
	// to be the only caller that applied them — but it is never reached on
	// an empty plan, so that kind of edit silently never took effect.
	d.applyProjectRuntimeConfig(projectPath, cfg)
	d.applyProxyDisplayConfig(projectPath, cfg)
	d.applyDevOIDCProject(projectPath, cfg)
	d.applyAuthBreakoutProject(projectPath, cfg)
	d.reconcileScriptRuntimeFields(&plan, cfg, projectPath)

	log := d.startupLog(projectPath)
	if plan.IsEmpty() {
		log.Info("", "reconcile", "config reconcile: no changes")
		return plan, nil
	}
	log.Info("", "reconcile", fmt.Sprintf("config reconcile: scripts start=%v stop=%v restart=%v; proxies start=%v stop=%v restart=%v",
		plan.StartScripts, plan.StopScripts, plan.RestartScripts, plan.StartProxies, plan.StopProxies, plan.RestartProxies))

	stopProxies := make(map[string]bool)
	for _, name := range plan.StopProxies {
		stopProxies[name] = true
	}
	for _, name := range plan.RestartProxies {
		stopProxies[name] = true
	}
	for _, p := range d.proxym.ListScoped(scope.Project(projectPath)) {
		value, ok := d.proxyConfigs.Load(p.ID)
		if !ok || !stopProxies[value.(configuredProxy).name] {
			continue
		}
		if err := d.proxym.Stop(ctx, p.ID); err != nil {
			return plan, fmt.Errorf("stop proxy %s: %w", p.ID, err)
		}
		d.retireIncidentProxyOwner(p.ID)
		if d.stateMgr != nil {
			d.stateMgr.RemoveProxy(p.ID)
		}
		if d.proxyEntries != nil {
			d.proxyEntries.Remove(projectPath, value.(configuredProxy).name)
		}
	}
	// Retire proxy associations first, before script-stop events can try to
	// remove the same proxies.
	for _, name := range plan.StopScripts {
		d.stopReconcileScript(ctx, name, projectPath, true)
	}
	for _, name := range plan.RestartScripts {
		d.stopReconcileScript(ctx, name, projectPath, false)
	}

	// Delegate all starts (adds + just-stopped changed) and proxy
	// materialization to the autostart path. Idempotent for unchanged running
	// scripts (StartScriptExplicit skips them).
	result := d.RunAutostart(ctx, projectPath)
	if len(result.Errors) != 0 {
		return plan, fmt.Errorf("config reconcile: %s", strings.Join(result.Errors, "; "))
	}
	// A running script need not print its URL again after a config edit.
	// Replay its known URLs so new or changed script-linked proxies can start.
	if d.urlTracker != nil {
		seen := make(map[string]bool)
		for _, pc := range cfg.Proxies {
			if pc.Script == "" || seen[pc.Script] {
				continue
			}
			seen[pc.Script] = true
			id := makeProcessID(projectPath, pc.Script)
			for _, url := range d.urlTracker.GetURLs(id) {
				d.handleURLDetected(ProxyEvent{Type: URLDetected, ScriptID: id, URL: url, Path: projectPath})
			}
		}
	}
	d.applyProxyDisplayConfig(projectPath, cfg)
	d.applyDevOIDCProject(projectPath, cfg)
	d.applyAuthBreakoutProject(projectPath, cfg)
	return plan, nil
}

// applyAuthBreakoutProject re-applies the top-level auth-breakout block to
// every already-running proxy of the project after a config reconcile, so
// enabling, editing, or removing it takes effect without restarting the
// proxy — the same live-reapply shape as applyProxyDisplayConfig and
// applyDevOIDCProject just above. SetAuthBreakout(nil) on a disabled/removed
// block is deliberate: hub_helpers.go's applyAuthBreakout (the create-time
// chokepoint for a freshly started proxy) only ever installs rules and never
// clears them, so it cannot be reused here without leaving a stale breakout
// active after the block is deleted from .agnt.kdl.
func (d *Daemon) applyAuthBreakoutProject(projectPath string, cfg *config.AgntConfig) {
	if cfg == nil {
		return
	}
	var ab *proxy.AuthBreakout
	if cfg.AuthBreakout.IsEnabled() {
		ab = &proxy.AuthBreakout{Mode: cfg.AuthBreakout.GetMode(), Patterns: cfg.AuthBreakout.GetPatterns()}
	}
	for _, p := range d.proxym.ListScoped(scope.Project(projectPath)) {
		p.SetAuthBreakout(ab)
	}
}

// reconcileScriptRuntimeFields hot-reloads the cheap per-script fields
// (url-matchers, auto-restart) directly onto already-running, signature-
// unchanged scripts, and records any change to a field that genuinely needs
// a restart (ports, depends-on) on plan.NeedsRestart so the reconcile
// response never silently drops it. Scripts already in
// Start/Stop/RestartScripts are skipped — those are about to be
// (re)launched via RunAutostart, which stores their full new config anyway.
func (d *Daemon) reconcileScriptRuntimeFields(plan *ReconcilePlan, cfg *config.AgntConfig, projectPath string) {
	if cfg == nil {
		return
	}
	changing := make(map[string]bool, len(plan.StartScripts)+len(plan.StopScripts)+len(plan.RestartScripts))
	for _, n := range plan.StartScripts {
		changing[n] = true
	}
	for _, n := range plan.StopScripts {
		changing[n] = true
	}
	for _, n := range plan.RestartScripts {
		changing[n] = true
	}

	for name, newCfg := range cfg.GetAutostartScripts() {
		if changing[name] {
			continue
		}
		processID := makeProcessID(projectPath, name)
		oldVal, ok := d.scriptConfigs.Load(processID)
		if !ok {
			continue
		}
		oldCfg, ok := oldVal.(*config.ScriptConfig)
		if !ok {
			continue
		}

		urlMatchersChanged, autoRestartChanged, needsRestartFields := scriptRuntimeFieldDiff(oldCfg, newCfg)
		if !urlMatchersChanged && !autoRestartChanged && len(needsRestartFields) == 0 {
			continue
		}

		if urlMatchersChanged {
			d.urlTracker.SetURLMatchers(processID, newCfg.URLMatchers)
		}
		if autoRestartChanged {
			if newCfg.AutoRestart {
				if proc, err := d.hub.ProcessManager().Get(processID); err == nil {
					workingDir := resolveWorkingDir(projectPath, newCfg.Cwd)
					expectedPorts := d.getExpectedPortsForScript(name, newCfg, cfg.Proxies, workingDir, proc.Command, proc.Args)
					restartConfig := DefaultAutoRestartConfig()
					restartConfig.Enabled = true
					d.autoRestarter.Register(processID, restartConfig, proc.Command, proc.Args, proc.Env, expectedPorts, proc.ProjectPath, proc.WorkingDir)
				}
			} else if d.autoRestarter != nil {
				d.autoRestarter.Unregister(processID)
			}
		}
		if len(needsRestartFields) > 0 {
			plan.NeedsRestart = append(plan.NeedsRestart,
				fmt.Sprintf("%s: %s changed (needs restart)", name, strings.Join(needsRestartFields, ", ")))
		}

		// Persist the full edited config as the new snapshot. Safe here —
		// unlike the scriptSignature-changed case task 3 guards against,
		// none of these fields alter what the live OS process actually is;
		// this only updates the declared-intent metadata hooks/auto-restart/
		// url-matchers read live and ports/depends-on will pick up on the
		// next real restart.
		d.scriptConfigs.Store(processID, newCfg)
	}
}

// applyProxyDisplayConfig pushes display-only proxy settings onto proxies that
// are already running. Autostart materialization is idempotent, so an existing
// proxy never picks up an edited `status-url` on its own — without this the key
// would parse and then do nothing until the proxy was restarted, which is the
// parsed-but-unacted-on shape .claude/rules/daemon-architecture.md § Config
// Authority calls a bug. Display-only by construction: nothing here can change
// what the proxy serves, so applying it live cannot disturb in-flight traffic.
func (d *Daemon) applyProxyDisplayConfig(projectPath string, cfg *config.AgntConfig) {
	if cfg == nil {
		return
	}
	for _, p := range d.proxym.ListScoped(scope.Project(projectPath)) {
		name := stripProcessPrefix(p.ID)
		if value, ok := d.proxyConfigs.Load(p.ID); ok {
			name = value.(configuredProxy).name
		}
		pc, ok := cfg.Proxies[name]
		if !ok || pc == nil {
			continue
		}
		if p.GetStatusURL() != pc.StatusURL {
			p.SetStatusURL(pc.StatusURL)
		}
	}
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
