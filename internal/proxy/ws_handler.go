package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/publish"
)

// WebSocket keepalive timings. A ping is sent every wsPingPeriod; the peer's
// pong (or any inbound frame) extends the read deadline by wsPongWait. A peer
// that goes silent for wsPongWait is treated as dead and the read loop exits.
const (
	wsPongWait   = 60 * time.Second
	wsPingPeriod = (wsPongWait * 9) / 10
	wsWriteWait  = 10 * time.Second
)

// errorHandler handles proxy errors.
func (ps *ProxyServer) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	seq := ps.requestSeq.Add(1)
	reqID := fmt.Sprintf("req-%d", seq)
	timestamp := time.Now()

	errStr := err.Error()

	// Distinguish downstream (client) cancellation from upstream transport failure.
	// A client disconnect (tab close, navigation, AbortController) surfaces here as
	// context.Canceled / context.DeadlineExceeded. Flushing the transport pool on
	// those would kill healthy shared HTTPS connections and force every concurrent
	// request to redo TLS — which on slow upstreams (e.g. haam-dev ~2-3s handshake)
	// cascades into 502s. Only flush on genuine upstream transport errors.
	isClientCanceled := errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		(r.Context().Err() != nil && strings.Contains(errStr, "context canceled"))

	// Check if this is a transient connection error (common during development)
	// These happen when dev servers restart, connections timeout, etc.
	isTransient := isTransientConnectionError(errStr)

	// Flush stale connections on transient upstream errors so the browser's immediate
	// reconnect (especially WebSocket/HMR) gets a fresh connection to the backend.
	// Never flush on client-side cancellation — that nukes the pool for innocent
	// concurrent requests and causes cascading 502s.
	if isTransient && !isClientCanceled {
		ps.FlushConnections()
	}

	ps.logger.LogHTTP(HTTPLogEntry{
		ID:         reqID,
		Timestamp:  timestamp,
		Method:     r.Method,
		URL:        publish.ScrubSharePath(r.URL.String()),
		StatusCode: http.StatusBadGateway,
		Error:      errStr,
	})

	// Provide helpful error message based on error type
	var userMsg string
	var diagEvent string
	var diagLevel ProxyDiagnosticLevel

	if isClientCanceled {
		// The downstream client went away (tab close, navigation, AbortController).
		// This is not a proxy or upstream failure — don't alarm the user and don't
		// touch the connection pool.
		userMsg = "Client canceled the request before the response completed."
		diagEvent = "client_canceled"
		diagLevel = DiagnosticWarning
	} else if strings.Contains(errStr, "context canceled") {
		userMsg = fmt.Sprintf("Proxy Error: Request canceled. The proxy may be shutting down, or the target server (%s) is unavailable.", ps.TargetURL.String())
		diagEvent = "context_canceled"
		diagLevel = DiagnosticWarning
	} else if strings.Contains(errStr, "connection refused") {
		userMsg = fmt.Sprintf("Proxy Error: Cannot connect to target server %s. Make sure the server is running.", ps.TargetURL.String())
		diagEvent = "connection_refused"
		diagLevel = DiagnosticError
	} else if strings.Contains(errStr, "no such host") {
		userMsg = fmt.Sprintf("Proxy Error: Cannot resolve target host %s. Check the target URL.", ps.TargetURL.String())
		diagEvent = "dns_error"
		diagLevel = DiagnosticError
	} else if isTransient {
		// Friendly message for transient errors - these are normal during development
		userMsg = fmt.Sprintf("Connection to %s was interrupted. This often happens when the dev server restarts. Refresh to retry.", ps.TargetURL.Host)
		diagEvent = "transient_error"
		diagLevel = DiagnosticWarning
	} else {
		userMsg = fmt.Sprintf("Proxy Error: %s (target: %s)", errStr, ps.TargetURL.String())
		diagEvent = "unknown_error"
		diagLevel = DiagnosticError
	}

	// Broadcast diagnostic to connected browser clients
	ps.BroadcastProxyDiagnostic(&ProxyDiagnostic{
		Timestamp: timestamp,
		Level:     diagLevel,
		Category:  "proxy",
		Event:     diagEvent,
		Message:   userMsg,
		RequestID: reqID,
		Method:    r.Method,
		URL:       r.URL.String(),
		Target:    ps.TargetURL.String(),
		Data: map[string]any{
			"raw_error":    errStr,
			"is_transient": isTransient,
			"status_code":  http.StatusBadGateway,
		},
	})

	// Record this connection attempt for the loading page history
	ps.recordConnAttempt(diagEvent, userMsg)

	// For browser requests hitting a server that isn't up yet, serve a friendly
	// loading page with auto-refresh instead of a cryptic error string.
	if (isTransient || diagEvent == "connection_refused") && acceptsHTML(r) {
		ps.serveLoadingPage(w, r, ps.TargetURL.String())
		return
	}

	http.Error(w, userMsg, http.StatusBadGateway)
}

