package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/standardbeagle/go-cli-server/protocol"
	"github.com/standardbeagle/go-cli-server/script"
)

// handleScript dispatches SCRIPT sub-verbs.
func (h *Hub) handleScript(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if cmd.SubVerb == "" && len(cmd.Args) > 0 {
		cmd.SubVerb = strings.ToUpper(cmd.Args[0])
		cmd.Args = cmd.Args[1:]
	}

	switch cmd.SubVerb {
	case "LIST":
		return h.handleScriptList(conn, cmd)
	case "GET":
		return h.handleScriptGet(conn, cmd)
	case "OUTPUT":
		return h.handleScriptOutput(conn, cmd)
	case "RESTART":
		return h.handleScriptRestart(ctx, conn, cmd)
	case "STOP":
		return h.handleScriptStop(ctx, conn, cmd)
	case "":
		return conn.WriteMissingParam("SCRIPT", "action", "action required")
	default:
		return conn.WriteInvalidAction("SCRIPT", cmd.SubVerb,
			[]string{"LIST", "GET", "OUTPUT", "RESTART", "STOP"})
	}
}

// resolveScriptProjectPath extracts the project path from the command's JSON data
// or falls back to the connection's session project path.
func (h *Hub) resolveScriptProjectPath(conn *Connection, cmd *protocol.Command) string {
	var filter struct {
		Directory string `json:"directory"`
	}
	if len(cmd.Data) > 0 {
		_ = json.Unmarshal(cmd.Data, &filter)
	}
	if filter.Directory != "" {
		return normalizePath(filter.Directory)
	}
	if code := conn.SessionCode(); code != "" {
		if val, ok := h.sessions.Load(code); ok {
			session := val.(*Session)
			return normalizePath(session.ProjectPath)
		}
	}
	return ""
}

// normalizePath cleans and resolves a file path.
func normalizePath(path string) string {
	if path == "" || path == "." {
		return "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

// scriptEntryToSummary converts a script.Entry to a JSON-friendly summary map.
func scriptEntryToSummary(entry *script.Entry) map[string]any {
	cmd, args := entry.ResolvedCommand()

	summary := map[string]any{
		"name":        entry.Name,
		"state":       entry.State().String(),
		"process_id":  entry.ProcessID,
		"start_count": entry.StartCount(),
		"fail_count":  entry.FailCount(),
		"command":     cmd,
		"args":        args,
	}

	if lastErr := entry.LastError(); lastErr != "" {
		summary["last_error"] = lastErr
	}

	summary["owner_session"] = entry.Owner()
	summary["observer_count"] = entry.ObserverCount()

	history := entry.StateHistory()
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].State == script.StateStarting {
			summary["last_started"] = history[i].Timestamp.Format(time.RFC3339)
			break
		}
	}

	return summary
}

// handleScriptList handles SCRIPT LIST.
func (h *Hub) handleScriptList(conn *Connection, cmd *protocol.Command) error {
	reg := h.pm.ScriptRegistry()
	if reg == nil {
		return conn.WriteInternalErr("script registry not configured")
	}

	projectPath := h.resolveScriptProjectPath(conn, cmd)
	if projectPath == "" {
		return conn.WriteMissingParam("SCRIPT LIST", "directory",
			"directory required (via JSON data or session)")
	}

	entries := reg.List(projectPath)

	scripts := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		scripts = append(scripts, scriptEntryToSummary(entry))
	}

	data, _ := json.Marshal(map[string]any{
		"scripts": scripts,
		"count":   len(scripts),
	})
	return conn.WriteJSON(data)
}

// handleScriptGet handles SCRIPT GET <name>.
func (h *Hub) handleScriptGet(conn *Connection, cmd *protocol.Command) error {
	reg := h.pm.ScriptRegistry()
	if reg == nil {
		return conn.WriteInternalErr("script registry not configured")
	}

	if len(cmd.Args) < 1 {
		return conn.WriteMissingParam("SCRIPT GET", "name", "script name required")
	}

	name := cmd.Args[0]
	projectPath := h.resolveScriptProjectPath(conn, cmd)
	if projectPath == "" {
		return conn.WriteMissingParam("SCRIPT GET", "directory",
			"directory required (via JSON data or session)")
	}

	entry, ok := reg.Get(name, projectPath)
	if !ok {
		return conn.WriteNotFound("script", fmt.Sprintf("%s in %s", name, projectPath))
	}

	detail := scriptEntryToSummary(entry)

	// Add output tail (last 100 lines)
	allLines := entry.OutputLines()
	if len(allLines) > 100 {
		allLines = allLines[len(allLines)-100:]
	}
	detail["output"] = allLines

	// Add state history
	history := entry.StateHistory()
	historyMaps := make([]map[string]any, len(history))
	for i, ht := range history {
		historyMaps[i] = map[string]any{
			"state":     ht.State.String(),
			"timestamp": ht.Timestamp.Format(time.RFC3339),
		}
	}
	detail["history"] = historyMaps

	data, _ := json.Marshal(detail)
	return conn.WriteJSON(data)
}

