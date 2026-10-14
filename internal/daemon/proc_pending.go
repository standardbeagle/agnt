// Package daemon — pending-process tracker.
//
// PendingProcessTracker records processes that are waiting on declared
// `depends_on` dependencies before launching. The tracker is the single
// surface that PROC STATUS / PROC LIST consult to populate the
// `waiting_for` field while a process is gated.
//
// Lifecycle:
//
//	hubHandleProcRun (with deps)
//	  → tracker.Register(processID, deps, deadline)
//	  → tracker.MarkWaiting(processID, "depA")
//	  → goroutine waits on readySignaler.WaitReadyCtx(deps...)
//	    → on ready: tracker.MarkReady(processID)        // remaining deps shrink
//	    → on timeout: tracker.MarkFailed(processID, dep) // dependency_timeout:dep
//	  → on all deps ready: StartScriptExplicit
//	    → tracker.Remove(processID)
//
// The tracker is independent of the ProcessManager state machine because
// a process that has not been launched yet has no ProcessManager entry.
// `proc list` merges tracker entries (kind=pending) into its response so
// agents can see "api waiting_for [db, redis]" before the api process
// exists in the ProcessManager.
//
// All operations are safe for concurrent use. Lookups are O(1) via map
// access; the registry is sized in the low hundreds at most so a single
// RWMutex suffices.

package daemon

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// PendingProcessState describes the lifecycle of a pending process.
type PendingProcessState int

const (
	// PendingWaiting means the process is gated on at least one dependency.
	PendingWaiting PendingProcessState = iota
	// PendingFailed means a dependency timed out or the process never launched.
	PendingFailed
)

