package process

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrProcessExists is returned when trying to register a process with an existing ID.
	ErrProcessExists = errors.New("process already exists")
	// ErrProcessNotFound is returned when a process ID is not found.
	ErrProcessNotFound = errors.New("process not found")
	// ErrInvalidState is returned when an operation is invalid for the current state.
	ErrInvalidState = errors.New("invalid process state for operation")
	// ErrShuttingDown is returned when the manager is shutting down.
	ErrShuttingDown = errors.New("process manager is shutting down")
)

// ManagerConfig holds configuration for the ProcessManager.
type ManagerConfig struct {
	// DefaultTimeout is the default process timeout (0 = no timeout).
	DefaultTimeout time.Duration
	// MaxOutputBuffer is the per-stream buffer size in bytes.
	MaxOutputBuffer int
	// GracefulTimeout is how long to wait for graceful shutdown before SIGKILL.
	GracefulTimeout time.Duration
	// HealthCheckPeriod is how often to check process health (0 = disabled).
	HealthCheckPeriod time.Duration
}

// DefaultManagerConfig returns a ManagerConfig with sensible defaults.
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		DefaultTimeout:    0,                 // No timeout
		MaxOutputBuffer:   DefaultBufferSize, // 256KB
		GracefulTimeout:   5 * time.Second,
		HealthCheckPeriod: 10 * time.Second,
	}
}

// ProcessManager manages all spawned processes with lock-free access.
type ProcessManager struct {
	// processes is a lock-free map of process ID to ManagedProcess.
	processes sync.Map // map[string]*ManagedProcess

	// Atomic counters for metrics
	activeCount  atomic.Int64
	totalStarted atomic.Int64
	totalFailed  atomic.Int64

	// Configuration
	config ManagerConfig

	// Shutdown coordination
	shutdownOnce sync.Once
	shutdownChan chan struct{}
	shuttingDown atomic.Bool
	wg           sync.WaitGroup
}

// NewProcessManager creates a new ProcessManager with the given configuration.
func NewProcessManager(config ManagerConfig) *ProcessManager {
	pm := &ProcessManager{
		config:       config,
		shutdownChan: make(chan struct{}),
	}

	// Start health check loop if period is set
	if config.HealthCheckPeriod > 0 {
		pm.wg.Add(1)
		go pm.healthCheckLoop()
	}

	return pm
}

// Register adds a new process to the registry.
// Returns ErrProcessExists if a process with the same ID already exists.
func (pm *ProcessManager) Register(proc *ManagedProcess) error {
	if pm.shuttingDown.Load() {
		return ErrShuttingDown
	}

	_, loaded := pm.processes.LoadOrStore(proc.ID, proc)
	if loaded {
		return ErrProcessExists
	}

	pm.activeCount.Add(1)
	pm.totalStarted.Add(1)
	return nil
}

// Get retrieves a process by ID (lock-free read).
func (pm *ProcessManager) Get(id string) (*ManagedProcess, error) {
	val, ok := pm.processes.Load(id)
	if !ok {
		return nil, ErrProcessNotFound
	}
	return val.(*ManagedProcess), nil
}

// Remove deletes a process from the registry.
// Returns true if the process was found and removed.
func (pm *ProcessManager) Remove(id string) bool {
	if _, loaded := pm.processes.LoadAndDelete(id); loaded {
		pm.activeCount.Add(-1)
		return true
	}
	return false
}

// List returns all managed processes.
// Note: This is not a consistent snapshot due to sync.Map semantics.
func (pm *ProcessManager) List() []*ManagedProcess {
	var result []*ManagedProcess
	pm.processes.Range(func(key, value any) bool {
		result = append(result, value.(*ManagedProcess))
		return true
	})
	return result
}

// ListByLabel returns processes matching the given label key/value.
func (pm *ProcessManager) ListByLabel(key, value string) []*ManagedProcess {
	var result []*ManagedProcess
	pm.processes.Range(func(k, v any) bool {
		proc := v.(*ManagedProcess)
		if proc.Labels != nil && proc.Labels[key] == value {
			result = append(result, proc)
		}
		return true
	})
	return result
}

