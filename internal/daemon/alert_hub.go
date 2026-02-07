package daemon

import (
	"log"
	"sync"
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

// AlertHub routes formatted alert messages to available delivery mechanisms:
// PTY overlay (stdin injection) and/or MCP session notifications.
type AlertHub struct {
	overlaySink OverlayAlertSink
	mcpSinks    []MCPAlertSink
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
			log.Printf("[alerts] overlay delivery failed: %v", err)
		}
	}

	// Also deliver via MCP session notifications
	for _, sink := range mcpSinks {
		if err := sink.SendAlert(severity, formatted); err != nil {
			log.Printf("[alerts] MCP delivery failed: %v", err)
		}
	}
}
