package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"golang.org/x/sync/semaphore"
)

// AutoRestartConfig holds auto-restart settings for a process.
type AutoRestartConfig struct {
	Enabled       bool          // Whether auto-restart is enabled
	MaxRestarts   int           // Max restarts within window (0 = unlimited)
	RestartWindow time.Duration // Time window for restart limit (default 1 minute)
	RestartDelay  time.Duration // Delay before restart (default 1 second)
	OnlyOnError   bool          // Only restart if exit code != 0
}

// DefaultAutoRestartConfig returns sensible defaults.
func DefaultAutoRestartConfig() AutoRestartConfig {
	return AutoRestartConfig{
		Enabled:       true,
		MaxRestarts:   5,
		RestartWindow: time.Minute,
		RestartDelay:  time.Second,
		OnlyOnError:   false, // Restart on any exit (dev servers often exit cleanly)
	}
}

// processRestartState tracks restart history for rate limiting.
type processRestartState struct {
	config      AutoRestartConfig
	command     string
	args        []string
	projectPath string      // Root project path (for session association)
	workingDir  string      // Working directory for the process (may differ from projectPath)
	restarts    []time.Time // Timestamps of recent restarts
	mu          sync.Mutex
}

// shouldRestart checks if a restart is allowed based on rate limits.
func (s *processRestartState) shouldRestart(exitCode int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.config.Enabled {
		return false
	}

	// Check exit code condition
	if s.config.OnlyOnError && exitCode == 0 {
		return false
	}

	// Check rate limit
	if s.config.MaxRestarts > 0 {
		// Remove stale entries outside the window
		cutoff := time.Now().Add(-s.config.RestartWindow)
		var recent []time.Time
		for _, t := range s.restarts {
			if t.After(cutoff) {
				recent = append(recent, t)
			}
		}
		s.restarts = recent

		if len(s.restarts) >= s.config.MaxRestarts {
			return false
		}
	}

	return true
}

// recordRestart records a restart timestamp.
func (s *processRestartState) recordRestart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restarts = append(s.restarts, time.Now())
}

// maxConcurrentMonitors is the upper bound on simultaneous monitor goroutines.
const maxConcurrentMonitors = 50

// ProcessAutoRestarter manages auto-restart for processes.
type ProcessAutoRestarter struct {
	daemon     *Daemon
	processes  map[string]*processRestartState // processID -> state
	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	monitorSem *semaphore.Weighted // bounds concurrent monitor goroutines
}

// NewProcessAutoRestarter creates a new auto-restarter.
func NewProcessAutoRestarter(d *Daemon) *ProcessAutoRestarter {
	ctx, cancel := context.WithCancel(context.Background())
	return &ProcessAutoRestarter{
		daemon:     d,
		processes:  make(map[string]*processRestartState),
		ctx:        ctx,
		cancel:     cancel,
		monitorSem: semaphore.NewWeighted(maxConcurrentMonitors),
	}
}

// Register enables auto-restart for a process.
func (r *ProcessAutoRestarter) Register(processID string, config AutoRestartConfig, command string, args []string, projectPath, workingDir string) {
	r.mu.Lock()
	r.processes[processID] = &processRestartState{
		config:      config,
		command:     command,
		args:        args,
		projectPath: projectPath,
		workingDir:  workingDir,
	}
	r.mu.Unlock()

	// Acquire semaphore outside the lock to prevent deadlock: monitor goroutines
	// need r.mu.RLock, so we cannot hold the write lock while blocking on Acquire.
	if err := r.monitorSem.Acquire(r.ctx, 1); err != nil {
		debug.Warn("daemon", "Cannot start monitor for %s: context cancelled", processID)
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.monitorSem.Release(1)
		r.monitorProcess(processID)
	}()
}

// Unregister disables auto-restart for a process.
func (r *ProcessAutoRestarter) Unregister(processID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.processes, processID)
}

// IsRegistered checks if a process has auto-restart enabled.
func (r *ProcessAutoRestarter) IsRegistered(processID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.processes[processID]
	return exists
}

// GetConfig returns the auto-restart config for a process.
func (r *ProcessAutoRestarter) GetConfig(processID string) (AutoRestartConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, exists := r.processes[processID]
	if !exists {
		return AutoRestartConfig{}, false
	}
	return state.config, true
}

// monitorProcess watches a process and restarts it when it exits.
func (r *ProcessAutoRestarter) monitorProcess(processID string) {
	defer r.wg.Done()

	for {
		// Get process
		proc, err := r.daemon.hub.ProcessManager().Get(processID)
		if err != nil {
			// Process doesn't exist (might have been removed)
			r.mu.Lock()
			delete(r.processes, processID)
			r.mu.Unlock()
			return
		}

		// Wait for process to finish
		select {
		case <-r.ctx.Done():
			return
		case <-proc.Done():
			// Process exited
		}

		// Check if we should restart
		r.mu.RLock()
		state, exists := r.processes[processID]
		r.mu.RUnlock()

		if !exists {
			// Auto-restart was disabled
			return
		}

		exitCode := proc.ExitCode()
		if !state.shouldRestart(exitCode) {
			debug.Log("daemon", "Process %s exited (code %d), max restarts reached or disabled", processID, exitCode)
			return
		}

		// Wait before restart
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(state.config.RestartDelay):
		}

		// Double-check we're still registered
		r.mu.RLock()
		state, exists = r.processes[processID]
		r.mu.RUnlock()
		if !exists {
			return
		}

		debug.Log("daemon", "Restarting process %s (exit code was %d)", processID, exitCode)

		// Clear URL tracker state so new process output is scanned fresh
		r.daemon.urlTracker.ClearProcess(processID)

		// Remove old process
		r.daemon.hub.ProcessManager().RemoveByPath(processID, state.projectPath)

		// Restart with EADDRINUSE recovery (use startScriptWithRetry instead of StartScript
		// to avoid re-registering for auto-restart, since we're already monitoring)
		ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
		proc, startupErr := r.daemon.startScriptWithRetry(ctx, processID, state.projectPath, state.workingDir, state.command, state.args, nil, 0)
		cancel()

		if startupErr != nil {
			debug.Error("daemon", "Failed to restart process %s: %v", processID, startupErr)
			return
		}

		state.recordRestart()
		debug.Log("daemon", "Process %s restarted (new PID: %d)", processID, proc.PID())
	}
}

// Shutdown stops all monitoring goroutines.
func (r *ProcessAutoRestarter) Shutdown() {
	r.cancel()
	r.wg.Wait()
}

// Stats returns auto-restart statistics.
func (r *ProcessAutoRestarter) Stats() map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stats := make(map[string]interface{})
	for id, state := range r.processes {
		state.mu.Lock()
		stats[id] = map[string]interface{}{
			"enabled":       state.config.Enabled,
			"max_restarts":  state.config.MaxRestarts,
			"restart_count": len(state.restarts),
			"only_on_error": state.config.OnlyOnError,
		}
		state.mu.Unlock()
	}
	return stats
}
