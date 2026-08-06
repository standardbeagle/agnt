package proxy

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

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
}

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
	if c.ps.overlayNotifier.IsEnabled() {
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

	// Register screenshot by name so proxy exec can look up the file path
	c.ps.registerCapture(name, filePath)

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
func (c *wsConnState) handleSessionRequestMsg(msg *wsMessage) {
	c.spawnHandler(func() { c.ps.handleSessionRequest(c.asyncConn, msg.Data) })
}

// handleStoreRequestMsg handles store API requests from the browser
// (serialised via asyncConn).
func (c *wsConnState) handleStoreRequestMsg(msg *wsMessage) {
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
	config := DefaultDeepgramConfig()

	// Apply any config from message
	if lang := getStringField(msg.Data, "language"); lang != "" {
		config.Language = lang
	}
	if model := getStringField(msg.Data, "model"); model != "" {
		config.Model = model
	}

	session, err := NewVoiceSession(c.connID, c.asyncConn, config)
	if err != nil {
		if wErr := c.asyncConn.WriteJSON(map[string]interface{}{
			"type":  "voice_error",
			"error": err.Error(),
		}); wErr != nil {
			debug.Error("proxy", "voice_start: failed to send error to client: %v", wErr)
		}
		return
	}

	c.ps.voiceSessions.Store(c.connID, session)

	// Log voice start
	c.ps.logger.LogCustom(CustomLog{
		ID:        c.id,
		Timestamp: c.timestamp,
		Level:     "info",
		Message:   "[Voice] Transcription session started",
		Data:      map[string]interface{}{"model": config.Model, "language": config.Language},
		URL:       msg.URL,
	})
}

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
