package daemon

import (
	"context"
	"sync"
	"time"
)

// AutostartPhase identifies the phase of an autostart progress event.
type AutostartPhase int

const (
	// PhaseScriptStarting is emitted when a script's launch goroutine begins.
	PhaseScriptStarting AutostartPhase = iota
	// PhaseDependencyWaitStart is emitted when a script begins waiting for
	// a declared dependency to become ready.
	PhaseDependencyWaitStart
	// PhaseDependencyReady is emitted when a declared dependency signals ready.
	PhaseDependencyReady
	// PhaseScriptStarted is emitted after the process is successfully started.
	PhaseScriptStarted
	// PhaseScriptFailed is emitted when a script fails to start.
	PhaseScriptFailed
	// PhaseLayerComplete is emitted after every script in a topological layer
	// has finished its start attempt (success or failure).
	PhaseLayerComplete
	// PhaseDone is emitted once by the AutostartManager goroutine after the
	// whole autostart run has returned and the final result is stored.
	PhaseDone
)

// AutostartProgress reports progress during asynchronous autostart.
//
// It is populated by emitProgress and, when routed through an AutostartHandle,
// has ProjectPath and Timestamp stamped automatically by the handle's drain
// goroutine.
type AutostartProgress struct {
	ProjectPath string // Normalized project path (stamped by handle)
	Phase       AutostartPhase
	Script      string    // Script name (empty for layer-level events)
	Dependency  string    // Dependency name (for dependency phases)
	Layer       int       // Topological layer index
	Err         error     // Non-nil for PhaseScriptFailed
	Timestamp   time.Time // Stamped by handle on receive
}

// AutostartStartFunc is the signature used by AutostartManager to kick off an
// autostart run. It receives a context that will be cancelled when
// AutostartManager.Cancel is called, and a progress channel that the callee
// should close when it is done emitting events. The returned result becomes
// the handle's final Result() value.
type AutostartStartFunc func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult

// AutostartHandle represents a single in-flight (or completed) autostart run
// for a specific project path. Handles are returned by AutostartManager and
// can be shared between multiple callers via GetOrCreate.
//
// A handle goes through exactly one lifetime: created, running, done. Once
// Done() has fired, Result() and Progress() remain stable and safe to read.
type AutostartHandle struct {
	projectPath string
	done        chan struct{}
	cancel      context.CancelFunc

	mu       sync.RWMutex
	result   *AutostartResult
	progress []AutostartProgress
	scripts  map[string]string // scriptName → latest phase name (optional per-script view)
}

// Done returns a channel that is closed once the autostart run has finished.
// Callers can use this to block until completion or to poll via select.
func (h *AutostartHandle) Done() <-chan struct{} {
	return h.done
}

// ProjectPath returns the normalized project path this handle tracks.
func (h *AutostartHandle) ProjectPath() string {
	return h.projectPath
}

// Result returns the final AutostartResult once the run has completed.
// Returns nil if the run is still in flight.
func (h *AutostartHandle) Result() *AutostartResult {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.result
}

// Progress returns a defensive copy of all progress events observed so far.
// Late joiners calling this after completion receive the full history.
func (h *AutostartHandle) Progress() []AutostartProgress {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.progress) == 0 {
		return nil
	}
	out := make([]AutostartProgress, len(h.progress))
	copy(out, h.progress)
	return out
}

// Cancel cancels the in-flight autostart context. Calling Cancel on a handle
// whose run has already completed is a safe no-op.
func (h *AutostartHandle) Cancel() {
	if h.cancel != nil {
		h.cancel()
	}
}

// AutostartManager coordinates at-most-one autostart run per project path.
// Callers invoke GetOrCreate with a startFn; the first caller for a given
// path starts the run, and subsequent callers receive the same handle.
//
// All operations are safe for concurrent use. The registry itself is
// lock-free: it relies on sync.Map.LoadOrStore for exactly-once semantics.
type AutostartManager struct {
	handles sync.Map // normalizedPath (string) → *AutostartHandle
}

// NewAutostartManager returns a ready-to-use AutostartManager.
func NewAutostartManager() *AutostartManager {
	return &AutostartManager{}
}

