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
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/httpcaps"
	"github.com/standardbeagle/agnt/internal/overlay"
	"github.com/standardbeagle/agnt/internal/pathutil"
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
	// injectMu serializes whole injections (text plus its Enter retries), so
	// two senders cannot interleave text or submit each other's message.
	injectMu sync.Mutex
	// inputProbe reports whether the agent's screen still shows a typed
	// message near the cursor (ActivityMonitor.CursorRowsContain). The Enter
	// retries use it to stop once the message left the input box, even when
	// the agent works silently. Nil until the PTY output monitor exists.
	inputProbe atomic.Pointer[func(tail string) (found, ok bool)]

	// Auto-forward state for browser/proxy errors. atomic.Pointer because
	// SetAutoForwardConfig runs on the pipeline goroutine while HTTP handler
	// goroutines read it in processAutoForwardEvent.
	autoForward        atomic.Pointer[config.AutoForwardConfig]
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

// overlaySocketDir returns the per-user directory that holds overlay sockets.
// Uses /tmp (or os.TempDir on Windows) unconditionally — XDG_RUNTIME_DIR is
// intentionally ignored for the same reason as the daemon socket: pam_systemd
// cleans it up on logout, which would delete the socket mid-session.
//
// The directory, not the socket name, is what scopes sockets to a user.
// Session codes ("claude-1") are generated per user, so two users on one host
// produce the same socket name; directly under /tmp the second lost its
// overlay, and anyone could pre-create a victim's predictable path. The
// directory is created 0700 and verified by prepareOverlaySocket.
func overlaySocketDir() string {
	if os.PathSeparator == '\\' {
		// os.TempDir is already per-user on Windows; the username keeps the
		// name unique where TEMP is shared (services, some CI images).
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
		return filepath.Join(os.TempDir(), "agnt-overlay-"+pathutil.SafePathComponent(username))
	}
	return fmt.Sprintf("/tmp/agnt-overlay-%d", os.Getuid())
}

// DefaultOverlaySocketPath returns the default socket path for the overlay:
// a fixed name inside the per-user overlaySocketDir.
func DefaultOverlaySocketPath() string {
	return filepath.Join(overlaySocketDir(), "devtool-overlay.sock")
}

// overlaySocketProbeTimeout bounds the liveness probe of an existing socket.
const overlaySocketProbeTimeout = 500 * time.Millisecond

// listenOverlaySocket creates the overlay's Unix-socket listener at path. It
// is the one place both overlay servers (Overlay, AIOverlay) get a listener,
// so the hygiene rules cannot drift between them:
//
//   - A socket in the default per-user directory gets that directory created
//     and checked (0700, ours, not a symlink). A caller-chosen path
//     (--overlay-socket) is used as given.
//   - An existing socket is removed only when nothing answers on it. A socket
//     that accepts a connection belongs to a live session — a second run with
//     the same --session — and unlinking it would silently steal that
//     session's browser events; that is an error instead.
//   - The socket is chmod 0600. Anything that can connect can type into the
//     agent (/type, /key), so access must not depend on the umask.
func listenOverlaySocket(path string) (net.Listener, error) {
	if path == "" {
		return nil, fmt.Errorf("no overlay socket path")
	}
	if filepath.Dir(path) == overlaySocketDir() {
		if err := secureOverlayDir(filepath.Dir(path)); err != nil {
			return nil, fmt.Errorf("overlay socket directory %s: %w", filepath.Dir(path), err)
		}
	}

	if _, err := os.Lstat(path); err == nil {
		if conn, dialErr := net.DialTimeout("unix", path, overlaySocketProbeTimeout); dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("overlay socket %s is in use by a live session (is another agnt run using the same --session code?)", path)
		}
		debug.Log("overlay", "removing stale socket: %s", path)
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("stale overlay socket %s could not be removed: %w", path, err)
		}
	}

	debug.Log("overlay", "creating Unix socket listener at: %s", path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("failed to create overlay socket at %s: %w", path, err)
	}
	if err := restrictOverlaySocket(path); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("overlay socket %s could not be restricted to its owner: %w", path, err)
	}
	return listener, nil
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

// SocketPath returns the socket path the overlay is configured for.
func (o *Overlay) SocketPath() string {
	return o.socketPath
}

