// Package daemon — health_tracker.go
//
// HealthTracker observes ManagedProcess state edges lazily and tracks the
// last time each process transitioned into the Running state. It also drives
// the proxy "error stream suppression window": when a proxy's linked process
// is in a transient/unhealthy state (Starting/Stopping/Failed) or is within
// a 5s grace period after returning to Running, the daemon suppresses the
// proxy → AlertHub broadcast path so the AI agent does not see the
// rebuild-burst noise. Entries are still written to the TrafficLogger ring
// buffer, so `proxylog query` continues to surface them on demand.
//
// Design notes:
//
//   - Lazy edge detection. We do not register state-transition callbacks on
//     ManagedProcess (the vendored process package does not expose any), so
//     the tracker observes state via a normal atomic Load on every check
//     and remembers the last observed state per process. When the observed
//     state differs from the remembered state, we synthesise a transition
//     event: emit a diagnostic marker (open/close), and on transition INTO
//     Running update lastHealthyAt to time.Now().
//
//   - The tracker is the source of truth for "edges". The diagnostic
//     markers fire exactly once per edge because the remembered state is
//     overwritten atomically inside the same critical section that emits
//     the marker. Repeated calls in the same state are idempotent.
//
//   - Lock-free hot path. The check itself uses sync.Map.Load and atomic
//     pointer reads. Only the slow path (state edge detected) takes a
//     per-process mutex to coordinate the marker emission with the
//     lastHealthyAt update.
//
//   - No vendored package changes. lastHealthyAt lives in the daemon
//     side-table keyed by processID, populated by lazy observation. This is
//     intentionally Option (b) from the task description.

package daemon

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	goprocess "github.com/standardbeagle/go-cli-server/process"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// SuppressionGracePeriod is the time after a process returns to Running
// during which its linked proxies still suppress error broadcasts. This
// covers the race window where the browser retries failed fetches a few
// hundred ms after the new process binds.
const SuppressionGracePeriod = 5 * time.Second

// processHealthState is the per-process tracker entry. All fields are
// guarded by mu, but the common path (suppression check on a stable
// Running state) reads via atomic loads and avoids the lock entirely.
type processHealthState struct {
	mu sync.Mutex

	// lastObservedState is the most recent goprocess.ProcessState observed
	// for this process. Stored as uint32 so we can read it lock-free.
	lastObservedState atomic.Uint32

	// previousObservedState is the state the tracker observed immediately
	// before lastObservedState. Used by OutageClassifier to detect direct
	// Running → Failed transitions (no Stopping intermediate).
	previousObservedState atomic.Uint32

	// lastHealthyAt is the wall-clock time the process most recently
	// transitioned INTO Running. nil if it has never reached Running.
	lastHealthyAt atomic.Pointer[time.Time]

	// suppressionOpen reflects whether we have currently emitted an "open"
	// marker without a matching "close". Used to make markers fire exactly
	// once per edge.
	suppressionOpen atomic.Bool

	// outageStartedAt is the wall-clock time the process most recently
	// LEFT the Running state into a suppress state. nil while the process
	// is healthy. Used by OutageClassifier to compute outage duration.
	outageStartedAt atomic.Pointer[time.Time]

	// daemonInitiatedStop is set to true by daemon code immediately before
	// triggering a stop/restart on a process (RestartAll, auto-restart,
	// autostart recovery). Cleared by the classifier when the process
	// returns to Running Healthy. While set, an outage is biased toward
	// "Rebuild" classification.
	daemonInitiatedStop atomic.Bool

	// lastRebuildSignalAt is the most recent timestamp the AlertScanner
	// matched a rebuild/compile pattern in the process output. Used as a
	// secondary signal to bias outage classification toward Rebuild.
	lastRebuildSignalAt atomic.Pointer[time.Time]
}

