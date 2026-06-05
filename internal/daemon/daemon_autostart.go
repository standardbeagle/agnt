package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/go-cli-server/process"
	"github.com/standardbeagle/go-cli-server/script"
)

type AutostartResult struct {
	Scripts       []string       `json:"scripts,omitempty"`
	Proxies       []string       `json:"proxies,omitempty"`
	Errors        []string       `json:"errors,omitempty"`
	PortConflicts []PortConflict `json:"port_conflicts,omitempty"`
	PortsCleared  []PortConflict `json:"ports_cleared,omitempty"`
}

// AutostartPhase, AutostartProgress, and the Phase* constants live in
// autostart_manager.go so that AutostartManager and RunAutostartAsync share
// a single definition.

// emitProgress sends a progress event if the channel is non-nil.
// Never blocks: drops the event if the channel is full. The ProjectPath and
// Timestamp fields are populated here so that events observed directly on
// the channel (outside of AutostartManager) still carry that metadata.
func emitProgress(ch chan<- AutostartProgress, projectPath string, p AutostartProgress) {
	if ch == nil {
		return
	}
	if p.ProjectPath == "" {
		p.ProjectPath = projectPath
	}
	if p.Timestamp.IsZero() {
		p.Timestamp = time.Now()
	}
	select {
	case ch <- p:
	default:
	}
}

// pendingAutostart holds state for a two-phase autostart (prompt mode).
type pendingAutostart struct {
	config      *config.AgntConfig
	projectPath string
	conflicts   []PortConflict
}

// RunAutostart loads .agnt.kdl config from projectPath and starts configured
// processes/proxies synchronously. It delegates to RunAutostartAsync with a nil
// progress channel.
func (d *Daemon) RunAutostart(ctx context.Context, projectPath string) *AutostartResult {
	return d.RunAutostartAsync(ctx, projectPath, nil)
}

// RunAutostartNonInteractive runs autostart for a non-interactive caller (the
// MCP InitializedHandler). It is identical to RunAutostart except that the
// "prompt" port-conflict policy falls back to "skip" because there is no stdin
// for the interactive prompt. A warning is logged for each conflict that is
// skipped.
func (d *Daemon) RunAutostartNonInteractive(ctx context.Context, projectPath string) *AutostartResult {
	if projectPath == "" {
		return &AutostartResult{}
	}
	projectPath = normalizePath(projectPath)

	// Load config to check port-conflict policy.
	agntConfig, err := config.LoadAgntConfig(projectPath)
	if err != nil || agntConfig == nil {
		// No config or error: fall through to normal RunAutostart which
		// handles empty config gracefully.
		return d.RunAutostart(ctx, projectPath)
	}

	// If the policy is "prompt", temporarily override to "skip" for this call.
	if agntConfig.EffectivePortConflictPolicy() == "prompt" {
		agntConfig.Project.PortConflict = "skip"
		d.nonInteractiveConfigOverride.Store(projectPath, agntConfig)
		defer d.nonInteractiveConfigOverride.Delete(projectPath)

		d.startupLog(projectPath).Info("", "autostart_non_interactive",
			fmt.Sprintf("port-conflict policy overridden from prompt to skip (non-interactive) for %s", projectPath))
	}

	return d.RunAutostart(ctx, projectPath)
}

