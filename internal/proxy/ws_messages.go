package proxy

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/debug"
	storepkg "github.com/standardbeagle/agnt/internal/store"
)

// wsMessage is the parsed browser telemetry frame dispatched by the
// WebSocket read loop.
type wsMessage struct {
	Type      string                 `json:"type"`
	Data      map[string]interface{} `json:"data"`
	URL       string                 `json:"url"`
	SessionID string                 `json:"session_id"`
	FrameID   string                 `json:"frame_id"`
}

// wsConnState carries the per-connection state shared by message handlers:
// the proxy, the async writer, the connection ID, the per-session capture
// store (binary screenshots and sketch captures, read by panel_message and
// sketch handlers), and the handler-goroutine spawner. id/timestamp are the
// per-message metric identity, refreshed by the read loop before dispatch;
// the loop is single-goroutine so no locking is needed.
type wsConnState struct {
	ps           *ProxyServer
	asyncConn    *asyncWSWriter
	connID       string
	captures     map[string]string
	spawnHandler func(func())
	id           string
	timestamp    time.Time

	// trusted is controlCallerTrusted for the upgrade request: false for a
	// caller the dev OIDC issuer would not serve (an unauthenticated tunnel
	// visitor above all). Untrusted connections are refused secret_submit and
	// voice_start; see refusePrivileged.
	trusted bool
	// refusalReported and frameCapReported keep the matching diagnostics to
	// one per connection.
	refusalReported  bool
	frameCapReported bool
	// refusalToastAt is when this connection was last shown the agent-input
	// refusal toast (read loop only).
	refusalToastAt time.Time

	// voiceMu guards voicePending (voice_start dials in flight) and
	// voiceBacklog (audio frames held for them). It is the one piece of
	// connection state shared between the read loop and a handler goroutine.
	voiceMu      sync.Mutex
	voicePending int
	voiceBacklog [][]byte
}

// voiceBacklogMaxFrames bounds the audio held while a Deepgram dial is in
// flight. The browser sends 4096-sample frames at 16 kHz (about a quarter of
// a second each), so this covers the whole handshake timeout.
const voiceBacklogMaxFrames = 64

// refusePrivileged reports whether this connection must be refused a frame
// that writes a secret or spends an operator API key, and records the refusal
// as a diagnostic (once per connection) so it reaches the agent rather than
// only the debug log. The caller still owes the browser its own reply.
func (c *wsConnState) refusePrivileged(msgType string) bool {
	if c.trusted {
		return false
	}
	debug.Log("proxy", "refused %s on proxy %s: connection %s is not a local, tailnet-identified or Access-verified caller", msgType, c.ps.ID, c.connID)
	if !c.refusalReported {
		c.refusalReported = true
		c.ps.logger.LogDiagnostic(ProxyDiagnostic{
			Timestamp: time.Now(),
			Level:     DiagnosticWarning,
			Category:  "proxy",
			Event:     "ws_privileged_frame_refused",
			Message:   fmt.Sprintf("proxy %s refused a %s frame: the control WebSocket caller is not local, tailnet-identified or Cloudflare Access-verified (for example it arrived through an unauthenticated tunnel)", c.ps.ID, msgType),
			Data:      map[string]any{"proxy_id": c.ps.ID, "frame_type": msgType},
		})
	}
	return true
}

// agentInputRefusalToastEvery spaces out the refusal toast, so a page that
// streams refused frames (design_state on every selection) shows one notice,
// not a stack of them.
const agentInputRefusalToastEvery = 5 * time.Second

// refuseAgentInput gates a frame whose handler hands browser content to the
// agent: into its terminal through the overlay, and into the traffic log that
// channel mode forwards as user actions. For an untrusted connection the
// frame is dropped whole, before anything is logged, saved or forwarded.
//
// These frame types carry no request id and get no reply of their own, so the
// refusal is shown as a toast on the sender's own connection (toast.js renders
// it) — a refused sender sees why, rather than a panel that silently does
// nothing — on top of the diagnostic refusePrivileged records.
func (c *wsConnState) refuseAgentInput(msgType string) bool {
	if !c.refusePrivileged(msgType) {
		return false
	}
	if now := time.Now(); now.Sub(c.refusalToastAt) >= agentInputRefusalToastEvery {
		c.refusalToastAt = now
		// Broadcast lane on this one connection: best-effort, never blocks
		// the read loop on an untrusted peer.
		if frame, err := json.Marshal(map[string]interface{}{
			"type": "toast",
			"payload": map[string]interface{}{
				"type":     "error",
				"title":    "Not sent to the agent",
				"message":  "This connection cannot send to the agent: " + privilegedRefusalMessage + ".",
				"duration": 8000,
			},
		}); err == nil {
			if wErr := c.asyncConn.WriteMessage(websocket.TextMessage, frame); wErr != nil {
				debug.Log("proxy", "failed to send agent-input refusal toast: %v", wErr)
			}
		}
	}
	return true
}

