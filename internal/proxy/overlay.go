package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
)

// OverlayNotifier sends events to the agent overlay server via Unix socket.
type OverlayNotifier struct {
	socketPath string
	client     *http.Client
	enabled    bool
	mu         sync.RWMutex

	// The error-event queue (EnqueueHTTPError, EnqueueBrowserError).
	queueInit  sync.Once
	errQueue   chan OverlayEvent
	sending    atomic.Bool
	errDropped atomic.Int64
}

// OverlayEvent represents an event to send to the overlay.
type OverlayEvent struct {
	Type      string      `json:"type"`
	ProxyID   string      `json:"proxy_id"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// NewOverlayNotifier creates a new overlay notifier.
func NewOverlayNotifier() *OverlayNotifier {
	return &OverlayNotifier{
		enabled: false,
	}
}

// SetEndpoint sets the overlay socket path.
// Example: "/run/user/1000/devtool-overlay.sock" or "\\.\pipe\devtool-overlay-user"
func (n *OverlayNotifier) SetEndpoint(socketPath string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.setEndpointLocked(socketPath)
}

// CompareAndSwapEndpoint replaces the endpoint with next only while it is
// still old, so a caller retiring one session's binding cannot clobber a
// binding another session installed in the meantime.
func (n *OverlayNotifier) CompareAndSwapEndpoint(old, next string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.socketPath != old {
		return false
	}
	n.setEndpointLocked(next)
	return true
}

func (n *OverlayNotifier) setEndpointLocked(socketPath string) {
	n.socketPath = socketPath
	n.enabled = socketPath != ""

	if n.enabled {
		debug.Log("overlay-notifier", "endpoint set: %s", socketPath)
		// Create HTTP client with Unix socket transport. The dial honours the
		// request context (and so the client timeout).
		var dialer net.Dialer
		n.client = &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, "unix", socketPath)
				},
			},
		}
	} else {
		debug.Log("overlay-notifier", "endpoint cleared")
		n.client = nil
	}
}

// GetEndpoint returns the current socket path.
func (n *OverlayNotifier) GetEndpoint() string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.socketPath
}

// IsEnabled returns whether the notifier is enabled.
func (n *OverlayNotifier) IsEnabled() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.enabled
}

// NotifyPanelMessage sends a panel message to the overlay.
func (n *OverlayNotifier) NotifyPanelMessage(proxyID string, msg *PanelMessage) error {
	return n.send(OverlayEvent{
		Type:      "panel_message",
		ProxyID:   proxyID,
		Timestamp: time.Now(),
		Data:      msg,
	})
}

// NotifySketch sends a sketch to the overlay.
func (n *OverlayNotifier) NotifySketch(proxyID string, sketch *SketchEntry) error {
	return n.send(OverlayEvent{
		Type:      "sketch",
		ProxyID:   proxyID,
		Timestamp: time.Now(),
		Data:      sketch,
	})
}

// NotifyDesignState sends design state to the overlay.
func (n *OverlayNotifier) NotifyDesignState(proxyID string, state *DesignState) error {
	return n.send(OverlayEvent{
		Type:      "design_state",
		ProxyID:   proxyID,
		Timestamp: time.Now(),
		Data:      state,
	})
}

// NotifyDesignRequest sends a design request to the overlay.
func (n *OverlayNotifier) NotifyDesignRequest(proxyID string, request *DesignRequest) error {
	return n.send(OverlayEvent{
		Type:      "design_request",
		ProxyID:   proxyID,
		Timestamp: time.Now(),
		Data:      request,
	})
}

// NotifyDesignChat sends a design chat message to the overlay.
func (n *OverlayNotifier) NotifyDesignChat(proxyID string, chat *DesignChat) error {
	return n.send(OverlayEvent{
		Type:      "design_chat",
		ProxyID:   proxyID,
		Timestamp: time.Now(),
		Data:      chat,
	})
}

// NotifyDesignEdit sends a committed design geometry edit to the overlay.
func (n *OverlayNotifier) NotifyDesignEdit(proxyID string, edit *DesignEdit) error {
	return n.send(OverlayEvent{
		Type:      "design_edit",
		ProxyID:   proxyID,
		Timestamp: time.Now(),
		Data:      edit,
	})
}

// NotifyResponsiveRequest sends a responsive mode handoff request to the overlay.
func (n *OverlayNotifier) NotifyResponsiveRequest(proxyID string, request *ResponsiveRequest) error {
	return n.send(OverlayEvent{
		Type:      "responsive_request",
		ProxyID:   proxyID,
		Timestamp: time.Now(),
		Data:      request,
	})
}

// NotifyResponsiveState sends responsive mode panel state to the overlay.
func (n *OverlayNotifier) NotifyResponsiveState(proxyID string, state *ResponsiveState) error {
	return n.send(OverlayEvent{
		Type:      "responsive_state",
		ProxyID:   proxyID,
		Timestamp: time.Now(),
		Data:      state,
	})
}

// EnqueueBrowserError queues a browser JS error for the overlay's
// auto-forward without blocking the caller — the control WebSocket's read
// loop, which would otherwise stall every later frame from that page for up
// to the client timeout. Shares EnqueueHTTPError's queue and drop count.
func (n *OverlayNotifier) EnqueueBrowserError(proxyID string, err FrontendError) {
	n.enqueue(OverlayEvent{
		Type:      "browser_error",
		ProxyID:   proxyID,
		Timestamp: time.Now(),
		Data:      err,
	})
}

func httpErrorEvent(proxyID string, entry HTTPLogEntry) OverlayEvent {
	return OverlayEvent{
		Type:      "http_error",
		ProxyID:   proxyID,
		Timestamp: time.Now(),
		Data:      httpErrorPayload(entry),
	}
}

// httpErrorOverlayData is what the overlay is sent about an HTTP error: the
// fields it renders and dedups on (method, URL, status, response body for the
// error message, error text). Request and response headers and the request
// body are left out — they carry the browser's Cookie and Authorization, and
// none of it is rendered; the full transaction stays in the traffic log.
type httpErrorOverlayData struct {
	ID           string        `json:"id"`
	Timestamp    time.Time     `json:"timestamp"`
	Method       string        `json:"method"`
	URL          string        `json:"url"`
	StatusCode   int           `json:"status_code"`
	ResponseBody string        `json:"response_body,omitempty"`
	Duration     time.Duration `json:"duration"`
	Error        string        `json:"error,omitempty"`
}

func httpErrorPayload(entry HTTPLogEntry) httpErrorOverlayData {
	return httpErrorOverlayData{
		ID:           entry.ID,
		Timestamp:    entry.Timestamp,
		Method:       entry.Method,
		URL:          entry.URL,
		StatusCode:   entry.StatusCode,
		ResponseBody: entry.ResponseBody,
		Duration:     entry.Duration,
		Error:        entry.Error,
	}
}

// errorQueueSize bounds the error events waiting for the overlay. A burst
// beyond it (an error storm) is dropped and counted; the overlay dedups and
// batches such a storm into a few lines anyway, and every error is still in
// the traffic log.
const errorQueueSize = 64

// EnqueueHTTPError queues an HTTP error for the overlay without blocking the
// caller (the proxy's request goroutine).
func (n *OverlayNotifier) EnqueueHTTPError(proxyID string, entry HTTPLogEntry) {
	n.enqueue(httpErrorEvent(proxyID, entry))
}

// enqueue queues an error event for the overlay. One sender goroutine drains
// the queue in order and exits when it is empty, so an idle notifier holds no
// goroutine. When the queue is full the event is dropped and counted
// (ErrorsDropped).
func (n *OverlayNotifier) enqueue(ev OverlayEvent) {
	n.queueInit.Do(func() { n.errQueue = make(chan OverlayEvent, errorQueueSize) })
	select {
	case n.errQueue <- ev:
	default:
		if d := n.errDropped.Add(1); d == 1 || d%100 == 0 {
			debug.Warn("overlay-notifier", "error event queue full: %d event(s) dropped so far", d)
		}
		return
	}
	n.startSender()
}

// ErrorsDropped is how many error events the queue dropped because it was
// full.
func (n *OverlayNotifier) ErrorsDropped() int64 { return n.errDropped.Load() }

// startSender starts the queue's sender unless one is running. The sender
// clears the running flag when it finds the queue empty and then re-checks,
// so an event enqueued in that window is never stranded.
func (n *OverlayNotifier) startSender() {
	if !n.sending.CompareAndSwap(false, true) {
		return
	}
	go func() {
		for {
			select {
			case ev := <-n.errQueue:
				if err := n.send(ev); err != nil {
					debug.Log("overlay-notifier", "failed to forward %s to overlay: %v", ev.Type, err)
				}
				continue
			default:
			}
			n.sending.Store(false)
			if len(n.errQueue) == 0 || !n.sending.CompareAndSwap(false, true) {
				return
			}
		}
	}()
}

func (n *OverlayNotifier) send(event OverlayEvent) error {
	n.mu.RLock()
	if !n.enabled || n.client == nil {
		n.mu.RUnlock()
		debug.Log("overlay-notifier", "event %s dropped: notifier not enabled (enabled=%v, client=%v, socketPath=%q)",
			event.Type, n.enabled, n.client != nil, n.socketPath)
		return nil
	}
	client := n.client
	socketPath := n.socketPath
	n.mu.RUnlock()

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	debug.Log("overlay-notifier", "sending %s event to overlay socket %s (%d bytes)", event.Type, socketPath, len(data))

	// Use dummy host - actual connection is via Unix socket
	resp, err := client.Post("http://localhost/event", "application/json", bytes.NewReader(data))
	if err != nil {
		debug.Log("overlay-notifier", "failed to POST %s event to overlay socket %s: %v", event.Type, socketPath, err)
		return err
	}
	defer resp.Body.Close()

	debug.Log("overlay-notifier", "%s event delivered to overlay (status=%d)", event.Type, resp.StatusCode)
	return nil
}
