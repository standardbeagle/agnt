// Package daemon provides the background daemon service.
package daemon

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/go-cli-server/process"
	"github.com/standardbeagle/go-cli-server/script"
)

// StartScriptConfig holds configuration for starting a script/process.
// This unified config is used by both autostartScript and hub handlers
// to ensure consistent behavior (EADDRINUSE recovery, URL tracking, auto-restart).
type StartScriptConfig struct {
	// ProcessID is the unique identifier for the process
	ProcessID string
	// ProjectPath is the root project directory (where .agnt.kdl is located).
	// Used for session association and proxy event handling.
	// If empty, defaults to WorkingDir.
	ProjectPath string
	// WorkingDir is the working directory for the process (may differ from ProjectPath
	// when script has cwd configured)
	WorkingDir string
	// Command is the executable to run
	Command string
	// Args are the command arguments
	Args []string
	// Env are environment variables (KEY=VALUE format)
	Env []string
	// ExpectedPorts are the ports the process is expected to use (for pre-flight cleanup and EADDRINUSE recovery)
	ExpectedPorts []int
	// URLMatchers are patterns for URL detection in output
	URLMatchers []string
	// AutoRestart enables automatic restart on process exit
	AutoRestart bool
	// AutoRestartConfig is optional custom restart configuration
	AutoRestartConfig *AutoRestartConfig
}

// StartScriptResult holds the result of starting a script.
type StartScriptResult struct {
	Process *process.ManagedProcess
	Reused  bool // True if an existing process was reused
}

// StartScript starts a script/process with unified behavior:
// - Pre-flight port cleanup and EADDRINUSE recovery
// - URL matcher setup for proxy auto-creation
// - Auto-restart registration for crash recovery
//
// This is the canonical way to start processes in the daemon.
// Both autostartScript and hub handlers should use this.
func (d *Daemon) StartScript(ctx context.Context, cfg StartScriptConfig) (*StartScriptResult, error) {
	// Default ProjectPath to WorkingDir if not set, and normalize so the
	// scriptRegistry key matches what RunAutostart / SCRIPT LIST / cleanup
	// paths use. Without this, callers that pass a raw cwd (e.g. Windows
	// mixed-case 'C:\Users\RUNNER~1\...') stash the entry under one key and
	// subsequent lookups with the normalized form miss it.
	projectPath := cfg.ProjectPath
	if projectPath == "" {
		projectPath = cfg.WorkingDir
	}
	projectPath = normalizePath(projectPath)

	// Ensure a ScriptEntry exists (idempotent). For standalone processes not
	// from .agnt.kdl, this creates the entry so the code path is uniform.
	//
	// The registry keys on (projectPath, name); with either empty the entry is
	// unreachable from SCRIPT LIST and the project-scoped cleanup paths, so a
	// registration would be write-only. Skip it rather than store an orphan.
	scriptName := stripProcessPrefix(cfg.ProcessID)
	if scriptName != "" && projectPath != "" {
		if _, err := d.registerScriptEntry(scriptName, projectPath, &script.Config{
			Command: cfg.Command,
			Args:    cfg.Args,
		}); err != nil {
			return nil, fmt.Errorf("script registry: %w", err)
		}
	} else {
		debug.Warn("daemon", "StartScript: no script entry for %q (name=%q project=%q)", cfg.ProcessID, scriptName, projectPath)
	}

	// Set URL matchers BEFORE starting the process to ensure they're available
	// when the URL tracker first scans the process output
	if len(cfg.URLMatchers) > 0 {
		d.urlTracker.SetURLMatchers(cfg.ProcessID, cfg.URLMatchers)
		debug.Log("daemon", "Pre-set URL matchers for %s: %v", cfg.ProcessID, cfg.URLMatchers)
	}

	// Start with automatic EADDRINUSE recovery.
	// Pass autoRestart so that non-EADDRINUSE early exits are treated as the
	// first crash cycle rather than fatal startup failures.
	proc, startupErr := d.startScriptWithRetry(ctx, cfg.ProcessID, projectPath, cfg.WorkingDir, cfg.Command, cfg.Args, cfg.Env, cfg.ExpectedPorts, cfg.AutoRestart)
	if startupErr != nil {
		// Clean up pre-set matchers on failure
		d.urlTracker.SetURLMatchers(cfg.ProcessID, nil)

		// Record in startup error store for later diagnosis
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID:  cfg.ProcessID,
			ScriptName: stripProcessPrefix(cfg.ProcessID),
			Level:      "error",
			EventType:  startupErr.ErrorType,
			Message:    startupErr.Message,
			Output:     startupErr.Output,
			Port:       startupErr.Port,
			Timestamp:  time.Now(),
		})

		return nil, startupErr
	}

	// Register for auto-restart if enabled. When the process exits quickly
	// (e.g. `exit 1`), startScriptWithRetry returns the already-failed proc.
	// monitorProcess finds it via Get, receives from the already-closed
	// proc.Done(), and proceeds with restart logic immediately.
	if cfg.AutoRestart && d.autoRestarter != nil {
		restartConfig := DefaultAutoRestartConfig()
		restartConfig.Enabled = true
		if cfg.AutoRestartConfig != nil {
			restartConfig = *cfg.AutoRestartConfig
		}
		d.autoRestarter.Register(cfg.ProcessID, restartConfig, cfg.Command, cfg.Args, cfg.Env, cfg.ExpectedPorts, cfg.ProjectPath, cfg.WorkingDir)
		debug.Log("daemon", "Registered %s for auto-restart", cfg.ProcessID)
	}

	return &StartScriptResult{
		Process: proc,
		Reused:  false, // startScriptWithRetry always creates new process
	}, nil
}