// HealthTracker observes process state for the purpose of gating proxy
// error broadcasts. It is owned by the Daemon and populated lazily.
type HealthTracker struct {
	// states maps processID → *processHealthState. sync.Map is preferred
	// because the map is overwhelmingly read-only on the hot path; entries
	// are only created on first observation of a process.
	states sync.Map

	// procLookup resolves a processID to a *ManagedProcess. In production
	// this is bound to d.hub.ProcessManager().Get; tests can supply a stub.
	procLookup func(processID string) (*goprocess.ManagedProcess, error)

	// emitDiagnostic is called when the suppression window opens or closes.
	// In production this routes to the AlertHub diagnostic path. Tests can
	// supply a spy. May be nil — emission is a best-effort signal.
	emitDiagnostic func(entry proxy.LogEntry, proxyID string)

	// nowFn returns the current time. Injected for deterministic tests.
	nowFn func() time.Time

	// onOutageStart is fired exactly once per Running → suppress edge,
	// inside the slow-path mutex. The classifier sets this to populate
	// its crash-rate ring buffer. May be nil — emission is best-effort.
	onOutageStart func(processID string, ts time.Time)

	// onReturnToHealthy is fired exactly once per suppress → Running
	// edge, inside the slow-path mutex. The classifier sets this to
	// reset per-outage one-shot markers (e.g. "expired warning").
	onReturnToHealthy func(processID string)

	// onProcessRunning fires on every any-state → Running transition.
	// Separate from onReturnToHealthy so daemon and OutageClassifier register independently.
	// Used to fire on-start lifecycle hooks.
	onProcessRunning func(processID string)
}

// NewHealthTracker constructs a HealthTracker with production lookups.
// Either argument may be nil for tests that supply their own.
func NewHealthTracker(procLookup func(string) (*goprocess.ManagedProcess, error), emit func(proxy.LogEntry, string)) *HealthTracker {
	return &HealthTracker{
		procLookup:     procLookup,
		emitDiagnostic: emit,
		nowFn:          time.Now,
	}
}

// IsInSuppressionWindow returns true when proxyID's linked process is in a
// transient/unhealthy state and its error broadcasts should be suppressed.
//
// Behaviour matrix (see also .claude/rules/daemon-lifecycle.md):
//
//	Process state            broadcast?
//	Pending                  yes (proxy hasn't seen its first start yet)
//	Starting                 no  (suppress: rebuild in progress)
//	Running, within grace    no  (suppress: 5s post-return-to-healthy)
//	Running, past grace      yes
//	Stopping                 no  (suppress: restart triggered)
//	Failed                   no  (suppress: brief failure window)
//	Stopped                  yes (Dead — errors ARE the story)
//
// linkedProcessID may be empty for unlinked proxies, in which case the
// check returns false unconditionally (no suppression).
//
// This call is on the hot path of every proxy log entry. It is lock-free
// for the steady-state Running case (just two atomic loads). The slow
// path runs only when an edge is detected, and is bounded by a per-process
// mutex (no global locks).
func (h *HealthTracker) IsInSuppressionWindow(proxyID, linkedProcessID string) bool {
	if h == nil || linkedProcessID == "" {
		return false
	}
	if h.procLookup == nil {
		return false
	}

	proc, err := h.procLookup(linkedProcessID)
	if err != nil || proc == nil {
		// Process was removed (e.g. cleaned up between lookups). Do not
		// suppress — the proxy is effectively unlinked at this moment.
		return false
	}

	currentState := proc.State()

	// Observe edges and update bookkeeping. The fast path is the common
	// case where currentState equals the last observed state; in that
	// case we skip the per-process mutex entirely.
	st := h.observe(proxyID, linkedProcessID, currentState)

	switch currentState {
	case goprocess.StatePending:
		// Process hasn't been started yet; not a rebuild scenario.
		return false
	case goprocess.StateStarting, goprocess.StateStopping, goprocess.StateFailed:
		return true
	case goprocess.StateRunning:
		// Honour the grace window. lastHealthyAt is set by observe() on
		// the transition INTO Running. If the pointer is nil for any
		// reason, fall back to "no suppression" — better to leak a few
		// errors than to silence a healthy process.
		lastHealthy := st.lastHealthyAt.Load()
		if lastHealthy == nil {
			return false
		}
		return h.nowFn().Sub(*lastHealthy) < SuppressionGracePeriod
	case goprocess.StateStopped:
		// "Dead" — errors ARE the story. Don't suppress.
		return false
	default:
		return false
	}
}