// RunAutostartAsync loads .agnt.kdl config from projectPath and starts
// configured processes/proxies. Progress events are emitted to the progress
// channel (if non-nil) after each milestone: script start, dependency wait,
// dependency ready, script failure, and layer completion.
//
// Scripts are started in dependency order using topological sort:
//   - Layer 0 scripts (no dependencies) start concurrently
//   - Layer 1+ scripts wait for all their dependencies to become ready
//   - Readiness is signaled by URL detection or TCP port probe
//   - Context cancellation replaces fixed dependency timeouts
func (d *Daemon) RunAutostartAsync(
	ctx context.Context,
	projectPath string,
	progress chan<- AutostartProgress,
) *AutostartResult {
	result := &AutostartResult{}

	// Step 1: Validate input
	if projectPath == "" {
		d.startupLog("").Error("", "autostart", "projectPath is empty")
		return result
	}

	// Normalize path so script registry keys match lookup queries.
	// On Windows, normalizePath lowercases the path for case-insensitive
	// matching, which prevents mismatches between Register and List.
	// Bind the logger AFTER normalization so every entry's ProcessID prefix
	// matches what the project-scoped startup_log query derives.
	projectPath = normalizePath(projectPath)

	log := d.startupLog(projectPath)

	log.Info("", "autostart", fmt.Sprintf("starting autostart for %s", projectPath))

	// Step 2: Load .agnt.kdl (or use non-interactive override if present).
	var agntConfig *config.AgntConfig
	if override, ok := d.nonInteractiveConfigOverride.Load(projectPath); ok {
		agntConfig = override.(*config.AgntConfig)
	} else {
		var err error
		agntConfig, err = config.LoadAgntConfig(projectPath)
		if err != nil {
			log.Error("", "config_error", fmt.Sprintf("failed to load .agnt.kdl from %s: %v", projectPath, err))
			return result
		}
	}
	if agntConfig == nil {
		log.Info("", "no_config", fmt.Sprintf("no .agnt.kdl in %s", projectPath))
		return result
	}
	log.Info("", "config_loaded", fmt.Sprintf("%d scripts, %d proxies from %s", len(agntConfig.Scripts), len(agntConfig.Proxies), projectPath))

	// Apply alerts subsystem config (hold buffer, transport thresholds).
	// Safe to run on every autostart; latest values win.
	d.ApplyAlertsConfig(agntConfig.Alerts)

	// Step 3: Port pre-flight check
	autostartScripts := agntConfig.GetAutostartScripts()
	managedPIDs := d.collectManagedPIDs()
	conflicts := detectPortConflicts(ctx, autostartScripts, managedPIDs)

	if len(conflicts) > 0 {
		policy := agntConfig.EffectivePortConflictPolicy()

		for _, c := range conflicts {
			log.WarnPort(c.ScriptName, "port_conflict_detected",
				fmt.Sprintf("port %d blocked by %s (PIDs: %v)", c.Port, c.ProcessName, c.PIDs), c.Port)
		}

		switch policy {
		case "fail":
			msg := fmt.Sprintf("port conflicts detected, aborting (port-conflict: fail): %d conflict(s)", len(conflicts))
			log.Error("", "port_conflict_abort", msg)
			result.Errors = append(result.Errors, msg)
			result.PortConflicts = conflicts
			return result

		case "skip":
			for _, c := range conflicts {
				log.WarnPort(c.ScriptName, "port_conflict_skipped",
					fmt.Sprintf("port %d conflict skipped (policy: skip)", c.Port), c.Port)
			}

		case "auto-kill":
			killResults := killPortBlockers(ctx, d.hub.ProcessManager(), d.eventHub, conflicts)
			for _, kr := range killResults {
				if kr.Killed {
					result.PortsCleared = append(result.PortsCleared, kr.PortConflict)
					log.Info(kr.ScriptName, "port_conflict_killed",
						fmt.Sprintf("cleared port %d (was: %s PIDs %v)", kr.Port, kr.ProcessName, kr.PIDs))
				} else {
					log.Error(kr.ScriptName, "port_conflict_failed", kr.Error)
					result.Errors = append(result.Errors, kr.Error)
				}
			}

		case "prompt":
			d.pendingAutostarts.Store(projectPath, &pendingAutostart{
				config:      agntConfig,
				projectPath: projectPath,
				conflicts:   conflicts,
			})
			result.PortConflicts = conflicts
			return result
		}
	}

	// Step 4: Register + start
	d.registerAndStartScripts(ctx, agntConfig, projectPath, result, progress)
	return result
}

// collectManagedPIDs returns a set of all PIDs currently managed by the daemon,
// plus any child/descendant processes spawned by managed processes.
func (d *Daemon) collectManagedPIDs() map[int]bool {
	managed := make(map[int]bool)
	var roots []int

	// Collect directly managed PIDs
	for _, proc := range d.hub.ProcessManager().List() {
		pid := proc.PID()
		if pid > 0 {
			managed[pid] = true
			roots = append(roots, pid)
		}
	}

	// Walk parent chain: find all processes whose ancestor is a managed PID.
	// This protects children like `dotnet` (spawned by `dotnet watch`),
	// `node` (spawned by `npx vite`), etc.
	allProcs, _ := platform.Scan()
	byPID := make(map[int]platform.ProcInfo, len(allProcs))
	for _, p := range allProcs {
		byPID[p.PID] = p
	}

	// For each process, walk its parent chain looking for a managed root
	for _, p := range allProcs {
		if managed[p.PID] {
			continue // already known
		}
		visited := make(map[int]bool)
		for ppid := p.PPID; ppid > 1; {
			if visited[ppid] {
				break // cycle protection
			}
			visited[ppid] = true
			if managed[ppid] {
				managed[p.PID] = true
				break
			}
			parent, ok := byPID[ppid]
			if !ok {
				break
			}
			ppid = parent.PPID
		}
	}

	_ = roots
	return managed
}