// privilegedRefusalMessage is the constant reply to a refused frame.
const privilegedRefusalMessage = "not available on this connection: it requires a local, tailnet or Cloudflare Access-verified caller"

// wsMessageHandlers dispatches browser telemetry frames by type. Each
// handler runs on the read-loop goroutine unless it explicitly defers to
// spawnHandler (session/store/chaos requests).
var wsMessageHandlers = map[string]func(c *wsConnState, msg *wsMessage){
	"frame_active":       (*wsConnState).handleFrameActive,
	"error":              (*wsConnState).handleError,
	"performance":        (*wsConnState).handlePerformance,
	"custom_log":         (*wsConnState).handleCustomLog,
	"screenshot":         (*wsConnState).handleScreenshot,
	"execution":          (*wsConnState).handleExecution,
	"interactions":       (*wsConnState).handleInteractions,
	"mutations":          (*wsConnState).handleMutations,
	"secret_submit":      (*wsConnState).handleSecretSubmit,
	"panel_message":      (*wsConnState).handlePanelMessage,
	"walkthrough_event":  (*wsConnState).handleWalkthroughEvent,
	"sketch":             (*wsConnState).handleSketch,
	"element_capture":    (*wsConnState).handleElementCapture,
	"sketch_capture":     (*wsConnState).handleSketchCapture,
	"design_state":       (*wsConnState).handleDesignState,
	"design_request":     (*wsConnState).handleDesignRequest,
	"design_chat":        (*wsConnState).handleDesignChat,
	"design_edit":        (*wsConnState).handleDesignEdit,
	"responsive_request": (*wsConnState).handleResponsiveRequest,
	"responsive_state":   (*wsConnState).handleResponsiveState,
	"session_request":    (*wsConnState).handleSessionRequestMsg,
	"store_request":      (*wsConnState).handleStoreRequestMsg,
	"chaos_request":      (*wsConnState).handleChaosRequestMsg,
	"voice_start":        (*wsConnState).handleVoiceStart,
	"voice_stop":         (*wsConnState).handleVoiceStop,
}

// handleBinaryFrame processes a binary WebSocket message: voice audio when a
// voice session is live, otherwise a screenshot PNG frame with the
// [1 byte: idLen][id][PNG] layout.
func (c *wsConnState) handleBinaryFrame(rawMessage []byte) {
	c.voiceMu.Lock()
	if c.voicePending > 0 {
		// Audio for a session whose Deepgram dial is still in flight. Past
		// the cap the frame is dropped: a whole frame, so the stream stays
		// aligned, and the gap is only in audio the dial was too slow for.
		if len(c.voiceBacklog) < voiceBacklogMaxFrames {
			c.voiceBacklog = append(c.voiceBacklog, rawMessage)
		}
		c.voiceMu.Unlock()
		return
	}
	c.voiceMu.Unlock()

	if session, ok := c.ps.voiceSessions.Load(c.connID); ok {
		session.(*VoiceSession).SendAudio(rawMessage)
		return
	}
	// Screenshot binary frame: [1 byte: idLen][idLen bytes: id][PNG bytes]
	if len(rawMessage) < 2 {
		return
	}
	idLen := int(rawMessage[0])
	if idLen <= 0 || len(rawMessage) < 1+idLen+1 {
		return
	}
	captureID := string(rawMessage[1 : 1+idLen])
	imgBytes := rawMessage[1+idLen:]
	filePath, err := c.ps.savePNGBytes("area-"+captureID, imgBytes)
	if err != nil {
		debug.Error("proxy", "Failed to save binary screenshot id=%s: %v", captureID, err)
		return
	}
	c.captures[captureID] = filePath
	debug.Log("proxy", "Saved binary screenshot: id=%s path=%s", captureID, filePath)
	if wErr := c.asyncConn.WriteJSON(map[string]interface{}{
		"type":      "capture_ack",
		"id":        captureID,
		"file_path": filePath,
	}); wErr != nil {
		debug.Error("proxy", "failed to send capture_ack: %v", wErr)
	}
}

