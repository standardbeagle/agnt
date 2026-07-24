package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/debug"
	storepkg "github.com/standardbeagle/agnt/internal/store"
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
		URL:        r.URL.String(),
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

	// Per-connection capture store: maps browser-generated IDs to saved file paths.
	// Written by the binary screenshot handler, read by panel_message and sketch handlers.
	// Single-goroutine access — no locking needed.
	sessionCaptures := make(map[string]string)

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
			if session, ok := ps.voiceSessions.Load(connID); ok {
				session.(*VoiceSession).SendAudio(rawMessage)
				continue
			}
			// Screenshot binary frame: [1 byte: idLen][idLen bytes: id][PNG bytes]
			if len(rawMessage) >= 2 {
				idLen := int(rawMessage[0])
				if idLen > 0 && len(rawMessage) >= 1+idLen+1 {
					captureID := string(rawMessage[1 : 1+idLen])
					imgBytes := rawMessage[1+idLen:]
					filePath, err := ps.savePNGBytes("area-"+captureID, imgBytes)
					if err != nil {
						debug.Error("proxy", "Failed to save binary screenshot id=%s: %v", captureID, err)
					} else {
						sessionCaptures[captureID] = filePath
						debug.Log("proxy", "Saved binary screenshot: id=%s path=%s", captureID, filePath)
						if wErr := asyncConn.WriteJSON(map[string]interface{}{
							"type":      "capture_ack",
							"id":        captureID,
							"file_path": filePath,
						}); wErr != nil {
							debug.Error("proxy", "failed to send capture_ack: %v", wErr)
						}
					}
				}
			}
			continue
		}

		// Parse JSON message
		var msg struct {
			Type      string                 `json:"type"`
			Data      map[string]interface{} `json:"data"`
			URL       string                 `json:"url"`
			SessionID string                 `json:"session_id"`
			FrameID   string                 `json:"frame_id"`
		}

		if err := json.Unmarshal(rawMessage, &msg); err != nil {
			// Best-effort: malformed browser telemetry frame, skip it. The data
			// is untrusted browser input so a parse failure is not actionable
			// beyond a diagnostic trace.
			debug.Log("proxy", "failed to parse browser WS message for proxy %s: %v", ps.ID, err)
			continue
		}

		seq := ps.requestSeq.Add(1)
		id := fmt.Sprintf("metric-%d", seq)
		timestamp := time.Now()

		switch msg.Type {
		case "frame_active":
			// A content frame reports it became the active interaction target.
			// MCP exec / audits default to it (always-wrap model).
			ps.SetActiveFrame(msg.FrameID)

		case "error":
			errEntry := FrontendError{
				ID:        id,
				Timestamp: timestamp,
				Message:   getStringField(msg.Data, "message"),
				Source:    getStringField(msg.Data, "source"),
				LineNo:    getIntField(msg.Data, "lineno"),
				ColNo:     getIntField(msg.Data, "colno"),
				Error:     getStringField(msg.Data, "error"),
				Stack:     getStringField(msg.Data, "stack"),
				URL:       msg.URL,
			}
			ps.logger.LogError(errEntry, msg.FrameID)
			ps.pageTracker.TrackError(errEntry, msg.SessionID, msg.FrameID)

			// Auto-forward browser errors to overlay for PTY injection. The
			// error is already in the traffic log (LogError above), so overlay
			// forwarding is supplementary — surface a failure to the debug log.
			if ps.overlayNotifier.IsEnabled() {
				if err := ps.overlayNotifier.NotifyBrowserError(ps.ID, errEntry); err != nil {
					debug.Log("proxy", "failed to forward browser error to overlay: %v", err)
				}
			}

		case "performance":
			metric := PerformanceMetric{
				ID:                   id,
				Timestamp:            timestamp,
				URL:                  msg.URL,
				NavigationStart:      getInt64Field(msg.Data, "navigation_start"),
				LoadEventEnd:         getInt64Field(msg.Data, "load_event_end"),
				DOMContentLoaded:     getInt64Field(msg.Data, "dom_content_loaded"),
				FirstPaint:           getInt64Field(msg.Data, "first_paint"),
				FirstContentfulPaint: getInt64Field(msg.Data, "first_contentful_paint"),
				PageTitle:            getStringField(msg.Data, "page_title"),
				PageWidth:            int(getInt64Field(msg.Data, "page_width")),
				PageHeight:           int(getInt64Field(msg.Data, "page_height")),
				ViewportWidth:        int(getInt64Field(msg.Data, "viewport_width")),
				ViewportHeight:       int(getInt64Field(msg.Data, "viewport_height")),
				Custom:               msg.Data,
			}

			// Extract resources if present
			if resourcesData, ok := msg.Data["resources"].([]interface{}); ok {
				for _, r := range resourcesData {
					if rm, ok := r.(map[string]interface{}); ok {
						metric.Resources = append(metric.Resources, ResourceTiming{
							Name:     getStringField(rm, "name"),
							Duration: getInt64Field(rm, "duration"),
							Size:     getInt64Field(rm, "size"),
						})
					}
				}
			}

			ps.logger.LogPerformance(metric, msg.FrameID)
			ps.pageTracker.TrackPerformance(metric, msg.SessionID, msg.FrameID)

		case "custom_log":
			ps.logger.LogCustom(CustomLog{
				ID:        id,
				Timestamp: timestamp,
				Level:     getStringField(msg.Data, "level"),
				Message:   getStringField(msg.Data, "message"),
				Data:      msg.Data,
				URL:       msg.URL,
			}, msg.FrameID)

		case "screenshot":
			// Save screenshot to temp file
			dataURL := getStringField(msg.Data, "data")
			name := getStringField(msg.Data, "name")
			if name == "" {
				name = fmt.Sprintf("screenshot-%d", timestamp.Unix())
			}

			selector := getStringField(msg.Data, "selector")
			if selector == "" {
				selector = "body"
			}

			filePath, err := ps.saveScreenshot(name, dataURL)
			if err != nil {
				// Log failed screenshot so it appears in proxylog
				ps.logger.LogScreenshot(Screenshot{
					ID:        id,
					Timestamp: timestamp,
					Name:      name,
					URL:       msg.URL,
					Width:     getIntField(msg.Data, "width"),
					Height:    getIntField(msg.Data, "height"),
					Format:    getStringField(msg.Data, "format"),
					Selector:  selector,
					Error:     err.Error(),
				})
				continue
			}

			// Register screenshot by name so proxy exec can look up the file path
			ps.registerCapture(name, filePath)

			ps.logger.LogScreenshot(Screenshot{
				ID:        id,
				Timestamp: timestamp,
				Name:      name,
				FilePath:  filePath,
				URL:       msg.URL,
				Width:     getIntField(msg.Data, "width"),
				Height:    getIntField(msg.Data, "height"),
				Format:    getStringField(msg.Data, "format"),
				Selector:  selector,
			})

		case "execution":
			// Log JavaScript execution result
			execID := getStringField(msg.Data, "exec_id")
			duration := time.Duration(getInt64Field(msg.Data, "duration")) * time.Millisecond
			result := getStringField(msg.Data, "result")

			execResult := ExecutionResult{
				ID:        id,
				Timestamp: timestamp,
				Code:      execID, // Will be filled in by the tool
				Result:    result,
				Error:     getStringField(msg.Data, "error"),
				Duration:  duration,
				URL:       msg.URL,
				Data:      msg.Data,
			}

			// Save large results to file
			if filePath, err := ps.saveLargeResult(execID, result); err == nil && filePath != "" {
				execResult.FilePath = filePath
				// Replace result with summary for logging
				execResult.Result = fmt.Sprintf("[Large result saved to %s (%d bytes)]", filePath, len(result))
			}

			ps.logger.LogExecution(execResult)

			// Send result to waiting channel if one exists
			if ch, ok := ps.pendingExecs.LoadAndDelete(execID); ok {
				resultChan := ch.(chan *ExecutionResult)
				select {
				case resultChan <- &execResult:
					close(resultChan)
				default:
					close(resultChan)
				}
			}

		case "interactions":
			// Handle batched interaction events from frontend
			events := getArrayField(msg.Data, "events")
			for _, eventData := range events {
				if em, ok := eventData.(map[string]interface{}); ok {
					interaction := parseInteractionEvent(em, id, timestamp, msg.URL)
					ps.logger.LogInteraction(interaction, msg.FrameID)
					ps.pageTracker.TrackInteraction(interaction, msg.SessionID, msg.FrameID)
				}
			}

		case "mutations":
			// Handle batched mutation events from frontend
			events := getArrayField(msg.Data, "events")
			for _, eventData := range events {
				if em, ok := eventData.(map[string]interface{}); ok {
					mutation := parseMutationEvent(em, id, timestamp, msg.URL)
					ps.logger.LogMutation(mutation, msg.FrameID)
					ps.pageTracker.TrackMutation(mutation, msg.SessionID, msg.FrameID)
				}
			}

		case "secret_submit":
			// Secret-entry submission from the browser's masked input field.
			//
			// ZERO-LEAK INVARIANT (branch-before-emit): the submitted value is
			// extracted here, handed to the daemon-wired secret sink, and
			// dropped. It is deleted from msg.Data immediately so no later
			// code path can see it, and it is NEVER placed into any event
			// struct, traffic-log entry, overlay notification, or channel
			// event — those carry only {name, fingerprint:last-4}. Do not
			// rely on downstream sanitizers; nothing downstream ever holds
			// the value.
			name := getStringField(msg.Data, "name")
			value := getStringField(msg.Data, "value")
			delete(msg.Data, "value")
			fingerprint := storepkg.Fingerprint(value)

			sinkErr := ps.DeliverSecret(name, value)
			value = ""

			// Ack the browser so the panel can confirm or retry.
			ack := map[string]interface{}{"type": "secret_ack", "name": name, "ok": sinkErr == nil}
			if sinkErr != nil {
				ack["error"] = sinkErr.Error()
			}
			if wErr := asyncConn.WriteJSON(ack); wErr != nil {
				debug.Log("proxy", "failed to send secret_ack for %q: %v", name, wErr)
			}

			// Emit the fingerprint-only panel message so the agent learns the
			// secret ARRIVED (by name + last-4), never what it is.
			status := "stored"
			if sinkErr != nil {
				status = "FAILED: " + sinkErr.Error()
			}
			pm := PanelMessage{
				ID:        id,
				Timestamp: timestamp,
				URL:       msg.URL,
				Message:   fmt.Sprintf("[secret-entry] %s (****%s) %s — reference it by name; the value is store-held and never enters the event stream", name, fingerprint, status),
			}
			ps.logger.LogPanelMessage(pm)
			if ps.overlayNotifier.IsEnabled() {
				if err := ps.overlayNotifier.NotifyPanelMessage(ps.ID, &pm); err != nil {
					debug.Log("proxy", "failed to notify overlay of secret-entry receipt: %v", err)
				}
			}

		case "panel_message":
			// Handle message from floating indicator panel
			panelMsg := parsePanelMessage(msg.Data, id, timestamp, msg.URL)

			// Process attachments: resolve file paths from registry or save inline data
			for i := range panelMsg.Attachments {
				att := &panelMsg.Attachments[i]
				areaDataLen := 0
				if att.Area != nil {
					areaDataLen = len(att.Area.Data)
				}
				debug.Log("proxy", "Panel attachment %d: type=%s, id=%s, hasArea=%v, areaDataLen=%d",
					i, att.Type, att.ID, att.Area != nil, areaDataLen)

				// Ensure Data map exists for storing file path
				if att.Data == nil {
					att.Data = make(map[string]interface{})
				}

				// Resolve file paths from the session capture store (populated by binary handler
				// for screenshots, by sketch_capture JSON handler for sketches).
				if att.Type == "screenshot" && att.ID != "" {
					if filePath, ok := sessionCaptures[att.ID]; ok {
						delete(sessionCaptures, att.ID)
						att.Data["file_path"] = filePath
						att.Data["file_name"] = filepath.Base(filePath)
						debug.Log("proxy", "Resolved screenshot: id=%s path=%s", att.ID, filePath)
					} else if att.FilePath != "" {
						// Fallback: browser sent filePath from capture_ack
						att.Data["file_path"] = att.FilePath
						att.Data["file_name"] = filepath.Base(att.FilePath)
						debug.Log("proxy", "Resolved screenshot from JS filePath: id=%s path=%s", att.ID, att.FilePath)
					} else {
						debug.Error("proxy", "No capture for screenshot id=%s (binary frame missing or failed)", att.ID)
					}
				}

				if att.Type == "sketch" && att.ID != "" {
					if filePath, ok := sessionCaptures[att.ID]; ok {
						delete(sessionCaptures, att.ID)
						att.Data["file_path"] = filePath
						att.Data["file_name"] = filepath.Base(filePath)
						debug.Log("proxy", "Resolved sketch: id=%s path=%s", att.ID, filePath)
					}
				}

				if att.Type == "style-edit" {
					// Resolve before/after screenshot file paths from sessionCaptures
					if screenshots, ok := att.Data["screenshots"].(map[string]interface{}); ok {
						resolved := make(map[string]interface{})
						for key, val := range screenshots {
							ctxID, _ := val.(string)
							if ctxID == "" {
								continue
							}
							if filePath, ok := sessionCaptures[ctxID]; ok {
								delete(sessionCaptures, ctxID)
								resolved[key] = filePath
								debug.Log("proxy", "Resolved style-edit %s screenshot: id=%s path=%s", key, ctxID, filePath)
							}
						}
						att.Data["screenshots"] = resolved
					}
				}
			}

			ps.logger.LogPanelMessage(panelMsg)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				debug.Log("proxy", "forwarding panel_message to overlay (proxy_id=%s, endpoint=%s)", ps.ID, ps.overlayNotifier.GetEndpoint())
				if err := ps.overlayNotifier.NotifyPanelMessage(ps.ID, &panelMsg); err != nil {
					debug.Log("proxy", "Failed to notify overlay of panel message: %v", err)
				}
			} else {
				debug.Log("proxy", "panel_message received but overlay notifier NOT enabled for proxy %s — message will not reach agent", ps.ID)
			}

			// Update audit folder summary after saving new files. Best-effort
			// housekeeping: a stale SUMMARY.md index does not lose any data.
			if err := UpdateAuditSummary(); err != nil {
				debug.Log("proxy", "failed to update audit summary: %v", err)
			}

		case "walkthrough_event":
			// Handle a walkthrough (live-demo) lifecycle event from the
			// chrome-frame panel. Lets the agent narrate live / react.
			wt := WalkthroughEntry{
				ID:        id,
				Timestamp: timestamp,
				URL:       msg.URL,
				Event:     getStringField(msg.Data, "event"),
				ScriptID:  getStringField(msg.Data, "scriptId"),
				Title:     getStringField(msg.Data, "title"),
				StepIndex: getIntField(msg.Data, "stepIndex"),
				Total:     getIntField(msg.Data, "total"),
				StepTitle: getStringField(msg.Data, "stepTitle"),
				Advance:   getStringField(msg.Data, "advance"),
				How:       getStringField(msg.Data, "how"),
				Mode:      getStringField(msg.Data, "mode"),
				Message:   getStringField(msg.Data, "message"),
			}
			ps.logger.LogWalkthrough(wt)

		case "sketch":
			// Handle sketch/wireframe from sketch mode
			sketchEntry := parseSketchEntry(msg.Data, id, timestamp, msg.URL)

			// Save sketch image to audit directory
			if sketchEntry.ImageData != "" {
				filePath, err := ps.saveScreenshot("sketch-"+id, sketchEntry.ImageData)
				if err == nil {
					sketchEntry.FilePath = filePath
				}
			}

			ps.logger.LogSketch(sketchEntry)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				if err := ps.overlayNotifier.NotifySketch(ps.ID, &sketchEntry); err != nil {
					debug.Log("proxy", "failed to forward sketch to overlay: %v", err)
				}
			}

			// Update audit folder summary. Best-effort housekeeping: a stale
			// SUMMARY.md index does not lose any data.
			if err := UpdateAuditSummary(); err != nil {
				debug.Log("proxy", "failed to update audit summary: %v", err)
			}

		case "element_capture":
			// Handle element capture from panel with reference ID
			capture := parseElementCapture(msg.Data, timestamp, msg.URL)
			ps.logger.LogElementCapture(capture)

		case "sketch_capture":
			// Handle sketch capture from panel with reference ID
			capture := parseSketchCapture(msg.Data, timestamp, msg.URL)

			// Save sketch image to file if present
			if capture.ImageData != "" {
				filePath, err := ps.saveScreenshot("sketch-"+capture.ID, capture.ImageData)
				if err == nil {
					capture.FilePath = filePath
					sessionCaptures[capture.ID] = filePath
				}
				capture.ImageData = ""
			}

			ps.logger.LogSketchCapture(capture)

		case "design_state":
			// Handle design state when element is selected for iteration
			designState := parseDesignState(msg.Data, id, timestamp, msg.URL)
			ps.logger.LogDesignState(designState)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				if err := ps.overlayNotifier.NotifyDesignState(ps.ID, &designState); err != nil {
					debug.Log("proxy", "failed to forward design state to overlay: %v", err)
				}
			}

		case "design_request":
			// Handle request for new design alternatives
			designRequest := parseDesignRequest(msg.Data, id, timestamp, msg.URL)
			// Persist the live-segment screenshot (if any) so the agent message can
			// reference an on-disk PNG instead of a giant inline data URL.
			if shot := getStringField(msg.Data, "screenshot"); strings.HasPrefix(shot, "data:") {
				if p, err := ps.saveScreenshot("design-"+designRequest.Selector, shot); err == nil {
					designRequest.ScreenshotPath = p
				}
			}
			ps.logger.LogDesignRequest(designRequest)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				if err := ps.overlayNotifier.NotifyDesignRequest(ps.ID, &designRequest); err != nil {
					debug.Log("proxy", "failed to forward design request to overlay: %v", err)
				}
			}

		case "design_chat":
			// Handle chat message about selected element
			designChat := parseDesignChat(msg.Data, id, timestamp, msg.URL)
			if shot := getStringField(msg.Data, "screenshot"); strings.HasPrefix(shot, "data:") {
				if p, err := ps.saveScreenshot("design-"+designChat.Selector, shot); err == nil {
					designChat.ScreenshotPath = p
				}
			}
			ps.logger.LogDesignChat(designChat)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				if err := ps.overlayNotifier.NotifyDesignChat(ps.ID, &designChat); err != nil {
					debug.Log("proxy", "failed to forward design chat to overlay: %v", err)
				}
			}

		case "design_edit":
			// Handle a committed direct-manipulation geometry edit
			designEdit := parseDesignEdit(msg.Data, id, timestamp, msg.URL)
			ps.logger.LogDesignEdit(designEdit)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				if err := ps.overlayNotifier.NotifyDesignEdit(ps.ID, &designEdit); err != nil {
					debug.Log("proxy", "failed to forward design edit to overlay: %v", err)
				}
			}

		case "responsive_request":
			// Handle handoff from responsive mode requesting agent fixes
			responsiveRequest := parseResponsiveRequest(msg.Data, id, timestamp, msg.URL)
			ps.logger.LogResponsiveRequest(responsiveRequest)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				if err := ps.overlayNotifier.NotifyResponsiveRequest(ps.ID, &responsiveRequest); err != nil {
					debug.Log("proxy", "failed to forward responsive request to overlay: %v", err)
				}
			}

		case "responsive_state":
			// Handle responsive mode panel state (current width + shift count)
			responsiveState := parseResponsiveState(msg.Data, id, timestamp, msg.URL)
			ps.logger.LogResponsiveState(responsiveState)

			// Forward to overlay if configured
			if ps.overlayNotifier.IsEnabled() {
				if err := ps.overlayNotifier.NotifyResponsiveState(ps.ID, &responsiveState); err != nil {
					debug.Log("proxy", "failed to forward responsive state to overlay: %v", err)
				}
			}

		case "session_request":
			// Handle session API requests from browser. Writes go through the
			// serialising asyncConn — this runs in its own goroutine concurrent
			// with the read loop and the broadcast drain.
			spawnHandler(func() { ps.handleSessionRequest(asyncConn, msg.Data) })

		case "store_request":
			// Handle store API requests from browser (serialised via asyncConn).
			spawnHandler(func() { ps.handleStoreRequest(asyncConn, msg.Data) })

		case "chaos_request":
			// Handle chaos control requests from the indicator panel
			// (serialised via asyncConn). Acts directly on this proxy's
			// chaos engine — no daemon round trip.
			spawnHandler(func() { ps.handleChaosRequest(asyncConn, msg.Data) })

		case "voice_start":
			// Start voice transcription session
			config := DefaultDeepgramConfig()

			// Apply any config from message
			if lang := getStringField(msg.Data, "language"); lang != "" {
				config.Language = lang
			}
			if model := getStringField(msg.Data, "model"); model != "" {
				config.Model = model
			}

			session, err := NewVoiceSession(connID, asyncConn, config)
			if err != nil {
				if wErr := asyncConn.WriteJSON(map[string]interface{}{
					"type":  "voice_error",
					"error": err.Error(),
				}); wErr != nil {
					debug.Error("proxy", "voice_start: failed to send error to client: %v", wErr)
				}
				continue
			}

			ps.voiceSessions.Store(connID, session)

			// Log voice start
			ps.logger.LogCustom(CustomLog{
				ID:        id,
				Timestamp: timestamp,
				Level:     "info",
				Message:   "[Voice] Transcription session started",
				Data:      map[string]interface{}{"model": config.Model, "language": config.Language},
				URL:       msg.URL,
			})

		case "voice_stop":
			// Stop voice transcription session
			if session, ok := ps.voiceSessions.LoadAndDelete(connID); ok {
				session.(*VoiceSession).Close()

				if wErr := asyncConn.WriteJSON(map[string]interface{}{
					"type":    "voice_stopped",
					"message": "Transcription session ended",
				}); wErr != nil {
					debug.Error("proxy", "voice_stop: failed to send response to client: %v", wErr)
				}

				// Log voice stop
				ps.logger.LogCustom(CustomLog{
					ID:        id,
					Timestamp: timestamp,
					Level:     "info",
					Message:   "[Voice] Transcription session stopped",
					URL:       msg.URL,
				})
			}
		}
	}
}
