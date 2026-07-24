// Package daemon — outage_classifier.go
//
// OutageClassifier extends HealthTracker by classifying every process
// outage as either Healthy, Rebuild, LongRebuild, ExpiredRebuild, or
// Crash. The classification drives a tri-state SuppressionMode that the
// proxyBroadcastGate consults to decide whether to forward a proxy log
// entry to the EventHub.
//
// Classification rules (see also the Dart task description):
//
//	Healthy          process is Running and past the grace window
//	Rebuild          short outage (<RebuildShortWindow), evidence of intentional restart
//	LongRebuild      ongoing outage between RebuildShortWindow and RebuildLongWindow
//	ExpiredRebuild   ongoing outage past RebuildLongWindow — give up suppressing
//	Crash            non-zero exit + not daemon-initiated, OR direct Running→Failed,
//	                 OR restart rate exceeds the rate limit
//
// The "evidence of intentional restart" bias means at least ONE of:
//   - HealthTracker.IsDaemonInitiatedStop returns true (daemon set the flag
//     before stopping)
//   - HealthTracker.LastRebuildSignal is within RebuildSignalGrace of the
//     outage start (the AlertScanner saw a "rebuilding"/"compiling" line
//     just before the stop)
//
// Without that bias, an outage is treated as a Crash by default — the
// safer assumption is that the process died unexpectedly.
//
// Suppression mode mapping:
//
//	Healthy           → ModeOff             (broadcast normally)
//	Rebuild           → ModeFull            (drop all errors)
//	LongRebuild       → ModeDiagnosticOnly  (drop errors, keep warnings)
//	ExpiredRebuild    → ModeOff             (give up; resume errors)
//	Crash             → ModeOff             (broadcast immediately)
//
// The hot path is SuppressionMode(processID), called once per proxy log
// entry by proxyBroadcastGate. It reads only atomics on the tracker side,
// plus an O(1) restart-rate check on a small ring of timestamps.

package health

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"

	goprocess "github.com/standardbeagle/go-cli-server/process"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// SuppressionMode is the tri-state result of the classifier — what the
// gate should do for a proxy linked to this process.
type SuppressionMode int

const (
	// ModeOff broadcasts every entry normally.
	ModeOff SuppressionMode = iota
	// ModeFull drops everything except diagnostic entries.
	ModeFull
	// ModeDiagnosticOnly drops error-class entries and forwards
	// warnings, info, and diagnostics. The TrafficLogger only
	// distinguishes error vs non-error today; in practice this collapses
	// to ModeFull for HTTP and Error log types but allows custom
	// warn-level entries through. See proxyBroadcastGate for the exact
	// classification.
	ModeDiagnosticOnly
)

// String returns a human-readable mode name for diagnostics.
func (m SuppressionMode) String() string {
	switch m {
	case ModeOff:
		return "off"
	case ModeFull:
		return "full"
	case ModeDiagnosticOnly:
		return "diagnostic-only"
	default:
		return "unknown"
	}
}

// OutageType categorises a process outage. The set is closed.
type OutageType int

const (
	// OutageHealthy means the process is Running and past the grace window.
	OutageHealthy OutageType = iota
	// OutageRebuild is a short outage (<RebuildShortWindow) with rebuild evidence.
	OutageRebuild
	// OutageLongRebuild is an ongoing outage in RebuildShortWindow..RebuildLongWindow.
	OutageLongRebuild
	// OutageExpiredRebuild is an ongoing outage past RebuildLongWindow.
	OutageExpiredRebuild
	// OutageCrash is everything else: unexpected exits, direct Running→Failed,
	// or chronic restart loops.
	OutageCrash
)

// String returns a human-readable name for diagnostics.
func (o OutageType) String() string {
	switch o {
	case OutageHealthy:
		return "healthy"
	case OutageRebuild:
		return "rebuild"
	case OutageLongRebuild:
		return "long-rebuild"
	case OutageExpiredRebuild:
		return "expired-rebuild"
	case OutageCrash:
		return "crash"
	default:
		return "unknown"
	}
}