// StartupError represents a startup failure with recovery information.
type StartupError struct {
	ProcessID string
	Port      int
	ErrorType string // "EADDRINUSE", "generic"
	Message   string
	Output    string // Last lines of process output (for user-facing diagnostics)
	Retried   bool
}

func (e *StartupError) Error() string {
	return e.Message
}

// portPatterns matches common port specifications in scripts
var portPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-p\s+(\d+)`),           // -p 3000
	regexp.MustCompile(`--port[=\s]+(\d+)`),    // --port 3000 or --port=3000
	regexp.MustCompile(`PORT[=:]\s*(\d+)`),     // PORT=3000 or PORT: 3000
	regexp.MustCompile(`localhost:(\d+)`),      // localhost:3000
	regexp.MustCompile(`127\.0\.0\.1:(\d+)`),   // 127.0.0.1:3000
	regexp.MustCompile(`0\.0\.0\.0:(\d+)`),     // 0.0.0.0:3000
	regexp.MustCompile(`next dev.*-p\s*(\d+)`), // next dev -p 3000
	regexp.MustCompile(`vite.*--port\s*(\d+)`), // vite --port 3000
}

// eaddrinusePatterns matches EADDRINUSE error messages across platforms.
// Includes Node.js EADDRINUSE, Go bind errors, .NET HttpListener conflicts,
// and generic port-in-use messages.
var eaddrinusePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)EADDRINUSE.*:(\d+)`),
	regexp.MustCompile(`(?i)address already in use.*:(\d+)`),
	regexp.MustCompile(`(?i)listen.*EADDRINUSE`),
	regexp.MustCompile(`(?i)port (\d+).*already in use`),
	regexp.MustCompile(`(?i)address.*:(\d+).*in use`),
	// .NET HttpListener / HTTP.sys on Windows
	regexp.MustCompile(`(?i)conflicts with an existing registration.*:(\d+)`),
	regexp.MustCompile(`(?i)Failed to listen on prefix.*:(\d+)`),
	// .NET Kestrel
	regexp.MustCompile(`(?i)Unable to bind to.*:(\d+)`),
	// Python
	regexp.MustCompile(`(?i)OSError.*Address already in use.*:(\d+)`),
	regexp.MustCompile(`(?i)\[Errno 98\].*:(\d+)`),    // Linux
	regexp.MustCompile(`(?i)\[Errno 10048\].*:(\d+)`), // Windows
}

