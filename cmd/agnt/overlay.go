package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// PtyWriter is an interface for writing to a PTY.
// This allows both Unix PTY (*os.File) and Windows ConPTY to be used.
type PtyWriter interface {
	io.Writer
}

// Overlay receives events from devtool-mcp and injects them into the PTY.
type Overlay struct {
	socketPath string
	ptmx       PtyWriter
	server     *http.Server
	listener   net.Listener
	upgrader   websocket.Upgrader
	clients    sync.Map // map[*websocket.Conn]bool
	mu         sync.RWMutex
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

// ToastMessage is a toast notification to show in the browser.
type ToastMessage struct {
	Type     string `json:"type"`     // success, error, warning, info
	Title    string `json:"title"`    // Toast title (optional)
	Message  string `json:"message"`  // Toast message
	Duration int    `json:"duration"` // Duration in ms (0 for default)
}

// DefaultOverlaySocketPath returns the default socket path for the overlay.
func DefaultOverlaySocketPath() string {
	// Windows: use Unix domain socket in temp directory (supported since Windows 10 1803)
	// Note: Named pipes (\\.\pipe\...) require different APIs, so we use Unix sockets
	if os.PathSeparator == '\\' {
		username := os.Getenv("USERNAME")
		if username == "" {
			username = "default"
		}
		// Use temp directory for Unix socket on Windows
		return filepath.Join(os.TempDir(), fmt.Sprintf("devtool-overlay-%s.sock", username))
	}

	// Unix: use XDG_RUNTIME_DIR if available, otherwise /tmp
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "devtool-overlay.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("devtool-overlay-%d.sock", os.Getuid()))
}

func newOverlay(socketPath string, ptmx PtyWriter) *Overlay {
	if socketPath == "" {
		socketPath = DefaultOverlaySocketPath()
	}
	return &Overlay{
		socketPath: socketPath,
		ptmx:       ptmx,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for local development
			},
		},
	}
}

// SocketPath returns the socket path the overlay is listening on.
func (o *Overlay) SocketPath() string {
	return o.socketPath
}

