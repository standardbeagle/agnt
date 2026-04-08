// Package autostart provides orchestration logic for automatically starting
// scripts and proxies from .agnt.kdl configuration. It handles dependency
// ordering, port conflict detection, proxy event scheduling, and two-phase
// autostart (prompt mode for port conflicts).
package autostart

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/preflight"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/agnt/internal/readiness"
	"github.com/standardbeagle/agnt/internal/startuplog"
	"github.com/standardbeagle/go-cli-server/process"
	"github.com/standardbeagle/go-cli-server/script"
)

// ProxyEventType represents the type of proxy event.
type ProxyEventType int

const (
	// URLDetected indicates a URL was detected from script output
	URLDetected ProxyEventType = iota
	// ExplicitStart indicates a proxy should start with explicit config
	ExplicitStart
	// ScriptStopped indicates a script stopped and its proxies should be cleaned up
	ScriptStopped
	// FallbackPortCheck indicates a delayed check for script-linked proxies that
	// weren't created by URL detection — creates proxy using fallback-port if needed
	FallbackPortCheck
)

// ProxyEvent represents an event that triggers proxy creation or cleanup.
type ProxyEvent struct {
	Type      ProxyEventType
	ScriptID  string // Process/script ID that triggered the event
	URL       string // Detected URL (for URLDetected events)
	ProxyID   string // Specific proxy ID (for ExplicitStart events)
	ProxyName string // Config proxy name (for FallbackPortCheck events)
	Config    *config.ProxyConfig
	Path      string // Project path
}

// AutostartResult holds the results of an autostart operation.
type AutostartResult struct {
	Scripts       []string                 `json:"scripts,omitempty"`
	Proxies       []string                 `json:"proxies,omitempty"`
	Errors        []string                 `json:"errors,omitempty"`
	PortConflicts []preflight.PortConflict `json:"port_conflicts,omitempty"`
	PortsCleared  []preflight.PortConflict `json:"ports_cleared,omitempty"`
}

// PendingAutostart holds state for a two-phase autostart (prompt mode).
type PendingAutostart struct {
	Config      *config.AgntConfig
	ProjectPath string
	Conflicts   []preflight.PortConflict
}

// StartScriptConfig holds configuration for starting a script/process.
type StartScriptConfig struct {
	ProcessID     string
	ProjectPath   string
	WorkingDir    string
	Command       string
	Args          []string
	Env           []string
	ExpectedPorts []int
	URLMatchers   []string
	AutoRestart   bool
}

// StartScriptResult holds the result of starting a script.
type StartScriptResult struct {
	Process *process.ManagedProcess
	Reused  bool
}

// Deps provides the daemon capabilities needed by autostart orchestration.
type Deps interface {
	// Process management
	CollectManagedPIDs() map[int]bool
	GetProcess(id string) (*process.ManagedProcess, error)
	RemoveProcessByPath(id string, projectPath string) bool

	// Script registry
	GetScriptEntry(id, projectPath string) (*script.Entry, bool)
	RegisterScriptEntry(id, projectPath string, cfg *script.Config) (*script.Entry, error)
	RemoveScriptEntry(id, projectPath string)
	ListScriptEntries(projectPath string) []*script.Entry
	GetScriptEntryByProcessID(processID string) (*script.Entry, bool)

	// Per-script agnt config storage
	GetScriptConfig(processID string) (*config.ScriptConfig, bool)
	SetScriptConfig(processID string, cfg *config.ScriptConfig)
	DeleteScriptConfig(processID string)

	// Readiness coordination
	ProcessReadiness() *readiness.ProcessReadiness

	// Startup logging
	StartupLog() *startuplog.StartupLogStore

	// Proxy event submission (non-blocking send to channel)
	SubmitProxyEvent(event ProxyEvent)

	// Pending autostarts (two-phase port conflict prompt mode)
	LoadPendingAutostart(projectPath string) (*PendingAutostart, bool)
	StorePendingAutostart(projectPath string, pending *PendingAutostart)
	DeletePendingAutostart(projectPath string) bool

	// Expected ports for a script (for port probes)
	GetExpectedPortsForScript(scriptName string, scriptCfg *config.ScriptConfig, proxyConfigs map[string]*config.ProxyConfig, projectPath string, command string, args []string) []int

	// Start a script with EADDRINUSE recovery
	StartScript(ctx context.Context, cfg StartScriptConfig) (*StartScriptResult, error)

	// Path normalization (platform-specific)
	NormalizePath(path string) string

	// Wait for a port to become free
	WaitForPortFree(port int, timeout time.Duration)

	// Kill port blockers (delegates to preflight.KillPortBlockers)
	KillPortBlockers(ctx context.Context, conflicts []preflight.PortConflict) []preflight.KillResult

	// Auto-restart registration
	RegisterAutoRestart(processID string)
	UnregisterAutoRestart(processID string)
}

