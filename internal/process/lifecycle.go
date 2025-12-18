package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

// Start begins execution of a process.
// The process must be in StatePending.
func (pm *ProcessManager) Start(ctx context.Context, proc *ManagedProcess) error {
	if pm.shuttingDown.Load() {
		return ErrShuttingDown
	}

	// Atomic state transition: Pending -> Starting
	if !proc.CompareAndSwapState(StatePending, StateStarting) {
		return fmt.Errorf("%w: cannot start process %s (state: %s)",
			ErrInvalidState, proc.ID, proc.State())
	}

	// Register the process (fails if ID exists)
	if err := pm.Register(proc); err != nil {
		proc.SetState(StatePending) // Rollback state
		return err
	}

	// Build the command
	proc.cmd = exec.CommandContext(proc.ctx, proc.Command, proc.Args...)
	proc.cmd.Dir = proc.ProjectPath

	// Use client's environment if provided, otherwise fall back to daemon's environment
	if len(proc.Env) > 0 {
		proc.cmd.Env = proc.Env
	} else {
		proc.cmd.Env = os.Environ()
	}

	// Set platform-specific process attributes for clean shutdown
	setProcAttr(proc.cmd)

	// Connect output streams to ring buffers
	proc.cmd.Stdout = proc.stdout
	proc.cmd.Stderr = proc.stderr

	// Start the process
	if err := proc.cmd.Start(); err != nil {
		proc.SetState(StateFailed)
		pm.IncrementFailed()
		return fmt.Errorf("failed to start process %s: %w", proc.ID, err)
	}

	// Setup platform-specific process group management (Job Object on Windows)
	// This must be called immediately after Start() to catch child processes
	if err := SetupJobObject(proc.cmd); err != nil {
		// Non-fatal - process will still work, just without child process tracking
		// Log would go here if we had logging infrastructure
	}

	// Record start time and PID
	now := time.Now()
	proc.startTime.Store(&now)
	proc.pid.Store(int32(proc.cmd.Process.Pid))
	proc.SetState(StateRunning)

	// Start goroutine to wait for completion
	pm.wg.Add(1)
	go pm.waitForProcess(proc)

	return nil
}

// waitForProcess monitors the process until it exits.
func (pm *ProcessManager) waitForProcess(proc *ManagedProcess) {
	defer pm.wg.Done()

	// Wait for process to exit
	err := proc.cmd.Wait()

	// Cleanup platform-specific resources (Job Object on Windows)
	if proc.cmd != nil && proc.cmd.Process != nil {
		CleanupJobObject(proc.cmd.Process.Pid)
	}

	// Record end time
	now := time.Now()
	proc.endTime.Store(&now)

	// Extract exit code and set final state
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			proc.exitCode.Store(int32(exitErr.ExitCode()))
		} else {
			proc.exitCode.Store(-1)
		}
		proc.SetState(StateFailed)
		pm.IncrementFailed()
	} else {
		proc.exitCode.Store(0)
		proc.SetState(StateStopped)
	}

	// Signal completion
	close(proc.done)
}

// Stop terminates a process gracefully, falling back to force kill.
func (pm *ProcessManager) Stop(ctx context.Context, id string) error {
	proc, err := pm.Get(id)
	if err != nil {
		return err
	}

	return pm.StopProcess(ctx, proc)
}

