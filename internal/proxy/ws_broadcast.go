package proxy

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/debug"
)

// broadcastRaw sends data to every registered wsWriter. Failed sends are
// silently ignored (best-effort broadcast); the broken connection will be
// removed when its handler goroutine detects the next read error.
func (ps *ProxyServer) broadcastRaw(data []byte) int {
	sent := 0
	ps.wsConns.Range(func(_, value interface{}) bool {
		conn := connFromMap(value)
		if conn == nil {
			return true
		}
		if conn.WriteMessage(websocket.TextMessage, data) == nil {
			sent++
		}
		return true
	})
	return sent
}

// connTrusted reports whether a wsConns registry value is a trusted
// connection. A connection registered by handleWebSocket carries the verdict
// controlCallerTrusted reached on its upgrade (default: untrusted). Any other
// value is a writer a test stored directly; production has no such entry.
func connTrusted(value interface{}) bool {
	if aw, ok := value.(*asyncWSWriter); ok {
		return aw.trusted
	}
	return true
}

// trustedConnIDs lists the registry ids of the trusted connections.
func (ps *ProxyServer) trustedConnIDs() []string {
	var ids []string
	ps.wsConns.Range(func(key, value interface{}) bool {
		if id, ok := key.(string); ok && connTrusted(value) {
			ids = append(ids, id)
		}
		return true
	})
	return ids
}

// sendToConns sends data to the named connections, returning how many took it.
func (ps *ProxyServer) sendToConns(connIDs []string, data []byte) int {
	sent := 0
	for _, id := range connIDs {
		value, ok := ps.wsConns.Load(id)
		if !ok {
			continue
		}
		if conn := connFromMap(value); conn != nil && conn.WriteMessage(websocket.TextMessage, data) == nil {
			sent++
		}
	}
	return sent
}

// broadcastTrusted is broadcastRaw for frames that carry the agent's or the
// operator's own material — terminal output previews, tool-call events, the
// secret-entry prompt. A visitor on an unauthenticated tunnel is a connected
// client too, and must not be sent them.
func (ps *ProxyServer) broadcastTrusted(data []byte) int {
	return ps.sendToConns(ps.trustedConnIDs(), data)
}

// ProxyDiagnosticLevel indicates the severity of a diagnostic event.
type ProxyDiagnosticLevel string

const (
	DiagnosticInfo    ProxyDiagnosticLevel = "info"
	DiagnosticWarning ProxyDiagnosticLevel = "warning"
	DiagnosticError   ProxyDiagnosticLevel = "error"
)

// ProxyDiagnostic represents a diagnostic event from the proxy server.
type ProxyDiagnostic struct {
	Timestamp time.Time            `json:"timestamp"`
	Level     ProxyDiagnosticLevel `json:"level"`
	Category  string               `json:"category"` // "proxy", "connection", "request"
	Event     string               `json:"event"`    // "error", "timeout", "refused", etc.
	Message   string               `json:"message"`
	RequestID string               `json:"request_id,omitempty"`
	Method    string               `json:"method,omitempty"`
	URL       string               `json:"url,omitempty"`
	Target    string               `json:"target,omitempty"`
	Data      map[string]any       `json:"data,omitempty"`
}

// BroadcastActivityState sends an activity state update to all connected browser clients.
// Returns the number of clients that received the update.
func (ps *ProxyServer) BroadcastActivityState(active bool) int {
	message := map[string]interface{}{
		"type": "activity",
		"payload": map[string]interface{}{
			"active": active,
		},
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		debug.Error("proxy", "BroadcastActivityState: failed to marshal: %v", err)
		return 0
	}

	return ps.broadcastRaw(messageBytes)
}

// ToastOptions describes a toast broadcast to browser clients. Input/Name
// request the secret-entry masked field (see ws_handler "secret_submit").
type ToastOptions struct {
	Type     string
	Title    string
	Message  string
	Duration int
	Input    string // "" or "secret"
	Name     string // secret name when Input == "secret"
}

// BroadcastToast sends a toast notification to all connected browser clients.
// Returns the number of clients that received the toast.
func (ps *ProxyServer) BroadcastToast(toastType, title, message string, duration int) (int, error) {
	return ps.BroadcastToastOpts(ToastOptions{Type: toastType, Title: title, Message: message, Duration: duration})
}