// handleFrameActive records the content frame the page last reported as the
// active interaction target. MCP exec / audits default to it (always-wrap
// model).
func (c *wsConnState) handleFrameActive(msg *wsMessage) {
	c.ps.SetActiveFrame(msg.FrameID)
}

func (c *wsConnState) handleError(msg *wsMessage) {
	errEntry := FrontendError{
		ID:        c.id,
		Timestamp: c.timestamp,
		Message:   getStringField(msg.Data, "message"),
		Source:    getStringField(msg.Data, "source"),
		LineNo:    getIntField(msg.Data, "lineno"),
		ColNo:     getIntField(msg.Data, "colno"),
		Error:     getStringField(msg.Data, "error"),
		Stack:     getStringField(msg.Data, "stack"),
		URL:       msg.URL,
	}
	c.ps.logger.LogError(errEntry, msg.FrameID)
	c.ps.pageTracker.TrackError(errEntry, msg.SessionID, msg.FrameID)

	// Auto-forward browser errors to overlay for PTY injection. The
	// error is already in the traffic log (LogError above), so overlay
	// forwarding is supplementary — surface a failure to the debug log.
	//
	// Only for a trusted connection. An error frame's text is whatever the
	// sender says it is, and this forward types it into the agent's terminal.
	// An untrusted visitor's errors stay telemetry (logged above); no toast or
	// diagnostic for that, since the visitor did nothing wrong and a public
	// page would raise one per visitor.
	if c.trusted && c.ps.overlayNotifier.IsEnabled() {
		if err := c.ps.overlayNotifier.NotifyBrowserError(c.ps.ID, errEntry); err != nil {
			debug.Log("proxy", "failed to forward browser error to overlay: %v", err)
		}
	}
}