// bareColonPortPattern matches any ":<port>" token; used as a fallback when an
// eaddrinuse pattern matched but captured no port. Package-level so it is
// compiled once, not on every extractPortFromError call.
var bareColonPortPattern = regexp.MustCompile(`:(\d{2,5})\b`)

// extractPortFromCommand extracts a port number from a command and its arguments.
func extractPortFromCommand(command string, args []string) int {
	// Build full command line for pattern matching
	fullCmd := command + " " + strings.Join(args, " ")

	for _, pattern := range portPatterns {
		if matches := pattern.FindStringSubmatch(fullCmd); len(matches) > 1 {
			if port, err := strconv.Atoi(matches[1]); err == nil && port > 0 && port < 65536 {
				return port
			}
		}
	}
	return 0
}

// extractPortFromProxyConfig gets the expected port from a proxy configuration.
func extractPortFromProxyConfig(proxyConfig *config.ProxyConfig) int {
	if proxyConfig == nil {
		return 0
	}

	// Direct port specification
	if proxyConfig.Port > 0 {
		return proxyConfig.Port
	}

	// Extract from URL
	if proxyConfig.URL != "" {
		if u, err := url.Parse(proxyConfig.URL); err == nil {
			if port := u.Port(); port != "" {
				if p, err := strconv.Atoi(port); err == nil {
					return p
				}
			}
		}
	}

	// Extract from legacy target
	if proxyConfig.Target != "" {
		if u, err := url.Parse(proxyConfig.Target); err == nil {
			if port := u.Port(); port != "" {
				if p, err := strconv.Atoi(port); err == nil {
					return p
				}
			}
		}
	}

	// Fallback port (used when URL detection fails)
	if proxyConfig.FallbackPort > 0 {
		return proxyConfig.FallbackPort
	}

	return 0
}

// detectEADDRINUSE checks process output for EADDRINUSE errors.
// Returns the port number if found, 0 otherwise.
func detectEADDRINUSE(output string) int {
	for _, pattern := range eaddrinusePatterns {
		if matches := pattern.FindStringSubmatch(output); len(matches) > 1 {
			if port, err := strconv.Atoi(matches[1]); err == nil && port > 0 && port < 65536 {
				return port
			}
		}
		// Pattern matched but no port captured - try to find port separately
		if pattern.MatchString(output) {
			// Look for any port number in the error line
			if matches := bareColonPortPattern.FindStringSubmatch(output); len(matches) > 1 {
				if port, err := strconv.Atoi(matches[1]); err == nil && port > 0 && port < 65536 {
					return port
				}
			}
		}
	}
	return 0
}

// preflightPortCleanup cleans up any process using the specified port.
// Only kills processes that are NOT managed by this daemon.
func (d *Daemon) preflightPortCleanup(ctx context.Context, port int) ([]int, error) {
	if port <= 0 {
		return nil, nil
	}

	debug.Log("daemon", "Pre-flight cleanup: checking port %d", port)

	// Guarded kill: KillProcessByPort itself has no managed/self exclusion —
	// it kills every holder it re-discovers at fire time — so the guard
	// re-scans and refuses when the daemon or a managed process holds the port.
	killedPIDs, protected, err := killPortHoldersGuarded(ctx, d.hub.ProcessManager(), port)
	if err != nil {
		return nil, fmt.Errorf("failed to cleanup port %d: %w", port, err)
	}
	if len(protected) > 0 {
		return nil, fmt.Errorf("port %d held by daemon or managed process (PIDs %v), kill skipped", port, protected)
	}

	if len(killedPIDs) > 0 {
		debug.Info("daemon", "Pre-flight cleanup: killed %d process(es) on port %d: %v", len(killedPIDs), port, killedPIDs)
		// Poll until the port is actually free (up to 5 seconds)
		d.waitForPortFree(port, 5*time.Second)
	}

	return killedPIDs, nil
}

