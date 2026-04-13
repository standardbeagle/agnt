package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/standardbeagle/go-cli-server/process"
	"github.com/standardbeagle/go-cli-server/protocol"
)

// handleProc handles PROC commands.
func (h *Hub) handleProc(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if h.pm == nil {
		return conn.WriteErr(protocol.ErrInvalidCommand, "process management not enabled")
	}

	// Normalize: if no SubVerb but args present, use first arg as SubVerb
	if cmd.SubVerb == "" && len(cmd.Args) > 0 {
		cmd.SubVerb = strings.ToUpper(cmd.Args[0])
		cmd.Args = cmd.Args[1:]
	}

	switch cmd.SubVerb {
	case "STATUS":
		return h.handleProcStatus(conn, cmd)
	case "OUTPUT":
		return h.handleProcOutput(conn, cmd)
	case "STOP":
		return h.handleProcStop(ctx, conn, cmd)
	case "LIST":
		return h.handleProcList(conn, cmd)
	case "CLEANUP-PORT":
		return h.handleProcCleanupPort(ctx, conn, cmd)
	case "STDIN":
		return h.handleProcStdin(conn, cmd)
	case "STREAM":
		return h.handleProcStream(conn, cmd)
	case "":
		return conn.WriteMissingParam("PROC", "action", "action required")
	default:
		return conn.WriteInvalidAction("PROC", cmd.SubVerb, []string{"STATUS", "OUTPUT", "STOP", "LIST", "CLEANUP-PORT", "STDIN", "STREAM"})
	}
}

func (h *Hub) handleProcStatus(conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Args) == 0 {
		return conn.WriteMissingParam("PROC STATUS", "id", "process ID required")
	}

	proc, err := h.pm.Get(cmd.Args[0])
	if err != nil {
		return conn.WriteNotFound("process", cmd.Args[0])
	}

	status := map[string]any{
		"id":      proc.ID,
		"state":   proc.State().String(),
		"pid":     proc.PID(),
		"runtime": proc.Runtime().String(),
	}

	if proc.StartTime() != nil {
		status["start_time"] = proc.StartTime().Format(time.RFC3339)
	}
	if proc.EndTime() != nil {
		status["end_time"] = proc.EndTime().Format(time.RFC3339)
	}
	if proc.IsDone() {
		status["exit_code"] = proc.ExitCode()
	}

	data, _ := json.Marshal(status)
	return conn.WriteJSON(data)
}

func (h *Hub) handleProcOutput(conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Args) == 0 {
		return conn.WriteMissingParam("PROC OUTPUT", "id", "process ID required")
	}

	proc, err := h.pm.Get(cmd.Args[0])
	if err != nil {
		return conn.WriteNotFound("process", cmd.Args[0])
	}

	output, truncated := proc.CombinedOutput()

	// Send as chunks for large output
	if len(output) > 64*1024 {
		chunkSize := 64 * 1024
		for i := 0; i < len(output); i += chunkSize {
			end := i + chunkSize
			if end > len(output) {
				end = len(output)
			}
			if err := conn.WriteChunk(output[i:end]); err != nil {
				return err
			}
		}
		return conn.WriteEnd()
	}

	result := map[string]any{
		"output":    string(output),
		"truncated": truncated,
	}
	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}

func (h *Hub) handleProcStop(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Args) == 0 {
		return conn.WriteMissingParam("PROC STOP", "id", "process ID required")
	}

	if err := h.pm.Stop(ctx, cmd.Args[0]); err != nil {
		return conn.WriteErr(protocol.ErrInvalidState, err.Error())
	}

	return conn.WriteOK("stopped")
}

func (h *Hub) handleProcList(conn *Connection, cmd *protocol.Command) error {
	procs := h.pm.List()

	result := make([]map[string]any, 0, len(procs))
	for _, proc := range procs {
		item := map[string]any{
			"id":           proc.ID,
			"state":        proc.State().String(),
			"project_path": proc.ProjectPath,
		}
		result = append(result, item)
	}

	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}

func (h *Hub) handleProcCleanupPort(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Args) == 0 {
		return conn.WriteMissingParam("PROC CLEANUP-PORT", "port", "port required")
	}

	var port int
	if _, err := fmt.Sscanf(cmd.Args[0], "%d", &port); err != nil {
		return conn.WriteErr(protocol.ErrInvalidArgs, "invalid port")
	}

	pids, err := h.pm.KillProcessByPort(ctx, port)
	if err != nil {
		return conn.WriteInternalErr(err.Error())
	}

	result := map[string]any{
		"port":    port,
		"killed":  pids,
		"success": true,
	}
	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}