func (c *wsConnState) handlePerformance(msg *wsMessage) {
	metric := PerformanceMetric{
		ID:                   c.id,
		Timestamp:            c.timestamp,
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

	c.ps.logger.LogPerformance(metric, msg.FrameID)
	c.ps.pageTracker.TrackPerformance(metric, msg.SessionID, msg.FrameID)
}

func (c *wsConnState) handleCustomLog(msg *wsMessage) {
	c.ps.logger.LogCustom(CustomLog{
		ID:        c.id,
		Timestamp: c.timestamp,
		Level:     getStringField(msg.Data, "level"),
		Message:   getStringField(msg.Data, "message"),
		Data:      msg.Data,
		URL:       msg.URL,
	}, msg.FrameID)
}

func (c *wsConnState) handleScreenshot(msg *wsMessage) {
	// Save screenshot to temp file
	dataURL := getStringField(msg.Data, "data")
	name := getStringField(msg.Data, "name")
	if name == "" {
		name = fmt.Sprintf("screenshot-%d", c.timestamp.Unix())
	}

	selector := getStringField(msg.Data, "selector")
	if selector == "" {
		selector = "body"
	}

	filePath, err := c.ps.saveScreenshot(name, dataURL)
	if err != nil {
		// Log failed screenshot so it appears in proxylog
		c.ps.logger.LogScreenshot(Screenshot{
			ID:        c.id,
			Timestamp: c.timestamp,
			Name:      name,
			URL:       msg.URL,
			Width:     getIntField(msg.Data, "width"),
			Height:    getIntField(msg.Data, "height"),
			Format:    getStringField(msg.Data, "format"),
			Selector:  selector,
			Error:     err.Error(),
		})
		return
	}

	c.ps.logger.LogScreenshot(Screenshot{
		ID:        c.id,
		Timestamp: c.timestamp,
		Name:      name,
		FilePath:  filePath,
		URL:       msg.URL,
		Width:     getIntField(msg.Data, "width"),
		Height:    getIntField(msg.Data, "height"),
		Format:    getStringField(msg.Data, "format"),
		Selector:  selector,
	})
}

func (c *wsConnState) handleExecution(msg *wsMessage) {
	// Log JavaScript execution result
	execID := getStringField(msg.Data, "exec_id")
	duration := time.Duration(getInt64Field(msg.Data, "duration")) * time.Millisecond
	result := getStringField(msg.Data, "result")

	execResult := ExecutionResult{
		ID:        c.id,
		Timestamp: c.timestamp,
		Code:      execID, // Will be filled in by the tool
		Result:    result,
		Error:     getStringField(msg.Data, "error"),
		Duration:  duration,
		URL:       msg.URL,
		Data:      msg.Data,
	}

	// Save large results to file
	if filePath, err := c.ps.saveLargeResult(execID, result); err == nil && filePath != "" {
		execResult.FilePath = filePath
		// Replace result with summary for logging
		execResult.Result = fmt.Sprintf("[Large result saved to %s (%d bytes)]", filePath, len(result))
	}

	c.ps.logger.LogExecution(execResult)

	// Send result to waiting channel if one exists
	if ch, ok := c.ps.pendingExecs.LoadAndDelete(execID); ok {
		resultChan := ch.(chan *ExecutionResult)
		select {
		case resultChan <- &execResult:
			close(resultChan)
		default:
			close(resultChan)
		}
	}
}

// handleInteractions handles batched interaction events from the frontend.
func (c *wsConnState) handleInteractions(msg *wsMessage) {
	events := getArrayField(msg.Data, "events")
	for _, eventData := range events {
		if em, ok := eventData.(map[string]interface{}); ok {
			interaction := parseInteractionEvent(em, c.id, c.timestamp, msg.URL)
			c.ps.logger.LogInteraction(interaction, msg.FrameID)
			c.ps.pageTracker.TrackInteraction(interaction, msg.SessionID, msg.FrameID)
		}
	}
}

// handleMutations handles batched mutation events from the frontend.
func (c *wsConnState) handleMutations(msg *wsMessage) {
	events := getArrayField(msg.Data, "events")
	for _, eventData := range events {
		if em, ok := eventData.(map[string]interface{}); ok {
			mutation := parseMutationEvent(em, c.id, c.timestamp, msg.URL)
			c.ps.logger.LogMutation(mutation, msg.FrameID)
			c.ps.pageTracker.TrackMutation(mutation, msg.SessionID, msg.FrameID)
		}
	}
}

// handleSecretSubmit processes a secret-entry submission from the browser's
// masked input field.
//
// ZERO-LEAK INVARIANT (branch-before-emit): the submitted value is
// extracted here, handed to the daemon-wired secret sink, and
// dropped. It is deleted from msg.Data immediately so no later
// code path can see it, and it is NEVER placed into any event
// struct, traffic-log entry, overlay notification, or channel
// event — those carry only {name, fingerprint:last-4}. Do not
// rely on downstream sanitizers; nothing downstream ever holds
// the value.
func (c *wsConnState) handleSecretSubmit(msg *wsMessage) {
	name := getStringField(msg.Data, "name")
	value := getStringField(msg.Data, "value")
	delete(msg.Data, "value")

	// Refused before the sink, the fingerprint, or any event is built: an
	// untrusted caller must not be able to overwrite a project secret, nor
	// have its submission announced to the agent as a stored secret.
	if c.refusePrivileged("secret_submit") {
		value = ""
		if wErr := c.asyncConn.WriteJSON(map[string]interface{}{
			"type": "secret_ack", "name": name, "ok": false, "error": privilegedRefusalMessage,
		}); wErr != nil {
			debug.Log("proxy", "failed to send secret_ack refusal for %q: %v", name, wErr)
		}
		return
	}
	fingerprint := storepkg.Fingerprint(value)

	sinkErr := c.ps.DeliverSecret(name, value)
	value = ""

	// Ack the browser so the panel can confirm or retry.
	ack := map[string]interface{}{"type": "secret_ack", "name": name, "ok": sinkErr == nil}
	if sinkErr != nil {
		ack["error"] = sinkErr.Error()
	}
	if wErr := c.asyncConn.WriteJSON(ack); wErr != nil {
		debug.Log("proxy", "failed to send secret_ack for %q: %v", name, wErr)
	}

	// Emit the fingerprint-only panel message so the agent learns the
	// secret ARRIVED (by name + last-4), never what it is.
	status := "stored"
	if sinkErr != nil {
		status = "FAILED: " + sinkErr.Error()
	}
	pm := PanelMessage{
		ID:        c.id,
		Timestamp: c.timestamp,
		URL:       msg.URL,
		Message:   fmt.Sprintf("[secret-entry] %s (****%s) %s — reference it by name; the value is store-held and never enters the event stream", name, fingerprint, status),
	}
	c.ps.logger.LogPanelMessage(pm)
	if c.ps.overlayNotifier.IsEnabled() {
		if err := c.ps.overlayNotifier.NotifyPanelMessage(c.ps.ID, &pm); err != nil {
			debug.Log("proxy", "failed to notify overlay of secret-entry receipt: %v", err)
		}
	}
}

// handlePanelMessage handles a message from the floating indicator panel,
// resolving attachment file paths from the session capture store.
func (c *wsConnState) handlePanelMessage(msg *wsMessage) {
	if c.refuseAgentInput("panel_message") {
		return
	}
	panelMsg := parsePanelMessage(msg.Data, c.id, c.timestamp, msg.URL)

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
			if filePath, ok := c.captures[att.ID]; ok {
				delete(c.captures, att.ID)
				att.Data["file_path"] = filePath
				att.Data["file_name"] = filepath.Base(filePath)
				debug.Log("proxy", "Resolved screenshot: id=%s path=%s", att.ID, filePath)
			} else if owned, ok := c.ps.ownedCapturePath(att.FilePath); ok {
				// Fallback: browser echoed the filePath from capture_ack. It is
				// browser input, so it is accepted only when it names a capture
				// this proxy wrote (see ownedCapturePath).
				att.FilePath = owned
				att.Data["file_path"] = owned
				att.Data["file_name"] = filepath.Base(owned)
				debug.Log("proxy", "Resolved screenshot from JS filePath: id=%s path=%s", att.ID, owned)
			} else if att.FilePath != "" {
				c.dropForeignAttachmentPath(att)
			} else {
				debug.Error("proxy", "No capture for screenshot id=%s (binary frame missing or failed)", att.ID)
			}
		}

		if att.Type == "sketch" && att.ID != "" {
			if filePath, ok := c.captures[att.ID]; ok {
				delete(c.captures, att.ID)
				att.Data["file_path"] = filePath
				att.Data["file_name"] = filepath.Base(filePath)
				debug.Log("proxy", "Resolved sketch: id=%s path=%s", att.ID, filePath)
			}
		}

		if att.Type == "style-edit" {
			// Resolve before/after screenshot file paths from captures
			if screenshots, ok := att.Data["screenshots"].(map[string]interface{}); ok {
				resolved := make(map[string]interface{})
				for key, val := range screenshots {
					ctxID, _ := val.(string)
					if ctxID == "" {
						continue
					}
					if filePath, ok := c.captures[ctxID]; ok {
						delete(c.captures, ctxID)
						resolved[key] = filePath
						debug.Log("proxy", "Resolved style-edit %s screenshot: id=%s path=%s", key, ctxID, filePath)
					}
				}
				att.Data["screenshots"] = resolved
			}
		}
	}

	// Any path still on an attachment is browser input that no branch above
	// resolved (a non-screenshot attachment, or a screenshot whose capture was
	// found by id). The whole message is forwarded to the agent, so it gets
	// the same test.
	for i := range panelMsg.Attachments {
		att := &panelMsg.Attachments[i]
		if att.FilePath == "" {
			continue
		}
		if owned, ok := c.ps.ownedCapturePath(att.FilePath); ok {
			att.FilePath = owned
		} else {
			c.dropForeignAttachmentPath(att)
		}
	}

	c.ps.logger.LogPanelMessage(panelMsg)

	// Forward to overlay if configured
	if c.ps.overlayNotifier.IsEnabled() {
		debug.Log("proxy", "forwarding panel_message to overlay (proxy_id=%s, endpoint=%s)", c.ps.ID, c.ps.overlayNotifier.GetEndpoint())
		if err := c.ps.overlayNotifier.NotifyPanelMessage(c.ps.ID, &panelMsg); err != nil {
			debug.Log("proxy", "Failed to notify overlay of panel message: %v", err)
		}
	} else {
		debug.Log("proxy", "panel_message received but overlay notifier NOT enabled for proxy %s — message will not reach agent", c.ps.ID)
	}

	// Update audit folder summary after saving new files. Best-effort
	// housekeeping: a stale SUMMARY.md index does not lose any data.
	if err := UpdateAuditSummary(c.ps.Path); err != nil {
		debug.Log("proxy", "failed to update audit summary: %v", err)
	}
}