// Run loads .agnt.kdl config from projectPath and starts configured processes/proxies.
// This is called during SESSION REGISTER to ensure autostart happens once per project.
// Scripts are started in dependency order using topological sort:
//   - Layer 0 scripts (no dependencies) start concurrently
//   - Layer 1+ scripts wait for all their dependencies to become ready
//   - Readiness is signaled by URL detection or TCP port probe
//   - Timeout on dependency wait logs a warning and starts the script anyway
//
// Returns the list of started scripts/proxies and any errors encountered.
func Run(ctx context.Context, projectPath string, d Deps) *AutostartResult {
	result := &AutostartResult{}
	log := d.StartupLog()

	// Step 1: Validate input
	if projectPath == "" {
		log.Error("", "", "autostart", "projectPath is empty")
		return result
	}

	// Normalize path so script registry keys match lookup queries.
	projectPath = d.NormalizePath(projectPath)

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
	managedPIDs := d.CollectManagedPIDs()
	conflicts := preflight.DetectPortConflicts(ctx, autostartScripts, managedPIDs)

	if len(conflicts) > 0 {
		policy := agntConfig.EffectivePortConflictPolicy()

		for _, c := range conflicts {
			log.Add(&startuplog.StartupLogEntry{
				ProcessID:  MakeProcessID(projectPath, c.ScriptName),
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
				log.Add(&startuplog.StartupLogEntry{
					ProcessID:  MakeProcessID(projectPath, c.ScriptName),
					ScriptName: c.ScriptName,
					Level:      "warning",
					EventType:  "port_conflict_skipped",
					Message:    fmt.Sprintf("port %d conflict skipped (policy: skip)", c.Port),
					Port:       c.Port,
					Timestamp:  time.Now(),
				})
			}

		case "auto-kill":
			killResults := d.KillPortBlockers(ctx, conflicts)
			for _, kr := range killResults {
				if kr.Killed {
					result.PortsCleared = append(result.PortsCleared, kr.PortConflict)
					log.Info(MakeProcessID(projectPath, kr.ScriptName), kr.ScriptName,
						"port_conflict_killed",
						fmt.Sprintf("cleared port %d (was: %s PIDs %v)", kr.Port, kr.ProcessName, kr.PIDs))
				} else {
					log.Error(MakeProcessID(projectPath, kr.ScriptName), kr.ScriptName,
						"port_conflict_failed", kr.Error)
					result.Errors = append(result.Errors, kr.Error)
				}
			}

		case "prompt":
			d.StorePendingAutostart(projectPath, &PendingAutostart{
				Config:      agntConfig,
				ProjectPath: projectPath,
				Conflicts:   conflicts,
			})
			result.PortConflicts = conflicts
			return result
		}
	}

	// Step 4: Register + start
	registerAndStartScripts(ctx, d, agntConfig, projectPath, result)
	return result
}

// Resume continues a paused autostart after port conflict resolution.
func Resume(ctx context.Context, projectPath string, d Deps) *AutostartResult {
	result := &AutostartResult{}

	pending, ok := d.LoadPendingAutostart(projectPath)
	if !ok {
		result.Errors = append(result.Errors, "no pending autostart for this project")
		return result
	}

	registerAndStartScripts(ctx, d, pending.Config, pending.ProjectPath, result)
	return result
}

// registerAndStartScripts registers all scripts, starts autostart scripts in
// dependency order, then starts proxies. Shared by Run and Resume.
func registerAndStartScripts(ctx context.Context, d Deps, cfg *config.AgntConfig, projectPath string, result *AutostartResult) {
	log := d.StartupLog()

	for name, scriptCfg := range cfg.Scripts {
		processID := MakeProcessID(projectPath, name)
		d.SetScriptConfig(processID, scriptCfg)
		if _, err := d.RegisterScriptEntry(name, projectPath, scriptConfigToEntry(scriptCfg)); err != nil {
			log.Error(processID, name, "register_failed", fmt.Sprintf("failed to register script: %v", err))
		}
	}

	// Prune stale script entries that are no longer in config.
	for _, entry := range d.ListScriptEntries(projectPath) {
		if _, inConfig := cfg.Scripts[entry.Name]; !inConfig {
			d.RemoveScriptEntry(entry.Name, projectPath)
			d.DeleteScriptConfig(entry.ProcessID)
			d.UnregisterAutoRestart(entry.ProcessID)
			log.Info(entry.ProcessID, entry.Name, "pruned_stale_script",
				fmt.Sprintf("removed script %q (no longer in config)", entry.Name))
		}
	}

	failedScripts := startAutostartScripts(ctx, d, cfg, projectPath, result)
	startAutostartProxies(ctx, d, cfg, projectPath, failedScripts, result)
}

// startAutostartScripts starts scripts in topological dependency order.
// Returns a set of script names that failed to start.
func startAutostartScripts(ctx context.Context, d Deps, cfg *config.AgntConfig, projectPath string, result *AutostartResult) map[string]bool {
	log := d.StartupLog()
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
		var layerWg sync.WaitGroup
		for _, name := range layer {
			scriptCfg := autostartScripts[name]
			if scriptCfg == nil {
				continue
			}

			layerWg.Add(1)
			go func(name string, scriptCfg *config.ScriptConfig) {
				defer layerWg.Done()
				processID := MakeProcessID(projectPath, name)

				// Skip if script is already running (idempotent autostart).
				if entry, ok := d.GetScriptEntry(name, projectPath); ok {
					state := entry.State()
					if state == script.StateRunning || state == script.StateStarting {
						log.Info(processID, name, "already_running", fmt.Sprintf("%s already %s, skipping", name, state))
						if len(scriptCfg.Ports) > 0 {
							d.ProcessReadiness().StartPortProbe(processID, scriptCfg.Ports[0], ctx)
						} else {
							d.ProcessReadiness().MarkReady(processID)
						}
						resultMu.Lock()
						result.Scripts = append(result.Scripts, name)
						resultMu.Unlock()
						return
					}
				}

				d.ProcessReadiness().MarkStarting(processID)

				// Wait for dependencies (layer 1+)
				waitForDependencies(ctx, d, name, scriptCfg, projectPath, layerIdx)

				// Start the script
				log.Info(processID, name, "starting", fmt.Sprintf("starting %s (layer %d)", name, layerIdx))
				if err := autostartScript(ctx, d, name, scriptCfg, projectPath, proxyConfigs); err != nil {
					log.Error(processID, name, "start_failed", err.Error())
					d.ProcessReadiness().MarkFailed(processID, err)
					resultMu.Lock()
					result.Errors = append(result.Errors, fmt.Sprintf("script %s: %v", name, err))
					failedScripts[name] = true
					resultMu.Unlock()
					return
				}

				log.Info(processID, name, "started", fmt.Sprintf("%s started", name))
				resultMu.Lock()
				result.Scripts = append(result.Scripts, name)
				resultMu.Unlock()

				// Port probe for dependency readiness signaling (only when explicitly opted in)
				if len(scriptCfg.Ports) > 0 {
					d.ProcessReadiness().StartPortProbe(processID, scriptCfg.Ports[0], ctx)
				}
			}(name, scriptCfg)
		}
		layerWg.Wait()
	}

	// Cleanup readiness entries
	for name := range autostartScripts {
		d.ProcessReadiness().Cleanup(MakeProcessID(projectPath, name))
	}

	return failedScripts
}

