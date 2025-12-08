package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
	proc.cmd.Env = os.Environ()

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
		_ = signalTerm(proc.cmd.Process.Pid)
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

// forceKill forcefully terminates the process.
func (pm *ProcessManager) forceKill(proc *ManagedProcess) error {
	if proc.cmd == nil || proc.cmd.Process == nil {
		return nil
	}

	// Kill the process forcefully
	if err := signalKill(proc.cmd.Process.Pid); err != nil {
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
