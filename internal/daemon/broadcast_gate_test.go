package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon/health"
	"github.com/standardbeagle/agnt/internal/proxy"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/assert"
)

type gateTrackerSpy struct {
	mu      sync.Mutex
	entries []gateSpyEntry
}

type gateSpyEntry struct {
	proxyID string
	event   string
	message string
}

func (s *gateTrackerSpy) emit(entry proxy.LogEntry, proxyID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry.Diagnostic == nil {
		return
	}
	s.entries = append(s.entries, gateSpyEntry{proxyID: proxyID, event: entry.Diagnostic.Event, message: entry.Diagnostic.Message})
}

type gateFakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newGateFakeClock(start time.Time) *gateFakeClock { return &gateFakeClock{now: start} }

func (c *gateFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *gateFakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type gateProcTableMap struct {
	mu    sync.RWMutex
	procs map[string]*goprocess.ManagedProcess
}

func newProcTable() *gateProcTableMap {
	return &gateProcTableMap{procs: make(map[string]*goprocess.ManagedProcess)}
}

func (p *gateProcTableMap) put(id string, proc *goprocess.ManagedProcess) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.procs[id] = proc
}

func (p *gateProcTableMap) lookup(id string) (*goprocess.ManagedProcess, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if proc, ok := p.procs[id]; ok {
		return proc, nil
	}
	return nil, goprocess.ErrProcessNotFound
}

func newGateFakeProcess(t *testing.T, id string, state goprocess.ProcessState) *goprocess.ManagedProcess {
	t.Helper()
	p := goprocess.NewManagedProcess(goprocess.ProcessConfig{
		ID:      id,
		Command: "/bin/true",
	})
	p.SetState(state)
	return p
}

// newGateDaemon builds a minimal Daemon struct around the supplied
// process table so we can exercise proxyBroadcastGate without spinning
// up the full hub. The fields touched by the gate are: eventHub (nil
// here — gate never calls into it), healthTracker, outageClassifier,
// scriptProxyMu, and proxyToScript. Everything else stays zero-valued.
func newGateDaemon(t *testing.T, table *gateProcTableMap) (*Daemon, *gateTrackerSpy, *gateFakeClock) {
	t.Helper()
	spy := &gateTrackerSpy{}
	clock := newGateFakeClock(time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC))
	tracker := health.NewHealthTracker(table.lookup, spy.emit)
	tracker.SetNowFuncForTest(clock.Now)
	classifier := health.NewOutageClassifier(tracker, table.lookup, spy.emit, func(string) string { return "" })
	classifier.SetNowFuncForTest(clock.Now)
	d := &Daemon{
		scriptProxies:    make(map[string][]string),
		proxyToScript:    make(map[string]string),
		healthTracker:    tracker,
		outageClassifier: classifier,
	}
	return d, spy, clock
}

func TestBroadcastGate_Suppressed(t *testing.T) {
	t.Parallel()
	table := newProcTable()
	d, _, _ := newGateDaemon(t, table)

	// Link proxy to a process that is currently Starting.
	proc := newGateFakeProcess(t, "proc-1", goprocess.StateStarting)
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
	t.Parallel()
	table := newProcTable()
	d, _, clock := newGateDaemon(t, table)

	proc := newGateFakeProcess(t, "proc-1", goprocess.StateRunning)
	table.put("proc-1", proc)
	d.trackScriptProxy("proc-1", "proxy-1")

	// First call stamps lastHealthyAt; we're inside the grace window.
	entry := proxy.LogEntry{Type: proxy.LogTypeError, Error: &proxy.FrontendError{Message: "test"}}
	assert.False(t, d.proxyBroadcastGate("proxy-1", entry),
		"first observation of running stamps grace and suppresses")

	// Past grace.
	clock.Advance(health.SuppressionGracePeriod + time.Millisecond)
	assert.True(t, d.proxyBroadcastGate("proxy-1", entry),
		"healthy past grace should pass through")
}

func TestBroadcastGate_DiagnosticAlwaysPasses(t *testing.T) {
	t.Parallel()
	table := newProcTable()
	d, _, _ := newGateDaemon(t, table)

	// Linked to a process in deep suppression.
	proc := newGateFakeProcess(t, "proc-1", goprocess.StateStopping)
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
	t.Parallel()
	table := newProcTable()
	d, _, _ := newGateDaemon(t, table)

	// No linked process for this proxy ID.
	entry := proxy.LogEntry{Type: proxy.LogTypeError, Error: &proxy.FrontendError{Message: "test"}}
	assert.True(t, d.proxyBroadcastGate("unlinked-proxy", entry),
		"unlinked proxy must never suppress")
}