// handleWebSocket handles WebSocket connections for frontend metrics.
func (ps *ProxyServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	debug.Log("proxy", "WebSocket connection attempt from %s to proxy %s", r.RemoteAddr, ps.ID)

	rawConn, err := ps.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		debug.Log("proxy", "WebSocket upgrade failed for proxy %s: %v", ps.ID, err)
		return
	}
	defer rawConn.Close()

	// Track per-message handler goroutines so they cannot outlive the
	// connection: the deferred Wait (registered after the Close defers, so
	// it runs first) awaits them before asyncConn/rawConn are torn down.
	var handlerWG sync.WaitGroup
	spawnHandler := func(fn func()) {
		handlerWG.Add(1)
		go func() {
			defer handlerWG.Done()
			fn()
		}()
	}

	// Wrap rawConn in async writer so broadcasts never block on slow clients.
	// asyncConn is stored in wsConns for broadcast use only; rawConn is used
	// for all per-connection reads and direct writes from this goroutine.
	asyncConn := newAsyncWSWriter(rawConn, websocket.TextMessage)
	defer asyncConn.Close()
	defer handlerWG.Wait()

	// Store connection for sending messages
	connID := fmt.Sprintf("conn-%d", time.Now().UnixNano())
	ps.wsConns.Store(connID, asyncConn)
	debug.Log("proxy", "WebSocket client connected: proxy=%s connID=%s remote=%s", ps.ID, connID, r.RemoteAddr)

	// Warn the browser if TLS cert verification was bypassed for this proxy.
	if ps.tlsCertSkipped.Load() {
		if warn, err := json.Marshal(map[string]interface{}{
			"type":     "toast",
			"toast":    "warning",
			"title":    "Self-Signed Certificate",
			"message":  "This proxy is bypassing TLS certificate verification. The backend is using a self-signed or invalid certificate.",
			"duration": 8000,
		}); err == nil {
			// Best-effort browser toast; if the control-lane write fails the
			// conn is already going away, so degrade quietly to the debug log.
			if werr := asyncConn.WriteSync(warn); werr != nil {
				debug.Log("proxy", "failed to send TLS-skip warning toast to browser: %v", werr)
			}
		}
	}

	defer func() {
		ps.wsConns.Delete(connID)
		debug.Log("proxy", "WebSocket client disconnected: proxy=%s connID=%s", ps.ID, connID)
	}()

	// Cleanup voice session on disconnect
	defer func() {
		if session, ok := ps.voiceSessions.LoadAndDelete(connID); ok {
			session.(*VoiceSession).Close()
		}
	}()

	// Per-connection dispatch state: captures map is written by the binary
	// screenshot handler and sketch_capture, read by panel_message.
	connState := &wsConnState{
		ps:           ps,
		asyncConn:    asyncConn,
		connID:       connID,
		captures:     make(map[string]string),
		spawnHandler: spawnHandler,
	}

	// Keepalive: a half-open connection (client vanished without a close frame)
	// would otherwise block ReadMessage forever, leaking this goroutine and the
	// wsConns entry. A read deadline plus a pong handler that extends it, driven
	// by a periodic ping, detects the dead peer. WriteControl is safe to call
	// concurrently with the asyncConn drain goroutine (gorilla guarantees this).
	rawConn.SetReadDeadline(time.Now().Add(wsPongWait))
	rawConn.SetPongHandler(func(string) error {
		return rawConn.SetReadDeadline(time.Now().Add(wsPongWait))
	})
	pingStop := make(chan struct{})
	defer close(pingStop)
	go func() {
		ticker := time.NewTicker(wsPingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := rawConn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)); err != nil {
					return
				}
			case <-pingStop:
				return
			}
		}
	}()

	// Read messages from frontend
	for {
		messageType, rawMessage, err := rawConn.ReadMessage()
		if err != nil {
			break
		}
		// Active client → extend the read deadline so a steady telemetry stream is
		// not disconnected between pings.
		rawConn.SetReadDeadline(time.Now().Add(wsPongWait))

		// Binary messages: voice audio or screenshot PNG bytes
		if messageType == websocket.BinaryMessage {
			connState.handleBinaryFrame(rawMessage)
			continue
		}

		// Parse JSON message
		var msg wsMessage
		if err := json.Unmarshal(rawMessage, &msg); err != nil {
			// Best-effort: malformed browser telemetry frame, skip it. The data
			// is untrusted browser input so a parse failure is not actionable
			// beyond a diagnostic trace.
			debug.Log("proxy", "failed to parse browser WS message for proxy %s: %v", ps.ID, err)
			continue
		}

		seq := ps.requestSeq.Add(1)
		connState.id = fmt.Sprintf("metric-%d", seq)
		connState.timestamp = time.Now()

		if handler, ok := wsMessageHandlers[msg.Type]; ok {
			handler(connState, &msg)
		}
	}
}
