// Package startup provides process startup resilience: EADDRINUSE recovery,
// pre-flight port cleanup, URL matcher setup, and auto-restart registration.
package startup

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
	"github.com/standardbeagle/agnt/internal/startuplog"
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
	// AutoRestartConfig is optional custom restart configuration.
	// If nil, default config is used.
	AutoRestartConfig *AutoRestartConfig
	// StripProcessPrefix extracts the script name from a process ID.
	// If nil, the process ID is used as-is.
	StripProcessPrefix func(processID string) string
}

// StartScriptResult holds the result of starting a script.
type StartScriptResult struct {
	Process *process.ManagedProcess
	Reused  bool // True if an existing process was reused
}

// StartupError represents a startup failure with recovery information.
type StartupError struct {
	ProcessID string
	Port      int
	ErrorType string // "EADDRINUSE", "start_failed", "cleanup_failed", "retry_failed", "startup_failed"
	Message   string
	Output    string // Last lines of process output (for user-facing diagnostics)
	Retried   bool
}

func (e *StartupError) Error() string {
	return e.Message
}

// AutoRestartConfig holds auto-restart settings for a process.
type AutoRestartConfig struct {
	Enabled       bool          // Whether auto-restart is enabled
	MaxRestarts   int           // Max restarts within window (0 = unlimited)
	RestartWindow time.Duration // Time window for restart limit (default 1 minute)
	RestartDelay  time.Duration // Delay before restart (default 1 second)
	OnlyOnError   bool          // Only restart if exit code != 0
}

// DefaultAutoRestartConfig returns sensible defaults.
// Auto-restart is disabled by default -- users restart manually from the overlay
// or via MCP tools. Enable explicitly in .agnt.kdl with `auto-restart true`.
func DefaultAutoRestartConfig() AutoRestartConfig {
	return AutoRestartConfig{
		Enabled:       false,
		MaxRestarts:   5,
		RestartWindow: time.Minute,
		RestartDelay:  2 * time.Second,
		OnlyOnError:   false,
	}
}

// ProcessManagerView exposes the process manager operations needed by startup resilience.
type ProcessManagerView interface {
	StartOrReuse(ctx context.Context, cfg process.ProcessConfig) (*process.StartOrReuseResult, error)
	StopProcess(ctx context.Context, proc *process.ManagedProcess) error
	RemoveByPath(id string, projectPath string) bool
	KillProcessByPort(ctx context.Context, port int) ([]int, error)
}

// ScriptRegistryView exposes the script registry operations needed by startup resilience.
type ScriptRegistryView interface {
	Register(name, projectPath string, cfg *script.Config) (*script.Entry, error)
	Get(name, projectPath string) (*script.Entry, bool)
	GetByProcessID(processID string) (*script.Entry, bool)
}

// URLTrackerView exposes the URL tracker operations needed by startup resilience.
type URLTrackerView interface {
	SetURLMatchers(processID string, matchers []string)
	ClearProcess(processID string)
}

// AutoRestarterView exposes the auto-restart registration needed by startup resilience.
type AutoRestarterView interface {
	Register(processID string, config AutoRestartConfig, command string, args []string, env []string, expectedPorts []int, projectPath, workingDir string)
}

// Deps provides the daemon capabilities needed by startup resilience.
type Deps interface {
	ProcessManager() ProcessManagerView
	ScriptRegistry() ScriptRegistryView
	URLTracker() URLTrackerView
	AutoRestarter() AutoRestarterView
	StartupLog() *startuplog.StartupLogStore
}

