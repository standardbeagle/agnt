package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/proxy"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleSession(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("SESSION").dispatch(ctx, conn, cmd, map[string]handlerFn{
		"REGISTER":   noCtx(d.hubHandleSessionRegister),
		"UNREGISTER": noCtx(d.hubHandleSessionUnregister),
		"HEARTBEAT":  noCtx(d.hubHandleSessionHeartbeat),
		"LIST":       noCtx(d.hubHandleSessionList),
		"GET":        noCtx(d.hubHandleSessionGet),
		"SEND":       noCtx(d.hubHandleSessionSend),
		"SCHEDULE":   noCtx(d.hubHandleSessionSchedule),
		"CANCEL":     noCtx(d.hubHandleSessionCancel),
		"TASKS":      noCtx(d.hubHandleSessionTasks),
		"FIND":       noCtx(d.hubHandleSessionFind),
		"ATTACH":     noCtx(d.hubHandleSessionAttach),
		"URL":        noCtx(d.hubHandleSessionURL),
	})
}

// sessionRegisterMetadata mirrors the JSON metadata payload clients send on
// SESSION REGISTER. Stored as its own type so parseSessionRegisterArgs can
// return both the unnormalized original project path (which
// RunAutostartAsync needs) and the fully-built Session struct.
type sessionRegisterMetadata struct {
	ProjectPath      string   `json:"project_path"`
	Command          string   `json:"command"`
	Args             []string `json:"args"`
	SessionPGID      int      `json:"session_pgid,omitempty"`
	SessionJobHandle uint64   `json:"session_job_handle,omitempty"`
}

// parseSessionRegisterArgs validates the SESSION REGISTER command and
// constructs a Session from its Args/Data payload. Returns the session plus
// the raw (unnormalized) metadata — callers that need the pre-normalization
// project path for downstream calls use the metadata.ProjectPath field.
func parseSessionRegisterArgs(cmd *hubproto.Command) (*Session, sessionRegisterMetadata, error) {
	var metadata sessionRegisterMetadata
	if len(cmd.Args) < 2 {
		return nil, metadata, fmt.Errorf("SESSION REGISTER requires: <code> <overlay_path>")
	}
	code := cmd.Args[0]
	overlayPath := cmd.Args[1]

	metadata, _ = unmarshalCommand[sessionRegisterMetadata](cmd)

	now := time.Now()
	session := &Session{
		Code:             code,
		OverlayPath:      overlayPath,
		ProjectPath:      normalizePath(metadata.ProjectPath),
		Command:          metadata.Command,
		Args:             metadata.Args,
		StartedAt:        now,
		Status:           SessionStatusActive,
		LastSeen:         now,
		SessionPGID:      metadata.SessionPGID,
		SessionJobHandle: metadata.SessionJobHandle,
	}
	return session, metadata, nil
}

// joinOrStartAutostart decides whether this session should start a new
// autostart run for the project or join an existing one as an observer.
// Returns the project's autostart handle (nil for late joiners on an
// untracked project) and the join status string reported back to the client.
//
// rawProjectPath is the pre-normalization metadata path that
// RunAutostartAsync still normalizes on its own for legacy-caller parity.
func (d *Daemon) joinOrStartAutostart(session *Session, rawProjectPath string) (*AutostartHandle, string) {
	existingSessions := d.sessionRegistry.ListActive(session.ProjectPath, false)
	hasExistingOwner := false
	for _, existing := range existingSessions {
		if existing.Code != session.Code {
			hasExistingOwner = true
			break
		}
	}

	if hasExistingOwner {
		// Another session already started scripts for this project. Skip
		// GetOrCreate entirely. If an autostart handle is still tracked for
		// this project we join as an observer on it; otherwise this is a
		// pure observer of already-running state.
		var handle *AutostartHandle
		if d.autostartManager != nil {
			handle = d.autostartManager.Get(session.ProjectPath)
		}
		debug.Log("daemon", "session %s joining existing project %s (observer)",
			session.Code, session.ProjectPath)
		return handle, "joined"
	}

	// First session for this project — start (or join) a project-scoped
	// autostart run. GetOrCreate guarantees at-most-one startFn invocation
	// per project path, so two concurrent registrations for the same
	// project cannot both run autostart.
	startFn := d.makeAutostartStartFn(session.ProjectPath, rawProjectPath)
	handle := d.autostartManager.GetOrCreate(session.ProjectPath, startFn)
	return handle, "starting"
}

