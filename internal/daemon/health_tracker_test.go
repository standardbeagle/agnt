package daemon

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	goprocess "github.com/standardbeagle/go-cli-server/process"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// fakeProcess is a tiny shim for tests so we don't need a full
// ProcessManager. It exposes only the methods the tracker uses
// (State/SetState through the goprocess.ManagedProcess type).
//
// We use the real *goprocess.ManagedProcess because the lookup signature
// is part of the production contract; constructing one with NewManagedProcess
// is cheap and uses no OS resources until Start() is called.
func newFakeProcess(t *testing.T, id string, state goprocess.ProcessState) *goprocess.ManagedProcess {
	t.Helper()
	p := goprocess.NewManagedProcess(goprocess.ProcessConfig{
		ID:      id,
		Command: "/bin/true",
	})
	p.SetState(state)
	return p
}

// trackerSpy collects diagnostic markers emitted by the tracker so tests
// can assert on transitions. Thread-safe.
type trackerSpy struct {
	mu      sync.Mutex
	entries []spyEntry
}

type spyEntry struct {
	proxyID string
	event   string
	message string
}

func (s *trackerSpy) emit(entry proxy.LogEntry, proxyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.Diagnostic == nil {
		return
	}
	s.entries = append(s.entries, spyEntry{
		proxyID: proxyID,
		event:   entry.Diagnostic.Event,
		message: entry.Diagnostic.Message,
	})
}

func (s *trackerSpy) snapshot() []spyEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]spyEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

func (s *trackerSpy) eventCount(event string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.entries {
		if e.event == event {
			n++
		}
	}
	return n
}

// procTable is a deterministic process lookup for tests.
type procTable struct {
	mu    sync.RWMutex
	procs map[string]*goprocess.ManagedProcess
}

func newProcTable() *procTable {
	return &procTable{procs: make(map[string]*goprocess.ManagedProcess)}
}

func (p *procTable) put(id string, proc *goprocess.ManagedProcess) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.procs[id] = proc
}

func (p *procTable) lookup(id string) (*goprocess.ManagedProcess, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if proc, ok := p.procs[id]; ok {
		return proc, nil
	}
	return nil, goprocess.ErrProcessNotFound
}

func (p *procTable) remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.procs, id)
}

// fakeClock returns a controllable now() function for grace-period tests.
type fakeClock struct {
	now atomic.Pointer[time.Time]
}

func newFakeClock(start time.Time) *fakeClock {
	c := &fakeClock{}
	c.now.Store(&start)
	return c
}

func (c *fakeClock) Now() time.Time {
	return *c.now.Load()
}

func (c *fakeClock) Advance(d time.Duration) {
	t := c.Now().Add(d)
	c.now.Store(&t)
}

func newTestTracker(t *testing.T) (*HealthTracker, *procTable, *trackerSpy, *fakeClock) {
	t.Helper()
	table := newProcTable()
	spy := &trackerSpy{}
	clock := newFakeClock(time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC))
	tracker := NewHealthTracker(table.lookup, spy.emit)
	tracker.nowFn = clock.Now
	return tracker, table, spy, clock
}