// registerAndStartScripts registers all scripts, cleans up stale entries,
// starts autostart scripts in dependency order, then starts proxies.
// Shared by RunAutostartAsync and resumeAutostart.
func (d *Daemon) registerAndStartScripts(ctx context.Context, cfg *config.AgntConfig, projectPath string, result *AutostartResult, progress chan<- AutostartProgress) {
	log := d.startupLog(projectPath)

	for name, scriptCfg := range cfg.Scripts {
		processID := makeProcessID(projectPath, name)
		d.scriptConfigs.Store(processID, scriptCfg)
		if _, err := d.scriptRegistry.Register(name, projectPath, scriptConfigToEntry(scriptCfg)); err != nil {
			log.Error(name, "register_failed", fmt.Sprintf("failed to register script: %v", err))
		}
	}

	// Prune stale script entries that are no longer in config.
	// This handles the case where scripts were removed from .agnt.kdl between sessions.
	for _, entry := range d.scriptRegistry.List(projectPath) {
		if _, inConfig := cfg.Scripts[entry.Name]; !inConfig {
			d.scriptRegistry.Remove(entry.Name, projectPath)
			d.scriptConfigs.Delete(entry.ProcessID)
			if d.autoRestarter != nil {
				d.autoRestarter.Unregister(entry.ProcessID)
			}
			log.Info(entry.Name, "pruned_stale_script",
				fmt.Sprintf("removed script %q (no longer in config)", entry.Name))
		}
	}

	// Clean up stale ProcessManager entries for autostart scripts BEFORE starting.
	// This ensures ports are freed and resources reclaimed before new processes launch.
	autostartScripts := cfg.GetAutostartScripts()
	for name := range autostartScripts {
		processID := makeProcessID(projectPath, name)
		if existing, err := d.hub.ProcessManager().Get(processID); err == nil {
			state := existing.State()
			if state != process.StateRunning && state != process.StateStarting {
				log.Info(name, "stale_cleanup",
					fmt.Sprintf("removing stale process (state=%s)", state))
				d.hub.ProcessManager().RemoveByPath(processID, projectPath)
			}
		}
	}

	failedScripts := d.startAutostartScripts(ctx, cfg, projectPath, result, progress)
	d.startAutostartProxies(ctx, cfg, projectPath, failedScripts, result)
}

// resumeAutostart continues a paused autostart after port conflict resolution.
func (d *Daemon) resumeAutostart(ctx context.Context, projectPath string) *AutostartResult {
	result := &AutostartResult{}

	val, ok := d.pendingAutostarts.LoadAndDelete(projectPath)
	if !ok {
		result.Errors = append(result.Errors, "no pending autostart for this project")
		return result
	}
	pending := val.(*pendingAutostart)

	d.registerAndStartScripts(ctx, pending.config, pending.projectPath, result, nil)
	return result
}

// startAutostartScripts starts scripts in topological dependency order.
// Returns a set of script names that failed to start.
//
// Pure layer orchestration: topo sort + per-layer waitgroup + context
// cancellation select. Per-script work lives in startOneScript.
func (d *Daemon) startAutostartScripts(ctx context.Context, cfg *config.AgntConfig, projectPath string, result *AutostartResult, progress chan<- AutostartProgress) map[string]bool {
	log := d.startupLog(projectPath)
	autostartScripts := cfg.GetAutostartScripts()
	proxyConfigs := cfg.Proxies
	failedScripts := make(map[string]bool)

	if len(autostartScripts) == 0 {
		return failedScripts
	}

	layers, sortErr := config.TopologicalSort(autostartScripts)
	if sortErr != nil {
		log.Error("", "dependency_sort", fmt.Sprintf("topological sort failed: %v", sortErr))
		result.Errors = append(result.Errors, fmt.Sprintf("dependency sort: %v", sortErr))
		return failedScripts
	}

	var resultMu sync.Mutex
	for layerIdx, layer := range layers {
		if cancelled := d.runLayer(ctx, layer, layerIdx, autostartScripts, proxyConfigs,
			projectPath, result, &resultMu, failedScripts, progress); cancelled {
			return failedScripts
		}
		emitProgress(progress, projectPath, AutostartProgress{
			Phase: PhaseLayerComplete, Layer: layerIdx,
		})
	}

	// NOTE: Ready signals are intentionally NOT cleaned up here. A proxy's
	// readiness gate (internal/proxy/readiness.go) is wired up asynchronously
	// via the proxyEvents path — it can register its wait-for waiters AFTER
	// autostart returns. Wiping ready state at autostart completion destroyed
	// the "dependency is ready" fact before the late-arriving gate could
	// observe it, leaving the proxy stuck serving 503 agnt_proxy_not_ready
	// forever. Ready-signal lifecycle is tied to process lifecycle instead:
	// handleScriptStopped calls readySignaler.Cleanup when a script actually
	// stops. See TestProxyWaitFor_ReadyPersistsPastAutostart.
	return failedScripts
}