// buildSessionRegisterResponse assembles the SESSION REGISTER response map.
// Never blocks on handle.Done() — if the run is already finished by the time
// we get here (empty config, cache hit, late joiner on a completed run), the
// full AutostartResult is folded in so clients get synchronous semantics
// when available. Otherwise the response carries only the join status and an
// empty backward-compat result.
func buildSessionRegisterResponse(
	d *Daemon,
	code, projectPath string,
	handle *AutostartHandle,
	joinStatus string,
) map[string]interface{} {
	resp := map[string]interface{}{"code": code}

	if handle == nil {
		// No autostart handle at all (late joiner on an untracked project).
		resp["status"] = joinStatus
		resp["autostart"] = &AutostartResult{}
		resp["progress"] = []map[string]interface{}{}
		return resp
	}

	resp["autostart_handle"] = handle.ProjectPath()
	resp["progress"] = progressProtoEvents(handle.Progress())

	select {
	case <-handle.Done():
		// Run finished — publish the full result AND a "done" status.
		result := handle.Result()
		if result == nil {
			result = &AutostartResult{}
		}
		emitAutostartErrorsToAlertStore(d, projectPath, result)
		resp["status"] = "done"
		resp["result"] = result
		resp["autostart"] = result // backward compat for existing clients
	default:
		// Run still in flight. Report the join status and emit an empty
		// backward-compat autostart map so clients that blindly read
		// result["autostart"] don't crash.
		resp["status"] = joinStatus
		resp["autostart"] = &AutostartResult{}
	}
	return resp
}