// observe records the current state for a process and emits suppression
// open/close markers on transitions. Returns the per-process state struct
// so the caller can read lastHealthyAt without re-loading it.
func (h *HealthTracker) observe(proxyID, processID string, currentState goprocess.ProcessState) *processHealthState {
	st := h.getOrCreate(processID)

	prevState := goprocess.ProcessState(st.lastObservedState.Load())
	if prevState == currentState {
		// Steady state — fast path. No transition to record.
		return st
	}

	// Slow path: transition detected. Take the per-process mutex to make
	// the state update + marker emission atomic relative to other observers.
	st.mu.Lock()
	defer st.mu.Unlock()

	// Re-check inside the lock. Another goroutine may have observed the
	// same edge concurrently; whoever wins the CAS owns the emission.
	prevState = goprocess.ProcessState(st.lastObservedState.Load())
	if prevState == currentState {
		return st
	}

	// Record the new state up-front so concurrent observers see it.
	// previousObservedState carries the prior state so the classifier
	// can detect direct Running → Failed transitions.
	st.previousObservedState.Store(uint32(prevState))
	st.lastObservedState.Store(uint32(currentState))

	// On transition INTO Running, stamp lastHealthyAt, clear the outage
	// start marker, and consume the daemon-initiated flag. The grace
	// period is honoured by the caller.
	returnedToHealthy := false
	if currentState == goprocess.StateRunning && prevState != goprocess.StateRunning {
		now := h.nowFn()
		st.lastHealthyAt.Store(&now)
		// Clear the outage marker — we're back to healthy. The classifier
		// considers the outage "concluded" and any future stop starts a
		// fresh outage with a fresh daemon-initiated decision.
		st.outageStartedAt.Store(nil)
		st.daemonInitiatedStop.Store(false)
		returnedToHealthy = true
	}

	// On transition OUT of Running into a non-healthy state, stamp the
	// outage start time. We use the prevState check (rather than a "was
	// healthy" predicate) because Pending → Starting is a normal startup,
	// not an outage. Only Running → suppress counts.
	outageStarted := false
	var outageStartTime time.Time
	if prevState == goprocess.StateRunning && currentState != goprocess.StateRunning {
		outageStartTime = h.nowFn()
		st.outageStartedAt.Store(&outageStartTime)
		outageStarted = true
	}

	// Fire edge callbacks under the per-process lock so the classifier's
	// ring buffer and one-shot flags update atomically with the tracker
	// bookkeeping. Recover from any panic — the gate hot path must not
	// take down the daemon if a callback misbehaves.
	if outageStarted && h.onOutageStart != nil {
		func() {
			defer func() { _ = recover() }()
			h.onOutageStart(processID, outageStartTime)
		}()
	}
	if returnedToHealthy && h.onReturnToHealthy != nil {
		func() {
			defer func() { _ = recover() }()
			h.onReturnToHealthy(processID)
		}()
	}
	if returnedToHealthy && h.onProcessRunning != nil {
		func() {
			defer func() { _ = recover() }()
			h.onProcessRunning(processID)
		}()
	}

	// Decide whether to emit a marker. We emit "open" when entering a
	// suppress state and we were not already suppressed, and "close" when
	// leaving a suppress state and we were previously suppressed.
	wasSuppressed := isSuppressState(prevState)
	nowSuppressed := isSuppressState(currentState)

	// Special case: transition INTO Running counts as the start of the
	// grace window. We only emit "close" once the grace expires, not at
	// the transition itself, so the AI agent sees the resumption marker
	// at the same instant errors actually start flowing again. To do
	// that lazily we leave suppressionOpen set; the close marker is
	// emitted by emitCloseIfGraceExpired (called by the gate).
	if !wasSuppressed && nowSuppressed && !st.suppressionOpen.Load() {
		st.suppressionOpen.Store(true)
		h.emit(proxyID, openMarkerMessage(processID, currentState), proxy.DiagnosticInfo, "stream_suppressed")
	}

	// If we left a suppress state directly into Stopped (no grace), close
	// the window immediately.
	if wasSuppressed && currentState == goprocess.StateStopped && st.suppressionOpen.Load() {
		st.suppressionOpen.Store(false)
		h.emit(proxyID, closeMarkerMessage(processID), proxy.DiagnosticInfo, "stream_resumed")
	}

	return st
}

