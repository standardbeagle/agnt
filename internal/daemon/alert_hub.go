package daemon

import (
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// proxyPathRegistry maps proxyID → project path for stream event routing.
// Populated by RegisterProxyPath when a proxy is wired to the alert hub;
// entries are never removed because proxyIDs are unique and stopped proxies
// cannot generate new events.
type proxyPathRegistry struct {
	m sync.Map // map[string]string
}

func (r *proxyPathRegistry) set(proxyID, path string) {
	r.m.Store(proxyID, path)
}

func (r *proxyPathRegistry) get(proxyID string) string {
	if v, ok := r.m.Load(proxyID); ok {
		return v.(string)
	}
	return ""
}

// MCPAlertSink delivers alert messages via MCP session notifications.
type MCPAlertSink interface {
	SendAlert(level string, message string) error
}

// OverlayAlertSink delivers alert messages via PTY stdin injection.
type OverlayAlertSink interface {
	TypeAlert(text string) error
	IsEnabled() bool
}

// HookEventSink receives Claude Code hook events drained from the daemon
// ring buffer. Implementations must be non-blocking: BroadcastHookEvent
// fans out under the hub's read lock and a slow sink would stall the
// drain goroutine. Sinks that need to queue should own an internal buffer
// and drop on overflow.
type HookEventSink interface {
	EmitHookEvent(ev HookEvent)
}

// StreamSink receives filtered proxy log events via a channel.
// The consumer reads from Ch until it is closed (on unregister or daemon shutdown).
//
// Concurrency contract: producers and the closer (RemoveStreamSink) use a
// lock-free reference count + closed flag instead of a per-sink RWMutex.
// Hot path is two atomic loads and two atomic adds; slow path (close) spins
// until refs drains. This is ~4× faster than the prior RWMutex pair under
// high fan-out (100+ sinks × thousands of events) and eliminates the
// per-sink RLock contention that caused TestAlertHub_FanOutToManySinks to
// flake under concurrent test load. See sendToStreamSinkSafe for the
// send-side dance and RemoveStreamSink for the close-side wait.
//
// closed is set to true BEFORE refs is consulted by the closer so any
// producer that has already entered the critical section (refs > 0) will
// be drained before close(Ch) runs. Producers that observe closed=true
// after their refs increment must decrement and exit without sending.
type StreamSink struct {
	Ch     chan proxy.LogEntry
	filter streamFilter
	closed atomic.Bool
	refs   atomic.Int32
}

// streamFilter holds criteria for filtering events sent to a StreamSink.
type streamFilter struct {
	types       map[proxy.LogEntryType]bool
	proxyID     string
	projectPath string // Filter to proxies whose Path matches this project directory
	processID   string // Filter to specific process output
	severity    string // "error", "warning", "info" — matches against error/custom/diagnostic levels
	grep        string // Substring match on process output lines
	grepStream  string // "stdout", "stderr", or "" for both
}

// matches returns true if the log entry passes the filter.
// proxyPath is the project directory of the proxy that generated the entry;
// an empty string means the entry is not tied to a specific proxy (e.g. hook events).
func (f *streamFilter) matches(entry proxy.LogEntry, proxyID, proxyPath string) bool {
	if len(f.types) > 0 && !f.types[entry.Type] {
		return false
	}
	if f.proxyID != "" && proxyID != f.proxyID {
		return false
	}
	if f.projectPath != "" && proxyPath != "" && proxyPath != f.projectPath {
		return false
	}
	if f.processID != "" && entry.Type == proxy.LogTypeProcessOutput {
		if entry.ProcessOutput == nil || entry.ProcessOutput.ProcessID != f.processID {
			return false
		}
	}
	if f.grepStream != "" && entry.Type == proxy.LogTypeProcessOutput {
		if entry.ProcessOutput == nil || entry.ProcessOutput.Stream != f.grepStream {
			return false
		}
	}
	if f.grep != "" && entry.Type == proxy.LogTypeProcessOutput {
		if entry.ProcessOutput == nil || !containsSubstring(entry.ProcessOutput.Line, f.grep) {
			return false
		}
	}
	if f.severity != "" {
		if !entryHasSeverity(entry, f.severity) {
			return false
		}
	}
	return true
}

// entryHasSeverity checks if a log entry matches the requested severity level.
func entryHasSeverity(entry proxy.LogEntry, severity string) bool {
	switch entry.Type {
	case proxy.LogTypeError:
		return severity == "error"
	case proxy.LogTypeHTTP:
		if entry.HTTP != nil && (entry.HTTP.StatusCode >= 500 || entry.HTTP.Error != "") {
			return severity == "error"
		}
		if entry.HTTP != nil && entry.HTTP.StatusCode >= 400 {
			return severity == "warning"
		}
		return false
	case proxy.LogTypeCustom:
		if entry.Custom != nil {
			return entry.Custom.Level == severity
		}
		return false
	case proxy.LogTypeDiagnostic:
		if entry.Diagnostic != nil {
			return string(entry.Diagnostic.Level) == severity
		}
		return false
	default:
		return false
	}
}

// ProxyBroadcaster broadcasts alert toasts to all active browser overlay
// connections. Implemented by proxyManagerBroadcaster which wraps
// *proxy.ProxyManager. Test code can use a stub without starting a real proxy.
type ProxyBroadcaster interface {
	// BroadcastAlertToast sends a toast to all connected browser clients.
	// toastType matches the toast severity ("error", "warning").
	// Implementations must be non-blocking; errors are swallowed at debug level.
	BroadcastAlertToast(toastType, title, message string)
}

// AlertHub routes formatted alert messages to available delivery mechanisms:
// PTY overlay (stdin injection), MCP session notifications, and stream sinks.
type AlertHub struct {
	overlaySink      OverlayAlertSink
	mcpSinks         []MCPAlertSink
	streamSinks      []*StreamSink
	hookSinks        []HookEventSink
	pushConfig       *config.PushConfig
	proxyBroadcaster ProxyBroadcaster
	mu               sync.RWMutex
	proxyPaths       proxyPathRegistry // proxyID → project path for stream routing
}

// NewAlertHub creates a new AlertHub.
func NewAlertHub() *AlertHub {
	return &AlertHub{}
}

// SetProxyBroadcaster registers a broadcaster for sending alert toasts to
// connected browser overlays. A nil value disables proxy broadcasts.
func (h *AlertHub) SetProxyBroadcaster(pb ProxyBroadcaster) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.proxyBroadcaster = pb
}