// hubHandleSessionRegister handles SESSION REGISTER command.
// SESSION REGISTER <code> <overlay_path> -- <json_metadata>
//
// The handler returns immediately after kicking off (or joining) the
// project's autostart run. PTY registration must not block on autostart.
func (d *Daemon) hubHandleSessionRegister(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	session, metadata, err := parseSessionRegisterArgs(cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	if err := d.sessionRegistry.Register(session); err != nil {
		// Session already exists — this is a re-registration (reconnect).
		// Cancel any pending cleanup from the old connection and update LastSeen.
		// Refresh SessionPGID and SessionJobHandle in case the client
		// restarted with a new leader / rebuilt its job object.
		d.cancelPendingCleanup(session.Code)
		if existing, ok := d.sessionRegistry.Get(session.Code); ok {
			existing.UpdateLastSeen()
			if session.SessionPGID > 0 || session.SessionJobHandle != 0 {
				existing.mu.Lock()
				if session.SessionPGID > 0 {
					existing.SessionPGID = session.SessionPGID
				}
				if session.SessionJobHandle != 0 {
					existing.SessionJobHandle = session.SessionJobHandle
				}
				existing.mu.Unlock()
			}
		}
	}

	d.logSessionTargetStarting(session)
	conn.SetSessionCode(session.Code)

	// Rebind overlay endpoints for existing proxies that may have been
	// created before this session registered.
	d.rebindProxyOverlays(session)

	// Reconcile script states against OS truth before deciding autostart.
	// Detects processes that died between sessions and transitions them to
	// Stopped so the agent receives verified-accurate state.
	if reconciled := d.reconcileScriptStates(session.ProjectPath); len(reconciled) > 0 {
		names := make([]string, len(reconciled))
		for i, e := range reconciled {
			names[i] = e.Name
		}
		debug.Log("daemon", "session %s: reconciled %d dead scripts: %v",
			session.Code, len(reconciled), names)
	}

	handle, joinStatus := d.joinOrStartAutostart(session, metadata.ProjectPath)
	d.claimProjectScripts(session)

	resp := buildSessionRegisterResponse(d, session.Code, session.ProjectPath, handle, joinStatus)
	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// logSessionTargetStarting writes a "target_starting" entry to the startup
// log so the overlay shows the PTY target command as soon as the session
// registers. No-op when the session has no command metadata.
func (d *Daemon) logSessionTargetStarting(session *Session) {
	if session.Command == "" {
		return
	}
	cmdLabel := session.Command
	if len(session.Args) > 0 {
		cmdLabel += " " + strings.Join(session.Args, " ")
	}
	d.startupErrorStore.Add(&StartupLogEntry{
		Level:     "info",
		EventType: "target_starting",
		Message:   fmt.Sprintf("starting %s", cmdLabel),
		Timestamp: time.Now(),
	})
}

// claimProjectScripts adds the session as an observer of all scripts for its
// project and claims ownership of any that are currently unowned. Script
// entries created by autostart may not exist yet if the run is still in its
// early phase — that's fine, subsequent SCRIPT LIST queries will pick them up.
func (d *Daemon) claimProjectScripts(session *Session) {
	if session.ProjectPath == "" {
		return
	}
	for _, entry := range d.scriptRegistry.List(session.ProjectPath) {
		entry.AddSession(session.Code)
		if entry.Owner() == "" {
			entry.SetOwner(session.Code)
		}
	}
}

// makeAutostartStartFn returns an AutostartStartFunc that runs the duplicate
// scan then invokes RunAutostartAsync. The function captures both the
// normalized project path (used for scans and as the handle key) and the
// original (unnormalized) metadata path, which RunAutostartAsync needs so
// that its own normalization stays consistent with legacy callers.
func (d *Daemon) makeAutostartStartFn(normalizedPath, metadataPath string) AutostartStartFunc {
	return func(ctx context.Context, progress chan<- AutostartProgress) *AutostartResult {
		// Duplicate scan runs synchronously BEFORE autostart so that orphaned
		// processes are killed and ports freed before new ones start.
		if d.dupScanner != nil && normalizedPath != "" {
			result := d.dupScanner.ScanForProject(normalizedPath)
			if len(result.Killed) > 0 {
				d.dupScanner.notify(result)
			}
		}

		// Run autostart, writing progress directly to the manager's channel.
		// The AutostartManager broadcast callback (configured at construction
		// time on the daemon) fans these events out to the alert hub for
		// monitor/MCP watch subscribers — no manual tee required here.
		result := d.RunAutostartAsync(ctx, metadataPath, progress)

		emitAutostartErrorsToAlertStore(d, normalizedPath, result)
		return result
	}
}

// broadcastAutostartProgress forwards an autostart progress event onto the
// alert hub as a diagnostic-style log entry so stream subscribers see it.
// Silently no-ops when the hub is unavailable.
func (d *Daemon) broadcastAutostartProgress(projectPath string, ev AutostartProgress) {
	if d.alertHub == nil {
		return
	}
	msg := fmt.Sprintf("autostart phase=%s script=%s layer=%d", phaseName(ev.Phase), ev.Script, ev.Layer)
	if ev.Err != nil {
		msg += " err=" + ev.Err.Error()
	}
	entry := proxy.LogEntry{
		Type: proxy.LogTypeDiagnostic,
		Diagnostic: &proxy.ProxyDiagnostic{
			Level:     diagnosticLevelForPhase(ev.Phase),
			Category:  "autostart",
			Event:     phaseName(ev.Phase),
			Message:   msg,
			Timestamp: ev.Timestamp,
		},
	}
	d.alertHub.BroadcastLogEntry(entry, projectPath)
}

// diagnosticLevelForPhase maps an autostart phase to a diagnostic level used
// by stream subscribers to filter events.
func diagnosticLevelForPhase(p AutostartPhase) proxy.ProxyDiagnosticLevel {
	if p == PhaseScriptFailed {
		return proxy.DiagnosticError
	}
	return proxy.DiagnosticInfo
}

// progressProtoEvents converts a slice of AutostartProgress into the
// wire-format progress payload returned by SESSION REGISTER.
func progressProtoEvents(events []AutostartProgress) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(events))
	for _, ev := range events {
		entry := map[string]interface{}{
			"phase": phaseName(ev.Phase),
			"layer": ev.Layer,
		}
		if ev.Script != "" {
			entry["script"] = ev.Script
		}
		if ev.Dependency != "" {
			entry["dependency"] = ev.Dependency
		}
		if !ev.Timestamp.IsZero() {
			entry["timestamp"] = ev.Timestamp.Format(time.RFC3339Nano)
		}
		if ev.Err != nil {
			entry["error"] = ev.Err.Error()
		}
		out = append(out, entry)
	}
	return out
}

