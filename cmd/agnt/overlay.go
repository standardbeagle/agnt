package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
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
	clients         sync.Map // map[*websocket.Conn]*sync.Mutex (per-conn write lock)
	mu              sync.RWMutex
	auditSummarizer *overlay.AuditSummarizer
	activityCh      chan struct{} // Signaled when output activity is detected

	// Auto-forward state for browser/proxy errors
	autoForward        *config.AutoForwardConfig
	autoForwardEnabled atomic.Bool // Runtime toggle (config + indicator override)

	// forwardPaused, when set, suppresses PTY injection of errors/notifications
	// into the agent (the in-process delivery path). Pull surfaces are untouched:
	// alert matches still report to the daemon store. Toggled from the overlay.
	forwardPaused atomic.Bool

	// alertQueue bundles the canonical PTY-injection queue (dedup, batch,
	// activity-defer) with the cascade filter it was configured with.
	// Browser-JS and HTTP errors flow through Inject so they share the
	// queue with regex-matched process alerts (see the messaging-queue
	// skill for the single-queue invariant). atomic.Pointer because
	// SetAlertScanner runs on the pipeline goroutine after Start() already
	// has the HTTP event server reading it.
	alertQueue atomic.Pointer[alertWiring]

	// enterSettle is the echo-settle window duration in sendEntersUntilActivity.
	// Zero means use the production default (1100ms).
	enterSettle time.Duration
	// enterRetryDelays are the three retry delays in sendEntersUntilActivity.
	// Zero elements mean use production defaults (1500ms, 2000ms, 3000ms).
	enterRetryDelays [3]time.Duration
	// sleepFn and afterFn are the clock typeText and sendEntersUntilActivity
	// run on. Nil in production, where they mean time.Sleep and time.After.
	//
	// The Enter retry loop only makes sense against activity that arrives inside
	// a window it owns — after the echo drain, before the next retry fires. A
	// test that reaches for that window with its own sleeps is racing this
	// goroutine, and loses whenever the machine is loaded: output published
	// before the drain is swallowed by it, output after the deadline arrives to
	// an Enter already sent. Injecting the clock lets a test observe the window
	// instead of guessing at it — afterFn is called precisely once the loop is
	// waiting, and fires only when the test says so.
	sleepFn func(time.Duration)
	afterFn func(time.Duration) <-chan time.Time
}

// sleep blocks for d on the overlay's clock.
func (o *Overlay) sleep(d time.Duration) {
	if o.sleepFn != nil {
		o.sleepFn(d)
		return
	}
	time.Sleep(d)
}

// after returns a channel that receives after d on the overlay's clock.
func (o *Overlay) after(d time.Duration) <-chan time.Time {
	if o.afterFn != nil {
		return o.afterFn(d)
	}
	return time.After(d)
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
// Uses /tmp (or os.TempDir on Windows) unconditionally — XDG_RUNTIME_DIR is
// intentionally ignored for the same reason as the daemon socket: pam_systemd
// cleans it up on logout, which would delete the socket mid-session.
// Windows uses UID-equivalent USERNAME to avoid per-user collisions.
func DefaultOverlaySocketPath() string {
	if os.PathSeparator == '\\' {
		username := os.Getenv("USERNAME")
		if username == "" {
			u, err := user.Current()
			if err == nil {
				username = u.Username
			}
		}
		if username == "" {
			username = fmt.Sprintf("%d", os.Getuid())
		}
		return filepath.Join(os.TempDir(), fmt.Sprintf("devtool-overlay-%s.sock", username))
	}
	return fmt.Sprintf("/tmp/devtool-overlay-%d.sock", os.Getuid())
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
		debug.Log("overlay", "removing stale socket: %s", o.socketPath)
		if err := os.Remove(o.socketPath); err != nil {
			debug.Log("overlay", "failed to remove stale socket %s: %v", o.socketPath, err)
		}
	}

	// Create Unix socket listener
	debug.Log("overlay", "creating Unix socket listener at: %s", o.socketPath)
	listener, err := net.Listen("unix", o.socketPath)
	if err != nil {
		return fmt.Errorf("failed to create overlay socket at %s: %w", o.socketPath, err)
	}
	o.listener = listener
	debug.Log("overlay", "socket listener started successfully")

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
			debug.Error("overlay", "server error: %v", err)
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
		debug.Error("overlay", "WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	o.clients.Store(conn, &sync.Mutex{})
	defer o.clients.Delete(conn)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				debug.Warn("overlay", "WebSocket error: %v", err)
			}
			break
		}

		var msg OverlayMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			debug.Warn("overlay", "invalid message: %v", err)
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
	debug.Log("overlay", "received proxy event: type=%s proxy_id=%s data_len=%d", event.Type, event.ProxyID, len(event.Data))

	// Auto-forward errors go through debounce filter before formatting
	if event.Type == "browser_error" || event.Type == "http_error" {
		o.processAutoForwardEvent(event)
		o.Broadcast("proxy_event", event)
		return
	}

	text := formatProxyEventText(event, o.auditSummarizer)
	if text != "" {
		// These are explicit user actions (panel message, sketch, design
		// interaction). Route them through the canonical queue as Protected so
		// they share activity-deferral (never injected mid-response) but are
		// NEVER dropped by the overload throttle or dedup.
		o.injectOrFallback(&overlay.AlertMatch{
			Pattern: &overlay.AlertPattern{
				ID:       "user:" + event.Type,
				Severity: overlay.AlertSeverityInfo,
				Category: "user",
			},
			Line:         text,
			ScriptID:     "proxy:" + event.ProxyID,
			Source:       overlay.AlertSourceUser,
			RenderedText: text,
			Protected:    true,
		})
	} else {
		debug.Log("overlay", "formatProxyEventText returned empty for type=%s", event.Type)
	}
	o.Broadcast("proxy_event", event)
}

