package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/go-cli-server/script"
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
// Auto-restart is disabled by default — users restart manually from the overlay
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

// RestartEvent captures information about a process restart for output display.
type RestartEvent struct {
	Timestamp  time.Time     // When the restart occurred
	ExitCode   int           // Exit code of the failed process
	Runtime    time.Duration // How long the process ran before failing
	LastOutput string        // Last N lines of output before crash
}

// maxRestartEvents is the maximum number of restart events to retain per process.
const maxRestartEvents = 10

// maxLastOutputLines is the number of output lines to capture from the failed process.
const maxLastOutputLines = 20

// processRestartState tracks restart history for rate limiting.
type processRestartState struct {
	config           AutoRestartConfig
	command          string
	args             []string
	env              []string
	expectedPorts    []int          // Ports to clean up before restart (preflight cleanup)
	projectPath      string         // Root project path (for session association)
	workingDir       string         // Working directory for the process (may differ from projectPath)
	restarts         []time.Time    // Timestamps of recent restarts
	restartEvents    []RestartEvent // History of restart events for output display
	consecutiveFails int            // Consecutive rapid failures for backoff calculation
	mu               sync.Mutex

	// monitorCtx/monitorCancel bound the monitor goroutine spawned for this
	// registration. Re-registering or unregistering a process cancels the old
	// monitor so it cannot race the new one through a duplicate restart.
	monitorCtx    context.Context
	monitorCancel context.CancelFunc

	// Atomic stats for lock-free reads from Stats().
	// These mirror data in the mutex-protected fields above but allow
	// Stats() to avoid acquiring per-process locks.
	restartCount    atomic.Int64
	lastRestartTime atomic.Pointer[time.Time]
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
	now := time.Now()
	s.mu.Lock()
	s.restarts = append(s.restarts, now)
	s.mu.Unlock()
	s.restartCount.Add(1)
	s.lastRestartTime.Store(&now)
}

// addRestartEvent records a restart event with debug info from the failed process.
func (s *processRestartState) addRestartEvent(event RestartEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restartEvents = append(s.restartEvents, event)
	if len(s.restartEvents) > maxRestartEvents {
		s.restartEvents = s.restartEvents[len(s.restartEvents)-maxRestartEvents:]
	}
}

// getRestartEvents returns a copy of all restart events.
func (s *processRestartState) getRestartEvents() []RestartEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.restartEvents) == 0 {
		return nil
	}
	events := make([]RestartEvent, len(s.restartEvents))
	copy(events, s.restartEvents)
	return events
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

	// shutdownMu/shutdown guard wg.Add against Shutdown's wg.Wait, mirroring
	// Daemon.goTracked: an Add racing Wait is a WaitGroup misuse panic.
	shutdownMu sync.Mutex
	shutdown   bool
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
// Re-registering replaces the previous registration and cancels its monitor,
// so exactly one monitor goroutine is active per process at any time.
func (r *ProcessAutoRestarter) Register(processID string, config AutoRestartConfig, command string, args []string, env []string, expectedPorts []int, projectPath, workingDir string) {
	monitorCtx, monitorCancel := context.WithCancel(r.ctx)
	state := &processRestartState{
		config:        config,
		command:       command,
		args:          args,
		env:           env,
		expectedPorts: expectedPorts,
		projectPath:   projectPath,
		workingDir:    workingDir,
		monitorCtx:    monitorCtx,
		monitorCancel: monitorCancel,
	}

	r.mu.Lock()
	old := r.processes[processID]
	r.processes[processID] = state
	r.mu.Unlock()
	if old != nil {
		old.monitorCancel()
	}

	// Acquire semaphore outside the lock to prevent deadlock: monitor goroutines
	// need r.mu.RLock, so we cannot hold the write lock while blocking on Acquire.
	if err := r.monitorSem.Acquire(r.ctx, 1); err != nil {
		monitorCancel()
		debug.Warn("daemon", "Cannot start monitor for %s: context cancelled", processID)
		return
	}

	// The Add must be atomic with the shutdown check: Shutdown sets shutdown
	// under shutdownMu before wg.Wait(), so taking the same lock means Add(1)
	// either strictly precedes Wait or is skipped.
	r.shutdownMu.Lock()
	if r.shutdown {
		r.shutdownMu.Unlock()
		monitorCancel()
		r.monitorSem.Release(1)
		return
	}
	r.wg.Add(1)
	r.shutdownMu.Unlock()

	go func() {
		defer r.monitorSem.Release(1)
		r.monitorProcess(processID, state)
	}()
}

