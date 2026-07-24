package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/overlay"
)

// AIOverlay is a lightweight overlay server for agnt ai claude.
// Instead of writing to a PTY, it queues messages on a channel for the
// REPL loop to deliver as --resume turns.
type AIOverlay struct {
	socketPath      string
	messages        chan string
	server          *http.Server
	listener        net.Listener
	auditSummarizer *overlay.AuditSummarizer
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
		messages:        make(chan string, 64),
		auditSummarizer: summarizer,
	}
}

// Start begins listening on the Unix socket.
func (a *AIOverlay) Start(ctx context.Context) error {
	if a.socketPath == "" {
		return fmt.Errorf("no socket path")
	}

	// Remove stale socket
	if _, err := os.Stat(a.socketPath); err == nil {
		os.Remove(a.socketPath)
	}

	listener, err := net.Listen("unix", a.socketPath)
	if err != nil {
		return fmt.Errorf("failed to create AI overlay socket: %w", err)
	}
	a.listener = listener

	mux := http.NewServeMux()
	mux.HandleFunc("/type", a.handleType)
	mux.HandleFunc("/event", a.handleEvent)
	mux.HandleFunc("/health", a.handleHealth)

	a.server = &http.Server{Handler: mux}

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
	if a.listener != nil {
		a.listener.Close()
	}
	if a.socketPath != "" {
		os.Remove(a.socketPath)
	}
}

// Messages returns the channel that receives queued messages.
func (a *AIOverlay) Messages() <-chan string {
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
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if msg.Text != "" {
		a.enqueue(msg.Text)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (a *AIOverlay) handleEvent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var event ProxyEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	text := formatProxyEventText(event, a.auditSummarizer)
	if text != "" {
		a.enqueue(text)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// enqueue adds a message to the channel. Drops if the channel is full
// (shouldn't happen with 64-buffer, but avoids blocking HTTP handlers).
func (a *AIOverlay) enqueue(text string) {
	select {
	case a.messages <- text:
	default:
		debug.Warn("overlay", "AI overlay message queue full, dropping message")
	}
}
