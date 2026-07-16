package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/standardbeagle/go-cli-server/script"
)

// Start begins execution of a process.
func (pm *ProcessManager) Start(ctx context.Context, proc *ManagedProcess) error {
	// Take the WaitGroup ticket under startMu, ordered against Shutdown's
	// shuttingDown.Store(true). This closes the TOCTOU where a Start that passed
	// a bare shuttingDown guard but had not yet reached wg.Add(1) could leak its
	// waitForProcess goroutine past a Shutdown whose wg.Wait() saw a zero counter.
	// Once we hold the ticket, wg.Wait() must account for the eventual waiter.
	pm.startMu.Lock()
	if pm.shuttingDown.Load() {
		pm.startMu.Unlock()
		return ErrShuttingDown
	}
	pm.wg.Add(1)
	pm.startMu.Unlock()

	// The ticket is owned by this Start until the process is actually spawned and
	// its waitForProcess goroutine is launched. Any early-error return below hands
	// the ticket back here (the process never spawned, so there is no waiter to
	// call wg.Done). Once started=true, ownership of the single wg.Done passes to
	// waitForProcess.
	started := false
	defer func() {
		if !started {
			pm.wg.Done()
		}
	}()

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

	// Test seam: freeze a Start in the window after it has passed the
	// shutting-down guard AND Register's own guard but before the process is
	// spawned, so a race test can drive Shutdown to completion concurrently. Nil
	// in production. With the fix the WaitGroup ticket is already held here, so a
	// concurrent Shutdown's wg.Wait() must still account for this Start.
	if pm.startGuardHook != nil {
		pm.startGuardHook()
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

	// On context cancellation (run-timeout or manual Cancel), do a GRACEFUL group
	// stop instead of the exec default (immediate SIGKILL of the root only). The
	// hook snapshots the descendant tree while it is still intact — after the root
	// is reaped the /proc PPID chain is gone and grandchildren (e.g. dotnet
	// watch → app) would be un-findable, left orphaned holding ports. WaitDelay
	// then escalates to SIGKILL if the group does not exit in time.
	gracefulTimeout := pm.config.GracefulTimeout
	if gracefulTimeout == 0 {
		gracefulTimeout = 5 * time.Second
	}
	proc.cmd.WaitDelay = gracefulTimeout
	proc.cmd.Cancel = func() error {
		pm.snapshotDescendants(proc)
		if proc.cmd.Process != nil {
			return pm.signalProcessGroup(proc.cmd.Process.Pid, syscall.SIGTERM)
		}
		return nil
	}

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
			pm.failStart(proc)
			return fmt.Errorf("failed to create stdin pipe for %s: %w", proc.ID, err)
		}
		proc.stdin = stdinPipe
	}

	// Start the process
	if err := proc.cmd.Start(); err != nil {
		pm.failStart(proc)
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

	// Start goroutine to wait for completion. The process is spawned; hand the
	// WaitGroup ticket to waitForProcess (its deferred wg.Done pairs with the
	// Add above) and disarm this frame's fallback Done.
	started = true
	go pm.waitForProcess(proc)

	return nil
}

func (pm *ProcessManager) failStart(proc *ManagedProcess) {
	proc.SetState(StateFailed)
	pm.IncrementFailed()
	pm.RemoveByPath(proc.ID, proc.ProjectPath)
	if proc.cancel != nil {
		proc.cancel()
	}
	close(proc.done)
}

