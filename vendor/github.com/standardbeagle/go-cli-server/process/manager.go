package process

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/standardbeagle/go-cli-server/script"
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
	// ErrStdinNotEnabled is returned when trying to write to stdin that's not enabled.
	ErrStdinNotEnabled = errors.New("stdin not enabled for this process")
)

// processKey creates a composite key from process ID and project path.
func processKey(id, projectPath string) string {
	return projectPath + "\x00" + id
}

// PIDTracker is an interface for tracking process PIDs for orphan cleanup.
type PIDTracker interface {
	Add(id string, pid int, pgid int, projectPath string) error
	Remove(id string, projectPath string) error
}

// DescendantTracker extends PIDTracker with descendant tree tracking.
// Implementations can start a background scanner to periodically discover
// and persist the full descendant tree of each tracked process.
type DescendantTracker interface {
	PIDTracker
	StartDescendantScanner(ctx context.Context, interval time.Duration)
	GetDescendants(pid int) []int
	UpdateDescendants(pid int, descendants []int) error
}

// ManagerConfig holds configuration for the ProcessManager.
type ManagerConfig struct {
	DefaultTimeout    time.Duration
	MaxOutputBuffer   int
	GracefulTimeout   time.Duration
	HealthCheckPeriod time.Duration
	PIDTracker        PIDTracker
}

// DefaultManagerConfig returns a ManagerConfig with sensible defaults.
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		DefaultTimeout:    0,
		MaxOutputBuffer:   DefaultBufferSize,
		GracefulTimeout:   5 * time.Second,
		HealthCheckPeriod: 10 * time.Second,
	}
}

// ProcessManager manages all spawned processes with lock-free access.
type ProcessManager struct {
	// processes is a lock-free map of process ID to ManagedProcess.
	processes sync.Map

	// Atomic counters for metrics
	activeCount  atomic.Int64
	totalStarted atomic.Int64
	totalFailed  atomic.Int64

	// Configuration
	config ManagerConfig

	// PID tracking for orphan cleanup
	pidTracker PIDTracker

	// Script registry for automatic lifecycle integration
	scriptRegistry *script.Registry

	// Shutdown coordination
	shutdownOnce  sync.Once
	shutdownChan  chan struct{}
	shuttingDown  atomic.Bool
	wg            sync.WaitGroup
	scannerCancel context.CancelFunc
}

// DefaultScanInterval is the default interval for descendant tree scanning.
const DefaultScanInterval = 5 * time.Second

// NewProcessManager creates a new ProcessManager with the given configuration.
func NewProcessManager(config ManagerConfig) *ProcessManager {
	pm := &ProcessManager{
		config:       config,
		pidTracker:   config.PIDTracker,
		shutdownChan: make(chan struct{}),
	}

	if config.HealthCheckPeriod > 0 {
		pm.wg.Add(1)
		go pm.healthCheckLoop()
	}

	// Start descendant scanner if the PID tracker supports it
	if dt, ok := pm.pidTracker.(DescendantTracker); ok {
		ctx, cancel := context.WithCancel(context.Background())
		pm.scannerCancel = cancel
		dt.StartDescendantScanner(ctx, DefaultScanInterval)
	}

	return pm
}

// Register adds a new process to the registry.
func (pm *ProcessManager) Register(proc *ManagedProcess) error {
	if pm.shuttingDown.Load() {
		return ErrShuttingDown
	}

	key := processKey(proc.ID, proc.ProjectPath)
	_, loaded := pm.processes.LoadOrStore(key, proc)
	if loaded {
		return ErrProcessExists
	}

	pm.activeCount.Add(1)
	pm.totalStarted.Add(1)
	return nil
}

// Get retrieves a process by ID (searches all paths).
func (pm *ProcessManager) Get(id string) (*ManagedProcess, error) {
	var found *ManagedProcess
	pm.processes.Range(func(key, value any) bool {
		proc := value.(*ManagedProcess)
		if proc.ID == id {
			found = proc
			return false
		}
		return true
	})
	if found != nil {
		return found, nil
	}
	return nil, ErrProcessNotFound
}

