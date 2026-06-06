package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/standardbeagle/go-cli-server/script"
)

// Start begins execution of a process.
func (pm *ProcessManager) Start(ctx context.Context, proc *ManagedProcess) error {
	if pm.shuttingDown.Load() {
		return ErrShuttingDown
	}

	// Atomic state transition: Pending -> Starting
	if !proc.CompareAndSwapState(StatePending, StateStarting) {
		return fmt.Errorf("%w: cannot start process %s (state: %s)",
			ErrInvalidState, proc.ID, proc.State())
	}

	// Register the process
	if err := pm.Register(proc); err != nil {
		proc.SetState(StatePending)
		return err
	}

	// Build the command
	proc.cmd = exec.CommandContext(proc.ctx, proc.Command, proc.Args...)
	proc.cmd.Dir = proc.WorkingDir // Use WorkingDir for actual cwd (may differ from ProjectPath)

	if len(proc.Env) > 0 {
		proc.cmd.Env = proc.Env
	} else {
		proc.cmd.Env = os.Environ()
	}

	// Set platform-specific process attributes
	setProcAttr(proc.cmd)

	// Connect output streams to ring buffers, optionally wrapping with line callbacks
	if proc.outputCallback != nil {
		proc.stdoutLineWriter = newLineWriter(proc.stdout, proc.ID, proc.outputCallback)
		proc.stderrLineWriter = newLineWriter(proc.stderr, proc.ID, proc.outputCallback)
		proc.cmd.Stdout = proc.stdoutLineWriter
		proc.cmd.Stderr = proc.stderrLineWriter
	} else {
		proc.cmd.Stdout = proc.stdout
		proc.cmd.Stderr = proc.stderr
	}

	// Setup stdin pipe if enabled
	if proc.stdinEnabled {
		stdinPipe, err := proc.cmd.StdinPipe()
		if err != nil {
			proc.SetState(StateFailed)
			pm.IncrementFailed()
			return fmt.Errorf("failed to create stdin pipe for %s: %w", proc.ID, err)
		}
		proc.stdin = stdinPipe
	}

	// Start the process
	if err := proc.cmd.Start(); err != nil {
		proc.SetState(StateFailed)
		pm.IncrementFailed()
		return fmt.Errorf("failed to start process %s: %w", proc.ID, err)
	}

	// Setup platform-specific process group management (non-fatal if it fails)
	_ = SetupJobObject(proc.cmd)

	// Record start time and PID
	now := time.Now()
	proc.startTime.Store(&now)
	pid := proc.cmd.Process.Pid
	proc.pid.Store(int32(pid))
	proc.SetState(StateRunning)

	// Track PID for orphan cleanup
	if pm.pidTracker != nil {
		pgid := getProcessGroupID(pid)
		_ = pm.pidTracker.Add(proc.ID, pid, pgid, proc.ProjectPath)
	}

	// Notify script registry of successful start
	if pm.scriptRegistry != nil {
		if entry, ok := pm.scriptRegistry.GetByProcessID(proc.ID); ok {
			entry.SetState(script.StateRunning)
			entry.IncrementStartCount()
		}
	}

	// Start goroutine to wait for completion
	pm.wg.Add(1)
	go pm.waitForProcess(proc)

	return nil
}

