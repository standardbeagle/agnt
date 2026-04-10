package proxy

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/debug"
)

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
		return 0
	}

	sentCount := 0
	ps.wsConns.Range(func(key, value interface{}) bool {
		conn := value.(*websocket.Conn)
		err := conn.WriteMessage(websocket.TextMessage, messageBytes)
		if err == nil {
			sentCount++
		}
		return true
	})

	return sentCount
}

// BroadcastToast sends a toast notification to all connected browser clients.
// Returns the number of clients that received the toast.
func (ps *ProxyServer) BroadcastToast(toastType, title, message string, duration int) (int, error) {
	debug.Log("proxy", "BroadcastToast called: proxy=%s type=%s title=%q message=%q", ps.ID, toastType, title, message)

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

	messageBytes, err := json.Marshal(toast)
	if err != nil {
		debug.Error("proxy", "BroadcastToast: failed to marshal: %v", err)
		return 0, fmt.Errorf("failed to marshal toast: %w", err)
	}

	// Send to all connected clients
	sentCount := 0
	failCount := 0
	ps.wsConns.Range(func(key, value interface{}) bool {
		conn := value.(*websocket.Conn)
		err := conn.WriteMessage(websocket.TextMessage, messageBytes)
		if err == nil {
			sentCount++
			debug.Log("proxy", "BroadcastToast: sent to client %v", key)
		} else {
			failCount++
			debug.Log("proxy", "BroadcastToast: failed to send to client %v: %v", key, err)
		}
		return true
	})

	debug.Log("proxy", "BroadcastToast: sent=%d failed=%d total=%d", sentCount, failCount, connCount)

	// Return success even with no clients - caller can check sent_count
	// This makes toast a "best effort" operation that doesn't fail builds/workflows
	return sentCount, nil
}

// BroadcastOutputPreview sends output preview lines to all connected browser clients.
// Returns the number of clients that received the preview.
func (ps *ProxyServer) BroadcastOutputPreview(lines []string) int {
	message := map[string]interface{}{
		"type": "output_preview",
		"payload": map[string]interface{}{
			"lines": lines,
		},
	}

	messageBytes, err := json.Marshal(message)
	if err != nil {
		return 0
	}

	sentCount := 0
	ps.wsConns.Range(func(key, value interface{}) bool {
		conn := value.(*websocket.Conn)
		err := conn.WriteMessage(websocket.TextMessage, messageBytes)
		if err == nil {
			sentCount++
		}
		return true
	})

	return sentCount
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

	sentCount := 0
	ps.wsConns.Range(func(key, value interface{}) bool {
		conn := value.(*websocket.Conn)
		err := conn.WriteMessage(websocket.TextMessage, messageBytes)
		if err == nil {
			sentCount++
		}
		return true
	})

	debug.Log("proxy", "BroadcastProxyDiagnostic: sent to %d clients: %s - %s", sentCount, diag.Event, diag.Message)
	return sentCount
}
