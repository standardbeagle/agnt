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
	// ErrProcessAmbiguous is returned when a bare process ID matches multiple project paths.
	ErrProcessAmbiguous = errors.New("process ID is ambiguous across project paths")
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

type VerifiedDescendantTracker interface {
	GetVerifiedDescendants(pid int) []int
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
	shutdownOnce sync.Once
	shutdownChan chan struct{}
	shuttingDown atomic.Bool
	// startMu orders Start's wg.Add(1) against Shutdown's shuttingDown.Store(true)
	// so the WaitGroup can never miss an in-flight start. Without it a Start that
	// passed the shuttingDown guard but had not yet reached wg.Add could have its
	// waitForProcess goroutine leak past a Shutdown whose wg.Wait saw a zero
	// counter. It guards ONLY the add/flag ordering, not the whole Start path.
	startMu       sync.Mutex
	wg            sync.WaitGroup
	scannerCancel context.CancelFunc

	// startGuardHook, when non-nil, is invoked inside Start immediately after the
	// shutting-down guard is passed and the WaitGroup ticket is taken. It exists
	// only as a test seam to freeze a Start in that window and race Shutdown; it
	// is always nil in production and costs a single nil check.
	startGuardHook func()
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
	count := 0
	pm.processes.Range(func(key, value any) bool {
		proc := value.(*ManagedProcess)
		if proc.ID == id {
			found = proc
			count++
		}
		return count < 2
	})
	if count > 1 {
		return nil, ErrProcessAmbiguous
	}
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
	count := 0
	pm.processes.Range(func(key, value any) bool {
		proc := value.(*ManagedProcess)
		if proc.ID == id {
			keyToDelete = key.(string)
			count++
		}
		return count < 2
	})
	if count > 1 {
		return false
	}
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
		// Order the flag-set against Start's wg.Add under startMu: once this
		// returns, any Start still holding startMu has already done its wg.Add,
		// so the wg.Wait below cannot miss its waitForProcess goroutine.
		pm.startMu.Lock()
		pm.shuttingDown.Store(true)
		pm.startMu.Unlock()
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
						// Stop the exact process we hold, not pm.Stop(p.ID) which
						// re-resolves by ID — ambiguous when two projects share a
						// script name and could stop the wrong one (or double-stop
						// one while its same-ID sibling escapes graceful shutdown).
						if err := pm.StopProcess(ctx, p); err != nil {
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
			pm.processes.Range(func(key, value any) bool {
				proc := value.(*ManagedProcess)
				if proc.IsRunning() {
					_ = pm.forceKill(proc)
				}
				return true
			})
			<-done
		}

		pm.wg.Wait()

		errMu.Lock()
		joinedErr := errors.Join(errs...)
		errMu.Unlock()
		if joinedErr != nil {
			shutdownErr = joinedErr
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
		<-done
	}

	for _, proc := range toStop {
		pm.RemoveByPath(proc.ID, proc.ProjectPath)
	}

	errMu.Lock()
	joinedErr := errors.Join(errs...)
	errMu.Unlock()
	if joinedErr != nil {
		return stoppedIDs, joinedErr
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
		<-done
	}

	for _, proc := range toStop {
		pm.RemoveByPath(proc.ID, proc.ProjectPath)
	}

	errMu.Lock()
	joinedErr := errors.Join(errs...)
	errMu.Unlock()
	if joinedErr != nil {
		return joinedErr
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
		// CAS, not check-then-set: waitForProcess may transition this process
		// concurrently, and a plain SetState would clobber the real terminal
		// state (Stopped/Failed decided there) with a spurious Failed.
		proc.CompareAndSwapState(StateRunning, StateFailed)
	default:
	}
}

func (pm *ProcessManager) checkStartingProcess(proc *ManagedProcess) {
	start := proc.StartTime()
	if start != nil && time.Since(*start) > 30*time.Second {
		// CAS so a Starting→Running transition that lands between the read and
		// the write is not overwritten — otherwise a healthy just-started
		// process is wrongly marked Failed (with a spurious failure count).
		if proc.CompareAndSwapState(StateStarting, StateFailed) {
			pm.IncrementFailed()
		}
	}
}

func (pm *ProcessManager) checkStoppingProcess(proc *ManagedProcess) {
	select {
	case <-proc.done:
		proc.CompareAndSwapState(StateStopping, StateStopped)
	default:
	}
}

// KillProcessByPort finds and kills processes listening on the specified port.
func (pm *ProcessManager) KillProcessByPort(ctx context.Context, port int) ([]int, error) {
	pids := FindPIDsByPort(port)
	if len(pids) == 0 {
		return nil, nil
	}
	return pm.killProcesses(ctx, pids), nil
}

func (pm *ProcessManager) killProcesses(ctx context.Context, pids []int) []int {
	var killedPids []int

	// Capture each PID's identity before signalling so the SIGKILL escalation
	// can tell the original process from one the kernel recycled the PID into
	// during the grace window.
	identities := make(map[int]string, len(pids))
	for _, pid := range pids {
		identities[pid] = processIdentity(pid)
	}

	// Phase 1: SIGTERM to process group + descendants. Only report a PID as
	// killed if the signal was actually delivered (process still present).
	for _, pid := range pids {
		if err := pm.signalProcessGroup(pid, syscall.SIGTERM); err == nil {
			killedPids = append(killedPids, pid)
		}
	}

	// Phase 2: Wait for graceful exit, but honor cancellation instead of a hard
	// 3s stall on the caller's goroutine (port preflight / shutdown hot paths).
	select {
	case <-ctx.Done():
		return killedPids
	case <-time.After(3 * time.Second):
	}

	// Phase 3: SIGKILL escalation for survivors — skip any PID whose identity no
	// longer matches, i.e. the original exited and the PID was recycled.
	for _, pid := range pids {
		if !isProcessAlive(pid) {
			continue
		}
		if id := identities[pid]; id != "" && processIdentity(pid) != id {
			continue // recycled PID — not our target
		}
		cleanupProcessTree(pid)
	}

	// Phase 4: wait for the survivors to actually die. SIGKILL is delivered
	// asynchronously: the kernel releases the process's resources — including
	// the listening socket that made a caller want it dead — only once it exits.
	// Returning at SIGKILL left the port still bound, so a port-preflight caller
	// would immediately fail to bind the port it had just "freed".
	waitForProcessesToExit(ctx, pids, identities, 2*time.Second)

	return killedPids
}

// waitForProcessesToExit polls until every pid has released its resources or the
// budget expires. A zombie counts as gone (it holds no descriptors), as does a
// recycled PID — the process we killed is dead either way.
func waitForProcessesToExit(ctx context.Context, pids []int, identities map[int]string, budget time.Duration) {
	deadline := time.Now().Add(budget)
	for {
		remaining := false
		for _, pid := range pids {
			if hasReleasedResources(pid) {
				continue
			}
			if id := identities[pid]; id != "" && processIdentity(pid) != id {
				continue // recycled: our target is dead
			}
			remaining = true
			break
		}
		if !remaining {
			return
		}
		if time.Now().After(deadline) {
			return // caller escalated as far as it can; do not block forever
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Millisecond):
		}
	}
}