// runLayer launches one topological layer of scripts concurrently and waits
// for either the layer to finish or the context to be cancelled. Returns
// true if the wait ended because the context was cancelled, in which case
// the caller should abort remaining layers.
func (d *Daemon) runLayer(
	ctx context.Context,
	layer []string,
	layerIdx int,
	autostartScripts map[string]*config.ScriptConfig,
	proxyConfigs map[string]*config.ProxyConfig,
	projectPath string,
	result *AutostartResult,
	resultMu *sync.Mutex,
	failedScripts map[string]bool,
	progress chan<- AutostartProgress,
) (cancelled bool) {
	layerDone := make(chan struct{})
	var layerWg sync.WaitGroup

	for _, name := range layer {
		scriptCfg := autostartScripts[name]
		if scriptCfg == nil {
			continue
		}
		layerWg.Add(1)
		go func(name string, scriptCfg *config.ScriptConfig) {
			defer layerWg.Done()
			d.startOneScript(ctx, name, scriptCfg, projectPath, layerIdx,
				proxyConfigs, result, resultMu, failedScripts, progress)
		}(name, scriptCfg)
	}

	go func() {
		layerWg.Wait()
		close(layerDone)
	}()

	// Wait for layer completion or context cancellation. On cancellation,
	// drain layerDone so in-flight goroutines exit before returning.
	select {
	case <-layerDone:
		return false
	case <-ctx.Done():
		<-layerDone
		return true
	}
}

// startOneScript runs the body of the per-script goroutine launched by
// startAutostartScripts. It handles the idempotent already-running fast path,
// dependency wait, script start, error reporting, and readiness signaling.
//
// The failedScripts map and result slice are protected by resultMu; callers
// must supply the same mutex for all goroutines within a layer.
func (d *Daemon) startOneScript(
	ctx context.Context,
	name string,
	scriptCfg *config.ScriptConfig,
	projectPath string,
	layerIdx int,
	proxyConfigs map[string]*config.ProxyConfig,
	result *AutostartResult,
	resultMu *sync.Mutex,
	failedScripts map[string]bool,
	progress chan<- AutostartProgress,
) {
	log := d.startupLog(projectPath)
	processID := makeProcessID(projectPath, name)

	// Idempotent fast path: if the script is already running (e.g. a previous
	// session left it up), skip the start but still configure readiness so
	// dependents can proceed.
	if entry, ok := d.scriptRegistry.Get(name, projectPath); ok {
		state := entry.State()
		if state == script.StateRunning || state == script.StateStarting {
			log.Info(name, "already_running",
				fmt.Sprintf("%s already %s, skipping", name, state.String()))
			d.setupReadinessSignal(processID, name, scriptCfg, proxyConfigs, projectPath)
			resultMu.Lock()
			result.Scripts = append(result.Scripts, name)
			resultMu.Unlock()
			return
		}
	}

	emitProgress(progress, projectPath, AutostartProgress{
		Phase: PhaseScriptStarting, Script: name, Layer: layerIdx,
	})

	// Wait for dependencies (layer 1+).
	d.waitForDependenciesCtx(ctx, name, scriptCfg, projectPath, layerIdx, progress)
	if ctx.Err() != nil {
		return
	}

	log.Info(name, "starting", fmt.Sprintf("starting %s (layer %d)", name, layerIdx))
	if err := d.autostartScript(ctx, name, scriptCfg, projectPath, proxyConfigs); err != nil {
		log.Error(name, "start_failed", err.Error())
		emitProgress(progress, projectPath, AutostartProgress{
			Phase: PhaseScriptFailed, Script: name, Layer: layerIdx, Err: err,
		})
		resultMu.Lock()
		result.Errors = append(result.Errors, fmt.Sprintf("script %s: %v", name, err))
		failedScripts[name] = true
		resultMu.Unlock()
		// Unblock dependents so they don't wait forever on the ready signal.
		// Downstream layers still observe failedScripts[name] and react.
		d.readySignaler.SignalReady(processID)
		return
	}

	log.Info(name, "started", fmt.Sprintf("%s started", name))
	emitProgress(progress, projectPath, AutostartProgress{
		Phase: PhaseScriptStarted, Script: name, Layer: layerIdx,
	})
	resultMu.Lock()
	result.Scripts = append(result.Scripts, name)
	resultMu.Unlock()

	d.setupReadinessSignal(processID, name, scriptCfg, proxyConfigs, projectPath)
}

