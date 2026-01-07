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
		o.handlePanelMessage(event)
	case "sketch":
		o.handleSketch(event)
	case "design_state":
		o.handleDesignState(event)
	case "design_request":
		o.handleDesignRequest(event)
	case "design_chat":
		o.handleDesignChat(event)
	default:
		log.Printf("[Overlay] ERROR: Unhandled proxy event type: %s (proxy_id=%s)", event.Type, event.ProxyID)
	}

	// Broadcast to connected clients
	o.Broadcast("proxy_event", event)
}

// attachmentInfo holds parsed attachment data for non-audit attachments.
type attachmentInfo struct {
	Type     string
	ID       string
	Selector string
	Tag      string
	Text     string
}

// handlePanelMessage processes panel_message events from the browser.
func (o *Overlay) handlePanelMessage(event ProxyEvent) {
	var data struct {
		Message     string `json:"message"`
		Attachments []struct {
			Type     string          `json:"type"`
			ID       string          `json:"id"`
			Selector string          `json:"selector"`
			Tag      string          `json:"tag"`
			Text     string          `json:"text"`
			Data     json.RawMessage `json:"data"`
		} `json:"attachments"`
		RequestNotification bool `json:"request_notification"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		log.Printf("Failed to parse panel_message: %v", err)
		return
	}

	// Process attachments - separate audit and non-audit
	auditReports, nonAuditAttachments := o.processAttachments(data.Attachments, data.Message)

	// Format the message for the AI tool
	text := o.formatPanelMessage(data.Message, auditReports, nonAuditAttachments, data.RequestNotification)

	o.typeText(TypeMessage{
		Text:    text,
		Enter:   true,
		Instant: true,
	})
}

// processAttachments separates audit attachments (with LLM summarization) from regular attachments.
func (o *Overlay) processAttachments(attachments []struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	Selector string          `json:"selector"`
	Tag      string          `json:"tag"`
	Text     string          `json:"text"`
	Data     json.RawMessage `json:"data"`
}, userMessage string) ([]string, []attachmentInfo) {
	var auditReports []string
	var nonAuditAttachments []attachmentInfo

	for _, att := range attachments {
		if att.Type == "audit" && len(att.Data) > 0 {
			report := o.processAuditAttachment(att.Data, userMessage)
			auditReports = append(auditReports, report)
		} else {
			nonAuditAttachments = append(nonAuditAttachments, attachmentInfo{
				Type:     att.Type,
				ID:       att.ID,
				Selector: att.Selector,
				Tag:      att.Tag,
				Text:     att.Text,
			})
		}
	}
	return auditReports, nonAuditAttachments
}

// processAuditAttachment processes a single audit attachment and returns a summary.
func (o *Overlay) processAuditAttachment(data json.RawMessage, userMessage string) string {
	var auditData struct {
		AuditType string          `json:"auditType"`
		Label     string          `json:"label"`
		Summary   string          `json:"summary"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &auditData); err != nil {
		log.Printf("Failed to parse audit data: %v", err)
		return fmt.Sprintf("**Audit**: (parse error)")
	}

	// Use LLM to generate quality report if available
	if o.auditSummarizer != nil && o.auditSummarizer.IsAvailable() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		report, err := o.auditSummarizer.SummarizeAudit(ctx, overlay.AuditData{
			AuditType: auditData.AuditType,
			Label:     auditData.Label,
			Summary:   auditData.Summary,
			Result:    auditData.Result,
		}, userMessage)

		if err != nil {
			log.Printf("Audit summarization failed: %v", err)
			return fmt.Sprintf("**%s**: %s", auditData.Label, auditData.Summary)
		}
		return report
	}

	// LLM not available, use basic summary
	return fmt.Sprintf("**%s**: %s", auditData.Label, auditData.Summary)
}

// formatPanelMessage formats the panel message with audit reports and attachments.
func (o *Overlay) formatPanelMessage(message string, auditReports []string, attachments []attachmentInfo, requestNotification bool) string {
	// Add default call-to-action if message is empty but there are audit reports
	userMessage := message
	if userMessage == "" && len(auditReports) > 0 {
		userMessage = "Review and fix the issues found in this audit report."
	}
	text := "from agnt current page: " + userMessage

	// Add LLM-generated audit reports
	if len(auditReports) > 0 {
		text += "\n\n[Audit Report]\n"
		for _, report := range auditReports {
			text += report + "\n"
		}
	}

	// Add non-audit attachments
	if len(attachments) > 0 {
		text += "\n\n[Attachments]\n"
		for i, att := range attachments {
			text += fmt.Sprintf("%d. %s: %s (%s)\n", i+1, att.Type, att.Selector, att.Text)
		}
	}

	// Add notification request if enabled
	if requestNotification {
		text += "\n\n[Note: When complete, please send a toast notification using: proxy {action: \"toast\", id: \"dev\", toast_message: \"Done!\", toast_type: \"success\"}]"
	}

	return text
}