// GetOrCreate returns the existing handle for projectPath if one is present,
// otherwise creates a new handle and launches startFn in a background
// goroutine.
//
// Exactly-once start is guaranteed: concurrent callers with the same
// projectPath all receive the same handle, but startFn is invoked only once.
//
// startFn receives:
//   - a cancellable context (cancelled via Cancel or handle.Cancel)
//   - a buffered progress channel; callers should emit via emitProgress and
//     MUST NOT close the channel themselves (the manager closes it after
//     startFn returns).
//
// The returned handle is not guaranteed to be done: use handle.Done() to
// wait for completion.
func (m *AutostartManager) GetOrCreate(projectPath string, startFn AutostartStartFunc) *AutostartHandle {
	key := normalizePath(projectPath)

	// Build a candidate handle first, then attempt to install it.
	// If another goroutine wins the race, the candidate is discarded and
	// startFn is never invoked for it.
	ctx, cancel := context.WithCancel(context.Background())
	candidate := &AutostartHandle{
		projectPath: key,
		done:        make(chan struct{}),
		cancel:      cancel,
		scripts:     make(map[string]string),
	}

	actual, loaded := m.handles.LoadOrStore(key, candidate)
	if loaded {
		// Lost the race: discard candidate, return the winner.
		cancel()
		return actual.(*AutostartHandle)
	}

	// Won the race: we own this handle. Launch the worker.
	go m.run(ctx, candidate, startFn)
	return candidate
}

// Cancel cancels the handle for projectPath, if one exists. No-op if no
// handle is registered or the handle has already completed.
func (m *AutostartManager) Cancel(projectPath string) {
	key := normalizePath(projectPath)
	v, ok := m.handles.Load(key)
	if !ok {
		return
	}
	h := v.(*AutostartHandle)
	h.Cancel()
}

// Progress returns a snapshot of progress events for projectPath, or nil if
// no handle exists for that path.
func (m *AutostartManager) Progress(projectPath string) []AutostartProgress {
	key := normalizePath(projectPath)
	v, ok := m.handles.Load(key)
	if !ok {
		return nil
	}
	return v.(*AutostartHandle).Progress()
}

// Get returns the handle for projectPath, or nil if none exists.
func (m *AutostartManager) Get(projectPath string) *AutostartHandle {
	key := normalizePath(projectPath)
	v, ok := m.handles.Load(key)
	if !ok {
		return nil
	}
	return v.(*AutostartHandle)
}

// Remove deletes the handle for projectPath from the registry. The handle
// itself is not cancelled; call Cancel separately if that is desired. This
// is intended for session-cleanup paths that want the next session to start
// fresh.
func (m *AutostartManager) Remove(projectPath string) {
	key := normalizePath(projectPath)
	m.handles.Delete(key)
}

// run executes startFn in a dedicated goroutine, drains the progress channel
// into the handle's history slice, and stores the final result.
//
// Flow:
//  1. Create a buffered progress channel so emitProgress never blocks.
//  2. Launch startFn in its own goroutine; when it returns we capture the
//     result and close the progress channel.
//  3. Drain progress events into the handle under the write lock (one event
//     at a time — no long-held lock around user code).
//  4. Once draining completes, emit a synthetic PhaseDone event, close the
//     handle's done channel, and invoke cancel to release context resources.
func (m *AutostartManager) run(ctx context.Context, h *AutostartHandle, startFn AutostartStartFunc) {
	// Buffered so emitProgress in the worker never blocks on a slow drainer.
	progress := make(chan AutostartProgress, 64)

	var (
		result   *AutostartResult
		workerWg sync.WaitGroup
	)

	workerWg.Add(1)
	go func() {
		defer workerWg.Done()
		defer close(progress)
		result = startFn(ctx, progress)
	}()

	// Drain until the worker closes the channel.
	for ev := range progress {
		if ev.ProjectPath == "" {
			ev.ProjectPath = h.projectPath
		}
		if ev.Timestamp.IsZero() {
			ev.Timestamp = time.Now()
		}
		h.mu.Lock()
		h.progress = append(h.progress, ev)
		if ev.Script != "" {
			h.scripts[ev.Script] = phaseName(ev.Phase)
		}
		h.mu.Unlock()
	}

	// Ensure the worker goroutine has actually returned before we publish
	// the result. (close(progress) is deferred inside it, so by the time
	// the range loop exits the worker has already stored into `result`,
	// but we defensively wait to make race detectors happy.)
	workerWg.Wait()

	// Publish final state.
	h.mu.Lock()
	h.result = result
	h.progress = append(h.progress, AutostartProgress{
		ProjectPath: h.projectPath,
		Phase:       PhaseDone,
		Timestamp:   time.Now(),
	})
	h.mu.Unlock()

	// Release the context to avoid leaking the cancel func, then signal done.
	h.cancel()
	close(h.done)
}

// phaseName returns a stable short name for each phase. Used for the
// per-script scripts map on the handle.
func phaseName(p AutostartPhase) string {
	switch p {
	case PhaseScriptStarting:
		return "starting"
	case PhaseDependencyWaitStart:
		return "waiting_dep"
	case PhaseDependencyReady:
		return "dep_ready"
	case PhaseScriptStarted:
		return "started"
	case PhaseScriptFailed:
		return "failed"
	case PhaseLayerComplete:
		return "layer_complete"
	case PhaseDone:
		return "done"
	default:
		return "unknown"
	}
}