// processAutoForwardEvent routes browser_error and http_error events
// through the canonical AlertScanner queue (dedup, batch, activity-defer).
// Replaces the previous global-debounce + direct typeText path which
// bypassed every gate and caused the "vite reconnect spam" bug. See
// .claude/skills/messaging-queue for the single-queue invariant.
func (o *Overlay) processAutoForwardEvent(event ProxyEvent) {
	if !o.autoForwardEnabled.Load() {
		return
	}

	// Source filter (browser vs http).
	source := "browser"
	if event.Type == "http_error" {
		source = "http"
	}
	if o.autoForward != nil && !o.autoForward.ShouldForwardSource(source) {
		return
	}

	// Severity filter for HTTP errors (4xx = warning, 5xx = error).
	if event.Type == "http_error" && o.autoForward != nil {
		minSev := o.autoForward.GetSeverity()
		var data struct {
			StatusCode int `json:"status_code"`
		}
		if json.Unmarshal(event.Data, &data) == nil {
			if minSev == "error" && data.StatusCode < 500 {
				return
			}
		}
	}

	canonical := canonicalAutoForwardMessage(event)
	if canonical == "" {
		return
	}

	// Cascade-pattern check: drop transport-layer noise (vite/webpack
	// reconnect spam) before it reaches the queue.
	if o.matchesJSCascade(canonical) {
		debug.Log("overlay", "auto-forward dropped cascade match: type=%s msg=%q", event.Type, canonical)
		return
	}

	rendered := formatProxyEventText(event, o.auditSummarizer)
	if rendered == "" {
		return
	}

	matchSource := overlay.AlertSourceBrowser
	patternID := "browser-js-error"
	if event.Type == "http_error" {
		matchSource = overlay.AlertSourceHTTP
		patternID = "http-error"
	}

	o.injectOrFallback(&overlay.AlertMatch{
		Pattern: &overlay.AlertPattern{
			ID:       patternID,
			Severity: overlay.AlertSeverityError,
			Category: source,
		},
		Line:         canonical,
		ScriptID:     "proxy:" + event.ProxyID,
		Source:       matchSource,
		RenderedText: rendered,
	})
}

// canonicalAutoForwardMessage extracts a stable dedup key from an auto-
// forward proxy event. For browser errors that's the JS error message;
// for HTTP errors it's "METHOD URL → status".
func canonicalAutoForwardMessage(event ProxyEvent) string {
	switch event.Type {
	case "browser_error":
		var d struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(event.Data, &d); err != nil {
			return ""
		}
		return d.Message
	case "http_error":
		var d struct {
			Method     string `json:"method"`
			URL        string `json:"url"`
			StatusCode int    `json:"status_code"`
		}
		if err := json.Unmarshal(event.Data, &d); err != nil {
			return ""
		}
		return fmt.Sprintf("%s %s -> %d", d.Method, d.URL, d.StatusCode)
	}
	return ""
}