// waitForDependencies blocks until all dependencies for a script are ready,
// failed, or exited. Any terminal state unblocks the waiter immediately.
func waitForDependencies(ctx context.Context, d Deps, name string, scriptCfg *config.ScriptConfig, projectPath string, layerIdx int) {
	if layerIdx == 0 {
		return
	}
	processID := MakeProcessID(projectPath, name)
	for _, dep := range scriptCfg.DependsOn {
		depProcessID := MakeProcessID(projectPath, dep.Name)
		timeout := dep.Timeout
		if timeout == 0 {
			timeout = config.DefaultDependencyTimeout
		}

		result := d.ProcessReadiness().Wait(depProcessID, timeout)

		switch {
		case result.State == readiness.RStateReady:
			d.StartupLog().Add(&startuplog.StartupLogEntry{
				ProcessID: processID, ScriptName: name,
				Level: "info", EventType: "dependency_ready",
				Message:   fmt.Sprintf("dependency %s is ready", dep.Name),
				Timestamp: time.Now(),
			})
		case result.State == readiness.RStateExited:
			d.StartupLog().Add(&startuplog.StartupLogEntry{
				ProcessID: processID, ScriptName: name,
				Level: "warning", EventType: "dependency_exited",
				Message:   fmt.Sprintf("dependency %s exited (starting anyway)", dep.Name),
				Timestamp: time.Now(),
			})
		case result.State == readiness.RStateFailed:
			d.StartupLog().Add(&startuplog.StartupLogEntry{
				ProcessID: processID, ScriptName: name,
				Level: "warning", EventType: "dependency_failed",
				Message:   fmt.Sprintf("dependency %s failed: %v (starting anyway)", dep.Name, result.Err),
				Timestamp: time.Now(),
			})
		default: // timeout
			d.StartupLog().Add(&startuplog.StartupLogEntry{
				ProcessID: processID, ScriptName: name,
				Level: "warning", EventType: "dependency_timeout",
				Message:   fmt.Sprintf("timeout waiting for %s after %v (starting anyway)", dep.Name, timeout),
				Timestamp: time.Now(),
			})
		}
	}
}