// GetByPath retrieves a process by ID and project path.
func (pm *ProcessManager) GetByPath(id, projectPath string) (*ManagedProcess, error) {
	key := processKey(id, projectPath)
	val, ok := pm.processes.Load(key)
	if !ok {
		return nil, ErrProcessNotFound
	}
	return val.(*ManagedProcess), nil
}

// Remove deletes a process from the registry by ID.
func (pm *ProcessManager) Remove(id string) bool {
	var keyToDelete string
	pm.processes.Range(func(key, value any) bool {
		proc := value.(*ManagedProcess)
		if proc.ID == id {
			keyToDelete = key.(string)
			return false
		}
		return true
	})
	if keyToDelete != "" {
		if _, loaded := pm.processes.LoadAndDelete(keyToDelete); loaded {
			pm.activeCount.Add(-1)
			return true
		}
	}
	return false
}

// RemoveByPath deletes a process from the registry by ID and path.
func (pm *ProcessManager) RemoveByPath(id, projectPath string) bool {
	key := processKey(id, projectPath)
	if _, loaded := pm.processes.LoadAndDelete(key); loaded {
		pm.activeCount.Add(-1)
		return true
	}
	return false
}

// List returns all managed processes.
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

// GetByPID returns the managed process with the given OS PID.
func (pm *ProcessManager) GetByPID(pid int) *ManagedProcess {
	var found *ManagedProcess
	pm.processes.Range(func(key, value any) bool {
		proc := value.(*ManagedProcess)
		if proc.PID() == pid && proc.IsRunning() {
			found = proc
			return false
		}
		return true
	})
	return found
}

// lookupByPID returns the managed process with the given OS PID regardless of
// its current state. Used internally by the stop/kill path to recover the
// ManagedProcess after it has transitioned to Stopping/Stopped/Failed so we
// can still consult cached descendants.
func (pm *ProcessManager) lookupByPID(pid int) *ManagedProcess {
	var found *ManagedProcess
	pm.processes.Range(func(key, value any) bool {
		proc := value.(*ManagedProcess)
		if proc.PID() == pid {
			found = proc
			return false
		}
		return true
	})
	return found
}

