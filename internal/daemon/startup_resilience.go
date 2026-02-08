// Package daemon provides the background daemon service.
package daemon

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/project"
	"github.com/standardbeagle/go-cli-server/process"
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
	// ExpectedPort is the port the process is expected to use (for EADDRINUSE recovery)
	ExpectedPort int
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
	// Default ProjectPath to WorkingDir if not set
	projectPath := cfg.ProjectPath
	if projectPath == "" {
		projectPath = cfg.WorkingDir
	}

	// Set URL matchers BEFORE starting the process to ensure they're available
	// when the URL tracker first scans the process output
	if len(cfg.URLMatchers) > 0 {
		d.urlTracker.SetURLMatchers(cfg.ProcessID, cfg.URLMatchers)
		debug.Log("daemon", "Pre-set URL matchers for %s: %v", cfg.ProcessID, cfg.URLMatchers)
	}

	// Start with automatic EADDRINUSE recovery
	proc, startupErr := d.startScriptWithRetry(ctx, cfg.ProcessID, projectPath, cfg.WorkingDir, cfg.Command, cfg.Args, cfg.Env, cfg.ExpectedPort)
	if startupErr != nil {
		// Clean up pre-set matchers on failure
		d.urlTracker.SetURLMatchers(cfg.ProcessID, nil)
		return nil, startupErr
	}

	// Register for auto-restart if enabled
	if cfg.AutoRestart && d.autoRestarter != nil {
		restartConfig := DefaultAutoRestartConfig()
		if cfg.AutoRestartConfig != nil {
			restartConfig = *cfg.AutoRestartConfig
		}
		d.autoRestarter.Register(cfg.ProcessID, restartConfig, cfg.Command, cfg.Args, cfg.ProjectPath, cfg.WorkingDir)
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

// eaddrinusePatterns matches EADDRINUSE error messages
var eaddrinusePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)EADDRINUSE.*:(\d+)`),
	regexp.MustCompile(`(?i)address already in use.*:(\d+)`),
	regexp.MustCompile(`(?i)listen.*EADDRINUSE`),
	regexp.MustCompile(`(?i)port (\d+).*already in use`),
	regexp.MustCompile(`(?i)address.*:(\d+).*in use`),
}

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

// preflightPortCleanup cleans up any process using the specified port.
// Only kills processes that are NOT managed by this daemon.
func (d *Daemon) preflightPortCleanup(ctx context.Context, port int) ([]int, error) {
	if port <= 0 {
		return nil, nil
	}

	debug.Log("daemon", "Pre-flight cleanup: checking port %d", port)

	// Use the process manager's KillProcessByPort which handles managed process detection
	killedPIDs, err := d.hub.ProcessManager().KillProcessByPort(ctx, port)
	if err != nil {
		return nil, fmt.Errorf("failed to cleanup port %d: %w", port, err)
	}

	if len(killedPIDs) > 0 {
		debug.Info("daemon", "Pre-flight cleanup: killed %d process(es) on port %d: %v", len(killedPIDs), port, killedPIDs)
		// Give processes time to fully terminate
		time.Sleep(200 * time.Millisecond)
	}

	return killedPIDs, nil
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
	expectedPort int,
) (*process.ManagedProcess, *StartupError) {

	// First attempt: Pre-flight cleanup if we know the port
	if expectedPort > 0 {
		if killedPIDs, err := d.preflightPortCleanup(ctx, expectedPort); err != nil {
			debug.Warn("daemon", "Pre-flight cleanup failed for port %d: %v", expectedPort, err)
		} else if len(killedPIDs) > 0 {
			debug.Info("daemon", "Cleaned up port %d before starting %s", expectedPort, processID)
		}
	}

	// Start the process
	// ProjectPath is the root project (for session association and proxy events)
	// WorkingDir is the actual cwd for the process
	result, err := d.hub.ProcessManager().StartOrReuse(ctx, process.ProcessConfig{
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
			ErrorType: "start_failed",
			Message:   fmt.Sprintf("failed to start process: %v", err),
		}
	}

	proc := result.Process

	// Monitor for early failure (first 3 seconds)
	startupErr := d.monitorStartupFailure(ctx, proc, expectedPort, 3*time.Second)
	if startupErr == nil {
		return proc, nil
	}

	// Startup failed - check if it's EADDRINUSE
	if startupErr.ErrorType != "EADDRINUSE" {
		return nil, startupErr
	}

	debug.Info("daemon", "Detected EADDRINUSE on port %d for %s, attempting recovery", startupErr.Port, processID)

	// Stop the failed process
	_ = d.hub.ProcessManager().StopProcess(ctx, proc)
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
			return nil, &StartupError{
				ProcessID: processID,
				Port:      portToClean,
				ErrorType: "port_in_use",
				Message:   fmt.Sprintf("port %d in use but no process found to kill", portToClean),
				Retried:   true,
			}
		}
		debug.Info("daemon", "Killed %d process(es) on port %d, retrying startup", len(killedPIDs), portToClean)
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
	retryErr := d.monitorStartupFailure(ctx, proc, expectedPort, 3*time.Second)
	if retryErr != nil {
		retryErr.Retried = true
		return nil, retryErr
	}

	debug.Info("daemon", "Successfully recovered from EADDRINUSE for %s", processID)
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

// getExpectedPortForScript determines the expected port for a script.
// It checks the proxy config first, then the command/args, then package.json scripts.
func (d *Daemon) getExpectedPortForScript(
	scriptName string,
	script *config.ScriptConfig,
	proxyConfigs map[string]*config.ProxyConfig,
	projectPath string,
	command string,
	args []string,
) int {

	// Check if there's a linked proxy with a port
	for _, proxyConfig := range proxyConfigs {
		if proxyConfig.Script == scriptName {
			if port := extractPortFromProxyConfig(proxyConfig); port > 0 {
				return port
			}
		}
	}

	// Extract from command line arguments
	if port := extractPortFromCommand(command, args); port > 0 {
		return port
	}

	// For Node.js projects, check the package.json script content
	if scriptCmd := project.GetScriptCommand(projectPath, scriptName); scriptCmd != "" {
		if port := extractPortFromCommand(scriptCmd, nil); port > 0 {
			return port
		}
	}

	return 0
}