// waitForProcess monitors the process until it exits.
func (pm *ProcessManager) waitForProcess(proc *ManagedProcess) {
	defer pm.wg.Done()

	// Save PGID before Wait — the PID equals the PGID since Setpgid/
	// CREATE_NEW_PROCESS_GROUP makes the child the group leader. We need
	// this after Wait because Getpgid fails on a dead process.
	pgid := 0
	if proc.cmd != nil && proc.cmd.Process != nil {
		pgid = proc.cmd.Process.Pid
	}

	// Look up stored descendants before Wait (while PM state is still valid)
	var storedDescendants []int
	if dt, ok := pm.pidTracker.(DescendantTracker); ok {
		storedDescendants = dt.GetDescendants(proc.PID())
	}

	err := proc.cmd.Wait()

	// Kill any surviving children in the process group. cmd.Wait only waits
	// for the direct child — grandchildren (e.g. dotnet watch → bifrostui)
	// can survive as orphans. Signaling a dead/empty group is a no-op.
	if pgid > 0 {
		cleanupProcessGroup(pgid)
	}

	// Kill stored descendants from tracker (catches processes that died
	// and were replaced between scans)
	killStoredDescendants(storedDescendants)

	// Cleanup platform-specific resources
	if proc.cmd != nil && proc.cmd.Process != nil {
		CleanupJobObject(proc.cmd.Process.Pid)
	}

	// Close stdin if open
	if proc.stdin != nil {
		proc.stdin.Close()
	}

	now := time.Now()
	proc.endTime.Store(&now)

	// Flush any remaining partial output lines
	if proc.stdoutLineWriter != nil {
		proc.stdoutLineWriter.flush()
	}
	if proc.stderrLineWriter != nil {
		proc.stderrLineWriter.flush()
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			proc.exitCode.Store(int32(exitErr.ExitCode()))
		} else {
			proc.exitCode.Store(-1)
		}
		proc.SetState(StateFailed)
		pm.IncrementFailed()

		if pm.scriptRegistry != nil {
			if entry, ok := pm.scriptRegistry.GetByProcessID(proc.ID); ok {
				entry.SetState(script.StateFailed)
				entry.IncrementFailCount()
				entry.SetLastError(err.Error())
			}
		}
	} else {
		proc.exitCode.Store(0)
		proc.SetState(StateStopped)

		if pm.scriptRegistry != nil {
			if entry, ok := pm.scriptRegistry.GetByProcessID(proc.ID); ok {
				entry.SetState(script.StateStopped)
			}
		}
	}

	if pm.pidTracker != nil {
		_ = pm.pidTracker.Remove(proc.ID, proc.ProjectPath)
	}

	close(proc.done)
}

// Stop terminates a process gracefully.
func (pm *ProcessManager) Stop(ctx context.Context, id string) error {
	proc, err := pm.Get(id)
	if err != nil {
		return err
	}

	return pm.StopProcess(ctx, proc)
}

// snapshotDescendants captures the current descendant tree for proc and stores
// it on the ManagedProcess. Must be called while the root process is still
// alive — any later call will return a partial or empty set because cancelled
// contexts cause exec.CommandContext to SIGKILL the root, reparenting every
// descendant to init and destroying the PPID chain.
func (pm *ProcessManager) snapshotDescendants(proc *ManagedProcess) {
	if proc == nil || proc.cmd == nil || proc.cmd.Process == nil {
		return
	}
	pid := proc.cmd.Process.Pid
	descendants := findAllDescendants(pid)
	proc.SetDescendants(descendants)

	// Mirror the snapshot into the persistent tracker if available so that
	// the SIGKILL escalation path (which runs after Wait() reaps the root)
	// still has something to kill.
	if pm.pidTracker != nil {
		if dt, ok := pm.pidTracker.(DescendantTracker); ok {
			_ = dt.UpdateDescendants(pid, descendants)
		}
	}
}