func (h *Hub) handleProcStdin(conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Args) == 0 {
		return conn.WriteMissingParam("PROC STDIN", "id", "process ID required")
	}
	if len(cmd.Data) == 0 {
		return conn.WriteMissingParam("PROC STDIN", "data", "data required")
	}

	n, err := h.pm.WriteStdin(cmd.Args[0], cmd.Data)
	if err != nil {
		return conn.WriteErr(protocol.ErrInvalidState, err.Error())
	}

	result := map[string]any{
		"bytes_written": n,
	}
	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}

func (h *Hub) handleProcStream(conn *Connection, cmd *protocol.Command) error {
	// TODO: Implement stdout/stderr streaming
	return conn.WriteInvalidAction("PROC", "STREAM", []string{"STATUS", "OUTPUT", "STOP", "LIST", "CLEANUP-PORT", "STDIN"})
}

// handleRun handles RUN and RUN-JSON commands.
func (h *Hub) handleRun(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if h.pm == nil {
		return conn.WriteErr(protocol.ErrInvalidCommand, "process management not enabled")
	}

	var cfg protocol.RunConfig
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &cfg); err != nil {
			return conn.WriteErr(protocol.ErrInvalidArgs, "invalid JSON config")
		}
	}

	if cfg.Command == "" && cfg.ScriptName == "" {
		return conn.WriteMissingParam("RUN", "command", "command or script_name required")
	}

	procCfg := process.ProcessConfig{
		ID:          cfg.ID,
		ProjectPath: cfg.Path,
		Command:     cfg.Command,
		Args:        cfg.Args,
		Env:         cfg.Env,
		EnableStdin: cfg.EnableStdin,
	}

	result, err := h.pm.StartOrReuse(ctx, procCfg)
	if err != nil {
		return conn.WriteInternalErr(err.Error())
	}

	response := map[string]any{
		"id":      result.Process.ID,
		"pid":     result.Process.PID(),
		"state":   result.Process.State().String(),
		"reused":  result.Reused,
		"cleaned": result.Cleaned,
	}

	data, _ := json.Marshal(response)
	return conn.WriteJSON(data)
}

// handleRelay handles RELAY commands for message routing.
func (h *Hub) handleRelay(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if cmd.SubVerb == "" && len(cmd.Args) > 0 {
		cmd.SubVerb = strings.ToUpper(cmd.Args[0])
		cmd.Args = cmd.Args[1:]
	}

	switch cmd.SubVerb {
	case "SEND":
		return h.handleRelaySend(conn, cmd)
	case "BROADCAST":
		return h.handleRelayBroadcast(conn, cmd)
	case "REQUEST":
		return h.handleRelayRequest(ctx, conn, cmd)
	default:
		return conn.WriteInvalidAction("RELAY", cmd.SubVerb, []string{"SEND", "BROADCAST", "REQUEST"})
	}
}

func (h *Hub) handleRelaySend(conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Args) == 0 {
		return conn.WriteMissingParam("RELAY SEND", "target", "target process ID required")
	}

	target := cmd.Args[0]
	proc, ok := h.GetExternalProcess(target)
	if !ok {
		return conn.WriteErr(protocol.ErrNotAttached, "target process not attached")
	}

	msg := &Message{
		From:      fmt.Sprintf("client-%d", conn.ID()),
		To:        target,
		Type:      "relay",
		Data:      cmd.Data,
		Timestamp: time.Now(),
	}

	select {
	case proc.Inbox <- msg:
		return conn.WriteOK("sent")
	default:
		return conn.WriteErr(protocol.ErrDeliveryFailed, "inbox full")
	}
}

func (h *Hub) handleRelayBroadcast(conn *Connection, cmd *protocol.Command) error {
	msg := &Message{
		From:      fmt.Sprintf("client-%d", conn.ID()),
		To:        "*",
		Type:      "broadcast",
		Data:      cmd.Data,
		Timestamp: time.Now(),
	}

	h.BroadcastToExternal(msg)
	return conn.WriteOK("broadcast sent")
}

func (h *Hub) handleRelayRequest(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	// TODO: Implement request-response pattern with correlation ID
	return conn.WriteInvalidAction("RELAY", "REQUEST", []string{"SEND", "BROADCAST"})
}