func (s PendingProcessState) String() string {
	switch s {
	case PendingWaiting:
		return "waiting"
	case PendingFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// PendingProcess captures the wire-visible fields a pending process exposes
// through PROC STATUS / PROC LIST while it is gated on dependencies.
type PendingProcess struct {
	// ProcessID is the daemon-side identifier (project_path + name).
	ProcessID string
	// Name is the script-style name passed to PROC RUN.
	Name string
	// ProjectPath is the normalised project path the process belongs to.
	ProjectPath string
	// Command is the resolved command string (for visibility in proc list).
	Command string
	// WaitingFor is the set of dependency names the process is still
	// waiting for. Sorted for stable output.
	WaitingFor []string
	// Deadline is the per-process timeout deadline. Zero means no deadline.
	Deadline time.Time
	// State distinguishes "still waiting" from "dependency timed out".
	State PendingProcessState
	// FailureReason is the human-readable reason set when State is
	// PendingFailed. Format: "dependency_timeout:<dep-name>" (the dependency
	// never signalled ready in time), "dependency_failed:<dep-name>" (the
	// dependency failed to start), or "dependency_timeout:start" (the launch
	// itself failed once the dependencies were ready).
	FailureReason string
	// CreatedAt is when the entry was registered.
	CreatedAt time.Time
}

// PendingProcessTracker is the registry of processes waiting on dependencies.
type PendingProcessTracker struct {
	mu      sync.RWMutex
	entries map[string]*pendingEntry
}

type pendingEntry struct {
	mu sync.Mutex
	// snapshot mirrors the PendingProcess struct returned by Get / List.
	// All mutations go through pendingEntry's mutex, then through the
	// tracker's mutex when registry-level state changes (add/remove).
	snapshot PendingProcess
	// remaining tracks the in-flight dependency wait set as a map for
	// O(1) removal via MarkReady. WaitingFor in the snapshot is rebuilt
	// from this map under the entry mutex.
	remaining map[string]struct{}
	// cancel stops the wait goroutine that will launch this process. Nil for
	// entries registered without one (tests, display-only entries).
	cancel context.CancelFunc
	// superseded is set when a newer RegisterLaunch for the same processID
	// cancelled this one. The replacement owns the process from then on, so
	// the superseded goroutine must not stop what it may have started.
	superseded atomic.Bool
}

// wasSuperseded reports whether this launch was cancelled by a replacement
// launch rather than by an explicit Cancel (PROC STOP, session cleanup).
func (e *pendingEntry) wasSuperseded() bool { return e.superseded.Load() }

// NewPendingProcessTracker returns a ready-to-use tracker.
func NewPendingProcessTracker() *PendingProcessTracker {
	return &PendingProcessTracker{
		entries: make(map[string]*pendingEntry),
	}
}

// Register adds processID to the tracker as waiting on the given deps.
// Idempotent: re-registering an existing process replaces the entry. The
// caller is responsible for spawning the wait goroutine.
//
// Returns the snapshot of the registered entry.
func (t *PendingProcessTracker) Register(p PendingProcess, deps []string) PendingProcess {
	entry := t.RegisterLaunch(p, deps, nil)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return cloneSnapshot(entry.snapshot)
}

// RegisterLaunch is Register for a real deferred launch: cancel is the
// cancellation handle of the goroutine that will start the process once its
// dependencies are ready. The tracker owns that handle from here on — Cancel
// (PROC STOP, session cleanup) invokes it, and re-registering the same
// processID cancels the launch it replaces so two goroutines never race to
// start one process.
//
// The returned entry is the launch's identity: the goroutine reports progress
// through it and retires it with RemoveExact, so a superseded goroutine can
// never mutate or delete its replacement.
func (t *PendingProcessTracker) RegisterLaunch(p PendingProcess, deps []string, cancel context.CancelFunc) *pendingEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	remaining := make(map[string]struct{}, len(deps))
	for _, d := range deps {
		if d == "" {
			continue
		}
		remaining[d] = struct{}{}
	}
	waitingFor := sortedKeys(remaining)

	entry := &pendingEntry{
		snapshot: PendingProcess{
			ProcessID:   p.ProcessID,
			Name:        p.Name,
			ProjectPath: p.ProjectPath,
			Command:     p.Command,
			WaitingFor:  waitingFor,
			Deadline:    p.Deadline,
			State:       PendingWaiting,
			CreatedAt:   time.Now(),
		},
		remaining: remaining,
		cancel:    cancel,
	}
	if prev, ok := t.entries[p.ProcessID]; ok && prev.cancel != nil {
		prev.superseded.Store(true)
		prev.cancel()
	}
	t.entries[p.ProcessID] = entry
	return entry
}

// Cancel stops the deferred launch for processID and removes its entry.
// Returns false if processID has no pending entry. After Cancel the wait
// goroutine will not launch the process; if it had already begun launching,
// it stops what it started (see runProcessAfterDeps).
func (t *PendingProcessTracker) Cancel(processID string) bool {
	t.mu.Lock()
	entry, ok := t.entries[processID]
	delete(t.entries, processID)
	t.mu.Unlock()
	if !ok {
		return false
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	return true
}

// RemoveExact deletes entry only if it is still the registered entry for its
// processID — a replacement registered under the same ID is left alone.
func (t *PendingProcessTracker) RemoveExact(entry *pendingEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries[entry.snapshot.ProcessID] == entry {
		delete(t.entries, entry.snapshot.ProcessID)
	}
}

// markReady removes dep from this launch's remaining wait set.
func (e *pendingEntry) markReady(dep string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.remaining, dep)
	e.snapshot.WaitingFor = sortedKeys(e.remaining)
}

// fail transitions this launch to PendingFailed with the given reason, e.g.
// "dependency_timeout:<dep>" or "dependency_failed:<dep>".
func (e *pendingEntry) fail(reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.snapshot.State = PendingFailed
	e.snapshot.FailureReason = reason
}

// MarkReady removes dep from the remaining wait set for processID. If
// processID is not registered, the call is a no-op. Returns the count of
// remaining dependencies after removal (0 means all deps are satisfied).
func (t *PendingProcessTracker) MarkReady(processID, dep string) int {
	t.mu.RLock()
	entry, ok := t.entries[processID]
	t.mu.RUnlock()
	if !ok {
		return 0
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()
	delete(entry.remaining, dep)
	entry.snapshot.WaitingFor = sortedKeys(entry.remaining)
	return len(entry.remaining)
}

// MarkFailed transitions processID to PendingFailed with reason
// "dependency_timeout:<dep>". The entry remains in the registry so
// PROC STATUS can return the failure reason; callers should call Remove
// once the failure has been surfaced (e.g., once the dependent failure
// has been folded into the script registry as StateFailed).
func (t *PendingProcessTracker) MarkFailed(processID, dep string) {
	t.mu.RLock()
	entry, ok := t.entries[processID]
	t.mu.RUnlock()
	if !ok {
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.snapshot.State = PendingFailed
	entry.snapshot.FailureReason = "dependency_timeout:" + dep
}

// Remove deletes processID from the tracker. Safe to call on an unknown
// processID.
func (t *PendingProcessTracker) Remove(processID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, processID)
}

// Get returns a snapshot of processID's pending state, or false if the
// process is not in the tracker.
func (t *PendingProcessTracker) Get(processID string) (PendingProcess, bool) {
	t.mu.RLock()
	entry, ok := t.entries[processID]
	t.mu.RUnlock()
	if !ok {
		return PendingProcess{}, false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return cloneSnapshot(entry.snapshot), true
}

// ListByProject returns snapshots of all pending processes whose
// ProjectPath matches. Pass an empty string to return all entries.
//
// The result is freshly allocated; the tracker retains no reference.
func (t *PendingProcessTracker) ListByProject(projectPath string) []PendingProcess {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]PendingProcess, 0, len(t.entries))
	for _, entry := range t.entries {
		entry.mu.Lock()
		match := projectPath == "" || entry.snapshot.ProjectPath == projectPath
		if match {
			out = append(out, cloneSnapshot(entry.snapshot))
		}
		entry.mu.Unlock()
	}
	// Stable sort by ProcessID for deterministic output.
	sort.Slice(out, func(i, j int) bool { return out[i].ProcessID < out[j].ProcessID })
	return out
}

// sortedKeys returns the keys of m sorted alphabetically.
func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cloneSnapshot returns a deep copy of a PendingProcess so callers
// cannot mutate tracker-internal state via slice aliasing.
func cloneSnapshot(p PendingProcess) PendingProcess {
	out := p
	if len(p.WaitingFor) > 0 {
		out.WaitingFor = append([]string(nil), p.WaitingFor...)
	}
	return out
}
