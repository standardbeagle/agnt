package daemon

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// DriftMetricsSnapshot holds a point-in-time snapshot of the dual-path event
// counters. OldPathCount counts Deliver() calls that reached legacy sinks;
// NewPathCount counts events acknowledged via IncrNewPath() (called by the
// incident adapter layer). The two counts converge over time when both paths
// are active. A growing delta indicates a bug in adapter wiring.
type DriftMetricsSnapshot struct {
	OldPathCount int64
	NewPathCount int64
}

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
// closeMu serializes Ch close against in-flight BroadcastLogEntry sends.
// Producers take closeMu.RLock for the duration of a single send attempt;
// RemoveStreamSink takes closeMu.Lock before closing Ch. This eliminates
// the "send on closed channel" panic AND the concurrent write-read-on-
// hchan race that -race reports. The lock is strictly bounded (the hold
// interval is a single non-blocking select), so contention is negligible.
//
// closed is set to true under closeMu.Lock before Ch is closed. Producers
// check closed before the select so a late sender (slice snapshot already
// taken) exits cheaply without hitting the closed channel.
type StreamSink struct {
	Ch      chan proxy.LogEntry
	filter  streamFilter
	closeMu sync.RWMutex
	closed  bool
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

// AlertHub routes formatted alert messages to available delivery mechanisms:
// PTY overlay (stdin injection), MCP session notifications, and stream sinks.
type AlertHub struct {
	overlaySink OverlayAlertSink
	mcpSinks    []MCPAlertSink
	streamSinks []*StreamSink
	hookSinks   []HookEventSink
	pushConfig  *config.PushConfig
	mu          sync.RWMutex
	proxyPaths  proxyPathRegistry // proxyID → project path for stream routing

	// Incident pipeline dual-path fields (Phase A migration).
	// incidentPipeline gates the old MCPAlertSink/OverlayAlertSink fan-out
	// in Deliver(). When true, those sinks are skipped; when false (default)
	// the old path is active and the incident.Bus runs in observability mode.
	// Access is protected by the same mu as the other AlertHub fields.
	incidentPipeline bool
	incidentBus      incident.Bus

	// Drift metrics — atomics for lock-free reads by the metrics subsystem.
	// oldPathCount increments on each Deliver() call regardless of the flag
	// (i.e. when old sinks fire). newPathCount is bumped by IncrNewPath
	// which is called by the incident adapter after successful Bus publication.
	oldPathCount atomic.Int64
	newPathCount atomic.Int64
}

// NewAlertHub creates a new AlertHub.
func NewAlertHub() *AlertHub {
	return &AlertHub{}
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
		Ch:     make(chan proxy.LogEntry, 64),
		filter: filter,
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.streamSinks = append(h.streamSinks, sink)
	return sink
}

// RemoveStreamSink unregisters a stream sink and closes its channel.
//
// The close path is serialized against in-flight sends via sink.closeMu:
// we take the per-sink write lock before setting closed=true and calling
// close(sink.Ch). Concurrent BroadcastLogEntry goroutines that already
// snapshotted the hub's streamSinks slice will block briefly on
// sink.closeMu.RLock() in sendToStreamSinkSafe, observe closed==true, and
// return without attempting to send. This eliminates both the send-on-
// closed-channel panic and the concurrent write/read race on the channel
// header.
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
			sink.closeMu.Lock()
			sink.closed = true
			close(sink.Ch)
			sink.closeMu.Unlock()
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
// on a StreamSink's Ch, serialized against concurrent close via sink.closeMu.
//
// The primary correctness guarantee: RemoveStreamSink takes closeMu.Lock
// before closing Ch, so a producer that holds closeMu.RLock here cannot
// observe a closed Ch mid-send. The closed flag covers the case where
// the sink was removed between the hub's RLock slice-snapshot and this
// call — we check closed first and exit without touching Ch.
//
// A deferred recover is kept as a belt-and-braces guard in case the
// invariant above is ever violated by a future refactor; it logs and
// swallows, never propagates.
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
	sink.closeMu.RLock()
	defer sink.closeMu.RUnlock()
	if sink.closed {
		return
	}
	select {
	case sink.Ch <- entry:
	default:
		if entry.Type == proxy.LogTypeProcessOutput && entry.ProcessOutput != nil {
			debug.Warn("alert-hub", "stream sink channel full, dropping process output proc=%s", entry.ProcessOutput.ProcessID)
		} else {
			debug.Warn("alert-hub", "stream sink channel full, dropping event type=%s proxy=%s", entry.Type, proxyID)
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

// SetIncidentPipeline sets the incident pipeline flag. When true, Deliver
// suppresses MCPAlertSink and OverlayAlertSink fan-out; StreamSink is
// unaffected. Default is false (old behaviour, dual-path observability mode).
func (h *AlertHub) SetIncidentPipeline(enabled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.incidentPipeline = enabled
}

// SetIncidentBus wires the incident.Bus for dual-path mode. When the bus is
// non-nil and incidentPipeline is false, both paths run in parallel.
// When incidentPipeline is true, only the bus path is active for Deliver.
// A nil bus is valid (NopBus semantics — old path is unaffected).
func (h *AlertHub) SetIncidentBus(bus incident.Bus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.incidentBus = bus
}

// IncrNewPath increments the new-path event counter. Called by the incident
// adapter layer after it successfully publishes an event to the incident.Bus.
// Enables drift detection by comparing with OldPathCount.
func (h *AlertHub) IncrNewPath() {
	h.newPathCount.Add(1)
}

// DriftMetrics returns a snapshot of the dual-path event counters.
// OldPathCount reflects Deliver() calls that reached legacy sinks (flag=false
// only). NewPathCount reflects IncrNewPath() calls from the incident adapter.
func (h *AlertHub) DriftMetrics() *DriftMetricsSnapshot {
	return &DriftMetricsSnapshot{
		OldPathCount: h.oldPathCount.Load(),
		NewPathCount: h.newPathCount.Load(),
	}
}

// Deliver sends a pre-formatted alert message to all available sinks.
// Checks the push config to determine which channels are enabled.
//
// When incidentPipeline is true (Phase A: flag-enabled mode), both
// MCPAlertSink and OverlayAlertSink fan-out are suppressed; delivery is
// handled by the incident.Bus Pinger instead. StreamSink is NOT gated here —
// it receives events via BroadcastLogEntry regardless of the flag.
//
// When incidentPipeline is false (default), the old path fires and
// oldPathCount is incremented for drift monitoring.
func (h *AlertHub) Deliver(severity string, formatted string) {
	if formatted == "" {
		return
	}

	h.mu.RLock()
	overlaySink := h.overlaySink
	mcpSinks := make([]MCPAlertSink, len(h.mcpSinks))
	copy(mcpSinks, h.mcpSinks)
	pushCfg := h.pushConfig
	pipeline := h.incidentPipeline
	h.mu.RUnlock()

	// When the incident pipeline is active, suppress the old sinks.
	// The new Pinger (wired via incident.Bus sessions) handles delivery.
	if pipeline {
		return
	}

	// Old path: PTY overlay + MCP notifications.
	h.oldPathCount.Add(1)

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
}