// Endpoint returns the socket path to register with the daemon: the path when
// the overlay is listening, "" when Start failed or has not run. Registering
// the bare path after a failed Start pointed the daemon at a socket nothing
// of ours answers — or one another process owns.
func (o *Overlay) Endpoint() string {
	if o.listener == nil {
		return ""
	}
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
	// Create the Unix socket listener (per-user dir, liveness-probed stale
	// removal, owner-only mode — see listenOverlaySocket).
	listener, err := listenOverlaySocket(o.socketPath)
	if err != nil {
		return err
	}
	debug.Log("overlay", "socket listener started successfully")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", o.handleWebSocket)
	mux.HandleFunc("/health", o.handleHealth)
	mux.HandleFunc("/type", o.handleType)
	mux.HandleFunc("/inject", o.handleInject)
	mux.HandleFunc("/key", o.handleKey)
	mux.HandleFunc("/event", o.handleEvent)
	mux.HandleFunc("/toast", o.handleToast)

	// Loopback (unix socket), routed through the shared constructor for
	// uniformity — every listener inherits the caps, so none is uncapped by
	// omission. Streaming because /ws is a long-lived gorilla/websocket
	// connection a whole-request write deadline would sever.
	caps := httpcaps.Streaming()
	listener = caps.LimitListener(listener)
	o.listener = listener
	o.server = caps.NewServer(mux)

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

	// Close listener and remove socket file. Only when this overlay actually
	// listened: after a failed Start the path may be a live session's socket
	// (that is one of the ways Start fails), and it is not ours to unlink.
	if o.listener != nil {
		o.listener.Close()
		os.Remove(o.socketPath)
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
		"status":      "ok",
		"socket_path": o.socketPath,
	})
}

// WebSocket keepalive/hardening timings for browser overlay connections.
// A ping is sent every overlayWSPingPeriod; a pong (or any inbound frame)
// extends the read deadline. Without these a dead browser connection sat in
// o.clients until process exit.
const (
	overlayWSPongWait   = 60 * time.Second
	overlayWSPingPeriod = (overlayWSPongWait * 9) / 10
	overlayWSWriteWait  = 10 * time.Second
	// overlayWSMaxMessage caps inbound frames; overlay messages are small
	// control payloads, so a large frame indicates a confused peer.
	overlayWSMaxMessage = 64 << 10
)