// matchesJSCascade reports whether canonical contains any configured
// cascade pattern. Patterns are pre-lowercased; matching is case-
// insensitive substring.
func (o *Overlay) matchesJSCascade(canonical string) bool {
	w := o.alertQueue.Load()
	if w == nil || len(w.jsCascadePatterns) == 0 {
		return false
	}
	lower := strings.ToLower(canonical)
	for _, p := range w.jsCascadePatterns {
		if p != "" && strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// injectOrFallback routes a match through the canonical alert queue (dedup,
// batch, activity-defer). When no scanner is wired — alerts disabled in
// config (setupAlertScanner returns nil) or the pipeline goroutine has not
// called SetAlertScanner yet — it falls back to direct PTY injection of the
// match's RenderedText: without the scanner there is no queue, so direct
// injection is the lesser evil over silent loss. The fallback bypasses every
// queue gate; keeping that policy in exactly one place is the point.
func (o *Overlay) injectOrFallback(m *overlay.AlertMatch) {
	if w := o.alertQueue.Load(); w != nil && w.scanner != nil {
		debug.Log("overlay", "queueing alert id=%s source=%s protected=%v", m.Pattern.ID, m.Source, m.Protected)
		w.scanner.Inject(m)
		return
	}
	debug.Log("overlay", "alert fallback (no scanner): id=%s", m.Pattern.ID)
	o.typeText(TypeMessage{Text: m.RenderedText, Enter: true, Instant: true})
}

// alertWiring pairs the alert scanner with the lowercased substring set
// used to drop transport-layer noise (vite/webpack HMR reconnect spam)
// from the auto-forward path. Patterns loaded from .agnt.kdl
// alerts.outage-hold. Set and read as one unit via Overlay.alertQueue.
type alertWiring struct {
	scanner           *overlay.AlertScanner
	jsCascadePatterns []string
}

// SetAlertScanner wires the canonical queue. Must be called once after
// setupAlertScanner returns. Cascade patterns are lifted from the same
// config so the auto-forward gate uses the same definition as the
// daemon-side hold buffer.
func (o *Overlay) SetAlertScanner(scanner *overlay.AlertScanner, cascadePatterns []string) {
	if len(cascadePatterns) == 0 {
		cascadePatterns = config.DefaultJSCascadePatterns
	}
	lowered := make([]string, len(cascadePatterns))
	for i, p := range cascadePatterns {
		lowered[i] = strings.ToLower(p)
	}
	o.alertQueue.Store(&alertWiring{scanner: scanner, jsCascadePatterns: lowered})
}

// SetAutoForwardConfig configures the auto-forward filter from .agnt.kdl alerts config.
func (o *Overlay) SetAutoForwardConfig(cfg *config.AutoForwardConfig) {
	o.autoForward = cfg
	o.autoForwardEnabled.Store(cfg.IsEnabled())
}

// SetAutoForwardEnabled toggles auto-forward at runtime (e.g., from indicator).
func (o *Overlay) SetAutoForwardEnabled(enabled bool) {
	o.autoForwardEnabled.Store(enabled)
}

// IsAutoForwardEnabled returns the current auto-forward state.
func (o *Overlay) IsAutoForwardEnabled() bool {
	return o.autoForwardEnabled.Load()
}

// SetForwardingPaused pauses/resumes PTY injection of agent-inbound alerts.
func (o *Overlay) SetForwardingPaused(paused bool) {
	o.forwardPaused.Store(paused)
}

// IsForwardingPaused reports whether PTY injection is currently paused.
func (o *Overlay) IsForwardingPaused() bool {
	return o.forwardPaused.Load()
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
	RawData  map[string]interface{}

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
			debug.Warn("overlay", "invalid type message: %v", err)
			return
		}
		o.typeText(typeMsg)

	case "key":
		var keyMsg KeyMessage
		if err := json.Unmarshal(msg.Payload, &keyMsg); err != nil {
			debug.Warn("overlay", "invalid key message: %v", err)
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
		debug.Warn("overlay", "unknown message type: %s", msg.Type)
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
			o.sleep(delay)
		}

		if msg.Enter {
			// Wait for Ink to process all characters before sending submit sequence
			o.sleep(100 * time.Millisecond)

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
	settle := o.enterSettle
	if settle == 0 {
		settle = 1100 * time.Millisecond
	}
	o.sleep(settle)

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
	retryDelays := o.enterRetryDelays
	if retryDelays[0] == 0 {
		retryDelays = [3]time.Duration{
			1500 * time.Millisecond,
			2000 * time.Millisecond,
			3000 * time.Millisecond,
		}
	}

	for _, delay := range retryDelays {
		select {
		case <-o.activityCh:
			return // Agent is responding — message was accepted
		case <-o.after(delay):
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
		debug.Log("overlay", "writeTopty: ptmx is nil, dropping %d bytes", len(s))
		return
	}
	n, err := o.ptmx.Write([]byte(s))
	if err != nil {
		debug.Log("overlay", "writeTopty error: %v (wrote %d/%d bytes)", err, n, len(s))
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
		conn, ok := key.(*websocket.Conn)
		if !ok {
			return true
		}
		// gorilla/websocket forbids concurrent writes to a single conn.
		// Broadcast is reachable from /event, /toast, and the read loop, so
		// serialize writes per connection with its dedicated mutex.
		if wmu, ok := value.(*sync.Mutex); ok {
			wmu.Lock()
			_ = conn.WriteMessage(websocket.TextMessage, msg)
			wmu.Unlock()
		} else {
			_ = conn.WriteMessage(websocket.TextMessage, msg)
		}
		return true
	})
}