// SetPushConfig sets the push channel configuration.
// When set, Deliver checks each channel before dispatching.
// A nil config means all channels are enabled (universal default).
func (h *AlertHub) SetPushConfig(pc *config.PushConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pushConfig = pc
}

// SetOverlaySink sets the overlay (PTY stdin) delivery sink.
func (h *AlertHub) SetOverlaySink(sink OverlayAlertSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.overlaySink = sink
}

// AddMCPSink registers an MCP session for alert delivery.
func (h *AlertHub) AddMCPSink(sink MCPAlertSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mcpSinks = append(h.mcpSinks, sink)
}

// RemoveMCPSink unregisters an MCP session sink.
func (h *AlertHub) RemoveMCPSink(sink MCPAlertSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, s := range h.mcpSinks {
		if s == sink {
			h.mcpSinks = append(h.mcpSinks[:i], h.mcpSinks[i+1:]...)
			return
		}
	}
}

// AddStreamSink registers a stream sink and returns it.
// The caller reads from sink.Ch until it is closed.
func (h *AlertHub) AddStreamSink(filter streamFilter) *StreamSink {
	sink := &StreamSink{
		// 256 cap — enough headroom that a 100-sink fan-out under burst
		// load (32 events/100µs in stress tests) fits without overflow
		// even when the consumer goroutine is briefly descheduled. Each
		// idle sink holds ~30KB; production typically runs <5 sinks so
		// the memory cost is negligible.
		Ch:     make(chan proxy.LogEntry, 256),
		filter: filter,
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.streamSinks = append(h.streamSinks, sink)
	return sink
}

// RemoveStreamSink unregisters a stream sink and closes its channel.
//
// The close path is serialized against in-flight sends via the closed/refs
// atomics on StreamSink. We set closed=true first so any new producer
// observes it and exits cheaply, then spin-wait for refs==0 so any
// producer that was mid-flight before the closed flip can complete its
// send and decrement before we close(Ch). The spin yields the scheduler
// every iteration so we don't burn a core under contention.
//
// Idempotent: a second call to RemoveStreamSink on the same sink is a
// no-op (the sink is no longer in the slice, so the close path is not
// re-entered).
func (h *AlertHub) RemoveStreamSink(sink *StreamSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, s := range h.streamSinks {
		if s == sink {
			h.streamSinks = append(h.streamSinks[:i], h.streamSinks[i+1:]...)
			// Mark closed; producers entering after this point fast-fail.
			sink.closed.Store(true)
			// Drain any producers that were already in the critical
			// section. Send is non-blocking, so each in-flight producer
			// completes in microseconds.
			for sink.refs.Load() > 0 {
				runtime.Gosched()
			}
			close(sink.Ch)
			return
		}
	}
}

// RegisterProxyPath records the project directory for a proxy so that
// project-scoped stream filters (projectPath != "") can exclude events
// from proxies belonging to other projects. Call once from wireProxyLogger.
// Entries are never removed because proxyIDs are unique and a stopped proxy
// cannot generate new LogEntry events.
func (h *AlertHub) RegisterProxyPath(proxyID, projectPath string) {
	if proxyID != "" {
		h.proxyPaths.set(proxyID, projectPath)
	}
}

// BroadcastLogEntry sends a log entry to all matching stream sinks.
// Called from the TrafficLogger callback when a new entry is logged.
//
// The send into sink.Ch is wrapped by sendToStreamSinkSafe so that a
// sink removed between the RLock snapshot and the send does not panic
// with send-on-closed-channel. RemoveStreamSink closes sink.Ch under
// the write lock; a BroadcastLogEntry goroutine that already snapshotted
// the slice before the remove will hold a dangling pointer and would
// otherwise crash the daemon on the next send.
func (h *AlertHub) BroadcastLogEntry(entry proxy.LogEntry, proxyID string) {
	h.mu.RLock()
	sinks := make([]*StreamSink, len(h.streamSinks))
	copy(sinks, h.streamSinks)
	h.mu.RUnlock()

	proxyPath := h.proxyPaths.get(proxyID)

	for _, sink := range sinks {
		if sink.filter.matches(entry, proxyID, proxyPath) {
			sendToStreamSinkSafe(sink, entry, proxyID)
		}
	}
}

// sendToStreamSinkSafe performs the non-blocking channel-send-with-default
// on a StreamSink's Ch, serialized against concurrent close via the
// closed/refs atomics on StreamSink (no RWMutex on the hot path).
//
// Algorithm:
//  1. Fast-fail if closed observed before incrementing refs.
//  2. refs++ to declare the producer is in flight.
//  3. Re-check closed: if RemoveStreamSink set it between (1) and (2),
//     the closer has not yet seen our refs increment but will before
//     close(Ch). Decrement and exit without sending.
//  4. Non-blocking send. Channel buffer absorbs bursts; full → drop.
//  5. refs-- regardless of send outcome.
//
// The deferred recover is retained as a belt-and-braces guard. It should
// never fire under the contract above; if it does, it indicates the
// invariant was violated by a future refactor.
//
// Isolated into its own function so the recover defer runs once per sink
// rather than once per BroadcastLogEntry call — a panic on sink N would
// otherwise skip sinks N+1..end.
func sendToStreamSinkSafe(sink *StreamSink, entry proxy.LogEntry, proxyID string) {
	defer func() {
		if r := recover(); r != nil {
			debug.Log("alert-hub", "stream sink send recovered (sink closed during broadcast): %v", r)
		}
	}()
	if sink.closed.Load() {
		return
	}
	sink.refs.Add(1)
	defer sink.refs.Add(-1)
	if sink.closed.Load() {
		return
	}
	select {
	case sink.Ch <- entry:
	default:
		if entry.Type == proxy.LogTypeProcessOutput && entry.ProcessOutput != nil {
			debug.Trace("alert-hub", "stream sink channel full, dropping process output proc=%s", entry.ProcessOutput.ProcessID)
		} else {
			debug.Trace("alert-hub", "stream sink channel full, dropping event type=%s proxy=%s", entry.Type, proxyID)
		}
	}
}

// AddHookSink registers a hook event sink. Safe to call while the drain
// goroutine is running — registration takes the write lock, broadcast
// takes the read lock, so the sink starts seeing events on the next push
// after this call returns.
func (h *AlertHub) AddHookSink(sink HookEventSink) {
	if sink == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hookSinks = append(h.hookSinks, sink)
}

// RemoveHookSink unregisters a hook event sink. No-op if the sink was
// never registered. Matching is by interface identity (pointer equality
// for pointer receivers).
func (h *AlertHub) RemoveHookSink(sink HookEventSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, s := range h.hookSinks {
		if s == sink {
			h.hookSinks = append(h.hookSinks[:i], h.hookSinks[i+1:]...)
			return
		}
	}
}

// BroadcastHookEvent fans a single HookEvent out to every registered
// hook sink. Called exclusively from drainHooks on the dedicated drain
// goroutine, so ordering is preserved: sinks see events in the order
// they were pushed into the ring buffer.
//
// The sink slice is copied under the read lock and emission happens
// outside the lock so a slow sink cannot block registration. Sinks are
// contracted to be non-blocking; if that contract is violated the drain
// goroutine stalls and ring buffer overflow kicks in — which is the
// right failure mode (surfaced via hookRing.OverflowCount).
func (h *AlertHub) BroadcastHookEvent(ev HookEvent) {
	h.mu.RLock()
	sinks := make([]HookEventSink, len(h.hookSinks))
	copy(sinks, h.hookSinks)
	h.mu.RUnlock()

	for _, sink := range sinks {
		sink.EmitHookEvent(ev)
	}
}

// BroadcastProcessOutput sends a process output line to all matching stream sinks.
// This reuses the same sink mechanism as BroadcastLogEntry so process events
// flow through the unified STREAM-EVENTS channel alongside proxy events.
//
// Uses sendToStreamSinkSafe for the same close-during-broadcast protection
// documented on BroadcastLogEntry.
func (h *AlertHub) BroadcastProcessOutput(entry proxy.LogEntry) {
	h.mu.RLock()
	sinks := make([]*StreamSink, len(h.streamSinks))
	copy(sinks, h.streamSinks)
	h.mu.RUnlock()

	for _, sink := range sinks {
		if sink.filter.matches(entry, "", "") {
			sendToStreamSinkSafe(sink, entry, "")
		}
	}
}

// containsSubstring reports whether substr is within s.
func containsSubstring(s, substr string) bool {
	return strings.Contains(s, substr)
}

// Deliver sends a pre-formatted alert message to the available delivery
// mechanisms (PTY overlay, MCP notifications, and browser toasts), gated by the
// push config. StreamSink is NOT driven here — it receives events via
// BroadcastLogEntry.
func (h *AlertHub) Deliver(severity string, formatted string) {
	if formatted == "" {
		return
	}

	h.mu.RLock()
	overlaySink := h.overlaySink
	mcpSinks := make([]MCPAlertSink, len(h.mcpSinks))
	copy(mcpSinks, h.mcpSinks)
	pushCfg := h.pushConfig
	pb := h.proxyBroadcaster
	h.mu.RUnlock()

	// Try overlay (PTY stdin injection)
	if pushCfg.PTYInjectionEnabled() && overlaySink != nil && overlaySink.IsEnabled() {
		if err := overlaySink.TypeAlert(formatted); err != nil {
			debug.Error("alerts", "overlay delivery failed: %v", err)
		}
	}

	// Also deliver via MCP session notifications
	if pushCfg.MCPNotificationsEnabled() {
		for _, sink := range mcpSinks {
			if err := sink.SendAlert(severity, formatted); err != nil {
				debug.Error("alerts", "MCP delivery failed: %v", err)
			}
		}
	}

	// Broadcast warning/error alerts to connected browser overlays as toasts.
	// Info-level alerts are intentionally excluded — they are noise on the overlay.
	if pb != nil && (severity == "error" || severity == "warning") {
		title := "Process Error"
		pb.BroadcastAlertToast(severity, title, formatted)
	}
}

// proxyManagerBroadcaster adapts *proxy.ProxyManager to the ProxyBroadcaster
// interface so AlertHub does not need to depend on the ProxyManager concrete type.
// BroadcastAlertToast fans out to all active proxies; per-proxy errors are
// swallowed at debug level so one stalled WebSocket client cannot block others.
type proxyManagerBroadcaster struct {
	pm *proxy.ProxyManager
}

// newProxyManagerBroadcaster creates a ProxyBroadcaster backed by pm.
// Passing a nil pm is valid — BroadcastAlertToast becomes a no-op.
func newProxyManagerBroadcaster(pm *proxy.ProxyManager) *proxyManagerBroadcaster {
	return &proxyManagerBroadcaster{pm: pm}
}

// BroadcastAlertToast sends a toast notification to all proxies managed by pm.
func (b *proxyManagerBroadcaster) BroadcastAlertToast(toastType, title, message string) {
	if b.pm == nil {
		return
	}
	for _, p := range b.pm.List() {
		if p == nil {
			continue
		}
		if _, err := p.BroadcastToast(toastType, title, message, 0); err != nil {
			debug.Log("alert-hub", "BroadcastAlertToast failed for proxy %s: %v", p.ID, err)
		}
	}
}