// BroadcastToastOpts is BroadcastToast with the full option set, including
// the secret-entry input mode.
func (ps *ProxyServer) BroadcastToastOpts(o ToastOptions) (int, error) {
	toastType, title, message, duration := o.Type, o.Title, o.Message, o.Duration
	debug.Log("proxy", "BroadcastToast called: proxy=%s type=%s title=%q message=%q input=%s", ps.ID, toastType, title, message, o.Input)

	// Count connected clients first for debugging
	connCount := 0
	ps.wsConns.Range(func(key, value interface{}) bool {
		connCount++
		return true
	})
	debug.Log("proxy", "BroadcastToast: %d WebSocket clients connected to proxy %s", connCount, ps.ID)

	// Build toast message
	toast := map[string]interface{}{
		"type": "toast",
		"payload": map[string]interface{}{
			"type":    toastType,
			"title":   title,
			"message": message,
		},
	}

	// Only include duration if non-zero
	if duration > 0 {
		toast["payload"].(map[string]interface{})["duration"] = duration
	}

	// Secret-entry input mode: the browser renders a masked field and routes
	// the submitted value via "secret_submit" (never through panel events).
	if o.Input != "" {
		payload := toast["payload"].(map[string]interface{})
		payload["input"] = o.Input
		payload["name"] = o.Name
	}

	messageBytes, err := json.Marshal(toast)
	if err != nil {
		debug.Error("proxy", "BroadcastToast: failed to marshal: %v", err)
		return 0, fmt.Errorf("failed to marshal toast: %w", err)
	}

	// Toasts carry agent- and operator-authored text (and, with Input, the
	// secret-entry prompt): trusted connections only.
	sentCount := ps.broadcastTrusted(messageBytes)
	debug.Log("proxy", "BroadcastToast: sent=%d total=%d", sentCount, connCount)

	// Return success even with no clients - caller can check sent_count
	// This makes toast a "best effort" operation that doesn't fail builds/workflows
	return sentCount, nil
}

// BroadcastOutputPreview sends output preview lines to all connected browser clients.
// throbber is the in-flight animated line (spinner text), rendered in place client-side.
// Returns the number of clients that received the preview.
func (ps *ProxyServer) BroadcastOutputPreview(lines []string, throbber string) int {
	message := map[string]interface{}{
		"type": "output_preview",
		"payload": map[string]interface{}{
			"lines":    lines,
			"throbber": throbber,
		},
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		debug.Error("proxy", "BroadcastOutputPreview: failed to marshal: %v", err)
		return 0
	}

	// The agent's terminal output: trusted connections only.
	return ps.broadcastTrusted(messageBytes)
}

// BroadcastToolEvent sends an agent tool-call state event to all connected
// browser clients. action is "call" (tool starting), "done" (finished OK), or
// "error" (finished with an error); detail is a compact human-readable summary
// (command, file path, error text). Rendered by the indicator's tool_event
// handler as micro-toasts + history entries with per-state icons/colors.
// Returns the number of clients that received the event.
func (ps *ProxyServer) BroadcastToolEvent(name, action, detail string) int {
	message := map[string]interface{}{
		"type": "tool_event",
		"payload": map[string]interface{}{
			"name":   name,
			"action": action,
			"detail": detail,
		},
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		debug.Error("proxy", "BroadcastToolEvent: failed to marshal: %v", err)
		return 0
	}

	// The agent's commands and file paths: trusted connections only.
	return ps.broadcastTrusted(messageBytes)
}

// BroadcastProxyDiagnostic sends a diagnostic event to all connected browser clients.
// This allows the browser to receive server-side proxy errors for debugging.
// Returns the number of clients that received the diagnostic.
func (ps *ProxyServer) BroadcastProxyDiagnostic(diag *ProxyDiagnostic) int {
	if diag == nil {
		return 0
	}

	// Also log to traffic logger for MCP access
	ps.logger.LogDiagnostic(*diag)

	message := map[string]interface{}{
		"type":    "proxy_diagnostic",
		"payload": diag,
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		debug.Error("proxy", "BroadcastProxyDiagnostic: marshal failed: %v", err)
		return 0
	}

	sentCount := ps.broadcastRaw(messageBytes)
	debug.Log("proxy", "BroadcastProxyDiagnostic: sent to %d clients: %s - %s", sentCount, diag.Event, diag.Message)
	return sentCount
}