// Tunables. These are package-level vars rather than constants so tests
// can shrink them, but production code never modifies them.
var (
	// RebuildShortWindow is the maximum outage duration that still
	// qualifies as a fast rebuild. Outages longer than this transition
	// to LongRebuild.
	RebuildShortWindow = 15 * time.Second

	// RebuildLongWindow is the absolute upper bound on rebuild
	// suppression. Past this point the classifier returns
	// OutageExpiredRebuild and the gate stops suppressing.
	RebuildLongWindow = 30 * time.Second

	// RebuildSignalGrace is the lookback window for AlertScanner rebuild
	// signals. A rebuild pattern matched within this many seconds before
	// the outage start is treated as evidence of an intentional restart.
	RebuildSignalGrace = 5 * time.Second

	// CrashRateLimit and CrashRateWindow define the chronic-crash check.
	// More than CrashRateLimit outages within CrashRateWindow forces a
	// crash classification regardless of other signals (matches the
	// auto-restarter's rate limit by default).
	CrashRateLimit  = 5
	CrashRateWindow = time.Minute

	// LongRebuildHeartbeat is how often the long-rebuild diagnostic
	// emitter fires while suppression is active in the LongRebuild band.
	LongRebuildHeartbeat = 10 * time.Second
)

// crashHistorySize bounds the per-process crash-rate ring buffer. It is
// sized to CrashRateLimit + 1 so a full window fits with one slot to
// spare for the most recent entry.
const crashHistorySize = 8

// classifierProcessState is the per-process side-table the classifier
// owns. HealthTracker owns the per-process state edges; this struct
// holds only what the classifier needs on top.
type classifierProcessState struct {
	// crashTimestamps is a small lock-protected ring of recent outage
	// start timestamps used to compute restart rate. Mutex is fine
	// because the classifier only takes it on the slow path (outage
	// onset and rate query) — never on the steady-state hot path.
	mu              sync.Mutex
	crashTimestamps []time.Time

	// longRebuildTimer is the per-process AfterFunc that emits
	// long-rebuild heartbeat diagnostics. nil while no heartbeat is
	// active. Replaced atomically when (re)started; tests can swap the
	// underlying time source via the classifier nowFn.
	longRebuildTimer atomic.Pointer[time.Timer]

	// expiredEmitted is set once when the classifier first transitions
	// to OutageExpiredRebuild for this outage, so the "rebuild exceeded
	// 30s" warning fires exactly once per outage.
	expiredEmitted atomic.Bool
}

// OutageClassifier wraps HealthTracker with classification + the per-
// process side-table. Construction always pairs the two — the classifier
// is useless without a tracker.
type OutageClassifier struct {
	tracker *HealthTracker
	states  sync.Map // processID → *classifierProcessState

	// procLookup resolves a processID to a *ManagedProcess so we can
	// read ExitCode() and the current State. Mirrors HealthTracker's
	// own lookup field — we keep a separate handle so tests can swap
	// it without touching the tracker.
	procLookup func(processID string) (*goprocess.ManagedProcess, error)

	// emitDiagnostic routes long-rebuild heartbeats and the
	// expired-rebuild warning to the EventHub. May be nil for tests
	// that don't care about emission.
	emitDiagnostic func(entry proxy.LogEntry, proxyID string)

	// proxyForProcess maps a processID back to its linked proxy ID so
	// the heartbeat emitter knows which proxy to address. Returns
	// empty string when no proxy is linked. Must never block.
	proxyForProcess func(processID string) string

	// nowFn returns the current time. Injected for deterministic tests.
	nowFn func() time.Time
}

// NewOutageClassifier wires a classifier on top of an existing
// HealthTracker. All function arguments may be nil for tests; in that
// case the classifier degrades to "always healthy" / no emission.
//
// The classifier registers callbacks on the tracker so that outage edges
// observed by the tracker hot path automatically populate the classifier
// crash-rate ring buffer and reset one-shot markers. The tracker MUST
// not have any other classifier already attached.
func NewOutageClassifier(
	tracker *HealthTracker,
	procLookup func(string) (*goprocess.ManagedProcess, error),
	emit func(proxy.LogEntry, string),
	proxyForProcess func(string) string,
) *OutageClassifier {
	c := &OutageClassifier{
		tracker:         tracker,
		procLookup:      procLookup,
		emitDiagnostic:  emit,
		proxyForProcess: proxyForProcess,
		nowFn:           time.Now,
	}
	if tracker != nil {
		tracker.onOutageStart = c.NoteOutageOnset
		tracker.onReturnToHealthy = c.resetOutageBookkeeping
	}
	return c
}