func (o *Overlay) Start(ctx context.Context) error {
	// Remove stale socket if it exists
	if _, err := os.Stat(o.socketPath); err == nil {
		os.Remove(o.socketPath)
	}

	// Create Unix socket listener
	listener, err := net.Listen("unix", o.socketPath)
	if err != nil {
		return fmt.Errorf("failed to create overlay socket: %w", err)
	}
	o.listener = listener

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", o.handleWebSocket)
	mux.HandleFunc("/health", o.handleHealth)
	mux.HandleFunc("/type", o.handleType)
	mux.HandleFunc("/key", o.handleKey)
	mux.HandleFunc("/event", o.handleEvent)
	mux.HandleFunc("/toast", o.handleToast)

	o.server = &http.Server{
		Handler: mux,
	}

	go func() {
		if err := o.server.Serve(listener); err != nil && err != http.ErrServerClosed {
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

	// Close listener and remove socket file
	if o.listener != nil {
		o.listener.Close()
	}
	os.Remove(o.socketPath)

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
		"status":      "ok",
		"socket_path": o.socketPath,
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

func (o *Overlay) handleToast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var msg ToastMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Set default type
	if msg.Type == "" {
		msg.Type = "info"
	}

	// Broadcast to all connected browsers to show toast
	o.Broadcast("toast", msg)

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

	o.processProxyEvent(event)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (o *Overlay) processProxyEvent(event ProxyEvent) {
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
			RequestNotification bool `json:"request_notification"`
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

		// Add notification request if enabled
		if data.RequestNotification {
			text += "\n\n[Note: When complete, please send a toast notification using: proxy {action: \"toast\", id: \"dev\", toast_message: \"Done!\", toast_type: \"success\"}]"
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
			Description  string `json:"description"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			log.Printf("Failed to parse sketch: %v", err)
			return
		}

		// Format message with description and file path
		var text string
		if data.Description != "" {
			text = data.Description
			text += fmt.Sprintf("\n\n[Sketch: %s with %d elements]", data.FilePath, data.ElementCount)
		} else {
			text = fmt.Sprintf("[Sketch saved: %s with %d elements]", data.FilePath, data.ElementCount)
		}

		// Type the formatted message into the PTY
		o.typeText(TypeMessage{
			Text:    text,
			Enter:   true,
			Instant: true,
		})

	case "design_state":
		// Handle design state - element selected for iteration
		var data struct {
			Selector     string `json:"selector"`
			XPath        string `json:"xpath"`
			OriginalHTML string `json:"original_html"`
			ContextHTML  string `json:"context_html"`
			URL          string `json:"url"`
			Metadata     struct {
				Tag     string   `json:"tag"`
				ID      string   `json:"id"`
				Classes []string `json:"classes"`
				Text    string   `json:"text"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			log.Printf("Failed to parse design_state: %v", err)
			return
		}

		// Format comprehensive UX designer instructions
		text := fmt.Sprintf(`[🎨 Design Mode: Premium UX Design Session]

**Element Selected for Redesign:**
- Selector: %s`, data.Selector)
		if data.Metadata.Tag != "" {
			text += fmt.Sprintf("\n- Element: <%s>", data.Metadata.Tag)
			if data.Metadata.ID != "" {
				text += fmt.Sprintf(` id="%s"`, data.Metadata.ID)
			}
			if len(data.Metadata.Classes) > 0 {
				text += fmt.Sprintf(` class="%s"`, strings.Join(data.Metadata.Classes, " "))
			}
		}
		if data.Metadata.Text != "" {
			textPreview := data.Metadata.Text
			if len(textPreview) > 50 {
				textPreview = textPreview[:50] + "..."
			}
			text += fmt.Sprintf("\n- Content: %q", textPreview)
		}

		text += fmt.Sprintf(`

**Your Mission:** Act as a world-class UX designer creating premium, million-dollar designs.

**Before designing, gather context using these diagnostic tools:**
1. Take a screenshot: proxy {action: "exec", id: "%s", code: "__devtool.screenshot('design-context')"}
2. Check user interactions: proxylog {proxy_id: "%s", types: ["interaction"], limit: 20}
3. Review any errors: proxylog {proxy_id: "%s", types: ["error"]}
4. See page performance: proxylog {proxy_id: "%s", types: ["performance"]}
5. Inspect element details: proxy {action: "exec", id: "%s", code: "__devtool.inspect('%s')"}
6. Check accessibility: proxy {action: "exec", id: "%s", code: "__devtool.auditAccessibility()"}

**Design Requirements:**
Create 3-5 distinct, premium alternatives that:
- Follow modern design principles (visual hierarchy, whitespace, contrast)
- Are accessible (WCAG AA compliant)
- Feel polished and professional
- Each have a unique design direction (e.g., minimal, bold, playful, premium, corporate)

**To add each alternative:**
proxy {action: "exec", id: "%s", code: "__devtool_design.addAlternative('<your complete HTML>')"}

Start by taking a screenshot to understand the visual context, then create your designs.`,
			event.ProxyID, event.ProxyID, event.ProxyID, event.ProxyID,
			event.ProxyID, data.Selector, event.ProxyID, event.ProxyID)

		o.typeText(TypeMessage{
			Text:    text,
			Enter:   true,
			Instant: true,
		})

	case "design_request":
		// Handle design request - user wants more alternatives
		var data struct {
			Selector          string `json:"selector"`
			XPath             string `json:"xpath"`
			CurrentHTML       string `json:"current_html"`
			OriginalHTML      string `json:"original_html"`
			ContextHTML       string `json:"context_html"`
			AlternativesCount int    `json:"alternatives_count"`
			URL               string `json:"url"`
			Metadata          struct {
				Tag     string   `json:"tag"`
				ID      string   `json:"id"`
				Classes []string `json:"classes"`
				Text    string   `json:"text"`
			} `json:"metadata"`
			ChatHistory []struct {
				Message string `json:"message"`
				Role    string `json:"role"`
			} `json:"chat_history"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			log.Printf("Failed to parse design_request: %v", err)
			return
		}

		// Format premium designer continuation request
		text := fmt.Sprintf(`[🎨 Design Mode: More Premium Alternatives Requested]

**Element:** %s
**Existing alternatives:** %d

`, data.Selector, data.AlternativesCount)

		// Include chat history context if present
		if len(data.ChatHistory) > 0 {
			text += "**User feedback/requests:**\n"
			for _, msg := range data.ChatHistory {
				if msg.Role == "user" {
					text += fmt.Sprintf("- %s\n", msg.Message)
				}
			}
			text += "\n"
		}

		// Include current HTML (truncated if long)
		currentHTML := data.CurrentHTML
		if len(currentHTML) > 500 {
			currentHTML = currentHTML[:500] + "..."
		}
		text += fmt.Sprintf("**Current design:**\n%s\n", currentHTML)

		text += fmt.Sprintf(`
**Continue as a world-class UX designer.** Create 2-3 MORE fresh alternatives that:
- Are distinctly different from the %d existing options
- Push creative boundaries while staying functional
- Consider the user's feedback above (if any)

**Quick diagnostics if needed:**
- Screenshot: proxy {action: "exec", id: "%s", code: "__devtool.screenshot('design-iteration')"}
- Recent clicks: proxylog {proxy_id: "%s", types: ["interaction"], limit: 10}
- Custom logs: proxylog {proxy_id: "%s", types: ["custom"]}

**Add each new alternative:**
proxy {action: "exec", id: "%s", code: "__devtool_design.addAlternative('<your fresh HTML>')"}`,
			data.AlternativesCount, event.ProxyID, event.ProxyID, event.ProxyID, event.ProxyID)

		o.typeText(TypeMessage{
			Text:    text,
			Enter:   true,
			Instant: true,
		})

	case "design_chat":
		// Handle design chat - user message about the element
		var data struct {
			Message      string `json:"message"`
			Selector     string `json:"selector"`
			CurrentHTML  string `json:"current_html"`
			OriginalHTML string `json:"original_html"`
			URL          string `json:"url"`
		}
		if err := json.Unmarshal(event.Data, &data); err != nil {
			log.Printf("Failed to parse design_chat: %v", err)
			return
		}

		// Include current HTML (truncated if long)
		currentHTML := data.CurrentHTML
		if len(currentHTML) > 400 {
			currentHTML = currentHTML[:400] + "..."
		}

		// Format design refinement request
		text := fmt.Sprintf(`[🎨 Design Refinement Request]

**User says:** "%s"

**Element:** %s
**Current design:**
%s

**As a premium UX designer, refine the design based on this feedback.**

If you need more context:
- Screenshot current state: proxy {action: "exec", id: "%s", code: "__devtool.screenshot('refinement')"}
- Check DOM mutations: proxylog {proxy_id: "%s", types: ["mutation"], limit: 10}
- View panel messages: proxylog {proxy_id: "%s", types: ["panel_message"]}
- Audit accessibility: proxy {action: "exec", id: "%s", code: "__devtool.auditAccessibility()"}

**Apply the refined design:**
proxy {action: "exec", id: "%s", code: "__devtool_design.addAlternative('<refined HTML>')"}`,
			data.Message, data.Selector, currentHTML,
			event.ProxyID, event.ProxyID, event.ProxyID, event.ProxyID, event.ProxyID)

		o.typeText(TypeMessage{
			Text:    text,
			Enter:   true,
			Instant: true,
		})

	default:
		// Log unknown event types as errors - they may indicate missing handlers
		log.Printf("[Overlay] ERROR: Unhandled proxy event type: %s (proxy_id=%s)", event.Type, event.ProxyID)
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
	// Always type character by character with delays to simulate realistic typing.
	// Ink/Claude Code's input handler may not process instant bulk text correctly.
	delay := 5 * time.Millisecond
	if !msg.Instant {
		delay = 10 * time.Millisecond
	}

	for _, ch := range msg.Text {
		o.writeTopty(string(ch))
		time.Sleep(delay)
	}

	if msg.Enter {
		// Wait for Ink to process all characters before sending submit sequence
		time.Sleep(100 * time.Millisecond)

		// Ink-based apps (like Claude Code) sometimes need two Enters:
		// - First Enter may be consumed by autocomplete/suggestions UI
		// - Second Enter actually submits
		o.writeTopty("\r")
		time.Sleep(50 * time.Millisecond)
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
		return "\r\n"
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
	if o.ptmx == nil {
		return
	}
	o.ptmx.Write([]byte(s))
	// Sync if available (for *os.File)
	if syncer, ok := o.ptmx.(interface{ Sync() error }); ok {
		syncer.Sync()
	}
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
