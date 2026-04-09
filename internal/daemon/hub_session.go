package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
	"github.com/standardbeagle/go-cli-server/script"
)

func (d *Daemon) hubHandleSession(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "SESSION %s: args=%v", cmd.SubVerb, cmd.Args)
	switch cmd.SubVerb {
	case "REGISTER":
		return d.hubHandleSessionRegister(conn, cmd)
	case "UNREGISTER":
		return d.hubHandleSessionUnregister(conn, cmd)
	case "HEARTBEAT":
		return d.hubHandleSessionHeartbeat(conn, cmd)
	case "LIST":
		return d.hubHandleSessionList(conn, cmd)
	case "GET":
		return d.hubHandleSessionGet(conn, cmd)
	case "SEND":
		return d.hubHandleSessionSend(conn, cmd)
	case "SCHEDULE":
		return d.hubHandleSessionSchedule(conn, cmd)
	case "CANCEL":
		return d.hubHandleSessionCancel(conn, cmd)
	case "TASKS":
		return d.hubHandleSessionTasks(conn, cmd)
	case "FIND":
		return d.hubHandleSessionFind(conn, cmd)
	case "ATTACH":
		return d.hubHandleSessionAttach(conn, cmd)
	case "URL":
		return d.hubHandleSessionURL(conn, cmd)
	default:
		return conn.WriteStructuredErr(&hubproto.StructuredError{
			Code:         hubproto.ErrInvalidArgs,
			Message:      "unknown SESSION sub-command",
			Command:      "SESSION",
			ValidActions: []string{"REGISTER", "UNREGISTER", "HEARTBEAT", "LIST", "GET", "SEND", "SCHEDULE", "CANCEL", "TASKS", "FIND", "ATTACH", "URL"},
		})
	}
}

// hubHandleSessionRegister handles SESSION REGISTER command.
// SESSION REGISTER <code> <overlay_path> -- <json_metadata>

func (d *Daemon) hubHandleSessionRegister(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 2 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION REGISTER requires: <code> <overlay_path>")
	}

	code := cmd.Args[0]
	overlayPath := cmd.Args[1]

	// Parse optional metadata from data payload
	var metadata struct {
		ProjectPath string   `json:"project_path"`
		Command     string   `json:"command"`
		Args        []string `json:"args"`
	}
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &metadata)
	}

	// Create session
	session := &Session{
		Code:        code,
		OverlayPath: overlayPath,
		ProjectPath: normalizePath(metadata.ProjectPath),
		Command:     metadata.Command,
		Args:        metadata.Args,
		StartedAt:   time.Now(),
		Status:      SessionStatusActive,
		LastSeen:    time.Now(),
	}

	if err := d.sessionRegistry.Register(session); err != nil {
		return conn.WriteErr(hubproto.ErrAlreadyExists, err.Error())
	}

	// Associate session with this connection for cleanup
	conn.SetSessionCode(code)

	// Rebind overlay endpoints for existing proxies that may have been
	// created before this session registered.
	d.rebindProxyOverlays(session)

	// Check if another active session already owns scripts for this project.
	// If so, join as observer (skip autostart). Different project paths always
	// get their own autostart.
	var autostartResult *AutostartResult
	existingSessions := d.sessionRegistry.ListActive(session.ProjectPath, false)
	hasExistingOwner := false
	for _, existing := range existingSessions {
		if existing.Code != code {
			hasExistingOwner = true
			break
		}
	}

	if hasExistingOwner {
		// Another session already started scripts for this project.
		// Join as observer — report what's already running, skip autostart.
		autostartResult = &AutostartResult{}
		for _, entry := range d.scriptRegistry.List(session.ProjectPath) {
			state := entry.State()
			if state == script.StateRunning || state == script.StateStarting {
				autostartResult.Scripts = append(autostartResult.Scripts, entry.Name)
			}
		}
		for _, p := range d.proxym.List() {
			if normalizePath(p.Path) == session.ProjectPath {
				autostartResult.Proxies = append(autostartResult.Proxies, p.ID)
			}
		}
		debug.Log("daemon", "session %s joining existing project %s (skipping autostart, %d scripts, %d proxies already running)",
			code, session.ProjectPath, len(autostartResult.Scripts), len(autostartResult.Proxies))
	} else {
		// First session for this project — run autostart
		autostartResult = d.RunAutostart(context.Background(), metadata.ProjectPath)
	}

	// Add session as observer of all project scripts and claim ownership of unowned ones
	if session.ProjectPath != "" {
		for _, entry := range d.scriptRegistry.List(session.ProjectPath) {
			entry.AddSession(code)
			if entry.Owner() == "" {
				entry.SetOwner(code)
			}
		}
	}

	resp := map[string]interface{}{
		"code":      code,
		"autostart": autostartResult,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
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
	var filter struct {
		Directory string `json:"directory"`
		Global    bool   `json:"global"`
	}

	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &filter)
	}

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
	var filter struct {
		Directory string `json:"directory"`
		Global    bool   `json:"global"`
	}

	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &filter)
	}

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
	if len(cmd.Data) > 0 {
		var data struct {
			Script string `json:"script"`
		}
		if err := json.Unmarshal(cmd.Data, &data); err == nil && data.Script != "" {
			scriptName = data.Script
		}
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
