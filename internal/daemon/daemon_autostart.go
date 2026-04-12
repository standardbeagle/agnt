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
	log := d.startupErrorStore // short alias

	// Step 1: Validate input
	if projectPath == "" {
		log.Error("", "", "autostart", "projectPath is empty")
		return result
	}

	// Normalize path so script registry keys match lookup queries.
	// On Windows, normalizePath lowercases the path for case-insensitive
	// matching, which prevents mismatches between Register and List.
	projectPath = normalizePath(projectPath)

	log.Info("", "", "autostart", fmt.Sprintf("starting autostart for %s", projectPath))

	// Step 2: Load .agnt.kdl
	agntConfig, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		log.Error("", "", "config_error", fmt.Sprintf("failed to load .agnt.kdl from %s: %v", projectPath, err))
		return result
	}
	if agntConfig == nil {
		log.Info("", "", "no_config", fmt.Sprintf("no .agnt.kdl in %s", projectPath))
		return result
	}
	log.Info("", "", "config_loaded", fmt.Sprintf("%d scripts, %d proxies from %s", len(agntConfig.Scripts), len(agntConfig.Proxies), projectPath))

	// Step 3: Port pre-flight check
	autostartScripts := agntConfig.GetAutostartScripts()
	managedPIDs := d.collectManagedPIDs()
	conflicts := detectPortConflicts(ctx, autostartScripts, managedPIDs)

	if len(conflicts) > 0 {
		policy := agntConfig.EffectivePortConflictPolicy()

		for _, c := range conflicts {
			log.Add(&StartupLogEntry{
				ProcessID:  makeProcessID(projectPath, c.ScriptName),
				ScriptName: c.ScriptName,
				Level:      "warning",
				EventType:  "port_conflict_detected",
				Message:    fmt.Sprintf("port %d blocked by %s (PIDs: %v)", c.Port, c.ProcessName, c.PIDs),
				Port:       c.Port,
				Timestamp:  time.Now(),
			})
		}

		switch policy {
		case "fail":
			msg := fmt.Sprintf("port conflicts detected, aborting (port-conflict: fail): %d conflict(s)", len(conflicts))
			log.Error("", "", "port_conflict_abort", msg)
			result.Errors = append(result.Errors, msg)
			result.PortConflicts = conflicts
			return result

		case "skip":
			for _, c := range conflicts {
				log.Add(&StartupLogEntry{
					ProcessID:  makeProcessID(projectPath, c.ScriptName),
					ScriptName: c.ScriptName,
					Level:      "warning",
					EventType:  "port_conflict_skipped",
					Message:    fmt.Sprintf("port %d conflict skipped (policy: skip)", c.Port),
					Port:       c.Port,
					Timestamp:  time.Now(),
				})
			}

		case "auto-kill":
			killResults := killPortBlockers(ctx, d.hub.ProcessManager(), conflicts)
			for _, kr := range killResults {
				if kr.Killed {
					result.PortsCleared = append(result.PortsCleared, kr.PortConflict)
					log.Info(makeProcessID(projectPath, kr.ScriptName), kr.ScriptName,
						"port_conflict_killed",
						fmt.Sprintf("cleared port %d (was: %s PIDs %v)", kr.Port, kr.ProcessName, kr.PIDs))
				} else {
					log.Error(makeProcessID(projectPath, kr.ScriptName), kr.ScriptName,
						"port_conflict_failed", kr.Error)
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
	log := d.startupErrorStore

	for name, scriptCfg := range cfg.Scripts {
		processID := makeProcessID(projectPath, name)
		d.scriptConfigs.Store(processID, scriptCfg)
		if _, err := d.scriptRegistry.Register(name, projectPath, scriptConfigToEntry(scriptCfg)); err != nil {
			log.Error(processID, name, "register_failed", fmt.Sprintf("failed to register script: %v", err))
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
			log.Info(entry.ProcessID, entry.Name, "pruned_stale_script",
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
				log.Info(processID, name, "stale_cleanup",
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
func (d *Daemon) startAutostartScripts(ctx context.Context, cfg *config.AgntConfig, projectPath string, result *AutostartResult, progress chan<- AutostartProgress) map[string]bool {
	log := d.startupErrorStore
	autostartScripts := cfg.GetAutostartScripts()
	proxyConfigs := cfg.Proxies
	failedScripts := make(map[string]bool)

	if len(autostartScripts) == 0 {
		return failedScripts
	}

	layers, sortErr := config.TopologicalSort(autostartScripts)
	if sortErr != nil {
		log.Error("", "", "dependency_sort", fmt.Sprintf("topological sort failed: %v", sortErr))
		result.Errors = append(result.Errors, fmt.Sprintf("dependency sort: %v", sortErr))
		return failedScripts
	}

	var resultMu sync.Mutex

	for layerIdx, layer := range layers {
		// Use a done channel so we can select on ctx.Done() alongside layer completion.
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
				processID := makeProcessID(projectPath, name)

				// Skip if script is already running (idempotent autostart).
				if entry, ok := d.scriptRegistry.Get(name, projectPath); ok {
					state := entry.State()
					if state == script.StateRunning || state == script.StateStarting {
						log.Info(processID, name, "already_running", fmt.Sprintf("%s already %s, skipping", name, state.String()))
						probePorts := d.getExpectedPortsForScript(name, scriptCfg, proxyConfigs,
							resolveWorkingDir(projectPath, scriptCfg.Cwd), "", nil)
						if len(probePorts) > 0 {
							d.readySignaler.StartPortProbe(processID, probePorts[0], ctx)
						} else {
							d.readySignaler.SignalReady(processID)
						}
						resultMu.Lock()
						result.Scripts = append(result.Scripts, name)
						resultMu.Unlock()
						return
					}
				}

				emitProgress(progress, projectPath, AutostartProgress{
					Phase: PhaseScriptStarting, Script: name, Layer: layerIdx,
				})

				// Wait for dependencies (layer 1+)
				d.waitForDependenciesCtx(ctx, name, scriptCfg, projectPath, layerIdx, progress)

				// Check if context was cancelled during dependency wait
				if ctx.Err() != nil {
					return
				}

				// Start the script
				log.Info(processID, name, "starting", fmt.Sprintf("starting %s (layer %d)", name, layerIdx))
				if err := d.autostartScript(ctx, name, scriptCfg, projectPath, proxyConfigs); err != nil {
					log.Error(processID, name, "start_failed", err.Error())
					emitProgress(progress, projectPath, AutostartProgress{
						Phase: PhaseScriptFailed, Script: name, Layer: layerIdx, Err: err,
					})
					resultMu.Lock()
					result.Errors = append(result.Errors, fmt.Sprintf("script %s: %v", name, err))
					failedScripts[name] = true
					resultMu.Unlock()
					return
				}

				log.Info(processID, name, "started", fmt.Sprintf("%s started", name))
				emitProgress(progress, projectPath, AutostartProgress{
					Phase: PhaseScriptStarted, Script: name, Layer: layerIdx,
				})
				resultMu.Lock()
				result.Scripts = append(result.Scripts, name)
				resultMu.Unlock()

				// Port probe for dependency readiness signaling.
				// If no ports are expected, signal ready immediately so
				// dependents are unblocked.
				probePorts := d.getExpectedPortsForScript(name, scriptCfg, proxyConfigs,
					resolveWorkingDir(projectPath, scriptCfg.Cwd), "", nil)
				if len(probePorts) > 0 {
					d.readySignaler.StartPortProbe(processID, probePorts[0], ctx)
				} else {
					d.readySignaler.SignalReady(processID)
				}
			}(name, scriptCfg)
		}

		// Close layerDone when all goroutines in this layer finish.
		go func() {
			layerWg.Wait()
			close(layerDone)
		}()

		// Wait for either layer completion or context cancellation.
		select {
		case <-layerDone:
			// Layer finished normally.
		case <-ctx.Done():
			// Context cancelled. Wait for in-flight goroutines to notice
			// and exit, then return partial results.
			<-layerDone
			return failedScripts
		}

		emitProgress(progress, projectPath, AutostartProgress{
			Phase: PhaseLayerComplete, Layer: layerIdx,
		})
	}

	// Cleanup signaler channels
	for name := range autostartScripts {
		d.readySignaler.Cleanup(makeProcessID(projectPath, name))
	}

	return failedScripts
}

// waitForDependenciesCtx blocks until all dependencies for a script are ready
// or the context is cancelled. Uses context cancellation for the primary wait.
// If the parent context has no deadline, applies per-dependency timeouts as
// a fallback to avoid blocking forever.
// Emits progress events for each dependency wait and resolution.
func (d *Daemon) waitForDependenciesCtx(ctx context.Context, name string, scriptCfg *config.ScriptConfig, projectPath string, layerIdx int, progress chan<- AutostartProgress) {
	if layerIdx == 0 {
		return
	}
	_, parentHasDeadline := ctx.Deadline()
	processID := makeProcessID(projectPath, name)
	for _, dep := range scriptCfg.DependsOn {
		if ctx.Err() != nil {
			return
		}
		d.waitForSingleDependency(ctx, parentHasDeadline, name, processID, dep, projectPath, layerIdx, progress)
	}
}

// waitForSingleDependency waits for one dependency to become ready, applying a
// per-dep timeout when the parent context has no deadline. Separated from the
// loop so that timeout contexts can be cancelled per iteration via defer.
func (d *Daemon) waitForSingleDependency(ctx context.Context, parentHasDeadline bool, name, processID string, dep config.ScriptDependency, projectPath string, layerIdx int, progress chan<- AutostartProgress) {
	depProcessID := makeProcessID(projectPath, dep.Name)

	emitProgress(progress, projectPath, AutostartProgress{
		Phase: PhaseDependencyWaitStart, Script: name, Dependency: dep.Name, Layer: layerIdx,
	})

	waitCtx := ctx
	if !parentHasDeadline {
		timeout := dep.Timeout
		if timeout == 0 {
			timeout = config.DefaultDependencyTimeout
		}
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	if err := d.readySignaler.WaitReadyCtx(depProcessID, waitCtx); err != nil {
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID: processID, ScriptName: name,
			Level: "warning", EventType: "dependency_wait",
			Message:   fmt.Sprintf("cancelled waiting for %s: %v (starting anyway)", dep.Name, err),
			Timestamp: time.Now(),
		})
	} else {
		emitProgress(progress, projectPath, AutostartProgress{
			Phase: PhaseDependencyReady, Script: name, Dependency: dep.Name, Layer: layerIdx,
		})
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID: processID, ScriptName: name,
			Level: "info", EventType: "dependency_ready",
			Message:   fmt.Sprintf("dependency %s is ready", dep.Name),
			Timestamp: time.Now(),
		})
	}
}

// startAutostartProxies starts proxies, skipping those that depend on failed scripts.
// For script-linked proxies with fallback-port, schedules a delayed check to create
// the proxy if URL detection doesn't fire in time.
func (d *Daemon) startAutostartProxies(ctx context.Context, cfg *config.AgntConfig, projectPath string, failedScripts map[string]bool, result *AutostartResult) {
	log := d.startupErrorStore

	for proxyName, proxyConfig := range cfg.Proxies {
		if proxyConfig.Script != "" && failedScripts[proxyConfig.Script] {
			msg := fmt.Sprintf("proxy %q skipped: depends on failed script %q", proxyName, proxyConfig.Script)
			log.Error("", proxyName, "proxy_skipped", msg)
			result.Errors = append(result.Errors, msg)
		}
	}

	for name, proxyConfig := range cfg.GetAutostartProxies() {
		log.Info("", name, "proxy_starting", fmt.Sprintf("starting proxy %s", name))
		if err := d.autostartProxy(ctx, name, proxyConfig, projectPath); err != nil {
			log.Error("", name, "proxy_failed", err.Error())
			result.Errors = append(result.Errors, fmt.Sprintf("proxy %s: %v", name, err))
		} else {
			log.Info("", name, "proxy_started", fmt.Sprintf("proxy %s started", name))
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
				if d.startupErrorStore != nil {
					d.startupErrorStore.Add(&StartupLogEntry{
						ScriptName: name,
						Level:      "warning",
						EventType:  "proxy_event_dropped",
						Message:    fmt.Sprintf("proxy event channel full: fallback-port check for proxy %s could not be scheduled", name),
						Timestamp:  time.Now(),
					})
				}
			}
		}()
	}
}

func (d *Daemon) autostartScript(ctx context.Context, name string, scriptCfg *config.ScriptConfig, projectPath string, proxyConfigs map[string]*config.ProxyConfig) error {

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
		debug.Log("daemon", "autostartScript: script %s already %s, skipping", name, state)
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
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: "",
				Level:     "error",
				EventType: "autostart_failed",
				Message:   fmt.Sprintf("project detection failed for %s: %v", workingDir, err),
				Timestamp: time.Now(),
			})
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
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID: "",
				Level:     "error",
				EventType: "autostart_failed",
				Message:   fmt.Sprintf("cannot run script %q: unknown project type %s", name, proj.Type),
				Timestamp: time.Now(),
			})
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

	// Success: ProcessManager lifecycle sets StateRunning automatically
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
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID: "",
			Level:     "info",
			EventType: "proxy_deferred",
			Message:   msg,
			Timestamp: time.Now(),
		})
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
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID: "",
			Level:     "warning",
			EventType: "proxy_creation_failed",
			Message:   fmt.Sprintf("proxy event channel full, cannot queue proxy %s for auto-start", name),
			Timestamp: time.Now(),
		})
	}

	return nil
}

