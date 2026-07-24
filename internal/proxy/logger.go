package proxy

import (
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/standardbeagle/agnt/internal/debug"
)

type TrafficLogger struct {
	entries []LogEntry
	maxSize int
	head    atomic.Int64 // Next write position
	count   atomic.Int64 // Total entries written (for ID generation)
	mu      sync.RWMutex // Protects entries slice
	// disp is the async fan-out boundary for the onLogEntry callback. It is
	// created lazily on the first SetOnLogEntry so a logger with no consumer
	// spawns no goroutine. nil until then. See logCallbackDispatcher.
	disp atomic.Pointer[logCallbackDispatcher]
}

// logCallbackDispatcher decouples the onLogEntry callback from the producer
// goroutine so a slow (or briefly-blocking) consumer can never stall the
// request/telemetry hot path — the "callback must not block" contract is now
// STRUCTURALLY enforced instead of trusted. log() does a non-blocking send onto
// a bounded buffered channel (drop-newest on overflow, matching the daemon's
// own BroadcastLogEntry and the incident bus); a single worker goroutine drains
// it and invokes the current callback in FIFO order.
//
// It is a separate object (not fields on TrafficLogger) so the worker closure
// holds only the dispatcher, never the TrafficLogger — that lets a finalizer on
// the TrafficLogger reclaim the goroutine when a proxy's logger becomes
// unreachable, since the logger's lifecycle owner (ProxyServer) is out of this
// file's reach. Tests call Close() for deterministic teardown.
type logCallbackDispatcher struct {
	ch        chan LogEntry
	done      chan struct{}
	cb        atomic.Pointer[func(LogEntry)]
	dropped   atomic.Int64
	closeOnce sync.Once
}

func newLogCallbackDispatcher() *logCallbackDispatcher {
	d := &logCallbackDispatcher{
		// 4096 mirrors the incident bus cap; deep enough to absorb bursts,
		// bounded so a wedged consumer costs memory, not the producer.
		ch:   make(chan LogEntry, 4096),
		done: make(chan struct{}),
	}
	go d.run()
	return d
}

func (d *logCallbackDispatcher) run() {
	for {
		select {
		case <-d.done:
			return
		case entry := <-d.ch:
			cb := d.cb.Load()
			if cb == nil || *cb == nil {
				continue
			}
			// A panicking sink must not take down the worker (and with it every
			// subsequent entry). The callback is documented "must not block";
			// panics are contained here as defence in depth — and logged, so a
			// recurring panicking sink is visible instead of silent forever.
			func() {
				defer func() {
					if r := recover(); r != nil {
						debug.Error("proxy", "log callback sink panicked: %v", r)
					}
				}()
				(*cb)(entry)
			}()
		}
	}
}

// dispatch is the producer-side, non-blocking hand-off. It never blocks: on a
// full buffer it drops the newest entry and counts it. The ring buffer already
// retains the entry (log() writes it before dispatch), so `proxylog query`
// still surfaces it — only the live stream fan-out drops.
func (d *logCallbackDispatcher) dispatch(entry LogEntry) {
	select {
	case d.ch <- entry:
	default:
		d.dropped.Add(1)
	}
}

func (d *logCallbackDispatcher) close() {
	d.closeOnce.Do(func() { close(d.done) })
}

// NewTrafficLogger creates a new logger with specified max entries.
func NewTrafficLogger(maxSize int) *TrafficLogger {
	if maxSize <= 0 {
		maxSize = 1000 // Default to 1000 entries
	}
	return &TrafficLogger{
		entries: make([]LogEntry, maxSize),
		maxSize: maxSize,
	}
}

// LogHTTP adds an HTTP request/response log entry.
func (tl *TrafficLogger) LogHTTP(entry HTTPLogEntry) {
	tl.log(LogEntry{
		Type: LogTypeHTTP,
		HTTP: &entry,
	})
}

// firstFrameID returns the first frame id from an optional variadic, or "".
// Lets browser-telemetry Log* methods accept a frame id without breaking the
// many existing (frame-less) callers and tests.
func firstFrameID(frameID []string) string {
	if len(frameID) > 0 {
		return frameID[0]
	}
	return ""
}

// LogError adds a frontend error log entry. Optional frameID attributes it to
// the emitting content frame.
func (tl *TrafficLogger) LogError(entry FrontendError, frameID ...string) {
	tl.log(LogEntry{
		Type:    LogTypeError,
		FrameID: firstFrameID(frameID),
		Error:   &entry,
	})
}

// LogPerformance adds a frontend performance log entry.
func (tl *TrafficLogger) LogPerformance(entry PerformanceMetric, frameID ...string) {
	tl.log(LogEntry{
		Type:        LogTypePerformance,
		FrameID:     firstFrameID(frameID),
		Performance: &entry,
	})
}