// emitAutostartErrorsToAlertStore pushes autostart errors into the
// agent-facing alert store so `get_errors` surfaces them immediately.
// Idempotent for the caller: safe to invoke multiple times if errors change.
func emitAutostartErrorsToAlertStore(d *Daemon, projectPath string, result *AutostartResult) {
	if result == nil || len(result.Errors) == 0 || d.alertStore == nil {
		return
	}
	for _, errMsg := range result.Errors {
		d.alertStore.Add(&AlertEntry{
			Severity:    "error",
			Category:    "autostart",
			Description: errMsg,
			ScriptID:    projectPath,
			Timestamp:   time.Now(),
		})
	}
}

// hubHandleSessionUnregister handles SESSION UNREGISTER command.
// SESSION UNREGISTER <code>

func (d *Daemon) hubHandleSessionUnregister(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION UNREGISTER requires: <code>")
	}

	code := cmd.Args[0]

	// Clean up session resources (processes, proxies) before unregistering
	d.CleanupSessionResources(code)

	return conn.WriteOK(fmt.Sprintf("session %s unregistered", code))
}

// hubHandleSessionHeartbeat handles SESSION HEARTBEAT command.
// SESSION HEARTBEAT <code>

func (d *Daemon) hubHandleSessionHeartbeat(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION HEARTBEAT requires: <code>")
	}

	code := cmd.Args[0]

	if err := d.sessionRegistry.Heartbeat(code); err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	return conn.WriteOK("heartbeat received")
}

// hubHandleSessionList handles SESSION LIST command.
// SESSION LIST [-- <directory_filter_json>]

func (d *Daemon) hubHandleSessionList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	filter, _ := unmarshalCommand[struct {
		Directory string `json:"directory"`
		Global    bool   `json:"global"`
	}](cmd)

	sessions := d.sessionRegistry.List(normalizePath(filter.Directory), filter.Global)

	// Convert to response format
	sessionList := make([]map[string]interface{}, 0, len(sessions))
	for _, s := range sessions {
		sessionList = append(sessionList, s.ToJSON())
	}

	resp := map[string]interface{}{
		"sessions":  sessionList,
		"count":     len(sessionList),
		"directory": filter.Directory,
		"global":    filter.Global,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionGet handles SESSION GET command.
// SESSION GET <code>

func (d *Daemon) hubHandleSessionGet(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION GET requires: <code>")
	}

	code := cmd.Args[0]

	session, ok := d.sessionRegistry.Get(code)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session %q not found", code))
	}

	data, _ := json.Marshal(session.ToJSON())
	return conn.WriteJSON(data)
}

// hubHandleSessionSend handles SESSION SEND command.
// SESSION SEND <code> -- <message>

func (d *Daemon) hubHandleSessionSend(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION SEND requires: <code>")
	}
	if len(cmd.Data) == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION SEND requires message data")
	}

	code := cmd.Args[0]
	message := string(cmd.Data)

	// Get session
	session, ok := d.sessionRegistry.Get(code)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session %q not found", code))
	}

	if session.GetStatus() != SessionStatusActive {
		return conn.WriteErr(hubproto.ErrInvalidState, fmt.Sprintf("session %q is not active", code))
	}

	// Send message to overlay
	if err := d.sendMessageToOverlay(session.OverlayPath, message); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to send message: %v", err))
	}

	resp := map[string]interface{}{
		"success":      true,
		"session_code": code,
		"message_len":  len(message),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionSchedule handles SESSION SCHEDULE command.
// SESSION SCHEDULE <code> <duration> -- <message>

func (d *Daemon) hubHandleSessionSchedule(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 2 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION SCHEDULE requires: <code> <duration>")
	}
	if len(cmd.Data) == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION SCHEDULE requires message data")
	}

	code := cmd.Args[0]
	durationStr := cmd.Args[1]
	message := string(cmd.Data)

	// Parse duration
	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid duration %q: %v", durationStr, err))
	}

	// Get session to determine project path
	session, ok := d.sessionRegistry.Get(code)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session %q not found", code))
	}

	// Schedule the task
	task, err := d.scheduler.Schedule(code, duration, message, session.ProjectPath)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to schedule: %v", err))
	}

	resp := map[string]interface{}{
		"task_id":      task.ID,
		"session_code": code,
		"deliver_at":   task.DeliverAt.Format(time.RFC3339),
		"message_len":  len(message),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionCancel handles SESSION CANCEL command.
// SESSION CANCEL <task_id>

func (d *Daemon) hubHandleSessionCancel(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION CANCEL requires: <task_id>")
	}

	taskID := cmd.Args[0]

	if err := d.scheduler.Cancel(taskID); err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	return conn.WriteOK(fmt.Sprintf("task %s cancelled", taskID))
}