// waitForProcess monitors the process until it exits.
func (pm *ProcessManager) waitForProcess(proc *ManagedProcess) {
	defer pm.wg.Done()

	// Save PGID before Wait — the PID equals the PGID since Setpgid/
	// CREATE_NEW_PROCESS_GROUP makes the child the group leader. We need
	// this after Wait because Getpgid fails on a dead process.
	pgid := 0
	rootIdentity := ""
	if proc.cmd != nil && proc.cmd.Process != nil {
		pgid = proc.cmd.Process.Pid
		// UPSTREAM: capture before Wait; afterward this numeric PID can name an
		// unrelated process and must not be treated as ownership evidence.
		rootIdentity = processIdentity(pgid)
	}

	err := proc.cmd.Wait()
	waitDelay := errors.Is(err, exec.ErrWaitDelay)
	if waitDelay {
		err = nil
	}

	// Gather the descendant set from snapshots taken while the tree was still
	// intact: the cmd.Cancel hook (timeout/manual-cancel path) and StopProcess
	// (explicit stop) both populate proc.Descendants(); the background tracker,
	// if any, contributes its last scan. Read AFTER Wait so the Cancel hook's
	// snapshot is visible — after Wait the /proc chain is gone, so a live walk
	// here would find nothing.
	descendants := proc.Descendants()
	descendantIDs := make(map[int]string)
	if stored, ok := pm.cleanupIdentities.LoadAndDelete(proc); ok {
		for pid, identity := range stored.(map[int]string) {
			descendantIDs[pid] = identity
		}
	}
	if dt, ok := pm.pidTracker.(VerifiedDescendantTracker); ok {
		descendants = mergeVerifiedDescendants(descendants, descendantIDs, dt.GetVerifiedDescendants(proc.PID()))
	} else if dt, ok := pm.pidTracker.(DescendantTracker); ok {
		descendants = append(descendants, dt.GetDescendants(proc.PID())...)
	}

	// Kill any surviving children — but ONLY on an abnormal/killed exit. The
	// group kill and the snapshotted-descendant kill both race the kernel
	// recycling the reaped root's PID (== PGID) into an unrelated group leader:
	// kill(-pgid, SIGKILL) would then murder an innocent process tree. cmd.Wait
	// has already reaped the root, so the guard that restricted descendant kills
	// to abnormal exits must cover the group kill too. A clean exit means the
	// process managed its own lifetime; its group is already empty.
	if err != nil {
		if pgid > 0 {
			cleanupReapedProcessGroup(pgid, rootIdentity)
		}
		killStoredDescendants(descendants, descendantIDs)
	}

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

		// A process we deliberately stopped dies by signal, so cmd.Wait returns a
		// non-nil ExitError with code -1 — but that is a normal stop, not a
		// failure. If the state is already Stopping (set by StopProcess), settle
		// to Stopped and do NOT inflate the failure counter. Only a process that
		// died on its own (from Running/Starting) is a genuine failure.
		if proc.CompareAndSwapState(StateStopping, StateStopped) {
			if pm.scriptRegistry != nil {
				if entry, ok := pm.scriptRegistry.GetByProcessID(proc.ID); ok {
					entry.SetState(script.StateStopped)
				}
			}
		} else {
			proc.SetState(StateFailed)
			pm.IncrementFailed()

			if pm.scriptRegistry != nil {
				if entry, ok := pm.scriptRegistry.GetByProcessID(proc.ID); ok {
					entry.SetState(script.StateFailed)
					entry.IncrementFailCount()
					entry.SetLastError(err.Error())
				}
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

	// Release the process context now that it is reaped. On the graceful stop path
	// StopProcess no longer cancels it (that would SIGKILL early), so cancel here
	// to avoid leaking the context's goroutine. Wait has already returned, so
	// os/exec's watcher is gone and this will not fire the cmd.Cancel hook.
	if proc.cancel != nil {
		proc.cancel()
	}

	close(proc.done)
}

// mergeVerifiedDescendants carries scanner-captured identities unchanged into
// post-Wait cleanup. UPSTREAM: never call processIdentity here; verification
// and cleanup are separated by a PID-reuse window.
func mergeVerifiedDescendants(descendants []int, identities map[int]string, verified []VerifiedDescendant) []int {
	for _, descendant := range verified {
		if descendant.PID <= 1 || descendant.Identity == "" {
			continue
		}
		descendants = append(descendants, descendant.PID)
		identities[descendant.PID] = descendant.Identity
	}
	return descendants
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
	// UPSTREAM: this walk occurred while the root was confirmed alive, making
	// it the last safe point to bind numeric descendant PIDs to identities.
	pm.cleanupIdentities.Store(proc, descendantIdentities(descendants))

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

	// Snapshot descendants while the tree is still intact (before any kill).
	pm.snapshotDescendants(proc)

	// Honor an already-cancelled caller context.
	select {
	case <-ctx.Done():
		return pm.forceKill(proc)
	default:
	}

	// Graceful stop: SIGTERM the whole group first. Do NOT proc.Cancel() here —
	// exec.CommandContext's cancel path escalates immediately, which previously
	// SIGKILLed the root before SIGTERM could ever be honored, making
	// GracefulTimeout an illusion. Cancellation is deferred to forceKill.
	//
	// If the graceful signal cannot be delivered (Windows with no console attached,
	// where CTRL_BREAK fails; or a group that is already gone), escalate now instead
	// of idling the full GracefulTimeout waiting for a signal the process never got.
	if proc.cmd != nil && proc.cmd.Process != nil {
		if gerr := pm.signalProcessGroup(proc.cmd.Process.Pid, syscall.SIGTERM); gerr != nil {
			return pm.forceKill(proc)
		}
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
	for {
		state := proc.State()
		if state == StateStopping || state == StateStopped || state == StateFailed {
			break
		}
		if proc.CompareAndSwapState(state, StateStopping) {
			break
		}
	}

	// Escalation: SIGKILL the whole group, then cancel the exec context so the
	// runtime's own killer fires and pipes/resources are released.
	killErr := pm.signalProcessGroup(proc.cmd.Process.Pid, syscall.SIGKILL)
	proc.Cancel()
	if killErr != nil {
		return fmt.Errorf("failed to force kill process %s: %w", proc.ID, killErr)
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

	pm.RemoveByPath(proc.ID, proc.ProjectPath)

	newProc := NewManagedProcess(ProcessConfig{
		ID:             id,
		ProjectPath:    proc.ProjectPath,
		WorkingDir:     proc.WorkingDir,
		Command:        proc.Command,
		Args:           proc.Args,
		Env:            proc.Env,
		Labels:         proc.Labels,
		BufferSize:     proc.stdout.Cap(),
		Timeout:        proc.timeout,
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
	defer pm.RemoveByPath(proc.ID, proc.ProjectPath)

	select {
	case <-proc.done:
		return proc.ExitCode(), nil
	case <-ctx.Done():
		_ = pm.StopProcess(ctx, proc) // Best-effort cleanup on context cancellation
		return -1, ctx.Err()
	}
}
