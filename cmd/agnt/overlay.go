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
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/overlay"
)

// PtyWriter is an interface for writing to a PTY.
// This allows both Unix PTY (*os.File) and Windows ConPTY to be used.
type PtyWriter interface {
	io.Writer
}

// Overlay receives events from devtool-mcp and injects them into the PTY.
type Overlay struct {
	socketPath      string
	ptmx            PtyWriter
	server          *http.Server
	listener        net.Listener
	upgrader        websocket.Upgrader
	clients         sync.Map // map[*websocket.Conn]bool
	mu              sync.RWMutex
	auditSummarizer *overlay.AuditSummarizer
	activityCh      chan struct{} // Signaled when output activity is detected
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

	// Initialize audit summarizer for LLM-powered audit reports
	auditSummarizer := overlay.NewAuditSummarizer(overlay.AuditSummarizerConfig{
		UseAPI:  true, // Use API mode for faster responses
		Timeout: 30 * time.Second,
	})

	return &Overlay{
		socketPath:      socketPath,
		ptmx:            ptmx,
		auditSummarizer: auditSummarizer,
		activityCh:      make(chan struct{}, 1), // Buffered to avoid blocking
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

// NotifyActivity signals that output activity has been detected.
// Called by ActivityMonitor when agent starts producing output.
func (o *Overlay) NotifyActivity() {
	select {
	case o.activityCh <- struct{}{}:
	default:
		// Channel already has a signal, don't block
	}
}

func (o *Overlay) Start(ctx context.Context) error {
	// Remove stale socket if it exists
	if _, err := os.Stat(o.socketPath); err == nil {
		log.Printf("[Overlay] removing stale socket: %s", o.socketPath)
		if err := os.Remove(o.socketPath); err != nil {
			log.Printf("[Overlay] WARNING: failed to remove stale socket %s: %v", o.socketPath, err)
		}
	}

	// Create Unix socket listener
	log.Printf("[Overlay] creating Unix socket listener at: %s", o.socketPath)
	listener, err := net.Listen("unix", o.socketPath)
	if err != nil {
		return fmt.Errorf("failed to create overlay socket at %s: %w", o.socketPath, err)
	}
	o.listener = listener
	log.Printf("[Overlay] socket listener started successfully")

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
	log.Printf("[Overlay] received proxy event: type=%s proxy_id=%s data_len=%d", event.Type, event.ProxyID, len(event.Data))
	text := formatProxyEventText(event, o.auditSummarizer)
	if text != "" {
		log.Printf("[Overlay] injecting %d chars into PTY for event type=%s", len(text), event.Type)
		o.typeText(TypeMessage{Text: text, Enter: true, Instant: true})
	} else {
		log.Printf("[Overlay] WARNING: formatProxyEventText returned empty for type=%s", event.Type)
	}
	o.Broadcast("proxy_event", event)
}

// styleChange represents a single CSS property change in a style-edit attachment.
type styleChange struct {
	Property string `json:"property"`
	Scope    string `json:"scope"`
	Original string `json:"original"`
	Current  string `json:"current"`
}

// attachmentInfo holds parsed attachment data for non-audit attachments.
type attachmentInfo struct {
	Type     string
	ID       string
	Selector string
	Tag      string
	Text     string
	Summary  string
	Area     *screenshotArea
	FilePath string // Path to the saved file (for screenshots, sketches)
	FileName string // Just the filename for display

	// Style-edit fields
	StyleChanges     []styleChange
	ReactComponent   string // e.g. "Button"
	ReactSource      string // e.g. "src/Button.tsx:42"
	ScreenshotBefore string
	ScreenshotAfter  string
}

// screenshotArea represents coordinates for a screenshot region.
type screenshotArea struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// extractStyleEditData populates style-edit fields on attachmentInfo from raw data.
func extractStyleEditData(info *attachmentInfo, data map[string]interface{}) {
	if changesRaw, ok := data["changes"].([]interface{}); ok {
		for _, c := range changesRaw {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			sc := styleChange{
				Property: getStr(cm, "property"),
				Scope:    getStr(cm, "scope"),
				Original: getStr(cm, "original"),
				Current:  getStr(cm, "current"),
			}
			info.StyleChanges = append(info.StyleChanges, sc)
		}
	}

	if rp, ok := data["reactProps"].(map[string]interface{}); ok {
		info.ReactComponent = getStr(rp, "component")
		info.ReactSource = getStr(rp, "source")
	}

	if ss, ok := data["screenshots"].(map[string]interface{}); ok {
		if before, ok := ss["before"].(string); ok {
			info.ScreenshotBefore = before
		}
		if after, ok := ss["after"].(string); ok {
			info.ScreenshotAfter = after
		}
	}
}

// getStr safely extracts a string from a map.
func getStr(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

// truncateText truncates text to maxLen characters, adding "..." if truncated.
func truncateText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
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
		// Send full text as single write - large buffer triggers paste detection
		// in terminal input handlers without needing bracketed paste escape sequences.
		o.writeTopty(msg.Text)

		if msg.Enter {
			// Progressive enter key timing to ensure agent accepts the message.
			// Send enters at 100ms, 200ms, then 500ms intervals until activity is detected.
			// This handles different AI agent input processing speeds.
			o.sendEntersUntilActivity()
		}
	} else {
		// Simulate typing character by character
		delay := 10 * time.Millisecond

		for _, ch := range msg.Text {
			o.writeTopty(string(ch))
			time.Sleep(delay)
		}

		if msg.Enter {
			// Wait for Ink to process all characters before sending submit sequence
			time.Sleep(100 * time.Millisecond)

			// Progressive enter key timing to ensure agent accepts the message.
			o.sendEntersUntilActivity()
		}
	}
}

// sendEntersUntilActivity sends enter keys until the agent starts producing
// output. Sends the first Enter immediately, waits for the text echo to
// settle, then retries with increasing delays. Max 4 enters total.
func (o *Overlay) sendEntersUntilActivity() {
	// Send the first Enter immediately — this must always happen
	// regardless of any activity signals from the text echo.
	o.writeTopty("\r")

	// Wait for the text + first Enter echo to settle through the PTY
	// and ActivityMonitor before watching for real agent responses.
	// 1.1s covers the typical agent acknowledgement latency.
	time.Sleep(1100 * time.Millisecond)

	// Drain all echo-triggered activity signals
	for {
		select {
		case <-o.activityCh:
			continue
		default:
			goto drained
		}
	}
drained:

	// Retry with increasing delays: 1.5s, 2s, 3s
	retryDelays := [3]time.Duration{
		1500 * time.Millisecond,
		2000 * time.Millisecond,
		3000 * time.Millisecond,
	}

	for _, delay := range retryDelays {
		select {
		case <-o.activityCh:
			return // Agent is responding — message was accepted
		case <-time.After(delay):
			o.writeTopty("\r")
		}
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
	if o.ptmx == nil {
		log.Printf("[Overlay] writeTopty: ptmx is nil, dropping %d bytes", len(s))
		return
	}
	n, err := o.ptmx.Write([]byte(s))
	if err != nil {
		log.Printf("[Overlay] writeTopty error: %v (wrote %d/%d bytes)", err, n, len(s))
	}
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