// startAutostartProxies starts proxies, skipping those that depend on failed scripts.
func startAutostartProxies(ctx context.Context, d Deps, cfg *config.AgntConfig, projectPath string, failedScripts map[string]bool, result *AutostartResult) {
	log := d.StartupLog()

	for proxyName, proxyConfig := range cfg.Proxies {
		if proxyConfig.Script != "" && failedScripts[proxyConfig.Script] {
			msg := fmt.Sprintf("proxy %q skipped: depends on failed script %q", proxyName, proxyConfig.Script)
			log.Error("", proxyName, "proxy_skipped", msg)
			result.Errors = append(result.Errors, msg)
		}
	}

	for name, proxyConfig := range cfg.GetAutostartProxies() {
		log.Info("", name, "proxy_starting", fmt.Sprintf("starting proxy %s", name))
		if err := autostartProxy(ctx, d, name, proxyConfig, projectPath); err != nil {
			log.Error("", name, "proxy_failed", err.Error())
			result.Errors = append(result.Errors, fmt.Sprintf("proxy %s: %v", name, err))
		} else {
			log.Info("", name, "proxy_started", fmt.Sprintf("proxy %s started", name))
			result.Proxies = append(result.Proxies, name)
		}
	}

	// Schedule fallback-port checks for script-linked proxies.
	scheduleFallbackPortChecks(ctx, d, cfg, projectPath, failedScripts)
}

// scheduleFallbackPortChecks schedules delayed FallbackPortCheck events for
// script-linked proxies that have a fallback-port configured.
func scheduleFallbackPortChecks(ctx context.Context, d Deps, cfg *config.AgntConfig, projectPath string, failedScripts map[string]bool) {
	for proxyName, proxyConfig := range cfg.Proxies {
		if proxyConfig.Script == "" {
			continue
		}
		if failedScripts[proxyConfig.Script] {
			continue
		}
		if proxyConfig.FallbackPort <= 0 {
			continue
		}

		name := proxyName
		pc := proxyConfig
		scriptID := MakeProcessID(projectPath, pc.Script)

		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}

			d.SubmitProxyEvent(ProxyEvent{
				Type:      FallbackPortCheck,
				ScriptID:  scriptID,
				ProxyName: name,
				Config:    pc,
				Path:      projectPath,
			})
		}()
	}
}

// autostartScript starts a single script from config with automatic EADDRINUSE recovery.
func autostartScript(ctx context.Context, d Deps, name string, scriptCfg *config.ScriptConfig, projectPath string, proxyConfigs map[string]*config.ProxyConfig) error {
	processID := MakeProcessID(projectPath, name)

	// Store agnt-specific config for later use
	d.SetScriptConfig(processID, scriptCfg)

	// Register in ScriptRegistry before starting (idempotent)
	entry, regErr := d.RegisterScriptEntry(name, projectPath, scriptConfigToEntry(scriptCfg))
	if regErr != nil {
		return fmt.Errorf("script registry: %w", regErr)
	}

	// Check ScriptRegistry state: if already running/starting, skip
	if state := entry.State(); state == script.StateRunning || state == script.StateStarting {
		debug.Log("autostart", "autostartScript: script %s already %s, skipping", name, state)
		return nil
	}

	// Clean up stale ProcessManager entry if it exists but isn't running
	if existing, err := d.GetProcess(processID); err == nil {
		state := existing.State()
		if state != process.StateRunning && state != process.StateStarting {
			debug.Log("autostart", "autostartScript: removing stale process %s (state=%s)", processID, state)
			d.RemoveProcessByPath(processID, projectPath)
		}
	}

	// Resolve working directory and environment
	workingDir := ResolveWorkingDir(projectPath, scriptCfg.Cwd)
	envSlice := EnvMapToSlice(scriptCfg.Env)

	var command string
	var args []string

	if scriptCfg.Run != "" {
		command, args = scriptCfg.ResolveShell()
	} else if scriptCfg.Command != "" {
		command = scriptCfg.Command
		args = scriptCfg.Args
	} else {
		proj, err := project.Detect(workingDir)
		if err != nil {
			debug.Error("autostart", "project detection failed for %s: %v", workingDir, err)
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
			debug.Error("autostart", "cannot run script %q: unknown project type %s", name, proj.Type)
			entry.SetState(script.StateFailed)
			entry.SetLastError(fmt.Sprintf("unknown project type: %s", proj.Type))
			entry.IncrementFailCount()
			return fmt.Errorf("cannot run script %q: unknown project type and no command specified", name)
		}
	}

	// Record resolved command in ScriptEntry
	entry.SetResolvedCommand(command, args)

	// Transition to starting
	entry.SetState(script.StateStarting)

	// Determine expected ports for pre-flight cleanup and EADDRINUSE recovery
	expectedPorts := d.GetExpectedPortsForScript(name, scriptCfg, proxyConfigs, workingDir, command, args)

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

		cmdStr := command
		if len(args) > 0 {
			cmdStr = fmt.Sprintf("%s %s", command, strings.Join(args, " "))
		}
		msg := fmt.Sprintf("%s (resolved command: %s, cwd: %s)", err.Error(), cmdStr, workingDir)
		return fmt.Errorf("%s", msg)
	}

	return nil
}

