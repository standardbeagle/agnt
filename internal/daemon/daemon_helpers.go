package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/go-cli-server/script"
)

func (d *Daemon) LoadURLMatchersForProcess(processID string) {
	// Get process to retrieve its project path
	proc, err := d.hub.ProcessManager().Get(processID)
	if err != nil {
		debug.Log("daemon", "LoadURLMatchersForProcess: process %s not found", processID)
		return
	}

	projectPath := proc.ProjectPath
	if projectPath == "" {
		debug.Log("daemon", "LoadURLMatchersForProcess: process %s has no project path", processID)
		return
	}

	// Parse process ID to extract script name (second part after colon)
	parts := strings.SplitN(processID, ":", 2)
	if len(parts) < 2 {
		return // Invalid process ID format
	}
	scriptName := parts[1]

	// Load agnt config
	agntConfig, err := config.LoadAgntConfig(projectPath)
	if err != nil {
		debug.Log("daemon", "LoadURLMatchersForProcess: failed to load config from %s: %v", projectPath, err)
		return // No config or error - skip URL matchers
	}

	// Find script config
	script, ok := agntConfig.Scripts[scriptName]
	if !ok || script == nil {
		debug.Log("daemon", "LoadURLMatchersForProcess: script %s not found in config", scriptName)
		return // Script not found in config
	}

	// Set URL matchers if specified
	if len(script.URLMatchers) > 0 {
		d.urlTracker.SetURLMatchers(processID, script.URLMatchers)
		debug.Log("daemon", "Set URL matchers for %s: %v", processID, script.URLMatchers)
	}
}

// StopAllResources stops all processes, proxies, and tunnels without shutting down the daemon.
// Unlike Shutdown, this does NOT prevent new resources from being created afterward.

func makeProcessID(projectPath, name string) string {
	return script.MakeProcessID(projectPath, name)
}

// stripProcessPrefix extracts the script name from a process ID.
// Process IDs use the format "project-hash:name"; this returns just "name".
func stripProcessPrefix(processID string) string {
	if idx := strings.Index(processID, ":"); idx >= 0 {
		return processID[idx+1:]
	}
	return processID
}

// mapKeys extracts keys from a script config map for logging.
func mapKeys(m map[string]*config.ScriptConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// mapKeysProxy extracts keys from a proxy config map for logging.
func mapKeysProxy(m map[string]*config.ProxyConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// resolveWorkingDir resolves the working directory for a script.
// If cwd is empty, returns projectPath.
// If cwd is absolute, returns it directly.
// If cwd is relative, joins it with projectPath and cleans the result.
func resolveWorkingDir(projectPath, cwd string) string {
	if cwd == "" {
		return projectPath
	}
	if filepath.IsAbs(cwd) {
		return cwd
	}
	return filepath.Clean(filepath.Join(projectPath, cwd))
}

// envMapToSlice converts a map of environment variables to a slice of "KEY=VALUE" strings.
func envMapToSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

// scriptConfigToEntry converts agnt's ScriptConfig to go-cli-server's script.Config.
func scriptConfigToEntry(cfg *config.ScriptConfig) *script.Config {
	return &script.Config{
		Run:     cfg.Run,
		Command: cfg.Command,
		Args:    cfg.Args,
		Env:     cfg.Env,
	}
}

// registerScriptEntry registers a script, replacing an existing entry whose
// config differs. Register rejects a re-register under a changed config, so
// editing a script's `run` in .agnt.kdl would otherwise leave the stale entry
// in place and fail every subsequent start of that script — `.agnt.kdl` is
// reloaded on every session connect and live reconcile, so a changed config is
// expected, not a conflict.
func (d *Daemon) registerScriptEntry(name, projectPath string, cfg *script.Config) (*script.Entry, error) {
	entry, replaced, err := d.scriptRegistry.Upsert(name, projectPath, cfg)
	if err != nil {
		return nil, err
	}
	if replaced {
		debug.Log("daemon", "script %s: config changed, registry entry replaced", name)
	}
	return entry, nil
}

func GetLogPath() string {
	// Check XDG_STATE_HOME first
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		// Fall back to ~/.local/state
		home, err := os.UserHomeDir()
		if err != nil {
			// Last resort: OS temp directory
			return filepath.Join(os.TempDir(), "agnt-daemon.log")
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "agnt", "daemon.log")
}

// rotateLogIfNeeded rotates the log file if it exceeds MaxLogSize.
func rotateLogIfNeeded(logPath string) {
	info, err := os.Stat(logPath)
	if err != nil {
		return // File doesn't exist, nothing to rotate
	}

	if info.Size() < MaxLogSize {
		return // Below threshold
	}

	// Rotate: shift existing backups
	for i := MaxLogBackups - 1; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", logPath, i)
		newPath := fmt.Sprintf("%s.%d", logPath, i+1)
		os.Rename(oldPath, newPath) // Ignore errors, files may not exist
	}

	// Move current log to .1
	os.Rename(logPath, logPath+".1")
}

// setupDebugLogging configures file-based logging for the daemon.
// This allows debugging even when the daemon runs detached (auto-started).
// Log files are rotated when they exceed MaxLogSize.
func setupDebugLogging() {
	logPath := GetLogPath()

	// Create log directory if needed
	logDir := filepath.Dir(logPath)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		// Can't create dir, continue with default stderr logging
		return
	}

	// Rotate if needed before opening
	rotateLogIfNeeded(logPath)

	// Open log file (append mode, create if not exists)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		// Can't open log file, continue with default stderr logging
		return
	}

	// Configure log to write to file
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	log.Printf("[INFO] Daemon log started at %s", logPath)
}