// StopProcess terminates the given process.
func (pm *ProcessManager) StopProcess(ctx context.Context, proc *ManagedProcess) error {
	// Only stop if running
	state := proc.State()
	if state == StateStopped || state == StateFailed {
		return nil // Already stopped
	}

	if !proc.CompareAndSwapState(StateRunning, StateStopping) {
		// Not running - check if already stopping
		if proc.State() == StateStopping {
			// Wait for it to stop
			select {
			case <-proc.done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return fmt.Errorf("%w: cannot stop process %s (state: %s)",
			ErrInvalidState, proc.ID, proc.State())
	}

	// Cancel the process context
	proc.Cancel()

	// Check if context is already cancelled (aggressive shutdown mode)
	select {
	case <-ctx.Done():
		// Context already cancelled, skip graceful shutdown and force kill immediately
		return pm.forceKill(proc)
	default:
		// Continue with normal shutdown
	}

	// Send termination signal to process group
	if proc.cmd != nil && proc.cmd.Process != nil {
		_ = pm.signalProcessGroup(proc.cmd.Process.Pid, syscall.SIGTERM)
		// Ignore error - continue with graceful shutdown
		// The process might have already exited
	}

	// Wait for graceful shutdown with timeout
	gracefulTimeout := pm.config.GracefulTimeout
	if gracefulTimeout == 0 {
		gracefulTimeout = 5 * time.Second
	}

	select {
	case <-proc.done:
		// Process exited gracefully
		return nil
	case <-time.After(gracefulTimeout):
		// Force kill
		return pm.forceKill(proc)
	case <-ctx.Done():
		// Context cancelled during wait, force kill immediately
		return pm.forceKill(proc)
	}
}

// forceKill forcefully terminates the process and its children.
func (pm *ProcessManager) forceKill(proc *ManagedProcess) error {
	if proc.cmd == nil || proc.cmd.Process == nil {
		return nil
	}

	// Kill the entire process group forcefully
	if err := pm.signalProcessGroup(proc.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("failed to force kill process %s: %w", proc.ID, err)
	}

	// Wait for death with very short timeout (100ms should be enough for forced kill)
	select {
	case <-proc.done:
		return nil
	case <-time.After(100 * time.Millisecond):
		// Process likely already dead but state not updated yet
		return nil
	}
}

// Restart stops a process and starts a new one with the same configuration.
func (pm *ProcessManager) Restart(ctx context.Context, id string) (*ManagedProcess, error) {
	proc, err := pm.Get(id)
	if err != nil {
		return nil, err
	}

	// Stop the existing process
	if err := pm.StopProcess(ctx, proc); err != nil {
		return nil, fmt.Errorf("failed to stop process for restart: %w", err)
	}

	// Remove the old process from registry
	pm.Remove(id)

	// Create a new process with the same config
	newProc := NewManagedProcess(ProcessConfig{
		ID:          id,
		ProjectPath: proc.ProjectPath,
		Command:     proc.Command,
		Args:        proc.Args,
		Labels:      proc.Labels,
		BufferSize:  proc.stdout.Cap(),
	})

	// Start the new process
	if err := pm.Start(ctx, newProc); err != nil {
		return nil, fmt.Errorf("failed to start process after restart: %w", err)
	}

	return newProc, nil
}

// StartCommand is a convenience method to create and start a process.
func (pm *ProcessManager) StartCommand(ctx context.Context, cfg ProcessConfig) (*ManagedProcess, error) {
	// Apply default buffer size from config
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = pm.config.MaxOutputBuffer
	}

	// Apply default timeout from config if not specified
	if cfg.Timeout == 0 && pm.config.DefaultTimeout > 0 {
		cfg.Timeout = pm.config.DefaultTimeout
	}

	proc := NewManagedProcess(cfg)
	if err := pm.Start(ctx, proc); err != nil {
		return nil, err
	}

	return proc, nil
}

// StartOrReuseResult contains the result of StartOrReuse operation.
type StartOrReuseResult struct {
	Process      *ManagedProcess
	Reused       bool   // True if an existing running process was returned
	Cleaned      bool   // True if a stopped/failed process was cleaned up before starting
	PortRetried  bool   // True if a port conflict was detected and resolved
	PortsCleared []int  // Ports that were cleared due to conflicts
	PortError    string // Non-empty if port conflict couldn't be resolved (e.g., managed process blocking)
}

// portConflictPatterns matches common EADDRINUSE error patterns
var portConflictPatterns = []*regexp.Regexp{
	regexp.MustCompile(`EADDRINUSE.*:(\d+)`),                             // Node.js style
	regexp.MustCompile(`listen tcp[^\d]*:(\d+).*address already in use`), // Go style
	regexp.MustCompile(`[Aa]ddress already in use.*[':]+(\d+)`),          // Python/generic with port after
	regexp.MustCompile(`[Aa]ddress already in use[^\d]*(\d+)`),           // Generic with port number
}

// detectPortConflict checks process output for port conflict errors and returns the port.
// Returns 0 if no port conflict detected.
func detectPortConflict(output []byte) int {
	outputStr := string(output)
	for _, pattern := range portConflictPatterns {
		matches := pattern.FindStringSubmatch(outputStr)
		if len(matches) >= 2 {
			if port, err := strconv.Atoi(matches[1]); err == nil && port > 0 && port <= 65535 {
				return port
			}
		}
	}
	return 0
}

// StartOrReuse implements idempotent process start with auto port conflict resolution:
// - If a process with the same ID+ProjectPath is running, return it
// - If a process with the same ID+ProjectPath is stopped/failed, remove it and start new
// - If no process exists, start a new one
// - If process fails quickly due to port conflict (EADDRINUSE), kill blocker and retry once
func (pm *ProcessManager) StartOrReuse(ctx context.Context, cfg ProcessConfig) (*StartOrReuseResult, error) {
	// Apply default buffer size from config
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = pm.config.MaxOutputBuffer
	}

	// Apply default timeout from config if not specified
	if cfg.Timeout == 0 && pm.config.DefaultTimeout > 0 {
		cfg.Timeout = pm.config.DefaultTimeout
	}

	result := &StartOrReuseResult{}

	// Check if process already exists with same ID+Path
	existing, err := pm.GetByPath(cfg.ID, cfg.ProjectPath)
	if err == nil {
		// Process exists, check its state
		state := existing.State()
		switch state {
		case StateRunning, StateStarting:
			// Already running, return it
			result.Process = existing
			result.Reused = true
			return result, nil
		case StateStopped, StateFailed:
			// Clean up old process and start new one
			pm.RemoveByPath(cfg.ID, cfg.ProjectPath)
			result.Cleaned = true
		case StateStopping:
			// Wait for it to stop, then start new
			select {
			case <-existing.Done():
				pm.RemoveByPath(cfg.ID, cfg.ProjectPath)
				result.Cleaned = true
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
			// Pending state - remove and start fresh
			pm.RemoveByPath(cfg.ID, cfg.ProjectPath)
		}
	}

	// Start new process
	proc := NewManagedProcess(cfg)
	if err := pm.Start(ctx, proc); err != nil {
		return nil, err
	}

	// Wait briefly to detect early failure (port conflicts happen fast)
	// Use 1.5s window to catch EADDRINUSE errors
	portCheckTimeout := 1500 * time.Millisecond
	select {
	case <-proc.Done():
		// Process exited quickly, check for port conflict
		if proc.State() == StateFailed {
			output, _ := proc.CombinedOutput()
			conflictPort := detectPortConflict(output)
			if conflictPort > 0 {
				// Port conflict detected - find who's blocking
				blockingPIDs := pm.findProcessesByPortLsof(ctx, conflictPort)
				if len(blockingPIDs) == 0 {
					blockingPIDs = pm.findProcessesByPortSs(ctx, conflictPort)
				}

				if len(blockingPIDs) == 0 {
					// Port was in use but process already gone, or we can't find it
					result.Process = proc
					result.PortError = fmt.Sprintf("port %d conflict detected but blocking process not found", conflictPort)
					return result, nil
				}

				// Check if any blocking PID is a managed process
				for _, pid := range blockingPIDs {
					if managedProc := pm.GetByPID(pid); managedProc != nil {
						// Don't kill managed processes - report the conflict
						result.Process = proc
						result.PortError = fmt.Sprintf("port %d is in use by managed process %q (PID %d)",
							conflictPort, managedProc.ID, pid)
						return result, nil
					}
				}

				// All blocking PIDs are orphaned/external - safe to kill
				killedPIDs, killErr := pm.KillProcessByPort(ctx, conflictPort)
				if killErr != nil {
					result.Process = proc
					result.PortError = fmt.Sprintf("failed to kill process on port %d: %v", conflictPort, killErr)
					return result, nil
				}
				if len(killedPIDs) == 0 {
					result.Process = proc
					result.PortError = fmt.Sprintf("port %d conflict but could not kill blocking process", conflictPort)
					return result, nil
				}

				// Give OS time to release the port
				time.Sleep(200 * time.Millisecond)

				// Clean up failed process and retry
				pm.RemoveByPath(cfg.ID, cfg.ProjectPath)

				// Retry start
				retryProc := NewManagedProcess(cfg)
				if err := pm.Start(ctx, retryProc); err != nil {
					result.PortError = fmt.Sprintf("retry after port cleanup failed: %v", err)
					result.PortsCleared = append(result.PortsCleared, conflictPort)
					// Return the failed original process for output inspection
					result.Process = proc
					return result, nil
				}

				result.Process = retryProc
				result.PortRetried = true
				result.PortsCleared = append(result.PortsCleared, conflictPort)
				return result, nil
			}
		}
		// Not a port conflict or couldn't resolve - return as-is
		result.Process = proc
		return result, nil

	case <-time.After(portCheckTimeout):
		// Process still running after 1.5s - assume it's starting up normally
		result.Process = proc
		return result, nil

	case <-ctx.Done():
		// Context cancelled during startup
		pm.StopProcess(ctx, proc)
		return nil, ctx.Err()
	}
}

// RunSync starts a process and waits for it to complete.
// Returns the exit code and any error.
func (pm *ProcessManager) RunSync(ctx context.Context, cfg ProcessConfig) (int, error) {
	proc, err := pm.StartCommand(ctx, cfg)
	if err != nil {
		return -1, err
	}

	// Wait for completion
	select {
	case <-proc.done:
		return proc.ExitCode(), nil
	case <-ctx.Done():
		// Context cancelled, stop the process
		pm.StopProcess(ctx, proc)
		return -1, ctx.Err()
	}
}