// LogCustom adds a custom log message.
func (tl *TrafficLogger) LogCustom(entry CustomLog, frameID ...string) {
	tl.log(LogEntry{
		Type:    LogTypeCustom,
		FrameID: firstFrameID(frameID),
		Custom:  &entry,
	})
}

// LogScreenshot adds a screenshot log entry.
func (tl *TrafficLogger) LogScreenshot(entry Screenshot) {
	tl.log(LogEntry{
		Type:       LogTypeScreenshot,
		Screenshot: &entry,
	})
}

// LogExecution adds a JavaScript execution result.
func (tl *TrafficLogger) LogExecution(entry ExecutionResult) {
	tl.log(LogEntry{
		Type:      LogTypeExecution,
		Execution: &entry,
	})
}

// LogResponse adds an execution response sent to MCP client.
func (tl *TrafficLogger) LogResponse(entry ExecutionResponse) {
	tl.log(LogEntry{
		Type:     LogTypeResponse,
		Response: &entry,
	})
}

// LogInteraction adds a user interaction event.
func (tl *TrafficLogger) LogInteraction(entry InteractionEvent, frameID ...string) {
	tl.log(LogEntry{
		Type:        LogTypeInteraction,
		FrameID:     firstFrameID(frameID),
		Interaction: &entry,
	})
}

// LogMutation adds a DOM mutation event.
func (tl *TrafficLogger) LogMutation(entry MutationEvent, frameID ...string) {
	tl.log(LogEntry{
		Type:     LogTypeMutation,
		FrameID:  firstFrameID(frameID),
		Mutation: &entry,
	})
}

// LogPanelMessage adds a panel message entry.
func (tl *TrafficLogger) LogPanelMessage(entry PanelMessage) {
	tl.log(LogEntry{
		Type:         LogTypePanelMessage,
		PanelMessage: &entry,
	})
}

// LogSketch adds a sketch entry.
func (tl *TrafficLogger) LogSketch(entry SketchEntry) {
	tl.log(LogEntry{
		Type:   LogTypeSketch,
		Sketch: &entry,
	})
}

// LogScreenshotCapture adds a screenshot capture entry.
func (tl *TrafficLogger) LogScreenshotCapture(entry ScreenshotCapture) {
	tl.log(LogEntry{
		Type:              LogTypeScreenshotCapture,
		ScreenshotCapture: &entry,
	})
}

// LogElementCapture adds an element capture entry.
func (tl *TrafficLogger) LogElementCapture(entry ElementCapture) {
	tl.log(LogEntry{
		Type:           LogTypeElementCapture,
		ElementCapture: &entry,
	})
}

// LogSketchCapture adds a sketch capture entry.
func (tl *TrafficLogger) LogSketchCapture(entry SketchCapture) {
	tl.log(LogEntry{
		Type:          LogTypeSketchCapture,
		SketchCapture: &entry,
	})
}

// LogDesignState adds a design state entry.
func (tl *TrafficLogger) LogDesignState(entry DesignState) {
	tl.log(LogEntry{
		Type:        LogTypeDesignState,
		DesignState: &entry,
	})
}

// LogDesignRequest adds a design request entry.
func (tl *TrafficLogger) LogDesignRequest(entry DesignRequest) {
	tl.log(LogEntry{
		Type:          LogTypeDesignRequest,
		DesignRequest: &entry,
	})
}

// LogDesignChat adds a design chat entry.
func (tl *TrafficLogger) LogDesignChat(entry DesignChat) {
	tl.log(LogEntry{
		Type:       LogTypeDesignChat,
		DesignChat: &entry,
	})
}

// LogDesignEdit adds a design geometry-edit entry.
func (tl *TrafficLogger) LogDesignEdit(entry DesignEdit) {
	tl.log(LogEntry{
		Type:       LogTypeDesignEdit,
		DesignEdit: &entry,
	})
}

// LogWalkthrough adds a walkthrough (live-demo) lifecycle entry.
func (tl *TrafficLogger) LogWalkthrough(entry WalkthroughEntry) {
	tl.log(LogEntry{
		Type:        LogTypeWalkthrough,
		Walkthrough: &entry,
	})
}

// LogResponsiveRequest adds a responsive mode handoff request entry.
func (tl *TrafficLogger) LogResponsiveRequest(entry ResponsiveRequest) {
	tl.log(LogEntry{
		Type:              LogTypeResponsiveRequest,
		ResponsiveRequest: &entry,
	})
}

// LogResponsiveState adds a responsive mode state entry.
func (tl *TrafficLogger) LogResponsiveState(entry ResponsiveState) {
	tl.log(LogEntry{
		Type:            LogTypeResponsiveState,
		ResponsiveState: &entry,
	})
}

// LogDiagnostic adds a server-side diagnostic event.
func (tl *TrafficLogger) LogDiagnostic(entry ProxyDiagnostic) {
	tl.log(LogEntry{
		Type:       LogTypeDiagnostic,
		Diagnostic: &entry,
	})
}