// waitForPortFree polls until the given port is free or timeout expires.
// Checks both IPv4 and IPv6 loopback since the process may bind on either.
func (d *Daemon) waitForPortFree(port int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	addrs := []string{
		fmt.Sprintf("127.0.0.1:%d", port),
		fmt.Sprintf("[::1]:%d", port),
	}
	for time.Now().Before(deadline) {
		allFree := true
		for _, addr := range addrs {
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				allFree = false
				break
			}
		}
		if allFree {
			debug.Log("daemon", "Port %d is free", port)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	debug.Warn("daemon", "Port %d still in use after %v", port, timeout)
	d.daemonStartupLogPort("warning", "port_cleanup_timeout",
		fmt.Sprintf("port %d still in use after %v", port, timeout), port)
}

// startScriptWithRetry starts a script with automatic EADDRINUSE recovery.
// It monitors the process output for startup failures and retries once after cleanup.
// projectPath is the root project directory (for session association).
// workingDir is the actual working directory for the process (may differ when script has cwd).
func (d *Daemon) startScriptWithRetry(
	ctx context.Context,
	processID string,
	projectPath string,
	workingDir string,
	command string,
	args []string,
	env []string,
	expectedPorts []int,
	autoRestart bool,
) (*process.ManagedProcess, *StartupError) {

	// Pre-flight cleanup: kill orphans on all expected ports
	for _, port := range expectedPorts {
		if killedPIDs, err := d.preflightPortCleanup(ctx, port); err != nil {
			debug.Warn("daemon", "Pre-flight cleanup failed for port %d: %v", port, err)
			d.recordStartupEntry(processID, "", "warning", "port_cleanup_failed",
				fmt.Sprintf("pre-flight cleanup failed for port %d: %v", port, err), port)
		} else if len(killedPIDs) > 0 {
			debug.Info("daemon", "Cleaned up port %d before starting %s", port, processID)
			d.recordStartupEntry(processID, stripProcessPrefix(processID), "info", "port_cleanup",
				fmt.Sprintf("killed %d process(es) on port %d", len(killedPIDs), port), port)
		}
	}

	// Wire output callback to feed ScriptEntry output buffer and broadcast to stream sinks.
	// The OutputCallback does not distinguish stdout/stderr, so stream is set to "combined".
	var scriptEntry *script.Entry
	if e, ok := d.scriptRegistry.Get(stripProcessPrefix(processID), projectPath); ok {
		scriptEntry = e
	}
	owner, _ := d.incidentProcessOwner.Load(processID)
	lifetimeOwner := ownerAsIncidentResource(owner)
	outputCB := func(_ string, line string) {
		if scriptEntry != nil {
			scriptEntry.AppendOutput(line)
		}
		d.eventHub.BroadcastProcessOutput(proxy.LogEntry{
			Type: proxy.LogTypeProcessOutput,
			ProcessOutput: &proxy.ProcessOutputEvent{
				ProcessID: processID,
				Stream:    "combined",
				Line:      line,
				Timestamp: time.Now(),
			},
		}, projectPath)
		d.alertScanner.ProcessLineWithLifetime(line, processID, lifetimeOwner)
	}

	// Start the process
	result, err := d.hub.ProcessManager().StartOrReuse(ctx, process.ProcessConfig{
		ID:             processID,
		ProjectPath:    projectPath,
		WorkingDir:     workingDir,
		Command:        command,
		Args:           args,
		Env:            env,
		OutputCallback: outputCB,
	})
	if err != nil {
		return nil, &StartupError{
			ProcessID: processID,
			ErrorType: "start_failed",
			Message:   fmt.Sprintf("failed to start process: %v", err),
		}
	}

	proc := result.Process

	// Monitor for early failure (first N seconds, configurable via DaemonConfig).
	// Use first expected port for EADDRINUSE detection.
	expectedPort := 0
	if len(expectedPorts) > 0 {
		expectedPort = expectedPorts[0]
	}
	monitorTimeout := d.config.StartupMonitorTimeout
	if monitorTimeout <= 0 {
		monitorTimeout = 3 * time.Second
	}
	startupErr := d.monitorStartupFailure(ctx, proc, expectedPort, monitorTimeout)
	if startupErr == nil {
		d.watchProcessExit(proc)
		return proc, nil
	}

	// Startup failed - check if it's EADDRINUSE
	if startupErr.ErrorType != "EADDRINUSE" {
		// When auto-restart is enabled, a quick exit (e.g. `exit 1`) is the
		// first crash cycle — not a fatal failure. Return the failed proc so
		// StartScript can register it with the auto-restarter. The auto-restarter
		// calls proc.Done() (already closed) and immediately schedules the restart.
		if autoRestart {
			debug.Log("daemon", "auto-restart: treating quick exit of %s as first crash cycle", processID)
			return proc, nil
		}
		// Process crashed during startup — still fire on-crash hook. proc.Done()
		// is already closed so the goroutine proceeds immediately.
		d.watchProcessExit(proc)
		return nil, startupErr
	}

	debug.Info("daemon", "Detected EADDRINUSE on port %d for %s, attempting recovery", startupErr.Port, processID)
	d.recordStartupEntry(processID, "", "warning", "eaddrinuse_detected",
		fmt.Sprintf("EADDRINUSE on port %d, attempting recovery", startupErr.Port), startupErr.Port)

	// Record EADDRINUSE retry in ScriptEntry
	if scriptEntry, ok := d.scriptRegistry.GetByProcessID(processID); ok {
		scriptEntry.AddRestartMarker()
	}

	// Stop the failed process. Best-effort: the process already failed to bind
	// (EADDRINUSE) and is being torn down anyway, so a StopProcess error (e.g.
	// already exited) is non-fatal — but log it so a stuck teardown is visible.
	if err := d.hub.ProcessManager().StopProcess(ctx, proc); err != nil {
		debug.Log("startup-resilience", "stop of EADDRINUSE process %s failed during recovery: %v", processID, err)
	}
	d.retireIncidentProcessOwner(proc.ID)
	d.hub.ProcessManager().RemoveByPath(processID, projectPath)

	// Clean up the port
	portToClean := startupErr.Port
	if portToClean == 0 && expectedPort > 0 {
		portToClean = expectedPort
	}

	if portToClean > 0 {
		killedPIDs, err := d.preflightPortCleanup(ctx, portToClean)
		if err != nil {
			return nil, &StartupError{
				ProcessID: processID,
				Port:      portToClean,
				ErrorType: "cleanup_failed",
				Message:   fmt.Sprintf("EADDRINUSE recovery failed: could not cleanup port %d: %v", portToClean, err),
				Retried:   true,
			}
		}
		if len(killedPIDs) == 0 {
			// No process found — port may be in TIME_WAIT or held by the kernel.
			// Wait for it to free naturally before retrying.
			debug.Log("daemon", "Port %d in use but no process found — waiting for release", portToClean)
			d.waitForPortFree(portToClean, 10*time.Second)
		}
		debug.Info("daemon", "Killed %d process(es) on port %d, retrying startup", len(killedPIDs), portToClean)
		d.recordStartupEntry(processID, "", "info", "eaddrinuse_killed",
			fmt.Sprintf("killed %d process(es) on port %d for EADDRINUSE recovery", len(killedPIDs), portToClean), portToClean)
	}

	// Retry: Start the process again
	result, err = d.hub.ProcessManager().StartOrReuse(ctx, process.ProcessConfig{
		ID:          processID,
		ProjectPath: projectPath,
		WorkingDir:  workingDir,
		Command:     command,
		Args:        args,
		Env:         env,
	})
	if err != nil {
		return nil, &StartupError{
			ProcessID: processID,
			Port:      portToClean,
			ErrorType: "retry_failed",
			Message:   fmt.Sprintf("retry after EADDRINUSE failed: %v", err),
			Retried:   true,
		}
	}

	proc = result.Process

	// Monitor the retry
	retryErr := d.monitorStartupFailure(ctx, proc, expectedPort, monitorTimeout)
	if retryErr != nil {
		retryErr.Retried = true
		return nil, retryErr
	}

	debug.Info("daemon", "Successfully recovered from EADDRINUSE for %s", processID)
	d.recordStartupEntry(processID, "", "info", "eaddrinuse_recovered",
		"successfully recovered from EADDRINUSE", 0)
	d.watchProcessExit(proc)
	return proc, nil
}

// monitorStartupFailure watches process output for early startup failures.
// Returns nil if the process starts successfully, or a StartupError if it fails.
func (d *Daemon) monitorStartupFailure(
	ctx context.Context,
	proc *process.ManagedProcess,
	expectedPort int,
	timeout time.Duration,
) *StartupError {

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil // Context cancelled, not a startup error

		case <-ticker.C:
			// Check if process is still running
			state := proc.State()
			if state == process.StateFailed || state == process.StateStopped {
				// Process died during startup - check output for cause
				stdout, _ := proc.Stdout()
				stderr, _ := proc.Stderr()
				combined := string(stdout) + "\n" + string(stderr)

				// Check for EADDRINUSE
				if port := detectEADDRINUSE(combined); port > 0 {
					return &StartupError{
						ProcessID: proc.ID,
						Port:      port,
						ErrorType: "EADDRINUSE",
						Message:   fmt.Sprintf("port %d already in use", port),
					}
				}

				// Generic startup failure
				exitCode := proc.ExitCode()
				return &StartupError{
					ProcessID: proc.ID,
					Port:      expectedPort,
					ErrorType: "startup_failed",
					Message:   fmt.Sprintf("process exited with code %d during startup", exitCode),
					Output:    lastLines(combined, 15),
				}
			}

			// Check output even while running (some frameworks log errors but stay alive briefly)
			stderr, _ := proc.Stderr()
			if port := detectEADDRINUSE(string(stderr)); port > 0 {
				return &StartupError{
					ProcessID: proc.ID,
					Port:      port,
					ErrorType: "EADDRINUSE",
					Message:   fmt.Sprintf("port %d already in use", port),
				}
			}

			// Timeout check
			if time.Now().After(deadline) {
				// Process survived startup period - assume success
				return nil
			}
		}
	}
}