// setupReadinessSignal configures the ready signaler for a script. If the
// script has expected ports, a TCP port probe is started; otherwise the
// signaler is marked ready immediately so dependents are unblocked.
//
// The probe runs on the daemon context (d.ctx), NOT the per-autostart context.
// The autostart context is cancelled the moment autostart returns
// (autostart_manager.go run() → h.cancel()), but a proxy gate may wait on a
// backend that only binds its port after autostart completes — e.g. a backend
// nothing else depends-on, so autostart never blocks on it. Binding the probe
// to the autostart context killed it before the port came up, leaving the gate
// stuck forever. The probe is torn down explicitly when the process stops
// (handleScriptStopped → readySignaler.Cleanup) or when the daemon shuts down.
// See TestProxyWaitFor_PortProbeSurvivesAutostart.
func (d *Daemon) setupReadinessSignal(
	processID, name string,
	scriptCfg *config.ScriptConfig,
	proxyConfigs map[string]*config.ProxyConfig,
	projectPath string,
) {
	probePorts := d.getExpectedPortsForScript(name, scriptCfg, proxyConfigs,
		resolveWorkingDir(projectPath, scriptCfg.Cwd), "", nil)
	if len(probePorts) > 0 {
		d.readySignaler.StartPortProbe(processID, probePorts[0], d.ctx)
		return
	}
	d.readySignaler.SignalReady(processID)
}

// waitForDependenciesCtx blocks until all dependencies for a script are ready
// or the context is cancelled. The wait is bounded only by the parent context
// (session lifetime) and any explicit per-dependency `timeout=N` declared in
// .agnt.kdl. There is no implicit fallback timeout — a slow-starting backend
// (e.g. dotnet cold start, NuGet restore) is "Starting", not "Stalled", per
// .claude/rules/daemon-lifecycle.md, so dependents must wait indefinitely
// rather than launch against a not-yet-listening dependency.
// Emits progress events for each dependency wait and resolution.
func (d *Daemon) waitForDependenciesCtx(ctx context.Context, name string, scriptCfg *config.ScriptConfig, projectPath string, layerIdx int, progress chan<- AutostartProgress) {
	if layerIdx == 0 {
		return
	}
	for _, dep := range scriptCfg.DependsOn {
		if ctx.Err() != nil {
			return
		}
		d.waitForSingleDependency(ctx, name, dep, projectPath, layerIdx, progress)
	}
}

// waitForSingleDependency waits for one dependency to become ready. The wait
// is unbounded (parent ctx only) unless the user set an explicit per-dep
// `timeout=N` in .agnt.kdl, in which case it is wrapped in a context with
// that deadline. Separated from the loop so the optional timeout context can
// be cancelled per iteration via defer.
func (d *Daemon) waitForSingleDependency(ctx context.Context, name string, dep config.ScriptDependency, projectPath string, layerIdx int, progress chan<- AutostartProgress) {
	depProcessID := makeProcessID(projectPath, dep.Name)
	log := d.startupLog(projectPath)

	emitProgress(progress, projectPath, AutostartProgress{
		Phase: PhaseDependencyWaitStart, Script: name, Dependency: dep.Name, Layer: layerIdx,
	})

	waitCtx := ctx
	if dep.Timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, dep.Timeout)
		defer cancel()
	}

	if err := d.readySignaler.WaitReadyCtx(depProcessID, waitCtx); err != nil {
		log.Warn(name, "dependency_wait",
			fmt.Sprintf("cancelled waiting for %s: %v (starting anyway)", dep.Name, err))
	} else {
		emitProgress(progress, projectPath, AutostartProgress{
			Phase: PhaseDependencyReady, Script: name, Dependency: dep.Name, Layer: layerIdx,
		})
		log.Info(name, "dependency_ready", fmt.Sprintf("dependency %s is ready", dep.Name))
	}
}