// ActiveCount returns the number of registered processes.
func (pm *ProcessManager) ActiveCount() int64 {
	return pm.activeCount.Load()
}

// TotalStarted returns the total number of processes ever started.
func (pm *ProcessManager) TotalStarted() int64 {
	return pm.totalStarted.Load()
}

// TotalFailed returns the total number of processes that failed.
func (pm *ProcessManager) TotalFailed() int64 {
	return pm.totalFailed.Load()
}

// IncrementFailed increments the failed process counter.
func (pm *ProcessManager) IncrementFailed() {
	pm.totalFailed.Add(1)
}

// Config returns the manager configuration.
func (pm *ProcessManager) Config() ManagerConfig {
	return pm.config
}

// IsShuttingDown returns true if the manager is shutting down.
func (pm *ProcessManager) IsShuttingDown() bool {
	return pm.shuttingDown.Load()
}

// Shutdown gracefully stops all managed processes.
// It blocks until all processes are stopped or the context is cancelled.
// If the context has a very short deadline (<3s), immediately sends SIGKILL to all processes.
func (pm *ProcessManager) Shutdown(ctx context.Context) error {
	var shutdownErr error

	pm.shutdownOnce.Do(func() {
		pm.shuttingDown.Store(true)
		close(pm.shutdownChan)

		// Check if we have a tight deadline - if so, use aggressive mode
		aggressiveMode := false
		if deadline, ok := ctx.Deadline(); ok {
			timeUntilDeadline := time.Until(deadline)
			if timeUntilDeadline < 3*time.Second {
				aggressiveMode = true
			}
		}

		// Stop all running processes in parallel
		var stopWg sync.WaitGroup
		var errMu sync.Mutex
		var errs []error

		pm.processes.Range(func(key, value any) bool {
			proc := value.(*ManagedProcess)
			if proc.IsRunning() {
				stopWg.Add(1)
				go func(p *ManagedProcess) {
					defer stopWg.Done()

					// In aggressive mode, skip straight to SIGKILL
					if aggressiveMode {
						if err := pm.forceKill(p); err != nil {
							errMu.Lock()
							errs = append(errs, err)
							errMu.Unlock()
						}
					} else {
						// Normal graceful shutdown
						if err := pm.Stop(ctx, p.ID); err != nil {
							errMu.Lock()
							errs = append(errs, err)
							errMu.Unlock()
						}
					}
				}(proc)
			}
			return true
		})

		// Wait for all stops to complete with timeout
		done := make(chan struct{})
		go func() {
			stopWg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// All processes stopped
		case <-ctx.Done():
			shutdownErr = ctx.Err()
		}

		// Wait for health check goroutine
		pm.wg.Wait()

		if len(errs) > 0 {
			shutdownErr = errors.Join(errs...)
		}
	})

	return shutdownErr
}

// healthCheckLoop periodically checks process health.
func (pm *ProcessManager) healthCheckLoop() {
	defer pm.wg.Done()

	ticker := time.NewTicker(pm.config.HealthCheckPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-pm.shutdownChan:
			return
		case <-ticker.C:
			pm.performHealthCheck()
		}
	}
}

// performHealthCheck verifies all processes are in expected states.
func (pm *ProcessManager) performHealthCheck() {
	pm.processes.Range(func(key, value any) bool {
		proc := value.(*ManagedProcess)

		switch proc.State() {
		case StateRunning:
			pm.checkRunningProcess(proc)
		case StateStarting:
			pm.checkStartingProcess(proc)
		case StateStopping:
			pm.checkStoppingProcess(proc)
		}

		return true
	})
}

// checkRunningProcess verifies a running process is still alive.
func (pm *ProcessManager) checkRunningProcess(proc *ManagedProcess) {
	// Check if the done channel is closed (process exited)
	select {
	case <-proc.done:
		// Process has exited but state wasn't updated
		// This shouldn't happen normally but handles edge cases
		if proc.State() == StateRunning {
			proc.SetState(StateFailed)
		}
	default:
		// Process is still running
	}
}