// handleSketch processes sketch events from the browser.
func (o *Overlay) handleSketch(event ProxyEvent) {
	var data struct {
		FilePath     string `json:"file_path"`
		ElementCount int    `json:"element_count"`
		Description  string `json:"description"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		log.Printf("Failed to parse sketch: %v", err)
		return
	}

	var text string
	if data.Description != "" {
		text = data.Description
		text += fmt.Sprintf("\n\n[Sketch: %s with %d elements]", data.FilePath, data.ElementCount)
	} else {
		text = fmt.Sprintf("[Sketch saved: %s with %d elements]", data.FilePath, data.ElementCount)
	}

	o.typeText(TypeMessage{
		Text:    text,
		Enter:   true,
		Instant: true,
	})
}

// handleDesignState processes design_state events when an element is selected for iteration.
func (o *Overlay) handleDesignState(event ProxyEvent) {
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

	text := o.formatDesignStateMessage(data.Selector, data.Metadata.Tag, data.Metadata.ID,
		data.Metadata.Classes, data.Metadata.Text, event.ProxyID)

	o.typeText(TypeMessage{
		Text:    text,
		Enter:   true,
		Instant: true,
	})
}

// formatDesignStateMessage formats the design state message for UX design sessions.
func (o *Overlay) formatDesignStateMessage(selector, tag, id string, classes []string, textContent, proxyID string) string {
	text := fmt.Sprintf(`[🎨 Design Mode: Premium UX Design Session]

**Element Selected for Redesign:**
- Selector: %s`, selector)

	if tag != "" {
		text += fmt.Sprintf("\n- Element: <%s>", tag)
		if id != "" {
			text += fmt.Sprintf(` id="%s"`, id)
		}
		if len(classes) > 0 {
			text += fmt.Sprintf(` class="%s"`, strings.Join(classes, " "))
		}
	}
	if textContent != "" {
		textPreview := textContent
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
		proxyID, proxyID, proxyID, proxyID, proxyID, selector, proxyID, proxyID)

	return text
}

// handleDesignRequest processes design_request events when user wants more alternatives.
func (o *Overlay) handleDesignRequest(event ProxyEvent) {
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

	text := o.formatDesignRequestMessage(data.Selector, data.AlternativesCount,
		data.ChatHistory, data.CurrentHTML, event.ProxyID)

	o.typeText(TypeMessage{
		Text:    text,
		Enter:   true,
		Instant: true,
	})
}

// formatDesignRequestMessage formats the design request message for continuation.
func (o *Overlay) formatDesignRequestMessage(selector string, altCount int, chatHistory []struct {
	Message string `json:"message"`
	Role    string `json:"role"`
}, currentHTML, proxyID string) string {
	text := fmt.Sprintf(`[🎨 Design Mode: More Premium Alternatives Requested]

**Element:** %s
**Existing alternatives:** %d

`, selector, altCount)

	// Include chat history context if present
	if len(chatHistory) > 0 {
		text += "**User feedback/requests:**\n"
		for _, msg := range chatHistory {
			if msg.Role == "user" {
				text += fmt.Sprintf("- %s\n", msg.Message)
			}
		}
		text += "\n"
	}

	// Include current HTML (truncated if long)
	html := currentHTML
	if len(html) > 500 {
		html = html[:500] + "..."
	}
	text += fmt.Sprintf("**Current design:**\n%s\n", html)

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
		altCount, proxyID, proxyID, proxyID, proxyID)

	return text
}

// handleDesignChat processes design_chat events for user feedback on designs.
func (o *Overlay) handleDesignChat(event ProxyEvent) {
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

	// Truncate HTML if too long
	currentHTML := data.CurrentHTML
	if len(currentHTML) > 400 {
		currentHTML = currentHTML[:400] + "..."
	}

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
			// Two returns 100ms apart
			time.Sleep(50 * time.Millisecond)
			o.writeTopty("\r")
			time.Sleep(100 * time.Millisecond)
			o.writeTopty("\r")
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

			// Ink-based apps (like Claude Code) sometimes need two Enters:
			// - First Enter may be consumed by autocomplete/suggestions UI
			// - Second Enter actually submits
			o.writeTopty("\r")
			time.Sleep(50 * time.Millisecond)
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