// StartScript starts a script/process with unified behavior:
//   - Pre-flight port cleanup and EADDRINUSE recovery
//   - URL matcher setup for proxy auto-creation
//   - Auto-restart registration for crash recovery
//
// This is the canonical way to start processes. Both autostartScript and hub
// handlers should use this.
func StartScript(ctx context.Context, d Deps, cfg StartScriptConfig) (*StartScriptResult, error) {
	// Default ProjectPath to WorkingDir if not set
	projectPath := cfg.ProjectPath
	if projectPath == "" {
		projectPath = cfg.WorkingDir
	}

	stripPrefix := cfg.StripProcessPrefix
	if stripPrefix == nil {
		stripPrefix = func(id string) string { return id }
	}

	// Ensure a ScriptEntry exists (idempotent). For standalone processes not
	// from .agnt.kdl, this creates the entry so the code path is uniform.
	scriptName := stripPrefix(cfg.ProcessID)
	d.ScriptRegistry().Register(scriptName, projectPath, &script.Config{
		Command: cfg.Command,
		Args:    cfg.Args,
	})

	// Set URL matchers BEFORE starting the process to ensure they're available
	// when the URL tracker first scans the process output
	if len(cfg.URLMatchers) > 0 {
		d.URLTracker().SetURLMatchers(cfg.ProcessID, cfg.URLMatchers)
		debug.Log("startup", "Pre-set URL matchers for %s: %v", cfg.ProcessID, cfg.URLMatchers)
	}

	// Start with automatic EADDRINUSE recovery
	proc, startupErr := startScriptWithRetry(ctx, d, stripPrefix, cfg.ProcessID, projectPath, cfg.WorkingDir, cfg.Command, cfg.Args, cfg.Env, cfg.ExpectedPorts)
	if startupErr != nil {
		// Clean up pre-set matchers on failure
		d.URLTracker().SetURLMatchers(cfg.ProcessID, nil)

		// Record in startup error store for later diagnosis
		d.StartupLog().Add(&startuplog.StartupLogEntry{
			ProcessID:  cfg.ProcessID,
			ScriptName: stripPrefix(cfg.ProcessID),
			Level:      "error",
			EventType:  startupErr.ErrorType,
			Message:    startupErr.Message,
			Output:     startupErr.Output,
			Port:       startupErr.Port,
			Timestamp:  time.Now(),
		})

		return nil, startupErr
	}

	// Register for auto-restart if enabled
	if cfg.AutoRestart && d.AutoRestarter() != nil {
		restartConfig := DefaultAutoRestartConfig()
		if cfg.AutoRestartConfig != nil {
			restartConfig = *cfg.AutoRestartConfig
		}
		d.AutoRestarter().Register(cfg.ProcessID, restartConfig, cfg.Command, cfg.Args, cfg.Env, cfg.ExpectedPorts, cfg.ProjectPath, cfg.WorkingDir)
		debug.Log("startup", "Registered %s for auto-restart", cfg.ProcessID)
	}

	return &StartScriptResult{
		Process: proc,
		Reused:  false, // startScriptWithRetry always creates new process
	}, nil
}

// StartScriptWithRetry starts a script with automatic EADDRINUSE recovery.
// It monitors the process output for startup failures and retries once after cleanup.
// projectPath is the root project directory (for session association).
// workingDir is the actual working directory for the process (may differ when script has cwd).
//
// This is exported for use by hub_proc.go and process_autorestart.go which call it
// directly to avoid re-registering for auto-restart.
func StartScriptWithRetry(
	ctx context.Context,
	d Deps,
	processID string,
	projectPath string,
	workingDir string,
	command string,
	args []string,
	env []string,
	expectedPorts []int,
) (*process.ManagedProcess, *StartupError) {
	return startScriptWithRetry(ctx, d, func(id string) string { return id }, processID, projectPath, workingDir, command, args, env, expectedPorts)
}