// startAutostartProxies starts proxies, skipping those that depend on failed scripts.
// For script-linked proxies with fallback-port, schedules a delayed check to create
// the proxy if URL detection doesn't fire in time.
func (d *Daemon) startAutostartProxies(ctx context.Context, cfg *config.AgntConfig, projectPath string, failedScripts map[string]bool, result *AutostartResult) {
	log := d.startupLog(projectPath)

	for proxyName, proxyConfig := range cfg.Proxies {
		if proxyConfig.Script != "" && failedScripts[proxyConfig.Script] {
			msg := fmt.Sprintf("proxy %q skipped: depends on failed script %q", proxyName, proxyConfig.Script)
			log.Error(proxyName, "proxy_skipped", msg)
			result.Errors = append(result.Errors, msg)
		}
	}

	for name, proxyConfig := range cfg.GetAutostartProxies() {
		log.Info(name, "proxy_starting", fmt.Sprintf("starting proxy %s", name))
		if err := d.autostartProxy(ctx, name, proxyConfig, projectPath); err != nil {
			log.Error(name, "proxy_failed", err.Error())
			result.Errors = append(result.Errors, fmt.Sprintf("proxy %s: %v", name, err))
		} else {
			log.Info(name, "proxy_started", fmt.Sprintf("proxy %s started", name))
			result.Proxies = append(result.Proxies, name)
		}
	}

	// Schedule fallback-port checks for script-linked proxies.
	// These proxies aren't started above (they rely on URL detection).
	// After a delay, check if URL detection created them; if not, use fallback-port.
	d.scheduleFallbackPortChecks(ctx, cfg, projectPath, failedScripts)
}

// scheduleFallbackPortChecks schedules delayed FallbackPortCheck events for
// script-linked proxies that have a fallback-port configured.
func (d *Daemon) scheduleFallbackPortChecks(ctx context.Context, cfg *config.AgntConfig, projectPath string, failedScripts map[string]bool) {
	for proxyName, proxyConfig := range cfg.Proxies {
		if proxyConfig.Script == "" {
			continue // Not script-linked
		}
		if failedScripts[proxyConfig.Script] {
			continue // Script failed to start
		}
		if proxyConfig.FallbackPort <= 0 {
			continue // No fallback configured
		}

		// Capture for goroutine
		name := proxyName
		pc := proxyConfig
		scriptID := makeProcessID(projectPath, pc.Script)

		go func() {
			// Wait for URL detection to have a chance
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}

			select {
			case d.proxyEvents <- ProxyEvent{
				Type:      FallbackPortCheck,
				ScriptID:  scriptID,
				ProxyName: name,
				Config:    pc,
				Path:      projectPath,
			}:
				debug.Log("daemon", "Scheduled fallback-port check for proxy %s (script %s)", name, pc.Script)
			default:
				debug.Warn("daemon", "Proxy event channel full, cannot schedule fallback check for proxy %s", name)
				d.startupLog(projectPath).Warn(name, "proxy_event_dropped",
					fmt.Sprintf("proxy event channel full: fallback-port check for proxy %s could not be scheduled", name))
			}
		}()
	}
}

// autostartScript starts a single script during RunAutostart. Thin wrapper
// around StartScriptExplicit — the autostart layer (registerAndStartScripts →
// startAutostartScripts → runLayer → startOneScript) owns layer sequencing,
// dependency waits, and failure bookkeeping; the per-script work (scriptConfigs
// cache, scriptRegistry.Register, resolveShell, StartScript, state transitions)
// lives in StartScriptExplicit. Before T4 both paths duplicated that logic.
// Keeping autostartScript as the named entrypoint so existing call sites stay
// readable; the body is now a one-liner delegate.
func (d *Daemon) autostartScript(ctx context.Context, name string, scriptCfg *config.ScriptConfig, projectPath string, proxyConfigs map[string]*config.ProxyConfig) error {
	return d.StartScriptExplicit(ctx, name, scriptCfg, projectPath, proxyConfigs)
}