// checkStartingProcess detects stuck processes in starting state.
func (pm *ProcessManager) checkStartingProcess(proc *ManagedProcess) {
	// If process has been starting for too long, mark as failed
	start := proc.StartTime()
	if start != nil && time.Since(*start) > 30*time.Second {
		proc.SetState(StateFailed)
		pm.IncrementFailed()
	}
}

// checkStoppingProcess detects stuck processes in stopping state.
func (pm *ProcessManager) checkStoppingProcess(proc *ManagedProcess) {
	// If process has been stopping for too long, force kill
	// This is handled by the Stop method's timeout, but this catches edge cases
	select {
	case <-proc.done:
		// Process has stopped
		if proc.State() == StateStopping {
			proc.SetState(StateStopped)
		}
	default:
		// Still stopping - let the Stop method handle it
	}
}

// KillProcessByPort finds and kills processes listening on the specified port.
// This is useful for cleaning up orphaned processes that are blocking port reuse.
// Returns the PIDs of killed processes and any error.
func (pm *ProcessManager) KillProcessByPort(ctx context.Context, port int) ([]int, error) {
	// Try lsof first (faster and more portable when available)
	pids := pm.findProcessesByPortLsof(ctx, port)

	// If lsof found nothing, fallback to ss (more reliable in some environments)
	if len(pids) == 0 {
		pids = pm.findProcessesByPortSs(ctx, port)
	}

	// If still nothing found, return early
	if len(pids) == 0 {
		return nil, nil
	}

	return pm.killProcesses(pids), nil
}

// findProcessesByPortLsof uses lsof to find processes on a port.
func (pm *ProcessManager) findProcessesByPortLsof(ctx context.Context, port int) []int {
	cmd := exec.CommandContext(ctx, "lsof", "-ti", fmt.Sprintf(":%d", port))
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	return pm.parsePIDLines(strings.TrimSpace(string(output)))
}

// findProcessesByPortSs uses ss to find processes on a port.
func (pm *ProcessManager) findProcessesByPortSs(ctx context.Context, port int) []int {
	// ss -tlnp shows listening TCP ports with process info
	// Format: ... users:(("process",pid=12345,fd=3))
	cmd := exec.CommandContext(ctx, "ss", "-tlnp")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var pids []int
	lines := strings.Split(string(output), "\n")
	portPattern := fmt.Sprintf(":%d", port)

	for _, line := range lines {
		// Check if this line is for our port
		if !strings.Contains(line, portPattern) {
			continue
		}

		// Extract PID from users:(("name",pid=12345,fd=3))
		start := strings.Index(line, "pid=")
		if start == -1 {
			continue
		}
		start += 4 // len("pid=")

		end := strings.IndexAny(line[start:], ",)")
		if end == -1 {
			continue
		}

		var pid int
		if _, err := fmt.Sscanf(line[start:start+end], "%d", &pid); err == nil {
			pids = append(pids, pid)
		}
	}

	return pids
}

// parsePIDLines parses newline-separated PIDs.
func (pm *ProcessManager) parsePIDLines(output string) []int {
	if output == "" {
		return nil
	}

	pidLines := strings.Split(output, "\n")
	var pids []int

	for _, line := range pidLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var pid int
		if _, err := fmt.Sscanf(line, "%d", &pid); err != nil {
			continue // Skip invalid lines
		}
		pids = append(pids, pid)
	}

	return pids
}

// killProcesses kills a list of PIDs gracefully (SIGTERM) then forcefully (SIGKILL).
// Returns the list of PIDs that were successfully signaled.
func (pm *ProcessManager) killProcesses(pids []int) []int {
	var killedPids []int

	// Kill each process
	for _, pid := range pids {
		// Try graceful termination first
		if err := signalTerm(pid); err != nil {
			// Process might have already exited, that's OK
			if !isNoSuchProcess(err) {
				// Real error, but continue with other processes
				continue
			}
		}
		killedPids = append(killedPids, pid)
	}

	// Wait briefly for graceful shutdown
	time.Sleep(500 * time.Millisecond)

	// Force kill any remaining processes
	for _, pid := range pids {
		// Check if process still exists
		if isProcessAlive(pid) {
			// Process still running, force kill
			_ = signalKill(pid)
		}
	}

	return killedPids
}