// handleAttach handles ATTACH command for external process registration.
func (h *Hub) handleAttach(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	var cfg protocol.AttachConfig
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &cfg); err != nil {
			return conn.WriteErr(protocol.ErrInvalidArgs, "invalid JSON config")
		}
	}

	if cfg.ID == "" {
		return conn.WriteMissingParam("ATTACH", "id", "id required")
	}

	// Note: For a full implementation, the external process would connect
	// on its own socket. For now, we just register the ID.
	proc := &ExternalProcess{
		ID:          cfg.ID,
		ProjectPath: cfg.ProjectPath,
		Labels:      cfg.Labels,
		Inbox:       make(chan *Message, 100),
	}

	if err := h.RegisterExternalProcess(proc); err != nil {
		return conn.WriteErr(protocol.ErrAlreadyExists, err.Error())
	}

	return conn.WriteOK("attached")
}

// handleDetach handles DETACH command for external process removal.
func (h *Hub) handleDetach(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Args) == 0 {
		return conn.WriteMissingParam("DETACH", "id", "process ID required")
	}

	h.UnregisterExternalProcess(cmd.Args[0])
	return conn.WriteOK("detached")
}

// handleSession handles SESSION commands.
func (h *Hub) handleSession(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if cmd.SubVerb == "" && len(cmd.Args) > 0 {
		cmd.SubVerb = strings.ToUpper(cmd.Args[0])
		cmd.Args = cmd.Args[1:]
	}

	switch cmd.SubVerb {
	case "REGISTER":
		return h.handleSessionRegister(conn, cmd)
	case "UNREGISTER":
		return h.handleSessionUnregister(conn, cmd)
	case "HEARTBEAT":
		return h.handleSessionHeartbeat(conn, cmd)
	case "LIST":
		return h.handleSessionList(conn, cmd)
	case "GET":
		return h.handleSessionGet(conn, cmd)
	default:
		return conn.WriteInvalidAction("SESSION", cmd.SubVerb, []string{"REGISTER", "UNREGISTER", "HEARTBEAT", "LIST", "GET"})
	}
}

func (h *Hub) handleSessionRegister(conn *Connection, cmd *protocol.Command) error {
	var cfg protocol.SessionRegisterConfig
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &cfg); err != nil {
			return conn.WriteErr(protocol.ErrInvalidArgs, "invalid JSON config")
		}
	}

	code := fmt.Sprintf("session-%d", time.Now().UnixNano())

	session := &Session{
		Code:        code,
		ProjectPath: cfg.ProjectPath,
		Command:     cfg.Command,
		Args:        cfg.Args,
		StartedAt:   time.Now(),
		LastSeen:    time.Now(),
	}

	h.sessions.Store(code, session)
	conn.SetSessionCode(code)

	result := map[string]any{
		"code":       code,
		"started_at": session.StartedAt.Format(time.RFC3339),
	}
	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}

func (h *Hub) handleSessionUnregister(conn *Connection, cmd *protocol.Command) error {
	code := conn.SessionCode()
	if code == "" && len(cmd.Args) > 0 {
		code = cmd.Args[0]
	}

	if code != "" {
		h.sessions.Delete(code)
		conn.SetSessionCode("")
	}

	return conn.WriteOK("unregistered")
}

func (h *Hub) handleSessionHeartbeat(conn *Connection, cmd *protocol.Command) error {
	code := conn.SessionCode()
	if code == "" {
		return conn.WriteNotFound("session", "current")
	}

	if val, ok := h.sessions.Load(code); ok {
		session := val.(*Session)
		session.LastSeen = time.Now()
	}

	return conn.WriteOK("heartbeat")
}

func (h *Hub) handleSessionList(conn *Connection, cmd *protocol.Command) error {
	var sessions []map[string]any

	h.sessions.Range(func(key, value any) bool {
		session := value.(*Session)
		sessions = append(sessions, map[string]any{
			"code":         session.Code,
			"project_path": session.ProjectPath,
			"command":      session.Command,
			"started_at":   session.StartedAt.Format(time.RFC3339),
			"last_seen":    session.LastSeen.Format(time.RFC3339),
		})
		return true
	})

	data, _ := json.Marshal(sessions)
	return conn.WriteJSON(data)
}

func (h *Hub) handleSessionGet(conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Args) == 0 {
		return conn.WriteMissingParam("SESSION GET", "code", "session code required")
	}

	val, ok := h.sessions.Load(cmd.Args[0])
	if !ok {
		return conn.WriteNotFound("session", cmd.Args[0])
	}

	session := val.(*Session)
	result := map[string]any{
		"code":         session.Code,
		"project_path": session.ProjectPath,
		"command":      session.Command,
		"args":         session.Args,
		"started_at":   session.StartedAt.Format(time.RFC3339),
		"last_seen":    session.LastSeen.Format(time.RFC3339),
	}

	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}