// handleScriptOutput handles SCRIPT OUTPUT <name>.
func (h *Hub) handleScriptOutput(conn *Connection, cmd *protocol.Command) error {
	reg := h.pm.ScriptRegistry()
	if reg == nil {
		return conn.WriteInternalErr("script registry not configured")
	}

	if len(cmd.Args) < 1 {
		return conn.WriteMissingParam("SCRIPT OUTPUT", "name", "script name required")
	}

	name := cmd.Args[0]
	projectPath := h.resolveScriptProjectPath(conn, cmd)
	if projectPath == "" {
		return conn.WriteMissingParam("SCRIPT OUTPUT", "directory",
			"directory required (via JSON data or session)")
	}

	entry, ok := reg.Get(name, projectPath)
	if !ok {
		return conn.WriteNotFound("script", fmt.Sprintf("%s in %s", name, projectPath))
	}

	tail := 0
	if len(cmd.Data) > 0 {
		var opts struct {
			Tail int `json:"tail"`
		}
		_ = json.Unmarshal(cmd.Data, &opts)
		tail = opts.Tail
	}

	allLines := entry.OutputLines()
	total := len(allLines)
	if tail > 0 && tail < total {
		allLines = allLines[total-tail:]
	}

	data, _ := json.Marshal(map[string]any{
		"lines": allLines,
		"count": len(allLines),
		"total": total,
	})
	return conn.WriteJSON(data)
}

// handleScriptRestart handles SCRIPT RESTART <name>.
func (h *Hub) handleScriptRestart(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	reg := h.pm.ScriptRegistry()
	if reg == nil {
		return conn.WriteInternalErr("script registry not configured")
	}

	if len(cmd.Args) < 1 {
		return conn.WriteMissingParam("SCRIPT RESTART", "name", "script name required")
	}

	name := cmd.Args[0]
	projectPath := h.resolveScriptProjectPath(conn, cmd)
	if projectPath == "" {
		return conn.WriteMissingParam("SCRIPT RESTART", "directory",
			"directory required (via JSON data or session)")
	}

	entry, ok := reg.Get(name, projectPath)
	if !ok {
		return conn.WriteNotFound("script", fmt.Sprintf("%s in %s", name, projectPath))
	}

	// Stop existing process if running
	if proc, err := h.pm.Get(entry.ProcessID); err == nil && proc.IsRunning() {
		if stopErr := h.pm.Stop(ctx, entry.ProcessID); stopErr != nil {
			return conn.WriteInternalErr(fmt.Sprintf("failed to stop process: %v", stopErr))
		}
	}

	entry.AddRestartMarker()
	entry.SetState(script.StateRestarting)

	resp := map[string]any{
		"name":       name,
		"process_id": entry.ProcessID,
		"state":      entry.State().String(),
		"success":    true,
		"message":    fmt.Sprintf("script %q stopped for restart", name),
	}
	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// handleScriptStop handles SCRIPT STOP <name>.
func (h *Hub) handleScriptStop(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	reg := h.pm.ScriptRegistry()
	if reg == nil {
		return conn.WriteInternalErr("script registry not configured")
	}

	if len(cmd.Args) < 1 {
		return conn.WriteMissingParam("SCRIPT STOP", "name", "script name required")
	}

	name := cmd.Args[0]
	projectPath := h.resolveScriptProjectPath(conn, cmd)
	if projectPath == "" {
		return conn.WriteMissingParam("SCRIPT STOP", "directory",
			"directory required (via JSON data or session)")
	}

	entry, ok := reg.Get(name, projectPath)
	if !ok {
		return conn.WriteNotFound("script", fmt.Sprintf("%s in %s", name, projectPath))
	}

	proc, err := h.pm.Get(entry.ProcessID)
	if err != nil || !proc.IsRunning() {
		entry.SetState(script.StateStopped)
		resp := map[string]any{
			"name":       name,
			"process_id": entry.ProcessID,
			"state":      entry.State().String(),
			"success":    true,
			"message":    fmt.Sprintf("script %q already stopped", name),
		}
		data, _ := json.Marshal(resp)
		return conn.WriteJSON(data)
	}

	if stopErr := h.pm.Stop(ctx, entry.ProcessID); stopErr != nil {
		return conn.WriteInternalErr(fmt.Sprintf("failed to stop: %v", stopErr))
	}

	entry.SetState(script.StateStopped)

	resp := map[string]any{
		"name":       name,
		"process_id": entry.ProcessID,
		"state":      "stopped",
		"success":    true,
		"message":    fmt.Sprintf("script %q stopped", name),
	}
	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}