// startScriptWithRetry starts a script with automatic EADDRINUSE recovery.
func startScriptWithRetry(
	ctx context.Context,
	d Deps,
	stripPrefix func(string) string,
	processID string,
	projectPath string,
	workingDir string,
	command string,
	args []string,
	env []string,
	expectedPorts []int,
) (*process.ManagedProcess, *StartupError) {

	// Pre-flight cleanup: kill orphans on all expected ports
	for _, port := range expectedPorts {
		if killedPIDs, err := preflightPortCleanup(ctx, d, port); err != nil {
			debug.Warn("startup", "Pre-flight cleanup failed for port %d: %v", port, err)
		} else if len(killedPIDs) > 0 {
			debug.Info("startup", "Cleaned up port %d before starting %s", port, processID)
			d.StartupLog().Add(&startuplog.StartupLogEntry{
				ProcessID: processID, ScriptName: stripPrefix(processID),
				Level: "info", EventType: "port_cleanup",
				Message: fmt.Sprintf("killed %d process(es) on port %d", len(killedPIDs), port),
				Port:    port, Timestamp: time.Now(),
			})
		}
	}

	// Wire output callback to feed ScriptEntry output buffer
	var outputCB process.OutputCallback
	if entry, ok := d.ScriptRegistry().Get(stripPrefix(processID), projectPath); ok {
		outputCB = func(_ string, line string) {
			entry.AppendOutput(line)
		}
	}

	// Start the process
	result, err := d.ProcessManager().StartOrReuse(ctx, process.ProcessConfig{
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

	// Monitor for early failure (first 3 seconds)
	// Use first expected port for EADDRINUSE detection
	expectedPort := 0
	if len(expectedPorts) > 0 {
		expectedPort = expectedPorts[0]
	}
	startupErr := monitorStartupFailure(ctx, proc, expectedPort, 3*time.Second)
	if startupErr == nil {
		return proc, nil
	}

	// Startup failed - check if it's EADDRINUSE
	if startupErr.ErrorType != "EADDRINUSE" {
		return nil, startupErr
	}

	debug.Info("startup", "Detected EADDRINUSE on port %d for %s, attempting recovery", startupErr.Port, processID)

	// Record EADDRINUSE retry in ScriptEntry
	if scriptEntry, ok := d.ScriptRegistry().GetByProcessID(processID); ok {
		scriptEntry.AddRestartMarker()
	}

	// Stop the failed process
	_ = d.ProcessManager().StopProcess(ctx, proc)
	d.ProcessManager().RemoveByPath(processID, projectPath)

	// Clean up the port
	portToClean := startupErr.Port
	if portToClean == 0 && expectedPort > 0 {
		portToClean = expectedPort
	}

	if portToClean > 0 {
		killedPIDs, err := preflightPortCleanup(ctx, d, portToClean)
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
			// No process found -- port may be in TIME_WAIT or held by the kernel.
			// Wait for it to free naturally before retrying.
			debug.Log("startup", "Port %d in use but no process found -- waiting for release", portToClean)
			waitForPortFree(portToClean, 10*time.Second)
		}
		debug.Info("startup", "Killed %d process(es) on port %d, retrying startup", len(killedPIDs), portToClean)
	}

	// Retry: Start the process again
	result, err = d.ProcessManager().StartOrReuse(ctx, process.ProcessConfig{
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
	retryErr := monitorStartupFailure(ctx, proc, expectedPort, 3*time.Second)
	if retryErr != nil {
		retryErr.Retried = true
		return nil, retryErr
	}

	debug.Info("startup", "Successfully recovered from EADDRINUSE for %s", processID)
	return proc, nil
}

// monitorStartupFailure watches process output for early startup failures.
// Returns nil if the process starts successfully, or a StartupError if it fails.
func monitorStartupFailure(
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
				if port := DetectEADDRINUSE(combined); port > 0 {
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
					Output:    LastLines(combined, 15),
				}
			}

			// Check output even while running (some frameworks log errors but stay alive briefly)
			stderr, _ := proc.Stderr()
			if port := DetectEADDRINUSE(string(stderr)); port > 0 {
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

// preflightPortCleanup cleans up any process using the specified port.
// Only kills processes that are NOT managed by this daemon.
func preflightPortCleanup(ctx context.Context, d Deps, port int) ([]int, error) {
	if port <= 0 {
		return nil, nil
	}

	debug.Log("startup", "Pre-flight cleanup: checking port %d", port)

	// Use the process manager's KillProcessByPort which handles managed process detection
	killedPIDs, err := d.ProcessManager().KillProcessByPort(ctx, port)
	if err != nil {
		return nil, fmt.Errorf("failed to cleanup port %d: %w", port, err)
	}

	if len(killedPIDs) > 0 {
		debug.Info("startup", "Pre-flight cleanup: killed %d process(es) on port %d: %v", len(killedPIDs), port, killedPIDs)
		// Poll until the port is actually free (up to 5 seconds)
		waitForPortFree(port, 5*time.Second)
	}

	return killedPIDs, nil
}

// WaitForPortFree polls until the given port is free or timeout expires.
// Checks both IPv4 and IPv6 loopback since the process may bind on either.
func WaitForPortFree(port int, timeout time.Duration) {
	waitForPortFree(port, timeout)
}

func waitForPortFree(port int, timeout time.Duration) {
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
			debug.Log("startup", "Port %d is free", port)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	debug.Warn("startup", "Port %d still in use after %v", port, timeout)
}

// GetExpectedPortsForScript determines the ports a script uses.
// Checks: explicit config ports, linked proxy, command args, package.json.
func GetExpectedPortsForScript(
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
			add(ExtractPortFromProxyConfig(proxyConfig))
		}
	}

	// Extract from command line arguments
	add(ExtractPortFromCommand(command, args))

	// For Node.js projects, check the package.json script content
	if scriptCmd := project.GetScriptCommand(projectPath, scriptName); scriptCmd != "" {
		add(ExtractPortFromCommand(scriptCmd, nil))
	}

	return ports
}

// ExtractPortFromCommand extracts a port number from a command and its arguments.
func ExtractPortFromCommand(command string, args []string) int {
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

// ExtractPortFromProxyConfig gets the expected port from a proxy configuration.
func ExtractPortFromProxyConfig(proxyConfig *config.ProxyConfig) int {
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

// DetectEADDRINUSE checks process output for EADDRINUSE errors.
// Returns the port number if found, 0 otherwise.
func DetectEADDRINUSE(output string) int {
	for _, pattern := range eaddrinusePatterns {
		if matches := pattern.FindStringSubmatch(output); len(matches) > 1 {
			if port, err := strconv.Atoi(matches[1]); err == nil && port > 0 && port < 65536 {
				return port
			}
		}
		// Pattern matched but no port captured - try to find port separately
		if pattern.MatchString(output) {
			// Look for any port number in the error line
			portMatch := regexp.MustCompile(`:(\d{2,5})\b`)
			if matches := portMatch.FindStringSubmatch(output); len(matches) > 1 {
				if port, err := strconv.Atoi(matches[1]); err == nil && port > 0 && port < 65536 {
					return port
				}
			}
		}
	}
	return 0
}

// LastLines returns the last n non-empty lines from text.
func LastLines(text string, n int) string {
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