// Unregister disables auto-restart for a process.
func (r *ProcessAutoRestarter) Unregister(processID string) {
	r.mu.Lock()
	old := r.processes[processID]
	delete(r.processes, processID)
	r.mu.Unlock()
	if old != nil {
		old.monitorCancel()
	}
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

// GetRestartEvents returns restart event history for a process.
// Returns nil if the process is not registered or has no restart events.
func (r *ProcessAutoRestarter) GetRestartEvents(processID string) []RestartEvent {
	r.mu.RLock()
	state, exists := r.processes[processID]
	r.mu.RUnlock()
	if !exists {
		return nil
	}
	return state.getRestartEvents()
}

// FormatRestartDelimiter formats restart events as a delimiter string
// that can be prepended to process output.
func FormatRestartDelimiter(events []RestartEvent) string {
	if len(events) == 0 {
		return ""
	}
	var b strings.Builder
	for i, event := range events {
		b.WriteString("═══════════════════════════════════════════════\n")
		b.WriteString(fmt.Sprintf(" PROCESS RESTARTED (exit code: %d) at %s\n",
			event.ExitCode, event.Timestamp.Format("15:04:05")))
		if event.Runtime > 0 {
			b.WriteString(fmt.Sprintf(" Previous run: %s\n", formatRestartRuntime(event.Runtime)))
		}
		b.WriteString("═══════════════════════════════════════════════\n")
		if event.LastOutput != "" {
			b.WriteString(" Last output before exit:\n")
			for _, line := range strings.Split(event.LastOutput, "\n") {
				b.WriteString("   ")
				b.WriteString(line)
				b.WriteString("\n")
			}
		}
		b.WriteString("───────────────────────────────────────────────\n")
		if i < len(events)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func formatRestartRuntime(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm %ds", m, s)
}

// monitorProcess watches a process and restarts it when it exits.
// It owns the registration state: on every iteration it verifies the map still
// holds myState, so a superseded or unregistered monitor exits instead of
// racing a newer monitor through a duplicate restart.
func (r *ProcessAutoRestarter) monitorProcess(processID string, myState *processRestartState) {
	defer r.wg.Done()

	pm := r.daemon.hub.ProcessManager()

	for {
		// Confirm we're still the active registration before touching anything.
		r.mu.RLock()
		current := r.processes[processID]
		r.mu.RUnlock()
		if current != myState {
			return
		}

		// Get process scoped to this registration's project path. A bare Get
		// would report ErrProcessAmbiguous when two projects share a script
		// name, which is a normal multi-project state — not a removal.
		proc, err := pm.GetByPath(processID, myState.projectPath)
		if err != nil {
			// Process doesn't exist (might have been removed). Only drop the
			// registration if it is still ours.
			r.mu.Lock()
			if r.processes[processID] == myState {
				delete(r.processes, processID)
			}
			r.mu.Unlock()
			return
		}

		// Wait for process to finish
		select {
		case <-r.ctx.Done():
			return
		case <-myState.monitorCtx.Done():
			return
		case <-proc.Done():
			// Process exited
		}

		// Check if we should restart
		r.mu.RLock()
		current = r.processes[processID]
		r.mu.RUnlock()

		if current != myState {
			// Auto-restart was disabled or re-registered (new monitor owns it)
			return
		}

		exitCode := proc.ExitCode()
		scriptName := stripProcessPrefix(processID)

		// Log process death to session log
		r.daemon.recordStartupEntry(processID, scriptName, "info", "exited",
			fmt.Sprintf("process exited with code %d (runtime: %s)", exitCode, formatRestartRuntime(proc.Runtime())), 0)

		// ScriptEntry state on process exit is now handled by ProcessManager lifecycle

		if !myState.shouldRestart(exitCode) {
			debug.Log("daemon", "Process %s exited (code %d), max restarts reached or disabled", processID, exitCode)
			r.daemon.recordStartupEntry(processID, scriptName, "warning", "restart_gave_up",
				fmt.Sprintf("auto-restart exhausted or disabled (exit code %d)", exitCode), 0)
			return
		}

		// Calculate restart delay with exponential backoff for rapid failures.
		// A process that ran < 5s is considered a rapid failure (likely port conflict).
		delay := myState.config.RestartDelay
		myState.mu.Lock()
		if proc.Runtime() < 5*time.Second {
			myState.consecutiveFails++
			// Exponential backoff: 1s, 2s, 4s, 8s, 16s (capped)
			for i := 1; i < myState.consecutiveFails && delay < 16*time.Second; i++ {
				delay *= 2
			}
			debug.Log("daemon", "Process %s failed rapidly (%v), backoff delay %v (attempt %d)", processID, proc.Runtime(), delay, myState.consecutiveFails)
			r.daemon.recordStartupEntry(processID, scriptName, "warning", "crash_loop_backoff",
				fmt.Sprintf("rapid failure (runtime %v), backoff delay %v (attempt %d)", proc.Runtime(), delay, myState.consecutiveFails), 0)
		} else {
			myState.consecutiveFails = 0 // Reset on successful run
		}
		myState.mu.Unlock()

		// Wait before restart
		select {
		case <-r.ctx.Done():
			return
		case <-myState.monitorCtx.Done():
			return
		case <-time.After(delay):
		}

		// Double-check we're still the active registration
		r.mu.RLock()
		current = r.processes[processID]
		r.mu.RUnlock()
		if current != myState {
			return
		}

		debug.Log("daemon", "Restarting process %s (exit code was %d)", processID, exitCode)

		// Mark ScriptEntry as restarting (not handled by ProcessManager lifecycle)
		if scriptEntry, ok := r.daemon.scriptRegistry.GetByProcessID(processID); ok {
			scriptEntry.SetState(script.StateRestarting)
			scriptEntry.AddRestartMarker()
		}

		// Fire on-restart hook before re-launching (blocks up to 5s).
		if cfgVal, ok := r.daemon.scriptConfigs.Load(processID); ok {
			if scriptCfg, ok := cfgVal.(*config.ScriptConfig); ok && scriptCfg.Hooks != nil && scriptCfg.Hooks.OnRestart != "" {
				restartScriptName := processID
				if entry, ok := r.daemon.scriptRegistry.GetByProcessID(processID); ok {
					restartScriptName = entry.Name
				}
				if err := RunLifecycleHook(scriptCfg.Hooks.OnRestart, restartScriptName, "restart", scriptCfg, exitCode); err != nil {
					debug.Warn("daemon", "on-restart hook for %s: %v", restartScriptName, err)
				}
			}
		}

		// Capture restart event from the dying process before removing it
		stdout, _ := proc.Stdout()
		stderr, _ := proc.Stderr()
		combined := string(stdout) + "\n" + string(stderr)
		myState.addRestartEvent(RestartEvent{
			Timestamp:  time.Now(),
			ExitCode:   exitCode,
			Runtime:    proc.Runtime(),
			LastOutput: lastLines(combined, maxLastOutputLines),
		})

		// Clear URL tracker state so new process output is scanned fresh
		r.daemon.urlTracker.ClearProcess(processID)

		// Remove the dead process, but only if the registry still holds this
		// instance: a manual restart between Done() and here would have
		// registered a live replacement we must not delete (mirrors the
		// instance-identity check in process_exit_watcher.go).
		if cur, getErr := pm.GetByPath(processID, myState.projectPath); getErr == nil && cur == proc {
			pm.RemoveByPath(processID, myState.projectPath)
		} else if getErr == nil {
			// Registry holds a different (live) instance — watch that instead.
			continue
		}
		// getErr != nil: already removed by someone else; registry is clear.

		// Restart with EADDRINUSE recovery (use startScriptWithRetry instead of StartScript
		// to avoid re-registering for auto-restart, since we're already monitoring).
		// No timeout — startup may take a long time (e.g., dotnet restore + compile).
		proc, startupErr := r.daemon.startScriptWithRetry(r.ctx, processID, myState.projectPath, myState.workingDir, myState.command, myState.args, myState.env, myState.expectedPorts, false)

		if startupErr != nil {
			debug.Error("daemon", "Failed to restart process %s: %v", processID, startupErr)

			// Log restart failure to session log
			r.daemon.startupErrorStore.Add(&StartupLogEntry{
				ProcessID:  processID,
				ScriptName: scriptName,
				Level:      "error",
				EventType:  "restart_failed",
				Message:    fmt.Sprintf("restart failed: %s", startupErr.Message),
				Output:     startupErr.Output,
				Port:       startupErr.Port,
				Timestamp:  time.Now(),
			})
			// Update ScriptEntry to failed state (process never started, so lifecycle won't handle this)
			if scriptEntry, ok := r.daemon.scriptRegistry.GetByProcessID(processID); ok {
				scriptEntry.SetState(script.StateFailed)
				scriptEntry.SetLastError(startupErr.Error())
				scriptEntry.IncrementFailCount()
			}
			return
		}

		myState.recordRestart()
		debug.Log("daemon", "Process %s restarted (new PID: %d)", processID, proc.PID())

		// Log restart success to session log
		r.daemon.recordStartupEntry(processID, scriptName, "info", "restarted",
			fmt.Sprintf("process restarted with new PID %d", proc.PID()), 0)

		// ScriptEntry running state is now set by ProcessManager lifecycle on process start

		// Flush stale connections on proxies linked to this process
		r.daemon.FlushScriptProxyConnections(processID)
	}
}

// Shutdown stops all monitoring goroutines. The provided context bounds how long
// Shutdown will wait for monitor goroutines to finish. If ctx expires, Shutdown
// returns immediately (goroutines are still cancelled via r.cancel but may not
// have exited yet).
func (r *ProcessAutoRestarter) Shutdown(ctx context.Context) {
	r.shutdownMu.Lock()
	r.shutdown = true
	r.shutdownMu.Unlock()
	r.cancel()

	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Stats returns auto-restart statistics.
// Uses atomic fields for restart_count and last_restart to avoid acquiring
// per-process locks, eliminating the nested lock pattern (r.mu RLock + state.mu Lock).
func (r *ProcessAutoRestarter) Stats() map[string]interface{} {
	r.mu.RLock()
	defer r.mu.RUnlock()

	stats := make(map[string]interface{})
	for id, state := range r.processes {
		entry := map[string]interface{}{
			"enabled":       state.config.Enabled,
			"max_restarts":  state.config.MaxRestarts,
			"restart_count": state.restartCount.Load(),
			"only_on_error": state.config.OnlyOnError,
		}
		if t := state.lastRestartTime.Load(); t != nil {
			entry["last_restart"] = t.Format(time.RFC3339)
		}
		stats[id] = entry
	}
	return stats
}