// autostartProxy starts a single proxy from config.
// Script-linked proxies are skipped here -- they're created by the event system when URLs are detected.
func autostartProxy(ctx context.Context, d Deps, name string, proxyConfig *config.ProxyConfig, projectPath string) error {
	if proxyConfig.Script != "" {
		msg := fmt.Sprintf("proxy %s: waiting for URL detection from script %q", name, proxyConfig.Script)
		if proxyConfig.FallbackPort > 0 {
			msg += fmt.Sprintf(" (fallback-port %d if detection fails)", proxyConfig.FallbackPort)
		}
		debug.Log("autostart", "%s", msg)
		return nil
	}

	proxyID := MakeProcessID(projectPath, name)

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
		targetURL = proxyConfig.Target
	}

	if targetURL == "" {
		debug.Log("autostart", "Proxy %s has no explicit target URL, skipping", name)
		return nil
	}

	d.SubmitProxyEvent(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  proxyConfig,
		Path:    projectPath,
	})

	return nil
}

// RestartScript restarts a single script. This is the exported version used by
// hub handlers (SCRIPT RESTART). It performs the same steps as autostartScript
// but is called explicitly by the user.
func RestartScript(ctx context.Context, d Deps, name string, scriptCfg *config.ScriptConfig, projectPath string, proxyConfigs map[string]*config.ProxyConfig) error {
	return autostartScript(ctx, d, name, scriptCfg, projectPath, proxyConfigs)
}

// MakeProcessID delegates to script.MakeProcessID for process ID generation.
func MakeProcessID(projectPath, name string) string {
	return script.MakeProcessID(projectPath, name)
}

// StripProcessPrefix extracts the script name from a process ID.
// Process IDs use the format "project-hash:name"; this returns just "name".
func StripProcessPrefix(processID string) string {
	if idx := strings.Index(processID, ":"); idx >= 0 {
		return processID[idx+1:]
	}
	return processID
}

// MapKeys extracts keys from a script config map for logging.
func MapKeys(m map[string]*config.ScriptConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// MapKeysProxy extracts keys from a proxy config map for logging.
func MapKeysProxy(m map[string]*config.ProxyConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// ResolveWorkingDir resolves the working directory for a script.
func ResolveWorkingDir(projectPath, cwd string) string {
	if cwd == "" {
		return projectPath
	}
	if filepath.IsAbs(cwd) {
		return cwd
	}
	return filepath.Clean(filepath.Join(projectPath, cwd))
}

// EnvMapToSlice converts a map of environment variables to a slice of "KEY=VALUE" strings.
func EnvMapToSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

// ScriptConfigToEntry converts agnt's ScriptConfig to go-cli-server's script.Config.
func ScriptConfigToEntry(cfg *config.ScriptConfig) *script.Config {
	return &script.Config{
		Run:       cfg.Run,
		Command:   cfg.Command,
		Args:      cfg.Args,
		Shell:     cfg.Shell,
		ShellArgs: cfg.ShellArgs,
		Autostart: cfg.Autostart,
		Env:       cfg.Env,
		Cwd:       cfg.Cwd,
	}
}

// scriptConfigToEntry is the unexported version used within the autostart package.
func scriptConfigToEntry(cfg *config.ScriptConfig) *script.Config {
	return ScriptConfigToEntry(cfg)
}