func TestSuppressionWindow_StartingStopping(t *testing.T) {
	tracker, table, _, _ := newTestTracker(t)

	starting := newFakeProcess(t, "proc-1", goprocess.StateStarting)
	stopping := newFakeProcess(t, "proc-2", goprocess.StateStopping)
	failed := newFakeProcess(t, "proc-3", goprocess.StateFailed)

	table.put("proc-1", starting)
	table.put("proc-2", stopping)
	table.put("proc-3", failed)

	assert.True(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"), "Starting should suppress")
	assert.True(t, tracker.IsInSuppressionWindow("proxy-2", "proc-2"), "Stopping should suppress")
	assert.True(t, tracker.IsInSuppressionWindow("proxy-3", "proc-3"), "Failed should suppress")
}

func TestSuppressionWindow_HealthyWithGrace(t *testing.T) {
	tracker, table, _, clock := newTestTracker(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateStarting)
	table.put("proc-1", proc)

	// First observation: Starting → suppress.
	require.True(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"))

	// Process transitions to Running. The tracker observes the edge on
	// the next IsInSuppressionWindow call and stamps lastHealthyAt.
	proc.SetState(goprocess.StateRunning)
	require.True(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"),
		"running within grace should still suppress")

	// 2.5s later — still inside the 5s grace window.
	clock.Advance(2500 * time.Millisecond)
	require.True(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"),
		"2.5s into grace should still suppress")

	// 3s later — total 5.5s elapsed, grace expired.
	clock.Advance(3 * time.Second)
	assert.False(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"),
		"past grace window should not suppress")
}

func TestSuppressionWindow_HealthyPastGrace(t *testing.T) {
	tracker, table, _, clock := newTestTracker(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)

	// First observation stamps lastHealthyAt at t0.
	require.True(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"),
		"first observation of running stamps lastHealthyAt and suppresses for the grace window")

	clock.Advance(SuppressionGracePeriod + time.Millisecond)
	assert.False(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"))
}

func TestSuppressionWindow_Unlinked(t *testing.T) {
	tracker, table, _, _ := newTestTracker(t)

	// Even if there's a process in a suppress state, an unlinked proxy
	// (empty linkedProcessID) must never suppress.
	starting := newFakeProcess(t, "proc-1", goprocess.StateStarting)
	table.put("proc-1", starting)

	assert.False(t, tracker.IsInSuppressionWindow("proxy-1", ""))
}

func TestSuppressionWindow_Dead(t *testing.T) {
	tracker, table, _, _ := newTestTracker(t)

	// StateStopped represents the "Dead" state per .claude/rules.
	dead := newFakeProcess(t, "proc-1", goprocess.StateStopped)
	table.put("proc-1", dead)

	assert.False(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"),
		"dead/stopped processes must NOT suppress — errors are the story")
}

func TestSuppressionWindow_MissingProcess(t *testing.T) {
	tracker, _, _, _ := newTestTracker(t)

	// Process was removed between trackProxy and the next log entry.
	// The tracker must handle the missing-process case cleanly.
	assert.False(t, tracker.IsInSuppressionWindow("proxy-1", "ghost-process"))
}

func TestSuppressionWindow_RestartCycle(t *testing.T) {
	// Adversarial scenario: Healthy → Stopping → Healthy → Stopping
	// within 6 seconds. The grace timer from the first Healthy must NOT
	// interfere with the second Stopping's suppression.
	tracker, table, _, clock := newTestTracker(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)

	// Initial observation stamps lastHealthyAt.
	tracker.IsInSuppressionWindow("proxy-1", "proc-1")

	// 6 seconds pass — past grace, broadcasting normally.
	clock.Advance(6 * time.Second)
	assert.False(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"))

	// Restart begins.
	proc.SetState(goprocess.StateStopping)
	assert.True(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"),
		"second stopping cycle must suppress regardless of earlier grace")

	// Restart completes.
	proc.SetState(goprocess.StateRunning)
	assert.True(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"),
		"new running edge starts a fresh grace window")

	// New 5s grace window from the second healthy edge.
	clock.Advance(SuppressionGracePeriod + time.Millisecond)
	assert.False(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"))
}

func TestDiagnosticMarkers_FireOnceOnTransition(t *testing.T) {
	tracker, table, spy, clock := newTestTracker(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)

	// Bring the tracker into a known Running state.
	tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	clock.Advance(SuppressionGracePeriod + time.Millisecond)
	tracker.IsInSuppressionWindow("proxy-1", "proc-1") // past grace, healthy

	// Transition to Stopping — emits a single open marker.
	proc.SetState(goprocess.StateStopping)
	for i := 0; i < 5; i++ {
		tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	}
	assert.Equal(t, 1, spy.eventCount("stream_suppressed"),
		"open marker must fire exactly once per edge, not per check")
	assert.Equal(t, 0, spy.eventCount("stream_resumed"),
		"close marker not yet fired")

	// Transition back to Running and run out the grace window.
	proc.SetState(goprocess.StateRunning)
	for i := 0; i < 5; i++ {
		tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	}
	clock.Advance(SuppressionGracePeriod + time.Millisecond)
	for i := 0; i < 5; i++ {
		tracker.IsInSuppressionWindow("proxy-1", "proc-1")
		tracker.MaybeCloseGraceWindow("proxy-1", "proc-1")
	}
	assert.Equal(t, 1, spy.eventCount("stream_resumed"),
		"close marker must fire exactly once per resume edge")
	assert.Equal(t, 1, spy.eventCount("stream_suppressed"),
		"no extra open markers")
}

func TestDiagnosticMarkers_DirectStoppedSkipsGrace(t *testing.T) {
	// When a process goes Failed → Stopped (dead, no recovery), the
	// close marker must fire immediately — there's no grace window for
	// "dead", because errors should resume flowing right away.
	tracker, table, spy, _ := newTestTracker(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateFailed)
	table.put("proc-1", proc)

	// Initial Failed → emits open marker.
	tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	require.Equal(t, 1, spy.eventCount("stream_suppressed"))

	// Transition to Stopped (dead) — emits close marker immediately.
	proc.SetState(goprocess.StateStopped)
	tracker.IsInSuppressionWindow("proxy-1", "proc-1")
	assert.Equal(t, 1, spy.eventCount("stream_resumed"),
		"failed → stopped should emit close marker immediately, no grace")
}

func TestForget_DropsState(t *testing.T) {
	tracker, table, _, _ := newTestTracker(t)

	proc := newFakeProcess(t, "proc-1", goprocess.StateStarting)
	table.put("proc-1", proc)

	// Populate state.
	tracker.IsInSuppressionWindow("proxy-1", "proc-1")

	// Process is removed from the table.
	table.remove("proc-1")
	tracker.Forget("proc-1")

	// Lookup now fails → no suppression, no panic.
	assert.False(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"))
}

func TestNilTracker_NeverSuppresses(t *testing.T) {
	// Defensive: a nil tracker (e.g. in a test daemon without the field
	// initialised) must not panic and must return false.
	var tracker *HealthTracker
	assert.False(t, tracker.IsInSuppressionWindow("proxy-1", "proc-1"))
	tracker.Forget("proc-1")                           // no panic
	tracker.MaybeCloseGraceWindow("proxy-1", "proc-1") // no panic
}

// newGateDaemon builds a minimal Daemon struct around the supplied
// process table so we can exercise proxyBroadcastGate without spinning
// up the full hub. The fields touched by the gate are: alertHub (nil
// here — gate never calls into it), healthTracker, outageClassifier,
// scriptProxyMu, and proxyToScript. Everything else stays zero-valued.
func newGateDaemon(t *testing.T, table *procTable) (*Daemon, *trackerSpy, *fakeClock) {
	t.Helper()
	spy := &trackerSpy{}
	clock := newFakeClock(time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC))
	tracker := NewHealthTracker(table.lookup, spy.emit)
	tracker.nowFn = clock.Now
	classifier := NewOutageClassifier(tracker, table.lookup, spy.emit, func(string) string { return "" })
	classifier.nowFn = clock.Now
	d := &Daemon{
		scriptProxies:    make(map[string][]string),
		proxyToScript:    make(map[string]string),
		healthTracker:    tracker,
		outageClassifier: classifier,
	}
	return d, spy, clock
}

func TestBroadcastGate_Suppressed(t *testing.T) {
	table := newProcTable()
	d, _, _ := newGateDaemon(t, table)

	// Link proxy to a process that is currently Starting.
	proc := newFakeProcess(t, "proc-1", goprocess.StateStarting)
	table.put("proc-1", proc)
	d.trackScriptProxy("proc-1", "proxy-1")

	// A non-diagnostic entry must be suppressed.
	entry := proxy.LogEntry{
		Type:  proxy.LogTypeError,
		Error: &proxy.FrontendError{Message: "ECONNREFUSED"},
	}
	assert.False(t, d.proxyBroadcastGate("proxy-1", entry),
		"error entry should be suppressed while linked process is starting")
}

func TestBroadcastGate_PassesThroughWhenHealthy(t *testing.T) {
	table := newProcTable()
	d, _, clock := newGateDaemon(t, table)

	proc := newFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)
	d.trackScriptProxy("proc-1", "proxy-1")

	// First call stamps lastHealthyAt; we're inside the grace window.
	entry := proxy.LogEntry{Type: proxy.LogTypeError, Error: &proxy.FrontendError{Message: "test"}}
	assert.False(t, d.proxyBroadcastGate("proxy-1", entry),
		"first observation of running stamps grace and suppresses")

	// Past grace.
	clock.Advance(SuppressionGracePeriod + time.Millisecond)
	assert.True(t, d.proxyBroadcastGate("proxy-1", entry),
		"healthy past grace should pass through")
}

func TestBroadcastGate_DiagnosticAlwaysPasses(t *testing.T) {
	table := newProcTable()
	d, _, _ := newGateDaemon(t, table)

	// Linked to a process in deep suppression.
	proc := newFakeProcess(t, "proc-1", goprocess.StateStopping)
	table.put("proc-1", proc)
	d.trackScriptProxy("proc-1", "proxy-1")

	// A diagnostic entry must pass through even when the regular path
	// would be suppressed. This is critical: the suppression markers
	// themselves are diagnostic entries and must not be silenced.
	diag := proxy.LogEntry{
		Type: proxy.LogTypeDiagnostic,
		Diagnostic: &proxy.ProxyDiagnostic{
			Level:   proxy.DiagnosticInfo,
			Message: "stream suppressed",
		},
	}
	assert.True(t, d.proxyBroadcastGate("proxy-1", diag),
		"diagnostic entries must always pass the gate")
}

func TestBroadcastGate_UnlinkedProxyAlwaysPasses(t *testing.T) {
	table := newProcTable()
	d, _, _ := newGateDaemon(t, table)

	// No linked process for this proxy ID.
	entry := proxy.LogEntry{Type: proxy.LogTypeError, Error: &proxy.FrontendError{Message: "test"}}
	assert.True(t, d.proxyBroadcastGate("unlinked-proxy", entry),
		"unlinked proxy must never suppress")
}