// detectPortForScript is deprecated and no longer used.
// Port detection is now handled by URLTracker emitting URLDetected events.
func (d *Daemon) detectPortForScript(ctx context.Context, scriptName string, proxyConfig *config.ProxyConfig) (int, error) {
	return 0, fmt.Errorf("deprecated: use event-driven proxy creation instead")
}

// Removed old autostartProxy implementation that did synchronous port detection.
// Now using event-driven approach:
// 1. URLTracker detects URLs from script output
// 2. Emits URLDetected events
// 3. handleURLDetected creates proxies for matching configs

// Old implementation kept detectPortForScript stub for reference, but it's no longer called.
func (d *Daemon) _old_detectPortForScript(ctx context.Context, scriptName string, proxyConfig *config.ProxyConfig) (int, error) {
	detector := config.NewPortDetector()

	// Create a timeout context for port detection (30 seconds)
	detectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Poll for port detection
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-detectCtx.Done():
			return 0, fmt.Errorf("timeout waiting for port detection")

		case <-ticker.C:
			// Get process to check if running
			proc, err := d.hub.ProcessManager().Get(scriptName)
			if err != nil {
				continue // Process may not be registered yet
			}

			// Check if process is running
			if !proc.IsRunning() {
				continue
			}

			// Try to get output and detect port from it
			output, _ := proc.CombinedOutput()
			if port := detector.DetectFromOutput(string(output)); port > 0 {
				return port, nil
			}

			// Try PID-based detection
			pid := proc.PID()
			if pid > 0 {
				if ports := detector.DetectFromPID(detectCtx, pid); len(ports) > 0 {
					return ports[0], nil
				}
			}
		}
	}
}

// MaxLogSize is the maximum log file size before rotation (5MB).
const MaxLogSize = 5 * 1024 * 1024

// MaxLogBackups is the number of rotated log files to keep.
const MaxLogBackups = 3

// GetLogPath returns the path to the daemon log file.