// IsManagedPID returns true if the given PID belongs to a running managed process.
func (pm *ProcessManager) IsManagedPID(pid int) bool {
	return pm.GetByPID(pid) != nil
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

// SetScriptRegistry sets the script registry for automatic lifecycle integration.
// When set, process lifecycle events automatically update the corresponding ScriptEntry.
func (pm *ProcessManager) SetScriptRegistry(r *script.Registry) {
	pm.scriptRegistry = r
}

// ScriptRegistry returns the currently set script registry, or nil.
func (pm *ProcessManager) ScriptRegistry() *script.Registry {
	return pm.scriptRegistry
}

// IsShuttingDown returns true if the manager is shutting down.
func (pm *ProcessManager) IsShuttingDown() bool {
	return pm.shuttingDown.Load()
}

// Shutdown gracefully stops all managed processes.
func (pm *ProcessManager) Shutdown(ctx context.Context) error {
	var shutdownErr error

	pm.shutdownOnce.Do(func() {
		pm.shuttingDown.Store(true)
		close(pm.shutdownChan)

		// Stop the descendant scanner
		if pm.scannerCancel != nil {
			pm.scannerCancel()
		}

		aggressiveMode := false
		if deadline, ok := ctx.Deadline(); ok {
			if time.Until(deadline) < 3*time.Second {
				aggressiveMode = true
			}
		}

		var stopWg sync.WaitGroup
		var errMu sync.Mutex
		var errs []error

		pm.processes.Range(func(key, value any) bool {
			proc := value.(*ManagedProcess)
			if proc.IsRunning() {
				stopWg.Add(1)
				go func(p *ManagedProcess) {
					defer stopWg.Done()

					if aggressiveMode {
						if err := pm.forceKill(p); err != nil {
							errMu.Lock()
							errs = append(errs, err)
							errMu.Unlock()
						}
					} else {
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

		done := make(chan struct{})
		go func() {
			stopWg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-ctx.Done():
			shutdownErr = ctx.Err()
		}

		pm.wg.Wait()

		if len(errs) > 0 {
			shutdownErr = errors.Join(errs...)
		}
	})

	return shutdownErr
}

// StopByProjectPath stops all running processes for a specific project path.
func (pm *ProcessManager) StopByProjectPath(ctx context.Context, projectPath string) ([]string, error) {
	var stopWg sync.WaitGroup
	var errMu sync.Mutex
	var errs []error
	var stoppedIDs []string

	var toStop []*ManagedProcess
	pm.processes.Range(func(key, value any) bool {
		proc := value.(*ManagedProcess)
		if proc.ProjectPath == projectPath {
			toStop = append(toStop, proc)
		}
		return true
	})

	for _, proc := range toStop {
		if proc.IsRunning() {
			stopWg.Add(1)
			go func(p *ManagedProcess) {
				defer stopWg.Done()
				if err := pm.StopProcess(ctx, p); err != nil {
					errMu.Lock()
					errs = append(errs, err)
					errMu.Unlock()
				}
			}(proc)
		}
		stoppedIDs = append(stoppedIDs, proc.ID)
	}

	done := make(chan struct{})
	go func() {
		stopWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		for _, proc := range toStop {
			if proc.IsRunning() {
				_ = pm.forceKill(proc)
			}
		}
	}

	for _, proc := range toStop {
		pm.RemoveByPath(proc.ID, proc.ProjectPath)
	}

	if len(errs) > 0 {
		return stoppedIDs, errors.Join(errs...)
	}
	return stoppedIDs, nil
}

// StopAll stops all running processes and removes them from the registry.
func (pm *ProcessManager) StopAll(ctx context.Context) error {
	var stopWg sync.WaitGroup
	var errMu sync.Mutex
	var errs []error

	var toStop []*ManagedProcess
	pm.processes.Range(func(key, value any) bool {
		proc := value.(*ManagedProcess)
		toStop = append(toStop, proc)
		return true
	})

	for _, proc := range toStop {
		if proc.IsRunning() {
			stopWg.Add(1)
			go func(p *ManagedProcess) {
				defer stopWg.Done()
				if err := pm.StopProcess(ctx, p); err != nil {
					errMu.Lock()
					errs = append(errs, err)
					errMu.Unlock()
				}
			}(proc)
		}
	}

	done := make(chan struct{})
	go func() {
		stopWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		for _, proc := range toStop {
			if proc.IsRunning() {
				_ = pm.forceKill(proc)
			}
		}
	}

	for _, proc := range toStop {
		pm.RemoveByPath(proc.ID, proc.ProjectPath)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// WriteStdin writes data to a process's stdin by ID.
func (pm *ProcessManager) WriteStdin(id string, data []byte) (int, error) {
	proc, err := pm.Get(id)
	if err != nil {
		return 0, err
	}
	return proc.WriteStdin(data)
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

func (pm *ProcessManager) checkRunningProcess(proc *ManagedProcess) {
	select {
	case <-proc.done:
		if proc.State() == StateRunning {
			proc.SetState(StateFailed)
		}
	default:
	}
}

func (pm *ProcessManager) checkStartingProcess(proc *ManagedProcess) {
	start := proc.StartTime()
	if start != nil && time.Since(*start) > 30*time.Second {
		proc.SetState(StateFailed)
		pm.IncrementFailed()
	}
}

func (pm *ProcessManager) checkStoppingProcess(proc *ManagedProcess) {
	select {
	case <-proc.done:
		if proc.State() == StateStopping {
			proc.SetState(StateStopped)
		}
	default:
	}
}

// KillProcessByPort finds and kills processes listening on the specified port.
func (pm *ProcessManager) KillProcessByPort(ctx context.Context, port int) ([]int, error) {
	pids := FindPIDsByPort(port)
	if len(pids) == 0 {
		return nil, nil
	}
	return pm.killProcesses(pids), nil
}

func (pm *ProcessManager) killProcesses(pids []int) []int {
	var killedPids []int

	// Phase 1: SIGTERM to process group + descendants for each PID
	for _, pid := range pids {
		pm.signalProcessGroup(pid, syscall.SIGTERM)
		killedPids = append(killedPids, pid)
	}

	// Phase 2: Wait for graceful exit
	time.Sleep(3 * time.Second)

	// Phase 3: SIGKILL escalation for survivors
	for _, pid := range pids {
		if isProcessAlive(pid) {
			cleanupProcessTree(pid)
		}
	}

	return killedPids
}
