package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Overlay receives events from devtool-mcp and injects them into the PTY.
type Overlay struct {
	port     int
	ptmx     *os.File
	server   *http.Server
	upgrader websocket.Upgrader
	clients  sync.Map // map[*websocket.Conn]bool
	mu       sync.RWMutex
}

// OverlayMessage represents a message from devtool-mcp.
type OverlayMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// TypeMessage is a message to type into the PTY.
type TypeMessage struct {
	Text    string `json:"text"`
	Enter   bool   `json:"enter"`   // Whether to send Enter after text
	Instant bool   `json:"instant"` // Type instantly vs simulate typing
}

// KeyMessage is a key event to inject.
type KeyMessage struct {
	Key      string `json:"key"`      // Key name (e.g., "Enter", "Tab", "Escape")
	Ctrl     bool   `json:"ctrl"`     // Ctrl modifier
	Alt      bool   `json:"alt"`      // Alt modifier
	Shift    bool   `json:"shift"`    // Shift modifier
	Sequence string `json:"sequence"` // Raw escape sequence to send
}

func newOverlay(port int, ptmx *os.File) *Overlay {
	return &Overlay{
		port: port,
		ptmx: ptmx,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for local development
			},
		},
	}
}

func (o *Overlay) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", o.handleWebSocket)
	mux.HandleFunc("/health", o.handleHealth)
	mux.HandleFunc("/type", o.handleType)
	mux.HandleFunc("/key", o.handleKey)
	mux.HandleFunc("/event", o.handleEvent)

	o.server = &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", o.port),
		Handler: mux,
	}

	go func() {
		if err := o.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Overlay server error: %v", err)
		}
	}()

	return nil
}

func (o *Overlay) Stop() {
	if o.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = o.server.Shutdown(ctx)
	}

	// Close all WebSocket connections
	o.clients.Range(func(key, value interface{}) bool {
		if conn, ok := key.(*websocket.Conn); ok {
			_ = conn.Close()
		}
		return true
	})
}

func (o *Overlay) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"port":   o.port,
	})
}

func (o *Overlay) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := o.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	o.clients.Store(conn, true)
	defer o.clients.Delete(conn)

	log.Printf("Overlay client connected")

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		var msg OverlayMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Invalid message: %v", err)
			continue
		}

		o.handleMessage(msg)
	}

	log.Printf("Overlay client disconnected")
}

