package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/httpcaps"
	"github.com/standardbeagle/agnt/internal/overlay"
)

// AIOverlay is a lightweight overlay server for agnt ai claude.
// Instead of writing to a PTY, it queues messages on a channel for the
// REPL loop to deliver as --resume turns.
type AIOverlay struct {
	socketPath      string
	messages        chan aiOverlayMessage
	server          *http.Server
	listener        net.Listener
	auditSummarizer *overlay.AuditSummarizer

	// policy is the `.agnt.kdl` alerts filter chain for auto-generated error
	// events — the same autoForwardPolicy the PTY overlay applies. Set once by
	// configureAlerts before Start; the zero value forwards nothing.
	policy autoForwardPolicy
	// forwardPaused mirrors Overlay.forwardPaused: while set, auto-generated
	// alerts are not queued for the agent. Explicit user actions still are.
	forwardPaused atomic.Bool
	// dropped counts messages lost because the queue was full, until the
	// consumer collects the count with TakeDropped and reports it.
	dropped atomic.Int64
}

// aiOverlayMessage is one message queued for delivery as a prompt turn.
type aiOverlayMessage struct {
	text string
	// protected marks an explicit user action (panel message, sketch,
	// `agnt session send`). Like AlertMatch.Protected on the PTY path, it is
	// never deduped, capped, or held back by a forwarding pause.
	protected bool
}

// newAIOverlay creates an AI overlay with a session-specific socket path.
func newAIOverlay(sessionCode string) *AIOverlay {
	socketPath := overlaySessionSocketPath(sessionCode)

	summarizer := overlay.NewAuditSummarizer(overlay.AuditSummarizerConfig{
		UseAPI:  true,
		Timeout: 30 * time.Second,
	})

	return &AIOverlay{
		socketPath:      socketPath,
		messages:        make(chan aiOverlayMessage, 64),
		auditSummarizer: summarizer,
	}
}

// configureAlerts loads the alerts filter chain for projectPath. It mirrors
// setupAlertScanner's decisions on the PTY path: a config that fails to load
// or has `alerts { enabled false }` forwards no auto-generated errors (the
// load failure loudly — a broken config must not quietly change alert
// behavior), and otherwise auto-forward follows `alerts.auto-forward`.
// Call before Start.
func (a *AIOverlay) configureAlerts(projectPath string) {
	cfg, err := loadResolvedConfig(projectPath)
	if err != nil {
		notifyUser(overlay.LevelWarn, "alerts disabled: failed to load config for the alert filter: %v", err)
		return
	}
	a.policy = alertPolicyFromConfig(cfg)
}

// alertPolicyFromConfig builds the auto-forward filter chain from a loaded
// config. Pure, so the mapping is testable without a socket.
func alertPolicyFromConfig(cfg *config.AgntConfig) autoForwardPolicy {
	if cfg == nil {
		cfg = config.DefaultAgntConfig()
	}
	if cfg.Alerts != nil && !cfg.Alerts.IsEnabled() {
		return autoForwardPolicy{}
	}
	var af *config.AutoForwardConfig
	var cascade []string
	if cfg.Alerts != nil {
		af = cfg.Alerts.AutoForward
		if cfg.Alerts.OutageHold != nil {
			cascade = cfg.Alerts.OutageHold.GetJSCascadePatterns()
		}
	}
	return autoForwardPolicy{
		enabled: af.IsEnabled(),
		cfg:     af,
		cascade: lowerCascadePatterns(cascade),
	}
}

// SetForwardingPaused pauses/resumes queueing of auto-generated alerts.
func (a *AIOverlay) SetForwardingPaused(paused bool) {
	a.forwardPaused.Store(paused)
}

// TakeDropped returns the number of messages dropped on a full queue since the
// last call, and resets the count.
func (a *AIOverlay) TakeDropped() int {
	return int(a.dropped.Swap(0))
}

// Start begins listening on the Unix socket.
func (a *AIOverlay) Start(ctx context.Context) error {
	if a.socketPath == "" {
		return fmt.Errorf("no socket path")
	}

	// Same listener rules as the PTY overlay: per-user dir, liveness-probed
	// stale removal, owner-only mode.
	listener, err := listenOverlaySocket(a.socketPath)
	if err != nil {
		return fmt.Errorf("failed to create AI overlay socket: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/type", a.handleType)
	mux.HandleFunc("/inject", a.handleInject)
	mux.HandleFunc("/event", a.handleEvent)
	mux.HandleFunc("/health", a.handleHealth)

	// Loopback (unix socket), routed through the shared constructor for
	// uniformity. Streaming because the interactive AI overlay holds long-lived
	// local connections that a whole-request write deadline would sever.
	caps := httpcaps.Streaming()
	listener = caps.LimitListener(listener)
	a.listener = listener
	a.server = caps.NewServer(mux)

	go func() {
		if err := a.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			debug.Error("overlay", "AI overlay server error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the server and removes the socket.
func (a *AIOverlay) Stop() {
	if a.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = a.server.Shutdown(ctx)
	}
	// Remove the socket only if this overlay listened on it; after a failed
	// Start the path may belong to a live session.
	if a.listener != nil {
		a.listener.Close()
		if a.socketPath != "" {
			os.Remove(a.socketPath)
		}
	}
}

// Messages returns the channel that receives queued messages.
func (a *AIOverlay) Messages() <-chan aiOverlayMessage {
	return a.messages
}

// SocketPath returns the socket path the overlay is listening on.
func (a *AIOverlay) SocketPath() string {
	return a.socketPath
}

func (a *AIOverlay) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "ok",
		"type":        "ai",
		"socket_path": a.socketPath,
	})
}

func (a *AIOverlay) handleType(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var msg TypeMessage
	if !decodeOverlayJSON(w, r, maxOverlayControlBody, &msg) {
		return
	}

	if msg.Text != "" {
		// /type is an explicit send (`agnt session send`, scheduled message).
		a.enqueue(aiOverlayMessage{text: sanitizeInjectedText(msg.Text), protected: true})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleInject receives the daemon's plain-text sends (see Overlay.handleInject).
func (a *AIOverlay) handleInject(w http.ResponseWriter, r *http.Request) {
	text, ok := readInjectBody(w, r)
	if !ok {
		return
	}
	a.enqueue(aiOverlayMessage{text: text, protected: true})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (a *AIOverlay) handleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var event ProxyEvent
	if !decodeOverlayJSON(w, r, maxOverlayEventBody, &event) {
		return
	}

	a.processProxyEvent(event)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// processProxyEvent applies the alerts filter chain and queues what survives.
// Auto-generated errors (browser_error / http_error) go through the shared
// autoForwardPolicy and the forwarding pause, exactly as on the PTY path;
// every other event is an explicit user action and is queued as protected.
func (a *AIOverlay) processProxyEvent(event ProxyEvent) {
	protected := !isAutoForwardEvent(event)
	if !protected {
		if _, ok := a.policy.admit(event); !ok {
			return
		}
		if a.forwardPaused.Load() {
			return
		}
	}
	if text := formatProxyEventText(event, a.auditSummarizer); text != "" {
		a.enqueue(aiOverlayMessage{text: text, protected: protected})
	}
}

// enqueue adds a message to the channel without blocking the HTTP handler. A
// full queue drops the message, counted in dropped so the consumer reports
// the loss to the user and the agent (see acpAlertGate.Pull) rather than
// leaving it in the debug log alone.
func (a *AIOverlay) enqueue(m aiOverlayMessage) {
	select {
	case a.messages <- m:
	default:
		a.dropped.Add(1)
		debug.Warn("overlay", "AI overlay message queue full, dropping message")
	}
}