// MaybeCloseGraceWindow inspects the linked process and, if it is now
// Running with the grace period expired, emits the close marker. This is
// called by the broadcast gate just before allowing an entry through, so
// the marker reaches the agent at the same instant suppression actually
// stops. Idempotent — only emits once per close edge.
func (h *HealthTracker) MaybeCloseGraceWindow(proxyID, linkedProcessID string) {
	if h == nil || linkedProcessID == "" {
		return
	}
	st, ok := h.lookup(linkedProcessID)
	if !ok {
		return
	}
	if !st.suppressionOpen.Load() {
		return
	}
	if goprocess.ProcessState(st.lastObservedState.Load()) != goprocess.StateRunning {
		return
	}
	lastHealthy := st.lastHealthyAt.Load()
	if lastHealthy == nil {
		return
	}
	if h.nowFn().Sub(*lastHealthy) < SuppressionGracePeriod {
		return
	}
	// Take the lock to avoid double-emission with concurrent observers.
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.suppressionOpen.Load() {
		return
	}
	st.suppressionOpen.Store(false)
	h.emit(proxyID, closeMarkerMessage(linkedProcessID), proxy.DiagnosticInfo, "stream_resumed")
}

// Forget drops tracking for a process. Called when the daemon cleans up a
// script (ScriptStopped event). Bounded by sync.Map.Delete.
func (h *HealthTracker) Forget(processID string) {
	if h == nil {
		return
	}
	h.states.Delete(processID)
}

// MarkDaemonInitiatedStop records that the daemon (not the OS, not the
// child) is about to stop or restart processID. The flag is consumed by
// the OutageClassifier — while set, a subsequent outage is biased toward
// "Rebuild" rather than "Crash". Callers MUST set this immediately before
// issuing the stop, never after, otherwise the classifier may observe the
// stop edge before the flag is set.
//
// Safe to call before any state has been observed for the process: this
// creates the per-process tracker entry on demand.
func (h *HealthTracker) MarkDaemonInitiatedStop(processID string) {
	if h == nil || processID == "" {
		return
	}
	st := h.getOrCreate(processID)
	st.daemonInitiatedStop.Store(true)
}

// IsDaemonInitiatedStop reports whether MarkDaemonInitiatedStop was called
// for processID and the flag has not been consumed yet.
func (h *HealthTracker) IsDaemonInitiatedStop(processID string) bool {
	if h == nil || processID == "" {
		return false
	}
	st, ok := h.lookup(processID)
	if !ok {
		return false
	}
	return st.daemonInitiatedStop.Load()
}

// RecordRebuildSignal stamps the current time as the most recent moment
// the AlertScanner detected a rebuild/compile pattern in processID's
// output. Read by the OutageClassifier as evidence the next stop edge is
// part of an in-progress rebuild rather than a crash.
func (h *HealthTracker) RecordRebuildSignal(processID string) {
	if h == nil || processID == "" {
		return
	}
	st := h.getOrCreate(processID)
	now := h.nowFn()
	st.lastRebuildSignalAt.Store(&now)
}

// LastRebuildSignal returns the most recent rebuild-signal timestamp for
// processID, or the zero time if none has been recorded.
func (h *HealthTracker) LastRebuildSignal(processID string) time.Time {
	if h == nil || processID == "" {
		return time.Time{}
	}
	st, ok := h.lookup(processID)
	if !ok {
		return time.Time{}
	}
	t := st.lastRebuildSignalAt.Load()
	if t == nil {
		return time.Time{}
	}
	return *t
}