// Classify returns the OutageType for processID at the current instant.
// Reads the current process state, exit code, and tracker bookkeeping;
// no side effects beyond updating the crash-rate ring on the slow path.
//
// Returns OutageHealthy if the classifier or tracker is nil, the
// process can't be found, or the process is past its grace window.
// "Don't suppress" is the safer default for all error cases.
func (c *OutageClassifier) Classify(processID string) OutageType {
	if c == nil || c.tracker == nil || processID == "" || c.procLookup == nil {
		return OutageHealthy
	}

	proc, err := c.procLookup(processID)
	if err != nil || proc == nil {
		return OutageHealthy
	}

	currentState := proc.State()

	// Healthy fast path: process is Running and past the grace window.
	if currentState == goprocess.StateRunning {
		lastHealthy := c.tracker.LastHealthyAt(processID)
		if lastHealthy.IsZero() {
			// Tracker hasn't observed an edge yet. Treat as healthy —
			// the gate's first call will populate this.
			return OutageHealthy
		}
		if c.nowFn().Sub(lastHealthy) >= SuppressionGracePeriod {
			return OutageHealthy
		}
		// Inside grace window — the gate still wants suppression. The
		// edge that just happened was Healthy → ... → Running, so this
		// is the tail of a rebuild. Fall through to outage classification
		// using outageStartedAt as the start of the cycle.
	}

	// Stopped is "dead — errors are the story". Surface immediately.
	if currentState == goprocess.StateStopped {
		return OutageCrash
	}

	// Rate-limit check: if this process has crashed too many times in the
	// rate window, force Crash regardless of other signals.
	if c.recentCrashCount(processID) > CrashRateLimit {
		return OutageCrash
	}

	// Direct Running → Failed transition (no Stopping intermediate) is
	// always a crash, regardless of the daemon-initiated flag — it
	// means the process died unexpectedly during a daemon-issued stop.
	// We use HealthTracker.PreviousObservedState which is updated each
	// time observe() runs, so by the time SuppressionMode is called
	// from the gate, prev/current already reflect the latest edge.
	if currentState == goprocess.StateFailed {
		prev := c.tracker.PreviousObservedState(processID)
		if prev == goprocess.StateRunning {
			return OutageCrash
		}
		if !c.tracker.IsDaemonInitiatedStop(processID) {
			// Failed via Stopping but not daemon-initiated = crash.
			return OutageCrash
		}
		// Daemon-initiated Failed via Stopping (e.g. autostart recovery
		// hit a real build error). Treat as a rebuild outage so the
		// agent gets a chance to react via the AlertScanner stream
		// rather than the proxy noise burst.
	}

	// Non-zero exit code on a stopped/failed process that the daemon
	// did not stop = crash.
	if currentState == goprocess.StateFailed || currentState == goprocess.StateStopped {
		if proc.ExitCode() > 0 && !c.tracker.IsDaemonInitiatedStop(processID) {
			return OutageCrash
		}
	}

	// At this point the outage is suppression-eligible. Compute its
	// duration to decide between Rebuild / LongRebuild / ExpiredRebuild.
	outageStarted := c.tracker.OutageStartedAt(processID)
	if outageStarted.IsZero() {
		// No outage marker yet. This happens on the very first call
		// when the process has been observed in a non-Running state
		// from the start (e.g. Pending → Starting, no Running edge
		// ever recorded). Treat as Rebuild — the gate's caller wants
		// to suppress during initial startup.
		return OutageRebuild
	}

	// If the outage looks like a rebuild (daemon-initiated OR rebuild
	// signal observed within RebuildSignalGrace before the outage), use
	// the standard window thresholds.
	if c.looksLikeRebuild(processID, outageStarted) {
		elapsed := c.nowFn().Sub(outageStarted)
		switch {
		case elapsed < RebuildShortWindow:
			return OutageRebuild
		case elapsed < RebuildLongWindow:
			return OutageLongRebuild
		default:
			return OutageExpiredRebuild
		}
	}

	// No rebuild evidence and the outage is suppression-eligible: this
	// is a crash that hasn't been classified yet (e.g. Stopping with
	// zero exit so far). Default to Crash so the agent doesn't miss it.
	return OutageCrash
}