// hubHandleSessionTasks handles SESSION TASKS command.
// SESSION TASKS [-- <directory_filter_json>]

func (d *Daemon) hubHandleSessionTasks(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	filter, _ := unmarshalCommand[struct {
		Directory string `json:"directory"`
		Global    bool   `json:"global"`
	}](cmd)

	tasks := d.scheduler.ListTasks(normalizePath(filter.Directory), filter.Global)

	// Convert to response format
	taskList := make([]map[string]interface{}, 0, len(tasks))
	for _, t := range tasks {
		taskList = append(taskList, t.ToJSON())
	}

	resp := map[string]interface{}{
		"tasks":     taskList,
		"count":     len(taskList),
		"directory": filter.Directory,
		"global":    filter.Global,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionFind handles SESSION FIND command.
// SESSION FIND <directory>

func (d *Daemon) hubHandleSessionFind(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION FIND requires: <directory>")
	}

	directory := cmd.Args[0]

	session, found := d.sessionRegistry.FindByDirectory(directory)
	if !found {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("no active session found for directory %q or its parents", directory))
	}

	data, _ := json.Marshal(session.ToJSON())
	return conn.WriteJSON(data)
}

// hubHandleSessionAttach handles SESSION ATTACH command.
// SESSION ATTACH <directory>

func (d *Daemon) hubHandleSessionAttach(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION ATTACH requires: <directory>")
	}

	directory := cmd.Args[0]

	session, found := d.sessionRegistry.FindByDirectory(directory)
	if !found {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("no active session found for directory %q or its parents", directory))
	}

	// Associate this connection with the session
	conn.SetSessionCode(session.Code)

	resp := map[string]interface{}{
		"attached":     true,
		"session_code": session.Code,
		"project_path": session.ProjectPath,
		"command":      session.Command,
		"started_at":   session.StartedAt.Format(time.RFC3339),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionURL handles SESSION URL command.
// Reports a detected URL from an agnt run session, triggering proxy creation.
// SESSION URL <code> <url> -- {"script": "dev"}

func (d *Daemon) hubHandleSessionURL(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 2 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION URL requires: <code> <url>")
	}

	code := cmd.Args[0]
	detectedURL := cmd.Args[1]

	// Get session
	session, ok := d.sessionRegistry.Get(code)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session %q not found", code))
	}

	// Parse script name from data payload (default to "dev")
	scriptName := "dev"
	if data, err := unmarshalCommand[struct {
		Script string `json:"script"`
	}](cmd); err == nil && data.Script != "" {
		scriptName = data.Script
	}

	// Construct scriptID in the format: {basename}:{scriptName}
	scriptID := makeProcessID(session.ProjectPath, scriptName)

	// Send proxy event to trigger proxy creation
	select {
	case d.proxyEvents <- ProxyEvent{
		Type:     URLDetected,
		ScriptID: scriptID,
		URL:      detectedURL,
		Path:     session.ProjectPath,
	}:
		// Event queued successfully
	default:
		return conn.WriteErr(hubproto.ErrInternal, "proxy event queue full")
	}

	resp := map[string]interface{}{
		"success":      true,
		"session_code": code,
		"url":          detectedURL,
		"script":       scriptName,
		"script_id":    scriptID,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// sendMessageToOverlay sends a message to an overlay socket.

func (d *Daemon) sendMessageToOverlay(socketPath string, message string) error {
	// Create HTTP client that connects via Unix socket
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
	}

	// Build request body
	body := bytes.NewBufferString(message)

	// POST to /inject endpoint
	req, err := http.NewRequest("POST", "http://unix/inject", body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send to overlay: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("overlay returned status %d", resp.StatusCode)
	}

	return nil
}

// hubHandleStore handles the STORE command and its sub-verbs.