func (o *Overlay) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := o.upgrader.Upgrade(w, r, nil)
	if err != nil {
		debug.Error("overlay", "WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	o.clients.Store(conn, &sync.Mutex{})
	defer o.clients.Delete(conn)

	conn.SetReadLimit(overlayWSMaxMessage)
	conn.SetReadDeadline(time.Now().Add(overlayWSPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(overlayWSPongWait))
	})

	// Ping the peer so a silently-dead connection (browser tab killed,
	// network drop without FIN) is detected and reaped instead of leaking.
	pingDone := make(chan struct{})
	defer close(pingDone)
	go func() {
		ticker := time.NewTicker(overlayWSPingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-ticker.C:
				v, ok := o.clients.Load(conn)
				if !ok {
					return
				}
				// Writes serialize on the per-conn mutex, same as Broadcast.
				wmu := v.(*sync.Mutex)
				wmu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(overlayWSWriteWait))
				err := conn.WriteMessage(websocket.PingMessage, nil)
				wmu.Unlock()
				if err != nil {
					conn.Close()
					return
				}
			}
		}
	}()

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

	// Accept, then type. With Enter, typeText waits several seconds for the
	// agent to react; a sender with a shorter timeout (the scheduler's 5s
	// delivery) would count that as a failure and retype the message.
	go o.typeText(msg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// maxInjectBody caps a plain-text /inject message.
const maxInjectBody = 64 << 10

// handleInject receives a plain-text message from the daemon (`agnt session
// send`, the browser's session send, and the incident pipeline's PTY hint) and
// queues it as an explicit send: activity-deferred, never throttled or deduped.
func (o *Overlay) handleInject(w http.ResponseWriter, r *http.Request) {
	text, ok := readInjectBody(w, r)
	if !ok {
		return
	}
	o.injectOrFallback(&overlay.AlertMatch{
		Pattern: &overlay.AlertPattern{
			ID:       "user:session_send",
			Severity: overlay.AlertSeverityInfo,
			Category: "user",
		},
		Line:         text,
		ScriptID:     "session",
		Source:       overlay.AlertSourceUser,
		RenderedText: text,
		Protected:    true,
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// readInjectBody reads and validates an /inject request body, writing the
// error response itself when it returns false.
func readInjectBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return "", false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInjectBody))
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return "", false
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		http.Error(w, "empty message", http.StatusBadRequest)
		return "", false
	}
	return text, true
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
	var cascade []string
	if w := o.alertQueue.Load(); w != nil {
		cascade = w.jsCascadePatterns
	}
	canonical, ok := autoForwardPolicy{
		enabled: o.autoForwardEnabled.Load(),
		cfg:     o.autoForward.Load(),
		cascade: cascade,
	}.admit(event)
	if !ok {
		return
	}

	rendered := formatProxyEventText(event, o.auditSummarizer)
	if rendered == "" {
		return
	}

	source := autoForwardSource(event)
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

// isAutoForwardEvent reports whether event is an auto-generated error event
// (as opposed to an explicit user action such as a panel message or sketch).
func isAutoForwardEvent(event ProxyEvent) bool {
	return event.Type == "browser_error" || event.Type == "http_error"
}

// autoForwardSource is the `alerts.auto-forward.sources` name for an event.
func autoForwardSource(event ProxyEvent) string {
	if event.Type == "http_error" {
		return "http"
	}
	return "browser"
}

// autoForwardPolicy is the `.agnt.kdl` alerts filter chain for browser_error /
// http_error proxy events: the enabled switch, the source filter, the HTTP
// severity floor, and the cascade (transport-noise) patterns. It is the one
// definition of "does this error reach the agent", shared by the PTY overlay
// (Overlay.processAutoForwardEvent) and the AI REPL overlay
// (AIOverlay.handleEvent). Before it existed the AI REPL path had no filter at
// all, so `alerts { auto-forward { enabled false } }` was silently ignored
// under `agnt ai claude` / `agnt acp`.
type autoForwardPolicy struct {
	// enabled is the runtime switch (config value, possibly toggled).
	enabled bool
	// cfg may be nil; its accessors are nil-safe and return the defaults.
	cfg *config.AutoForwardConfig
	// cascade holds lowercased substrings; a canonical message containing
	// one is transport-layer noise (vite/webpack reconnect spam).
	cascade []string
}

// admit reports whether event passes the chain, returning its canonical
// (dedup-key) message when it does.
func (p autoForwardPolicy) admit(event ProxyEvent) (canonical string, ok bool) {
	if !p.enabled {
		return "", false
	}

	// Source filter (browser vs http).
	if p.cfg != nil && !p.cfg.ShouldForwardSource(autoForwardSource(event)) {
		return "", false
	}

	// Severity filter for HTTP errors (4xx = warning, 5xx = error).
	if event.Type == "http_error" && p.cfg != nil {
		var data struct {
			StatusCode int `json:"status_code"`
		}
		if json.Unmarshal(event.Data, &data) == nil {
			if p.cfg.GetSeverity() == "error" && data.StatusCode < 500 {
				return "", false
			}
		}
	}

	canonical = canonicalAutoForwardMessage(event)
	if canonical == "" {
		return "", false
	}

	// Cascade-pattern check: drop transport-layer noise (vite/webpack
	// reconnect spam) before it reaches the queue.
	if matchesCascade(canonical, p.cascade) {
		debug.Log("overlay", "auto-forward dropped cascade match: type=%s msg=%q", event.Type, canonical)
		return "", false
	}
	return canonical, true
}

// matchesCascade reports whether canonical contains any of the pre-lowercased
// patterns (case-insensitive substring match).
func matchesCascade(canonical string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	lower := strings.ToLower(canonical)
	for _, p := range patterns {
		if p != "" && strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// lowerCascadePatterns returns the cascade patterns to match against,
// lowercased, substituting the defaults when none are configured.
func lowerCascadePatterns(patterns []string) []string {
	if len(patterns) == 0 {
		patterns = config.DefaultJSCascadePatterns
	}
	lowered := make([]string, len(patterns))
	for i, p := range patterns {
		lowered[i] = strings.ToLower(p)
	}
	return lowered
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
	o.alertQueue.Store(&alertWiring{scanner: scanner, jsCascadePatterns: lowerCascadePatterns(cascadePatterns)})
}

// SetAutoForwardConfig configures the auto-forward filter from .agnt.kdl alerts config.
func (o *Overlay) SetAutoForwardConfig(cfg *config.AutoForwardConfig) {
	o.autoForward.Store(cfg)
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

// truncateText truncates text to maxLen runes, adding "..." if truncated. It
// counts runes (not bytes) so multibyte content is measured by visible length
// and never sliced mid-rune into invalid UTF-8.
func truncateText(s string, maxLen int) string {
	return overlay.TruncateRunes(s, maxLen, "...")
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

// sanitizeInjectedText is the trust-boundary guard for everything typeText
// writes into the agent's PTY stdin. Injected message bodies come from many
// sources that can carry invalid UTF-8 or embedded control bytes — a summarizer
// subprocess emitting a partial multibyte rune or a spinner, raw dev-server
// output scanned into an alert line, a browser payload with raw bytes. Injected
// verbatim, those either corrupt the agent's input parser (the "corrupt binary
// message" failure) or drive the terminal / its line editor (ESC, NUL, Ctrl-C).
// We coerce to valid UTF-8 — invalid runs become U+FFFD, kept so corruption
// stays visible rather than vanishing — and drop every control rune except the
// newline/tab whitespace an injected message legitimately uses. Keystrokes and
// control sequences (sendKey, Enter, Ctrl-C, ESC) bypass this by writing to
// writeTopty directly, so their control bytes are preserved.
func sanitizeInjectedText(s string) string {
	if s == "" {
		return s
	}
	if utf8.ValidString(s) && !strings.ContainsFunc(s, isStrippableControl) {
		return s
	}
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(r rune) rune {
		if isStrippableControl(r) {
			return -1
		}
		return r
	}, s)
}

// isStrippableControl reports whether r is a control rune that must not reach
// the agent's stdin. Newline and tab are the only whitespace controls kept; the
// U+FFFD replacement rune is not a control rune, so mangled input stays visible.
func isStrippableControl(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return unicode.IsControl(r)
}

func (o *Overlay) typeText(msg TypeMessage) {
	// Single enforced sanitization point for injected content — see
	// sanitizeInjectedText. Every injection path funnels through here.
	msg.Text = sanitizeInjectedText(msg.Text)

	o.injectMu.Lock()
	defer o.injectMu.Unlock()

	if msg.Instant {
		// Send full text as single write - large buffer triggers paste detection
		// in terminal input handlers without needing bracketed paste escape sequences.
		o.writeTopty(msg.Text)

		if msg.Enter {
			// Progressive enter key timing to ensure agent accepts the message.
			// This handles different AI agent input processing speeds.
			o.sendEntersUntilActivity(msg.Text)
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
			o.sendEntersUntilActivity(msg.Text)
		}
	}
}

// SetInputProbe installs the check sendEntersUntilActivity uses to see
// whether a typed message is still waiting in the agent's input box.
func (o *Overlay) SetInputProbe(fn func(tail string) (found, ok bool)) {
	if fn == nil {
		o.inputProbe.Store(nil)
		return
	}
	o.inputProbe.Store(&fn)
}

// inputTailRunes is how much of a message's end the input probe looks for.
const inputTailRunes = 12

// messageTail returns the last inputTailRunes letters and digits of text.
func messageTail(text string) string {
	r := []rune(overlay.AlnumOnly(text))
	if len(r) > inputTailRunes {
		r = r[len(r)-inputTailRunes:]
	}
	return string(r)
}

// inputShows asks the probe whether tail is still near the cursor. ok is
// false without a probe or when the probe cannot tell.
func (o *Overlay) inputShows(tail string) (found, ok bool) {
	p := o.inputProbe.Load()
	if p == nil {
		return false, false
	}
	return (*p)(tail)
}

// inputEchoWait bounds how long sendEntersUntilActivity waits for the typed
// text to appear on screen before its first Enter.
const inputEchoWait = 300 * time.Millisecond

// sendEntersUntilActivity sends enter keys until the agent accepts text.
// Sends the first Enter, waits for the echo to settle, then retries with
// increasing delays. Max 4 enters total.
//
// Output after the settle window means the agent is responding. Silence is
// not a refusal, though: an agent can accept a message and work without
// printing. So when the screen showed the message in the input box before the
// first Enter, a retry is sent only while it still does. When it never showed
// (no screen, or the agent folded a paste into a placeholder), silence is all
// there is to go on and every retry is sent.
func (o *Overlay) sendEntersUntilActivity(text string) {
	tail := messageTail(text)
	shown := false
	for waited := time.Duration(0); ; waited += 20 * time.Millisecond {
		found, ok := o.inputShows(tail)
		if !ok || found || waited >= inputEchoWait {
			shown = ok && found
			break
		}
		o.sleep(20 * time.Millisecond)
	}
	accepted := func() bool {
		if !shown {
			return false
		}
		found, ok := o.inputShows(tail)
		return ok && !found
	}

	// Send the first Enter — this must always happen regardless of any
	// activity signals from the text echo.
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
		if accepted() {
			return // The message left the input box
		}
		select {
		case <-o.activityCh:
			return // Agent is responding — message was accepted
		case <-o.after(delay):
			if accepted() {
				return
			}
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
			_ = conn.SetWriteDeadline(time.Now().Add(overlayWSWriteWait))
			err := conn.WriteMessage(websocket.TextMessage, msg)
			wmu.Unlock()
			if err != nil {
				// Dead conn: evict so later broadcasts skip it and the read
				// loop unwinds instead of writing to a corpse forever.
				o.clients.Delete(conn)
				conn.Close()
			}
		} else {
			_ = conn.WriteMessage(websocket.TextMessage, msg)
		}
		return true
	})
}