// ClassifyProxy returns the OutageType for proxyID, taking the worse of:
//   - the linkedProcessID's process-state outage (existing Classify)
//   - the proxy's transport-signal outage (new HealthTracker bookkeeping)
//
// When the proxy is in synthetic transport outage but the process is
// healthy, the result is OutageRebuild. When the process is in a worse
// state (LongRebuild/ExpiredRebuild/Crash), the worse classification
// wins. linkedProcessID may be empty for unlinked proxies; in that case
// only the transport signal contributes.
func (c *OutageClassifier) ClassifyProxy(proxyID, linkedProcessID string) OutageType {
	if c == nil {
		return OutageHealthy
	}
	procOutage := OutageHealthy
	if linkedProcessID != "" {
		procOutage = c.Classify(linkedProcessID)
	}
	if c.tracker != nil && c.tracker.IsProxyInTransportOutage(proxyID) {
		return maxOutageType(procOutage, OutageRebuild)
	}
	return procOutage
}

// SuppressionModeProxy returns the gate decision for a proxy, considering
// both its linked process's state and any synthetic transport outage. The
// hot path is one ClassifyProxy plus a switch — same shape as SuppressionMode.
func (c *OutageClassifier) SuppressionModeProxy(proxyID, linkedProcessID string) SuppressionMode {
	if c == nil || proxyID == "" {
		return ModeOff
	}
	switch c.ClassifyProxy(proxyID, linkedProcessID) {
	case OutageRebuild:
		return ModeFull
	case OutageLongRebuild:
		if linkedProcessID != "" {
			c.startLongRebuildHeartbeat(linkedProcessID)
		}
		return ModeDiagnosticOnly
	case OutageExpiredRebuild:
		if linkedProcessID != "" {
			c.emitExpiredRebuildOnce(linkedProcessID)
			c.stopLongRebuildHeartbeat(linkedProcessID)
		}
		return ModeOff
	case OutageHealthy, OutageCrash:
		fallthrough
	default:
		if linkedProcessID != "" {
			c.stopLongRebuildHeartbeat(linkedProcessID)
		}
		return ModeOff
	}
}

// maxOutageType returns the more-severe of two outage types, ordered by
// suppression urgency: Healthy < Rebuild < LongRebuild < ExpiredRebuild
// < Crash. Crash is "most severe" in the sense the gate should NOT
// suppress (errors are real); Rebuild/LongRebuild are most severe for
// suppression intent.
//
// For ClassifyProxy purposes we want the worse-for-the-process result —
// if the process is crashed, that wins. If the process is healthy and
// the transport is in outage, transport outage wins.
func maxOutageType(a, b OutageType) OutageType {
	if a == OutageCrash || b == OutageCrash {
		return OutageCrash
	}
	if rank(a) >= rank(b) {
		return a
	}
	return b
}

func rank(o OutageType) int {
	switch o {
	case OutageHealthy:
		return 0
	case OutageRebuild:
		return 1
	case OutageLongRebuild:
		return 2
	case OutageExpiredRebuild:
		return 3
	case OutageCrash:
		return 4
	}
	return 0
}

// SuppressionMode maps Classify(processID) → tri-state mode. O(1) on the
// hot path: one Classify call plus a switch.
func (c *OutageClassifier) SuppressionMode(processID string) SuppressionMode {
	if c == nil || processID == "" {
		return ModeOff
	}
	switch c.Classify(processID) {
	case OutageRebuild:
		return ModeFull
	case OutageLongRebuild:
		c.startLongRebuildHeartbeat(processID)
		return ModeDiagnosticOnly
	case OutageExpiredRebuild:
		c.emitExpiredRebuildOnce(processID)
		c.stopLongRebuildHeartbeat(processID)
		return ModeOff
	case OutageHealthy, OutageCrash:
		fallthrough
	default:
		c.stopLongRebuildHeartbeat(processID)
		return ModeOff
	}
}