// getExpectedPortsForScript determines the ports a script uses.
// Checks: explicit config ports, linked proxy, command args, package.json.
func (d *Daemon) getExpectedPortsForScript(
	scriptName string,
	script *config.ScriptConfig,
	proxyConfigs map[string]*config.ProxyConfig,
	projectPath string,
	command string,
	args []string,
) []int {
	seen := make(map[int]bool)
	var ports []int
	add := func(port int) {
		if port > 0 && !seen[port] {
			seen[port] = true
			ports = append(ports, port)
		}
	}

	// Explicit ports from config (most reliable)
	for _, p := range script.Ports {
		add(p)
	}

	// Linked proxy port
	for _, proxyConfig := range proxyConfigs {
		if proxyConfig.Script == scriptName {
			add(extractPortFromProxyConfig(proxyConfig))
		}
	}

	// Extract from command line arguments
	add(extractPortFromCommand(command, args))

	// For Node.js projects, check the package.json script content
	if scriptCmd := project.GetScriptCommand(projectPath, scriptName); scriptCmd != "" {
		add(extractPortFromCommand(scriptCmd, nil))
	}

	return ports
}

// lastLines returns the last n non-empty lines from text.
func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	// Filter empty lines
	var nonEmpty []string
	for _, l := range lines {
		if trimmed := strings.TrimSpace(l); trimmed != "" {
			nonEmpty = append(nonEmpty, trimmed)
		}
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	if len(nonEmpty) > n {
		nonEmpty = nonEmpty[len(nonEmpty)-n:]
	}
	return strings.Join(nonEmpty, "\n")
}