// dropForeignAttachmentPath clears a browser-supplied attachment path that
// does not name one of this proxy's own captures, and says so. The path would
// otherwise be handed to the agent as "the attached screenshot", letting any
// control-socket client point the agent at an arbitrary local file.
func (c *wsConnState) dropForeignAttachmentPath(att *PanelAttachment) {
	debug.Error("proxy", "dropped attachment path from panel message on proxy %s (id=%s): %q is not a capture this proxy wrote", c.ps.ID, att.ID, att.FilePath)
	c.ps.logger.LogDiagnostic(ProxyDiagnostic{
		Timestamp: time.Now(),
		Level:     DiagnosticWarning,
		Category:  "proxy",
		Event:     "attachment_path_refused",
		Message:   fmt.Sprintf("proxy %s dropped a browser-supplied attachment path: it does not name a capture this proxy wrote", c.ps.ID),
		Data:      map[string]any{"proxy_id": c.ps.ID, "attachment_id": att.ID, "attachment_type": att.Type},
	})
	att.FilePath = ""
}

// handleWalkthroughEvent handles a walkthrough (live-demo) lifecycle event
// from the chrome-frame panel. Lets the agent narrate live / react.
func (c *wsConnState) handleWalkthroughEvent(msg *wsMessage) {
	wt := WalkthroughEntry{
		ID:        c.id,
		Timestamp: c.timestamp,
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
	c.ps.logger.LogWalkthrough(wt)
}

// handleSketch handles a sketch/wireframe from sketch mode.
func (c *wsConnState) handleSketch(msg *wsMessage) {
	if c.refuseAgentInput("sketch") {
		return
	}
	sketchEntry := parseSketchEntry(msg.Data, c.id, c.timestamp, msg.URL)

	// Save sketch image to audit directory
	if sketchEntry.ImageData != "" {
		filePath, err := c.ps.saveScreenshot("sketch-"+c.id, sketchEntry.ImageData)
		if err == nil {
			sketchEntry.FilePath = filePath
		}
	}

	c.ps.logger.LogSketch(sketchEntry)

	// Forward to overlay if configured
	if c.ps.overlayNotifier.IsEnabled() {
		if err := c.ps.overlayNotifier.NotifySketch(c.ps.ID, &sketchEntry); err != nil {
			debug.Log("proxy", "failed to forward sketch to overlay: %v", err)
		}
	}

	// Update audit folder summary. Best-effort housekeeping: a stale
	// SUMMARY.md index does not lose any data.
	if err := UpdateAuditSummary(c.ps.Path); err != nil {
		debug.Log("proxy", "failed to update audit summary: %v", err)
	}
}

// handleElementCapture handles an element capture from the panel with a
// reference ID.
func (c *wsConnState) handleElementCapture(msg *wsMessage) {
	capture := parseElementCapture(msg.Data, c.timestamp, msg.URL)
	c.ps.logger.LogElementCapture(capture)
}

// handleSketchCapture handles a sketch capture from the panel with a
// reference ID.
func (c *wsConnState) handleSketchCapture(msg *wsMessage) {
	capture := parseSketchCapture(msg.Data, c.timestamp, msg.URL)

	// Save sketch image to file if present
	if capture.ImageData != "" {
		filePath, err := c.ps.saveScreenshot("sketch-"+capture.ID, capture.ImageData)
		if err == nil {
			capture.FilePath = filePath
			c.captures[capture.ID] = filePath
		}
		capture.ImageData = ""
	}

	c.ps.logger.LogSketchCapture(capture)
}

// handleDesignState handles design state when an element is selected for
// iteration.
func (c *wsConnState) handleDesignState(msg *wsMessage) {
	if c.refuseAgentInput("design_state") {
		return
	}
	designState := parseDesignState(msg.Data, c.id, c.timestamp, msg.URL)
	// Persist the whole-page thumbnail (if any) as an on-disk JPEG, same
	// treatment as the segment screenshot.
	if thumb := getStringField(msg.Data, "pageThumb"); strings.HasPrefix(thumb, "data:") {
		if p, err := c.ps.saveScreenshot("design-page-"+designState.Selector, thumb); err == nil {
			designState.PageThumbPath = p
		}
	}
	c.ps.logger.LogDesignState(designState)

	// Forward to overlay if configured
	if c.ps.overlayNotifier.IsEnabled() {
		if err := c.ps.overlayNotifier.NotifyDesignState(c.ps.ID, &designState); err != nil {
			debug.Log("proxy", "failed to forward design state to overlay: %v", err)
		}
	}
}

// handleDesignRequest handles a request for new design alternatives.
func (c *wsConnState) handleDesignRequest(msg *wsMessage) {
	if c.refuseAgentInput("design_request") {
		return
	}
	designRequest := parseDesignRequest(msg.Data, c.id, c.timestamp, msg.URL)
	// Persist the live-segment screenshot (if any) so the agent message can
	// reference an on-disk PNG instead of a giant inline data URL.
	if shot := getStringField(msg.Data, "screenshot"); strings.HasPrefix(shot, "data:") {
		if p, err := c.ps.saveScreenshot("design-"+designRequest.Selector, shot); err == nil {
			designRequest.ScreenshotPath = p
		}
	}
	c.ps.logger.LogDesignRequest(designRequest)

	// Forward to overlay if configured
	if c.ps.overlayNotifier.IsEnabled() {
		if err := c.ps.overlayNotifier.NotifyDesignRequest(c.ps.ID, &designRequest); err != nil {
			debug.Log("proxy", "failed to forward design request to overlay: %v", err)
		}
	}
}

// handleDesignChat handles a chat message about a selected element.
func (c *wsConnState) handleDesignChat(msg *wsMessage) {
	if c.refuseAgentInput("design_chat") {
		return
	}
	designChat := parseDesignChat(msg.Data, c.id, c.timestamp, msg.URL)
	if shot := getStringField(msg.Data, "screenshot"); strings.HasPrefix(shot, "data:") {
		if p, err := c.ps.saveScreenshot("design-"+designChat.Selector, shot); err == nil {
			designChat.ScreenshotPath = p
		}
	}
	c.ps.logger.LogDesignChat(designChat)

	// Forward to overlay if configured
	if c.ps.overlayNotifier.IsEnabled() {
		if err := c.ps.overlayNotifier.NotifyDesignChat(c.ps.ID, &designChat); err != nil {
			debug.Log("proxy", "failed to forward design chat to overlay: %v", err)
		}
	}
}

// handleDesignEdit handles a committed direct-manipulation geometry edit.
func (c *wsConnState) handleDesignEdit(msg *wsMessage) {
	if c.refuseAgentInput("design_edit") {
		return
	}
	designEdit := parseDesignEdit(msg.Data, c.id, c.timestamp, msg.URL)
	c.ps.logger.LogDesignEdit(designEdit)

	// Forward to overlay if configured
	if c.ps.overlayNotifier.IsEnabled() {
		if err := c.ps.overlayNotifier.NotifyDesignEdit(c.ps.ID, &designEdit); err != nil {
			debug.Log("proxy", "failed to forward design edit to overlay: %v", err)
		}
	}
}

// handleResponsiveRequest handles a handoff from responsive mode requesting
// agent fixes.
func (c *wsConnState) handleResponsiveRequest(msg *wsMessage) {
	if c.refuseAgentInput("responsive_request") {
		return
	}
	responsiveRequest := parseResponsiveRequest(msg.Data, c.id, c.timestamp, msg.URL)
	c.ps.logger.LogResponsiveRequest(responsiveRequest)

	// Forward to overlay if configured
	if c.ps.overlayNotifier.IsEnabled() {
		if err := c.ps.overlayNotifier.NotifyResponsiveRequest(c.ps.ID, &responsiveRequest); err != nil {
			debug.Log("proxy", "failed to forward responsive request to overlay: %v", err)
		}
	}
}

// handleResponsiveState handles responsive mode panel state (current width +
// shift count).
func (c *wsConnState) handleResponsiveState(msg *wsMessage) {
	if c.refuseAgentInput("responsive_state") {
		return
	}
	responsiveState := parseResponsiveState(msg.Data, c.id, c.timestamp, msg.URL)
	c.ps.logger.LogResponsiveState(responsiveState)

	// Forward to overlay if configured
	if c.ps.overlayNotifier.IsEnabled() {
		if err := c.ps.overlayNotifier.NotifyResponsiveState(c.ps.ID, &responsiveState); err != nil {
			debug.Log("proxy", "failed to forward responsive state to overlay: %v", err)
		}
	}
}

// handleSessionRequestMsg handles session API requests from the browser.
// Writes go through the serialising asyncConn — this runs in its own
// goroutine concurrent with the read loop and the broadcast drain.
//
// Refused to an untrusted caller: a session request can type into, or
// schedule text for, an agent's terminal.
func (c *wsConnState) handleSessionRequestMsg(msg *wsMessage) {
	if c.refusePrivileged("session_request") {
		writeControlResponse(c.asyncConn, "session_response", getStringField(msg.Data, "request_id"), nil, privilegedRefusalMessage)
		return
	}
	c.spawnHandler(func() { c.ps.handleSessionRequest(c.asyncConn, msg.Data) })
}

// handleStoreRequestMsg handles store API requests from the browser
// (serialised via asyncConn).
//
// Refused to an untrusted caller: a store request reads and writes the
// project's persistent store.
func (c *wsConnState) handleStoreRequestMsg(msg *wsMessage) {
	if c.refusePrivileged("store_request") {
		writeControlResponse(c.asyncConn, "store_response", getStringField(msg.Data, "request_id"), nil, privilegedRefusalMessage)
		return
	}
	c.spawnHandler(func() { c.ps.handleStoreRequest(c.asyncConn, msg.Data) })
}

// handleChaosRequestMsg handles chaos control requests from the indicator
// panel (serialised via asyncConn). Acts directly on this proxy's chaos
// engine — no daemon round trip.
func (c *wsConnState) handleChaosRequestMsg(msg *wsMessage) {
	c.spawnHandler(func() { c.ps.handleChaosRequest(c.asyncConn, msg.Data) })
}

// handleVoiceStart starts a voice transcription session.
func (c *wsConnState) handleVoiceStart(msg *wsMessage) {
	// A voice session is opened on the operator's DEEPGRAM_API_KEY, so it is
	// refused to a caller that is not theirs to spend it.
	if c.refusePrivileged("voice_start") {
		if wErr := c.asyncConn.WriteJSON(map[string]interface{}{
			"type":  "voice_error",
			"error": privilegedRefusalMessage,
		}); wErr != nil {
			debug.Error("proxy", "voice_start: failed to send refusal to client: %v", wErr)
		}
		return
	}

	config := DefaultDeepgramConfig()

	// Apply any config from message
	if lang := getStringField(msg.Data, "language"); lang != "" {
		config.Language = lang
	}
	if model := getStringField(msg.Data, "model"); model != "" {
		config.Model = model
	}

	// id/timestamp are per-message state the read loop overwrites; copy them
	// for the handler goroutine.
	id, timestamp, pageURL := c.id, c.timestamp, msg.URL

	// The browser starts streaming audio as soon as it has sent voice_start,
	// so mark the dial pending before returning to the read loop: binary
	// frames that arrive meanwhile are audio and are held for the session
	// (handleBinaryFrame), not mistaken for screenshot frames.
	c.voiceMu.Lock()
	c.voicePending++
	c.voiceMu.Unlock()

	// The Deepgram dial runs off the read loop: it is a network handshake, and
	// on the read loop a slow or black-holed Deepgram stalled every other
	// frame on this connection. The connection's teardown waits for handler
	// goroutines before it reaps voice sessions, so a session stored here is
	// always closed.
	c.spawnHandler(func() {
		session, err := newVoiceSession(c.connID, c.asyncConn, config)

		// Settle the pending dial under voiceMu, so the held audio reaches the
		// new session before any frame the read loop takes next.
		c.voiceMu.Lock()
		c.voicePending--
		backlog := c.voiceBacklog
		if c.voicePending == 0 {
			c.voiceBacklog = nil
		}
		if err == nil {
			// A second voice_start replaces the session: close the one it
			// replaces, or its Deepgram socket, reader and keepalive live on
			// with nothing left that could reach them.
			if prev, loaded := c.ps.voiceSessions.Swap(c.connID, session); loaded {
				prev.(*VoiceSession).Close()
			}
			if c.voicePending == 0 {
				for _, frame := range backlog {
					if sErr := session.SendAudio(frame); sErr != nil {
						break
					}
				}
			}
		}
		c.voiceMu.Unlock()

		if err != nil {
			if wErr := c.asyncConn.WriteJSON(map[string]interface{}{
				"type":  "voice_error",
				"error": err.Error(),
			}); wErr != nil {
				debug.Error("proxy", "voice_start: failed to send error to client: %v", wErr)
			}
			return
		}

		// Log voice start
		c.ps.logger.LogCustom(CustomLog{
			ID:        id,
			Timestamp: timestamp,
			Level:     "info",
			Message:   "[Voice] Transcription session started",
			Data:      map[string]interface{}{"model": config.Model, "language": config.Language},
			URL:       pageURL,
		})
	})
}

// newVoiceSession is the voice session constructor; a variable so tests can
// exercise voice_start without a Deepgram account.
var newVoiceSession = NewVoiceSession

// handleVoiceStop stops the voice transcription session.
func (c *wsConnState) handleVoiceStop(msg *wsMessage) {
	session, ok := c.ps.voiceSessions.LoadAndDelete(c.connID)
	if !ok {
		return
	}
	session.(*VoiceSession).Close()

	if wErr := c.asyncConn.WriteJSON(map[string]interface{}{
		"type":    "voice_stopped",
		"message": "Transcription session ended",
	}); wErr != nil {
		debug.Error("proxy", "voice_stop: failed to send response to client: %v", wErr)
	}

	// Log voice stop
	c.ps.logger.LogCustom(CustomLog{
		ID:        c.id,
		Timestamp: c.timestamp,
		Level:     "info",
		Message:   "[Voice] Transcription session stopped",
		URL:       msg.URL,
	})
}