// NoteOutageOnset records an outage-start timestamp in the per-process
// crash-rate ring buffer. Called from the gate or HealthTracker.observe
// whenever a process leaves Running. Bounded ring (crashHistorySize
// entries) keeps memory and rate-check work O(1).
func (c *OutageClassifier) NoteOutageOnset(processID string, ts time.Time) {
	if c == nil || processID == "" {
		return
	}
	st := c.getOrCreate(processID)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.crashTimestamps = append(st.crashTimestamps, ts)
	if len(st.crashTimestamps) > crashHistorySize {
		st.crashTimestamps = st.crashTimestamps[len(st.crashTimestamps)-crashHistorySize:]
	}
}

// Forget drops all classifier-side state for processID. Called from the
// daemon when a script is fully cleaned up.
func (c *OutageClassifier) Forget(processID string) {
	if c == nil || processID == "" {
		return
	}
	if v, ok := c.states.LoadAndDelete(processID); ok {
		st := v.(*classifierProcessState)
		if t := st.longRebuildTimer.Swap(nil); t != nil {
			t.Stop()
		}
	}
}

// recentCrashCount returns how many outage starts have been recorded for
// processID within CrashRateWindow. O(crashHistorySize) — bounded.
func (c *OutageClassifier) recentCrashCount(processID string) int {
	st, ok := c.lookup(processID)
	if !ok {
		return 0
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	cutoff := c.nowFn().Add(-CrashRateWindow)
	// Drop expired entries while we're holding the lock — bounded slice.
	keep := st.crashTimestamps[:0]
	for _, t := range st.crashTimestamps {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	st.crashTimestamps = keep
	return len(keep)
}

// looksLikeRebuild returns true when the outage has at least one piece
// of "intentional restart" evidence: a daemon-initiated stop flag, OR a
// rebuild signal from AlertScanner within RebuildSignalGrace before the
// outage start.
func (c *OutageClassifier) looksLikeRebuild(processID string, outageStart time.Time) bool {
	if c.tracker.IsDaemonInitiatedStop(processID) {
		return true
	}
	signal := c.tracker.LastRebuildSignal(processID)
	if signal.IsZero() {
		return false
	}
	// The signal must precede the outage by no more than RebuildSignalGrace.
	delta := outageStart.Sub(signal)
	return delta >= 0 && delta <= RebuildSignalGrace
}

// startLongRebuildHeartbeat starts the per-process AfterFunc that emits
// a "rebuild ongoing" diagnostic every LongRebuildHeartbeat. Idempotent:
// if a timer is already running, this call is a no-op.
func (c *OutageClassifier) startLongRebuildHeartbeat(processID string) {
	if c == nil || c.emitDiagnostic == nil {
		return
	}
	st := c.getOrCreate(processID)
	if st.longRebuildTimer.Load() != nil {
		return
	}
	t := time.AfterFunc(LongRebuildHeartbeat, func() {
		c.fireLongRebuildHeartbeat(processID)
	})
	if !st.longRebuildTimer.CompareAndSwap(nil, t) {
		// Another goroutine started one first; cancel ours.
		t.Stop()
	}
}

// fireLongRebuildHeartbeat emits one heartbeat diagnostic and reschedules
// the timer if the process is still in the LongRebuild band.
func (c *OutageClassifier) fireLongRebuildHeartbeat(processID string) {
	if c == nil {
		return
	}
	// Defensive: if the classifier no longer wants this band, stop.
	mode := c.classifyForHeartbeat(processID)
	// Look up — never getOrCreate — the state: a Forget that landed between
	// the timer fire and now must not be undone by recreating the entry,
	// which would also re-arm a heartbeat for a cleaned-up process.
	st, ok := c.lookup(processID)
	if !ok {
		return
	}
	if mode != OutageLongRebuild {
		st.longRebuildTimer.Store(nil)
		return
	}
	proxyID := ""
	if c.proxyForProcess != nil {
		proxyID = c.proxyForProcess(processID)
	}
	outageStart := c.tracker.OutageStartedAt(processID)
	elapsed := time.Duration(0)
	if !outageStart.IsZero() {
		elapsed = c.nowFn().Sub(outageStart)
	}
	c.emit(proxyID, fmt.Sprintf(
		"proxy %s: rebuild ongoing, suppression active for %s",
		proxyID, FormatDuration(elapsed),
	), proxy.DiagnosticInfo, "rebuild_ongoing")

	// Reschedule. Bail if the timer was already torn down (Swap(nil)) by a
	// concurrent stop/forget: using the current value as the CAS-expected would
	// also match nil and re-arm a timer AFTER teardown. Capture the live timer
	// and CAS against it — if a stop races in between, the CAS fails and we
	// stop the fresh timer instead of leaking it.
	cur := st.longRebuildTimer.Load()
	if cur == nil {
		return
	}
	next := time.AfterFunc(LongRebuildHeartbeat, func() {
		c.fireLongRebuildHeartbeat(processID)
	})
	if !st.longRebuildTimer.CompareAndSwap(cur, next) {
		next.Stop()
	}
}

// classifyForHeartbeat is a tiny wrapper around Classify that ignores
// the SuppressionMode side-effects (heartbeat start, expired emission).
// The heartbeat timer must NOT recursively re-enter SuppressionMode.
func (c *OutageClassifier) classifyForHeartbeat(processID string) OutageType {
	return c.Classify(processID)
}

// stopLongRebuildHeartbeat cancels the per-process heartbeat timer. Safe
// to call when no timer is running.
func (c *OutageClassifier) stopLongRebuildHeartbeat(processID string) {
	if c == nil {
		return
	}
	st, ok := c.lookup(processID)
	if !ok {
		return
	}
	if t := st.longRebuildTimer.Swap(nil); t != nil {
		t.Stop()
	}
}

// emitExpiredRebuildOnce emits the "rebuild exceeded 30s" warning marker
// exactly once per outage. The expired flag is cleared whenever the
// process returns to Running (via Forget on the side-table when the
// outage marker resets — handled by the next Classify call).
func (c *OutageClassifier) emitExpiredRebuildOnce(processID string) {
	if c == nil || c.emitDiagnostic == nil {
		return
	}
	st := c.getOrCreate(processID)
	if !st.expiredEmitted.CompareAndSwap(false, true) {
		return
	}
	proxyID := ""
	if c.proxyForProcess != nil {
		proxyID = c.proxyForProcess(processID)
	}
	c.emit(proxyID, fmt.Sprintf(
		"proxy %s: rebuild exceeded %s, resuming error stream",
		proxyID, FormatDuration(RebuildLongWindow),
	), proxy.DiagnosticWarning, "rebuild_expired")
}

// resetOutageBookkeeping is invoked by HealthTracker.observe (via the
// classifier's reset hook below) when a process returns to Running, so
// the next outage starts with a clean slate for one-shot markers.
func (c *OutageClassifier) resetOutageBookkeeping(processID string) {
	if c == nil {
		return
	}
	st, ok := c.lookup(processID)
	if !ok {
		return
	}
	st.expiredEmitted.Store(false)
	if t := st.longRebuildTimer.Swap(nil); t != nil {
		t.Stop()
	}
}

func (c *OutageClassifier) getOrCreate(processID string) *classifierProcessState {
	if existing, ok := c.states.Load(processID); ok {
		return existing.(*classifierProcessState)
	}
	fresh := &classifierProcessState{}
	actual, _ := c.states.LoadOrStore(processID, fresh)
	return actual.(*classifierProcessState)
}

func (c *OutageClassifier) lookup(processID string) (*classifierProcessState, bool) {
	v, ok := c.states.Load(processID)
	if !ok {
		return nil, false
	}
	return v.(*classifierProcessState), true
}

// emit is a tiny wrapper around emitDiagnostic that recovers from
// emitter panics (the gate hot path must never bring down the daemon).
func (c *OutageClassifier) emit(proxyID, message string, level proxy.ProxyDiagnosticLevel, event string) {
	if c.emitDiagnostic == nil {
		return
	}
	defer debug.LogRecovered("outage-classifier", "emitDiagnostic emitter")
	entry := proxy.LogEntry{
		Type: proxy.LogTypeDiagnostic,
		Diagnostic: &proxy.ProxyDiagnostic{
			Level:     level,
			Category:  "suppression",
			Event:     event,
			Message:   message,
			Timestamp: c.nowFn(),
		},
	}
	c.emitDiagnostic(entry, proxyID)
}

// SetNowFuncForTest overrides the clock. Tests only — production uses time.Now.
func (c *OutageClassifier) SetNowFuncForTest(fn func() time.Time) {
	c.nowFn = fn
}