func (o *Overlay) handleType(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var msg TypeMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	o.typeText(msg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (o *Overlay) handleKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var msg KeyMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	o.sendKey(msg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ProxyEvent represents an event received from devtool-mcp proxy.
type ProxyEvent struct {
	Type      string          `json:"type"`
	ProxyID   string          `json:"proxy_id"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

func (o *Overlay) handleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var event ProxyEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Process the event
	o.processProxyEvent(event)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (o *Overlay) processProxyEvent(event ProxyEvent) {
	log.Printf("Received proxy event: type=%s proxy_id=%s", event.Type, event.ProxyID)

	switch event.Type {
	case "panel_message":
		// Handle panel message - could type it into the AI tool
		var data struct {
			Message     string `json:"message"`
			Attachments []struct {
				Type     string `json:"type"`
				Selector string `json:"selector"`
				Tag      string `json:"tag"`
				Text     string `json:"text"`
			} `json:"attachments"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			log.Printf("Failed to parse panel_message: %v", err)
			return
		}

		// Format the message for the AI tool
		text := data.Message
		if len(data.Attachments) > 0 {
			text += "\n\n[Attachments]\n"
			for i, att := range data.Attachments {
				text += fmt.Sprintf("%d. %s: %s (%s)\n", i+1, att.Type, att.Selector, att.Text)
			}
		}

		// Type the formatted message into the PTY
		o.typeText(TypeMessage{
			Text:    text,
			Enter:   true,
			Instant: true,
		})

	case "sketch":
		// Handle sketch - notify user about the sketch
		var data struct {
			FilePath     string `json:"file_path"`
			ElementCount int    `json:"element_count"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			log.Printf("Failed to parse sketch: %v", err)
			return
		}

		// Notify user about the sketch
		text := fmt.Sprintf("[Sketch saved: %s with %d elements]", data.FilePath, data.ElementCount)
		o.typeText(TypeMessage{
			Text:    text,
			Enter:   true,
			Instant: true,
		})

	case "interaction":
		// Log interactions for debugging
		log.Printf("User interaction: %s", string(event.Data))

	default:
		log.Printf("Unknown proxy event type: %s", event.Type)
	}

	// Broadcast to connected clients
	o.Broadcast("proxy_event", event)
}

func (o *Overlay) handleMessage(msg OverlayMessage) {
	switch msg.Type {
	case "type":
		var typeMsg TypeMessage
		if err := json.Unmarshal(msg.Payload, &typeMsg); err != nil {
			log.Printf("Invalid type message: %v", err)
			return
		}
		o.typeText(typeMsg)

	case "key":
		var keyMsg KeyMessage
		if err := json.Unmarshal(msg.Payload, &keyMsg); err != nil {
			log.Printf("Invalid key message: %v", err)
			return
		}
		o.sendKey(keyMsg)

	case "clear":
		// Send Ctrl+C to clear current input
		o.writeTopty("\x03")

	case "escape":
		// Send Escape key
		o.writeTopty("\x1b")

	default:
		log.Printf("Unknown message type: %s", msg.Type)
	}
}

func (o *Overlay) typeText(msg TypeMessage) {
	if msg.Instant {
		o.writeTopty(msg.Text)
	} else {
		// Simulate typing with small delays
		for _, ch := range msg.Text {
			o.writeTopty(string(ch))
			time.Sleep(10 * time.Millisecond)
		}
	}

	if msg.Enter {
		o.writeTopty("\r")
	}
}

func (o *Overlay) sendKey(msg KeyMessage) {
	// If raw sequence is provided, use it directly
	if msg.Sequence != "" {
		o.writeTopty(msg.Sequence)
		return
	}

	// Build key sequence based on modifiers and key name
	seq := o.buildKeySequence(msg)
	if seq != "" {
		o.writeTopty(seq)
	}
}

func (o *Overlay) buildKeySequence(msg KeyMessage) string {
	// Handle special keys
	switch msg.Key {
	case "Enter", "Return":
		return "\r"
	case "Tab":
		return "\t"
	case "Escape", "Esc":
		return "\x1b"
	case "Backspace":
		return "\x7f"
	case "Delete":
		return "\x1b[3~"
	case "Up":
		return "\x1b[A"
	case "Down":
		return "\x1b[B"
	case "Right":
		return "\x1b[C"
	case "Left":
		return "\x1b[D"
	case "Home":
		return "\x1b[H"
	case "End":
		return "\x1b[F"
	case "PageUp":
		return "\x1b[5~"
	case "PageDown":
		return "\x1b[6~"
	case "Insert":
		return "\x1b[2~"
	}

	// Handle Ctrl+key combinations
	if msg.Ctrl && len(msg.Key) == 1 {
		ch := msg.Key[0]
		if ch >= 'a' && ch <= 'z' {
			return string(ch - 'a' + 1)
		}
		if ch >= 'A' && ch <= 'Z' {
			return string(ch - 'A' + 1)
		}
	}

	// Handle Alt+key combinations (send ESC then key)
	if msg.Alt && len(msg.Key) == 1 {
		return "\x1b" + msg.Key
	}

	// Regular key
	if len(msg.Key) == 1 {
		if msg.Shift {
			return string(msg.Key[0] - 32) // Simple uppercase
		}
		return msg.Key
	}

	return ""
}

func (o *Overlay) writeTopty(s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, _ = o.ptmx.WriteString(s)
}

// Broadcast sends a message to all connected WebSocket clients.
func (o *Overlay) Broadcast(msgType string, payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	msg, err := json.Marshal(OverlayMessage{
		Type:    msgType,
		Payload: data,
	})
	if err != nil {
		return
	}

	o.clients.Range(func(key, value interface{}) bool {
		if conn, ok := key.(*websocket.Conn); ok {
			_ = conn.WriteMessage(websocket.TextMessage, msg)
		}
		return true
	})
}