// StopProcess terminates the given process.
func (pm *ProcessManager) StopProcess(ctx context.Context, proc *ManagedProcess) error {
	state := proc.State()
	if state == StateStopped || state == StateFailed {
		return nil
	}

	if !proc.CompareAndSwapState(StateRunning, StateStopping) {
		if proc.State() == StateStopping {
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

	// Snapshot descendants BEFORE cancelling the process context. Cancelling
	// causes the exec.CommandContext watcher to SIGKILL the root PID, which
	// reparents every descendant to init and breaks any post-cancel PPID walk.
	pm.snapshotDescendants(proc)

	proc.Cancel()

	select {
	case <-ctx.Done():
		return pm.forceKill(proc)
	default:
	}

	if proc.cmd != nil && proc.cmd.Process != nil {
		_ = pm.signalProcessGroup(proc.cmd.Process.Pid, syscall.SIGTERM)
	}

	gracefulTimeout := pm.config.GracefulTimeout
	if gracefulTimeout == 0 {
		gracefulTimeout = 5 * time.Second
	}

	select {
	case <-proc.done:
		return nil
	case <-time.After(gracefulTimeout):
		return pm.forceKill(proc)
	case <-ctx.Done():
		return pm.forceKill(proc)
	}
}

// forceKill forcefully terminates the process.
func (pm *ProcessManager) forceKill(proc *ManagedProcess) error {
	if proc.cmd == nil || proc.cmd.Process == nil {
		return nil
	}

	if err := pm.signalProcessGroup(proc.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("failed to force kill process %s: %w", proc.ID, err)
	}

	select {
	case <-proc.done:
		return nil
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}

// Restart stops a process and starts a new one with the same configuration.
func (pm *ProcessManager) Restart(ctx context.Context, id string) (*ManagedProcess, error) {
	proc, err := pm.Get(id)
	if err != nil {
		return nil, err
	}

	// Add restart marker to script entry before stopping
	if pm.scriptRegistry != nil {
		if entry, ok := pm.scriptRegistry.GetByProcessID(proc.ID); ok {
			entry.AddRestartMarker()
			entry.SetState(script.StateRestarting)
		}
	}

	if err := pm.StopProcess(ctx, proc); err != nil {
		return nil, fmt.Errorf("failed to stop process for restart: %w", err)
	}

	pm.Remove(id)

	newProc := NewManagedProcess(ProcessConfig{
		ID:             id,
		ProjectPath:    proc.ProjectPath,
		Command:        proc.Command,
		Args:           proc.Args,
		Labels:         proc.Labels,
		BufferSize:     proc.stdout.Cap(),
		EnableStdin:    proc.stdinEnabled,
		OutputCallback: proc.outputCallback,
	})

	if err := pm.Start(ctx, newProc); err != nil {
		return nil, fmt.Errorf("failed to start process after restart: %w", err)
	}

	return newProc, nil
}

// StartCommand is a convenience method to create and start a process.
func (pm *ProcessManager) StartCommand(ctx context.Context, cfg ProcessConfig) (*ManagedProcess, error) {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = pm.config.MaxOutputBuffer
	}

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
	Reused       bool
	Cleaned      bool
	PortRetried  bool
	PortsCleared []int
	PortError    string
}

// StartOrReuse implements idempotent process start.
func (pm *ProcessManager) StartOrReuse(ctx context.Context, cfg ProcessConfig) (*StartOrReuseResult, error) {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = pm.config.MaxOutputBuffer
	}

	if cfg.Timeout == 0 && pm.config.DefaultTimeout > 0 {
		cfg.Timeout = pm.config.DefaultTimeout
	}

	result := &StartOrReuseResult{}

	existing, err := pm.GetByPath(cfg.ID, cfg.ProjectPath)
	if err == nil {
		state := existing.State()
		switch state {
		case StateRunning, StateStarting:
			result.Process = existing
			result.Reused = true
			return result, nil
		case StateStopped, StateFailed:
			pm.RemoveByPath(cfg.ID, cfg.ProjectPath)
			result.Cleaned = true
		case StateStopping:
			select {
			case <-existing.Done():
				pm.RemoveByPath(cfg.ID, cfg.ProjectPath)
				result.Cleaned = true
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
			pm.RemoveByPath(cfg.ID, cfg.ProjectPath)
		}
	}

	proc := NewManagedProcess(cfg)
	if err := pm.Start(ctx, proc); err != nil {
		return nil, err
	}

	result.Process = proc
	return result, nil
}

// RunSync starts a process and waits for it to complete.
func (pm *ProcessManager) RunSync(ctx context.Context, cfg ProcessConfig) (int, error) {
	proc, err := pm.StartCommand(ctx, cfg)
	if err != nil {
		return -1, err
	}

	select {
	case <-proc.done:
		return proc.ExitCode(), nil
	case <-ctx.Done():
		_ = pm.StopProcess(ctx, proc) // Best-effort cleanup on context cancellation
		return -1, ctx.Err()
	}
}