// log adds an entry to the circular buffer.
//
// Consistency contract: the slice write, head advance, and count bump
// all happen under tl.mu — a concurrent Query holding the RLock either
// sees a slot's previous value (not yet overwritten) or its fully-
// written new value, never a half-filled LogEntry. The callback fires
// AFTER the lock is released so a slow/panicking callback cannot stall
// producers or readers.
func (tl *TrafficLogger) log(entry LogEntry) {
	tl.mu.Lock()
	pos := tl.head.Add(1) - 1
	idx := int(pos % int64(tl.maxSize))
	tl.entries[idx] = entry
	tl.count.Add(1)
	tl.mu.Unlock()

	// Hand the entry to the async dispatcher (non-blocking). Nil until a
	// consumer registers a callback, so a callback-less logger does no work.
	if d := tl.disp.Load(); d != nil {
		d.dispatch(entry)
	}
}

// SetOnLogEntry sets the callback fired (asynchronously, in FIFO order) after
// each log entry. Delivery is decoupled onto a bounded worker: a slow or
// blocking callback cannot stall producers, and the newest entry is dropped if
// the buffer overflows. The callback may be replaced at any time; the worker
// picks up the new one for subsequent entries. Call Close to stop the worker.
func (tl *TrafficLogger) SetOnLogEntry(fn func(LogEntry)) {
	d := tl.disp.Load()
	if d == nil {
		nd := newLogCallbackDispatcher()
		if tl.disp.CompareAndSwap(nil, nd) {
			d = nd
			// Safety net: reclaim the worker goroutine when the logger becomes
			// unreachable, since ProxyServer (the lifecycle owner) is out of
			// scope for an explicit Close in production. The finalizer captures
			// only the dispatcher, so it does not itself keep tl alive.
			disp := nd
			runtime.SetFinalizer(tl, func(*TrafficLogger) { disp.close() })
		} else {
			nd.close() // lost the create race; adopt the winner
			d = tl.disp.Load()
		}
	}
	if fn == nil {
		d.cb.Store(nil)
		return
	}
	d.cb.Store(&fn)
}

// Close stops the async onLogEntry worker goroutine. Idempotent and safe on a
// logger that never had a callback. Tests must call it for deterministic
// goroutine teardown; production relies on the finalizer safety net.
func (tl *TrafficLogger) Close() {
	if d := tl.disp.Load(); d != nil {
		d.close()
	}
}

// droppedCallbacks reports how many entries the async dispatcher dropped due to
// buffer overflow. Used by tests to assert overflow behaviour.
func (tl *TrafficLogger) droppedCallbacks() int64 {
	if d := tl.disp.Load(); d != nil {
		return d.dropped.Load()
	}
	return 0
}

// Query retrieves log entries matching the filter in chronological order.
func (tl *TrafficLogger) Query(filter LogFilter) []LogEntry {
	tl.mu.RLock()
	defer tl.mu.RUnlock()

	head := tl.head.Load()
	available := int(min(head, int64(tl.maxSize)))

	// When the buffer has wrapped, the oldest entry is at head % maxSize.
	// When it hasn't wrapped, entries start at index 0.
	start := 0
	if head > int64(tl.maxSize) {
		start = int(head % int64(tl.maxSize))
	}

	var results []LogEntry
	for i := 0; i < available; i++ {
		idx := (start + i) % tl.maxSize
		entry := tl.entries[idx]
		if filter.Matches(entry) {
			results = append(results, entry)
		}
	}

	// Honour Limit by keeping the most recent N matches (results are in
	// chronological order, so the newest are at the tail). Limit <= 0 = no cap.
	if filter.Limit > 0 && len(results) > filter.Limit {
		results = results[len(results)-filter.Limit:]
	}

	return results
}

// Clear removes all log entries.
func (tl *TrafficLogger) Clear() {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	tl.head.Store(0)
	tl.count.Store(0)
	// Zero out entries
	for i := range tl.entries {
		tl.entries[i] = LogEntry{}
	}
}

// Stats returns logger statistics.
func (tl *TrafficLogger) Stats() LoggerStats {
	total := tl.count.Load()
	available := int(min(total, int64(tl.maxSize)))
	return LoggerStats{
		TotalEntries:     total,
		AvailableEntries: int64(available),
		MaxSize:          int64(tl.maxSize),
		Dropped:          max(0, total-int64(tl.maxSize)),
	}
}

// LoggerStats holds logger statistics.
type LoggerStats struct {
	TotalEntries     int64 `json:"total_entries"`
	AvailableEntries int64 `json:"available_entries"`
	MaxSize          int64 `json:"max_size"`
	Dropped          int64 `json:"dropped"`
}

// LogFilter specifies criteria for querying logs.