// StartScriptExplicit starts a single script by config. Canonical entrypoint
// shared by the autostart path (via autostartScript) and the MCP PROC RUN hub
// handler. Owns: scriptConfigs cache, scriptRegistry.Register, command
// resolution (Run / Command / package-manager detection), state transitions,
// expectedPorts resolution, and the StartScript call. On failure, sets
// ScriptEntry.State = StateFailed, records LastError, increments FailCount,
// and returns a formatted error that includes the resolved command, working
// directory, and (when available) the trailing process output.
//
// proxyConfigs is used only for port resolution via getExpectedPortsForScript.
// Callers that don't have a proxyConfigs map (MCP ad-hoc processes) pass nil;
// the helper tolerates nil and falls back to command-line / PORT env scanning.
func (d *Daemon) StartScriptExplicit(ctx context.Context, name string, scriptCfg *config.ScriptConfig, projectPath string, proxyConfigs map[string]*config.ProxyConfig) error {

	// Make process ID unique per project to avoid collisions between sessions
	processID := makeProcessID(projectPath, name)

	// Store agnt-specific config for later use (e.g., restart with URLMatchers)
	d.scriptConfigs.Store(processID, scriptCfg)

	// Register in ScriptRegistry before starting (idempotent)
	entry, regErr := d.scriptRegistry.Register(name, projectPath, scriptConfigToEntry(scriptCfg))
	if regErr != nil {
		return fmt.Errorf("script registry: %w", regErr)
	}

	// Check ScriptRegistry state: if already running/starting, skip
	if state := entry.State(); state == script.StateRunning || state == script.StateStarting {
		debug.Log("daemon", "StartScriptExplicit: script %s already %s, skipping", name, state)
		return nil
	}

	// Resolve working directory and environment
	workingDir := resolveWorkingDir(projectPath, scriptCfg.Cwd)
	envSlice := envMapToSlice(scriptCfg.Env)

	var command string
	var args []string

	if scriptCfg.Run != "" {
		// Shell command string - resolve via config or platform default
		command, args = scriptCfg.ResolveShell()
	} else if scriptCfg.Command != "" {
		// Explicit command specified
		command = scriptCfg.Command
		args = scriptCfg.Args
	} else {
		// No command - run as package.json script via detected package manager
		// Use workingDir for detection so monorepo subdirectories find their package.json
		proj, err := project.Detect(workingDir)
		if err != nil {
			debug.Error("daemon", "project detection failed for %s: %v", workingDir, err)
			d.startupLog(projectPath).Error(name, "autostart_failed",
				fmt.Sprintf("project detection failed for %s: %v", workingDir, err))
			entry.SetState(script.StateFailed)
			entry.SetLastError(fmt.Sprintf("project detection failed: %v", err))
			entry.IncrementFailCount()
			return fmt.Errorf("project detection failed: %v", err)
		}

		switch proj.Type {
		case project.ProjectNode:
			pm := proj.PackageManager
			if pm == "" {
				pm = "npm"
			}
			command = pm
			// pnpm and yarn don't need "run" prefix for scripts
			if pm == "npm" || pm == "bun" {
				args = []string{"run", name}
			} else {
				args = []string{name}
			}
		case project.ProjectGo:
			command = "go"
			args = []string{"run", name}
		case project.ProjectPython:
			command = "python"
			args = []string{"-m", name}
		default:
			debug.Error("daemon", "cannot run script %q: unknown project type %s", name, proj.Type)
			d.startupLog(projectPath).Error(name, "autostart_failed",
				fmt.Sprintf("cannot run script %q: unknown project type %s", name, proj.Type))
			entry.SetState(script.StateFailed)
			entry.SetLastError(fmt.Sprintf("unknown project type: %s", proj.Type))
			entry.IncrementFailCount()
			return fmt.Errorf("cannot run script %q: unknown project type and no command specified", name)
		}
	}

	// Record resolved command in ScriptEntry
	entry.SetResolvedCommand(command, args)

	// Transition to starting (ProcessManager lifecycle handles Running state and StartCount)
	entry.SetState(script.StateStarting)

	// Determine expected ports for pre-flight cleanup and EADDRINUSE recovery
	expectedPorts := d.getExpectedPortsForScript(name, scriptCfg, proxyConfigs, workingDir, command, args)

	_, err := d.StartScript(ctx, StartScriptConfig{
		ProcessID:     processID,
		ProjectPath:   projectPath,
		WorkingDir:    workingDir,
		Command:       command,
		Args:          args,
		Env:           envSlice,
		ExpectedPorts: expectedPorts,
		URLMatchers:   scriptCfg.URLMatchers,
		AutoRestart:   scriptCfg.AutoRestart,
	})
	if err != nil {
		entry.SetState(script.StateFailed)
		entry.SetLastError(err.Error())
		entry.IncrementFailCount()

		// Include resolved command and process output in error for debugging
		cmdStr := command
		if len(args) > 0 {
			cmdStr = fmt.Sprintf("%s %s", command, strings.Join(args, " "))
		}
		msg := fmt.Sprintf("%s (resolved command: %s, cwd: %s)", err.Error(), cmdStr, workingDir)

		// Include process output if available (from StartupError)
		if startupErr, ok := err.(*StartupError); ok && startupErr.Output != "" {
			msg += "\n" + startupErr.Output
		}
		return fmt.Errorf("%s", msg)
	}

	// Success: ProcessManager lifecycle sets StateRunning automatically.
	// Fire on-start lifecycle hook if configured.
	if scriptCfg.Hooks != nil && scriptCfg.Hooks.OnStart != "" {
		runLifecycleHookAsync(scriptCfg.Hooks.OnStart, name, "start", scriptCfg, 0)
	}
	return nil
}