// LastHealthyAt returns the wall-clock time processID most recently
// transitioned INTO Running, or the zero time if it never has.
func (h *HealthTracker) LastHealthyAt(processID string) time.Time {
	if h == nil || processID == "" {
		return time.Time{}
	}
	st, ok := h.lookup(processID)
	if !ok {
		return time.Time{}
	}
	t := st.lastHealthyAt.Load()
	if t == nil {
		return time.Time{}
	}
	return *t
}

// OutageStartedAt returns the wall-clock time processID most recently
// LEFT the Running state, or the zero time if it is currently healthy or
// has never been observed.
func (h *HealthTracker) OutageStartedAt(processID string) time.Time {
	if h == nil || processID == "" {
		return time.Time{}
	}
	st, ok := h.lookup(processID)
	if !ok {
		return time.Time{}
	}
	t := st.outageStartedAt.Load()
	if t == nil {
		return time.Time{}
	}
	return *t
}

// LastObservedState returns the most recent process state observed by
// the tracker. Returns the zero value (StatePending) if no state has been
// observed yet — the caller should treat that as "unknown".
func (h *HealthTracker) LastObservedState(processID string) goprocess.ProcessState {
	if h == nil || processID == "" {
		return goprocess.StatePending
	}
	st, ok := h.lookup(processID)
	if !ok {
		return goprocess.StatePending
	}
	return goprocess.ProcessState(st.lastObservedState.Load())
}

// PreviousObservedState returns the state observed immediately before
// the current LastObservedState. Used by the classifier to detect direct
// Running → Failed transitions without a Stopping intermediate. Returns
// StatePending if the process has never transitioned.
func (h *HealthTracker) PreviousObservedState(processID string) goprocess.ProcessState {
	if h == nil || processID == "" {
		return goprocess.StatePending
	}
	st, ok := h.lookup(processID)
	if !ok {
		return goprocess.StatePending
	}
	return goprocess.ProcessState(st.previousObservedState.Load())
}

func (h *HealthTracker) getOrCreate(processID string) *processHealthState {
	if existing, ok := h.states.Load(processID); ok {
		return existing.(*processHealthState)
	}
	fresh := &processHealthState{}
	actual, _ := h.states.LoadOrStore(processID, fresh)
	return actual.(*processHealthState)
}

func (h *HealthTracker) lookup(processID string) (*processHealthState, bool) {
	v, ok := h.states.Load(processID)
	if !ok {
		return nil, false
	}
	return v.(*processHealthState), true
}

// emit routes a diagnostic marker through the configured emitter. Markers
// always bypass the suppression gate by construction — the gate only
// inspects entry.Type for non-diagnostic types (see broadcast_gate.go).
func (h *HealthTracker) emit(proxyID, message string, level proxy.ProxyDiagnosticLevel, event string) {
	if h.emitDiagnostic == nil {
		return
	}
	// Recover from any panic in the emitter — this runs on the hot log
	// path and must never take down the daemon.
	defer func() {
		_ = recover()
	}()
	entry := proxy.LogEntry{
		Type: proxy.LogTypeDiagnostic,
		Diagnostic: &proxy.ProxyDiagnostic{
			Level:     level,
			Category:  "suppression",
			Event:     event,
			Message:   message,
			Timestamp: h.nowFn(),
		},
	}
	h.emitDiagnostic(entry, proxyID)
}

func isSuppressState(s goprocess.ProcessState) bool {
	switch s {
	case goprocess.StateStarting, goprocess.StateStopping, goprocess.StateFailed:
		return true
	default:
		return false
	}
}

func openMarkerMessage(processID string, state goprocess.ProcessState) string {
	return fmt.Sprintf("proxy error stream suppressed: process %s %s", processID, state)
}

func closeMarkerMessage(processID string) string {
	return fmt.Sprintf("proxy error stream resumed: process %s healthy", processID)
}
