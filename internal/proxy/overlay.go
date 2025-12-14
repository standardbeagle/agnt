package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// OverlayNotifier sends events to the agent overlay server via Unix socket.
type OverlayNotifier struct {
	socketPath string
	client     *http.Client
	enabled    bool
	mu         sync.RWMutex
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
	n.socketPath = socketPath
	n.enabled = socketPath != ""

	if n.enabled {
		// Create HTTP client with Unix socket transport
		n.client = &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		}
	} else {
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

// NotifyInteraction sends an interaction event to the overlay.
func (n *OverlayNotifier) NotifyInteraction(proxyID string, interaction *InteractionEvent) error {
	return n.send(OverlayEvent{
		Type:      "interaction",
		ProxyID:   proxyID,
		Timestamp: time.Now(),
		Data:      interaction,
	})
}

// NotifyCustom sends a custom event to the overlay.
func (n *OverlayNotifier) NotifyCustom(proxyID, eventType string, data interface{}) error {
	return n.send(OverlayEvent{
		Type:      eventType,
		ProxyID:   proxyID,
		Timestamp: time.Now(),
		Data:      data,
	})
}

func (n *OverlayNotifier) send(event OverlayEvent) error {
	n.mu.RLock()
	if !n.enabled || n.client == nil {
		n.mu.RUnlock()
		log.Printf("[OverlayNotifier] not enabled or no client, dropping event type=%s", event.Type)
		return nil
	}
	socketPath := n.socketPath
	client := n.client
	n.mu.RUnlock()

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("[OverlayNotifier] failed to marshal event: %v", err)
		return err
	}

	log.Printf("[OverlayNotifier] sending event type=%s to socket=%s", event.Type, socketPath)

	// Use dummy host - actual connection is via Unix socket
	resp, err := client.Post("http://localhost/event", "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("[OverlayNotifier] failed to send event to overlay: %v", err)
		return err
	}
	defer resp.Body.Close()

	log.Printf("[OverlayNotifier] event sent successfully, status=%d", resp.StatusCode)
	return nil
}

// TypeToOverlay sends a type command to the overlay.
// The overlay will inject this text into the PTY.
func (n *OverlayNotifier) TypeToOverlay(text string, enter bool) error {
	n.mu.RLock()
	if !n.enabled || n.client == nil {
		n.mu.RUnlock()
		return nil
	}
	client := n.client
	n.mu.RUnlock()

	data, err := json.Marshal(map[string]interface{}{
		"text":    text,
		"enter":   enter,
		"instant": true,
	})
	if err != nil {
		return err
	}

	// Use dummy host - actual connection is via Unix socket
	resp, err := client.Post("http://localhost/type", "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

// SendKeyToOverlay sends a key to the overlay.
func (n *OverlayNotifier) SendKeyToOverlay(key string, ctrl, alt, shift bool) error {
	n.mu.RLock()
	if !n.enabled || n.client == nil {
		n.mu.RUnlock()
		return nil
	}
	client := n.client
	n.mu.RUnlock()

	data, err := json.Marshal(map[string]interface{}{
		"key":   key,
		"ctrl":  ctrl,
		"alt":   alt,
		"shift": shift,
	})
	if err != nil {
		return err
	}

	// Use dummy host - actual connection is via Unix socket
	resp, err := client.Post("http://localhost/key", "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}