// autostartProxy starts a single proxy from config.
// Called by RunAutostart with proxies from GetAutostartProxies (explicit target or Autostart flag).
// Script-linked proxies are skipped here — they're created by the event system when URLs are detected.
func (d *Daemon) autostartProxy(ctx context.Context, name string, proxyConfig *config.ProxyConfig, projectPath string) error {
	// Skip script-linked proxies - they're handled by URLDetected events
	// or by FallbackPortCheck events if URL detection fails
	if proxyConfig.Script != "" {
		msg := fmt.Sprintf("proxy %s: waiting for URL detection from script %q", name, proxyConfig.Script)
		if proxyConfig.FallbackPort > 0 {
			msg += fmt.Sprintf(" (fallback-port %d if detection fails)", proxyConfig.FallbackPort)
		}
		debug.Log("daemon", "%s", msg)
		d.startupLog(projectPath).Info(name, "proxy_deferred", msg)
		return nil
	}

	// Make proxy ID unique per project
	proxyID := makeProcessID(projectPath, name)

	// Determine target URL (must be explicitly specified)
	var targetURL string
	if proxyConfig.URL != "" {
		targetURL = proxyConfig.URL
	} else if proxyConfig.Port > 0 {
		host := proxyConfig.Host
		if host == "" {
			host = "localhost"
		}
		targetURL = fmt.Sprintf("http://%s:%d", host, proxyConfig.Port)
	} else if proxyConfig.Target != "" {
		// Legacy target field
		targetURL = proxyConfig.Target
	}

	if targetURL == "" {
		debug.Log("daemon", "Proxy %s has no explicit target URL, skipping", name)
		return nil
	}

	// Pre-flight check for explicit listen-port. An explicit port
	// means "this port or fail" (see StrictListenPort in proxy.Config);
	// surface the owning process name/PID via startupErrorStore before
	// Create() even runs, so the conflict is visible to the AI agent
	// even if the terse runtime bind error would otherwise be hard to
	// correlate. No kill action here — the user explicitly declared a
	// port, and silently killing the incumbent would be too surprising
	// for proxy config (scripts go through the port-conflict policy at
	// autostart time; proxies don't have an equivalent policy knob and
	// a conservative fail-loud default is the right call).
	if proxyConfig.ListenPort > 0 {
		pids := config.FindPIDsByPort(ctx, proxyConfig.ListenPort)
		if len(pids) > 0 {
			procName := config.ProcessNameByPID(pids[0])
			ownerHint := ""
			if procName != "" {
				ownerHint = fmt.Sprintf(" (owner: %s pid=%d)", procName, pids[0])
			} else {
				ownerHint = fmt.Sprintf(" (owner pid=%d)", pids[0])
			}
			msg := fmt.Sprintf("proxy %s: explicit listen-port %d is in use%s — proxy will fail to start; free the port or change listen-port", name, proxyConfig.ListenPort, ownerHint)
			debug.Warn("daemon", "%s", msg)
			d.startupLog(projectPath).ErrorPort(name, "proxy_listen_port_conflict", msg, proxyConfig.ListenPort)
			// Fall through to enqueue the ExplicitStart event anyway —
			// the strict-listen-port path in proxy.Start() will emit a
			// matching proxy_creation_failed entry with the actual bind
			// error, and we want both signals (preflight diagnosis +
			// hard failure) for debuggability. This also ensures we
			// don't silently skip the proxy if the owning process
			// releases the port between our scan and Create().
		}
	}

	// Send ExplicitStart event to create the proxy
	select {
	case d.proxyEvents <- ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  proxyConfig,
		Path:    projectPath,
	}:
		debug.Log("daemon", "Queued explicit proxy %s for auto-start", name)
	default:
		debug.Warn("daemon", "Proxy event channel full, cannot queue proxy %s for auto-start", name)
		d.startupLog(projectPath).Warn(name, "proxy_creation_failed",
			fmt.Sprintf("proxy event channel full, cannot queue proxy %s for auto-start", name))
	}

	return nil
}

// Note: legacy synchronous port detection (detectPortForScript /
// _old_detectPortForScript) has been removed. Proxy creation is now driven by
// URLTracker → URLDetected events; see proxy_events.go.

// MaxLogSize is the maximum log file size before rotation (5MB).
const MaxLogSize = 5 * 1024 * 1024

// MaxLogBackups is the number of rotated log files to keep.
const MaxLogBackups = 3

// GetLogPath returns the path to the daemon log file.
