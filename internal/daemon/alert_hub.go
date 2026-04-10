package daemon

import (
	"sync"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// MCPAlertSink delivers alert messages via MCP session notifications.
type MCPAlertSink interface {
	SendAlert(level string, message string) error
}

// OverlayAlertSink delivers alert messages via PTY stdin injection.
type OverlayAlertSink interface {
	TypeAlert(text string) error
	IsEnabled() bool
}

// StreamSink receives filtered proxy log events via a channel.
// The consumer reads from Ch until it is closed (on unregister or daemon shutdown).
type StreamSink struct {
	Ch     chan proxy.LogEntry
	filter streamFilter
}

// streamFilter holds criteria for filtering events sent to a StreamSink.
type streamFilter struct {
	types    map[proxy.LogEntryType]bool
	proxyID  string
	severity string // "error", "warning", "info" — matches against error/custom/diagnostic levels
}

// matches returns true if the log entry passes the filter.
func (f *streamFilter) matches(entry proxy.LogEntry, proxyID string) bool {
	if len(f.types) > 0 && !f.types[entry.Type] {
		return false
	}
	if f.proxyID != "" && proxyID != f.proxyID {
		return false
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
	mu          sync.RWMutex
}

// NewAlertHub creates a new AlertHub.
func NewAlertHub() *AlertHub {
	return &AlertHub{}
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
func (h *AlertHub) RemoveStreamSink(sink *StreamSink) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, s := range h.streamSinks {
		if s == sink {
			h.streamSinks = append(h.streamSinks[:i], h.streamSinks[i+1:]...)
			close(sink.Ch)
			return
		}
	}
}

// BroadcastLogEntry sends a log entry to all matching stream sinks.
// Called from the TrafficLogger callback when a new entry is logged.
func (h *AlertHub) BroadcastLogEntry(entry proxy.LogEntry, proxyID string) {
	h.mu.RLock()
	sinks := make([]*StreamSink, len(h.streamSinks))
	copy(sinks, h.streamSinks)
	h.mu.RUnlock()

	for _, sink := range sinks {
		if sink.filter.matches(entry, proxyID) {
			select {
			case sink.Ch <- entry:
			default:
				debug.Warn("alert-hub", "stream sink channel full, dropping event type=%s proxy=%s", entry.Type, proxyID)
			}
		}
	}
}

// Deliver sends a pre-formatted alert message to all available sinks.
func (h *AlertHub) Deliver(severity string, formatted string) {
	if formatted == "" {
		return
	}

	h.mu.RLock()
	overlaySink := h.overlaySink
	mcpSinks := make([]MCPAlertSink, len(h.mcpSinks))
	copy(mcpSinks, h.mcpSinks)
	h.mu.RUnlock()

	// Try overlay (PTY stdin injection)
	if overlaySink != nil && overlaySink.IsEnabled() {
		if err := overlaySink.TypeAlert(formatted); err != nil {
			debug.Error("alerts", "overlay delivery failed: %v", err)
		}
	}

	// Also deliver via MCP session notifications
	for _, sink := range mcpSinks {
		if err := sink.SendAlert(severity, formatted); err != nil {
			debug.Error("alerts", "MCP delivery failed: %v", err)
		}
	}
}
