package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/config"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
	"github.com/standardbeagle/go-cli-server/script"
)

func (d *Daemon) scriptActions() map[string]handlerFn {
	actions := map[string]handlerFn{
		"LIST":    noCtx(d.hubHandleScriptList),
		"GET":     noCtx(d.hubHandleScriptGet),
		"OUTPUT":  noCtx(d.hubHandleScriptOutput),
		"RESTART": d.hubHandleScriptRestart,
		"STOP":    d.hubHandleScriptStop,
	}
	valid := routerSubVerbs(actions)
	actions[""] = func(_ context.Context, conn *hubpkg.Connection, _ *hubproto.Command) error {
		return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
			Code:         hubproto.ErrMissingParam,
			Message:      "action required",
			Command:      "SCRIPT",
			Param:        "action",
			ValidActions: valid,
		})
	}
	return actions
}

func (d *Daemon) hubHandleScript(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	actions := d.scriptActions()
	return newCommandRouter("SCRIPT").
		withDefault(func(_ context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
			return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
				Code:         hubproto.ErrInvalidAction,
				Message:      "unknown action",
				Command:      "SCRIPT",
				Action:       cmd.SubVerb,
				ValidActions: routerSubVerbs(actions),
			})
		}).
		dispatch(ctx, conn, cmd, actions)
}

// resolveScriptProjectPath extracts the project path from the command's JSON data
// or falls back to the connection's session project path.

func (d *Daemon) resolveScriptProjectPath(conn *hubpkg.Connection, cmd *hubproto.Command) string {
	filter, _ := unmarshalCommand[struct {
		Directory string `json:"directory"`
	}](cmd)
	if filter.Directory != "" {
		return normalizePath(filter.Directory)
	}
	if sessionCode := conn.SessionCode(); sessionCode != "" {
		if session, ok := d.sessionRegistry.Get(sessionCode); ok {
			return normalizePath(session.ProjectPath)
		}
	}
	return ""
}

// scriptEntryToSummary converts a script.Entry to a JSON-friendly summary map.
// If alertStore is non-nil, includes a has_alerts field based on recent alerts
// for this script's process ID.
//
// The `kind` field is always ScriptKindProcess for entries returned from the
// vendored script.Registry. Proxy-kind entries go through
// proxyEntryToSummary and are merged into SCRIPT LIST in hubHandleScriptList.

func scriptEntryToSummary(entry *script.Entry, alertStore *ProcessAlertStore) map[string]interface{} {
	cmd, args := entry.ResolvedCommand()

	summary := map[string]interface{}{
		"name":        entry.Name,
		"kind":        string(ScriptKindProcess),
		"state":       entry.State().String(),
		"process_id":  entry.ProcessID,
		"start_count": entry.StartCount(),
		"fail_count":  entry.FailCount(),
		"command":     cmd,
		"args":        args,
	}

	lastErr := entry.LastError()
	if lastErr != "" {
		summary["last_error"] = lastErr
	}

	// Ownership and observer tracking
	summary["owner_session"] = entry.Owner()
	summary["observer_count"] = entry.ObserverCount()

	// Derive last_started from state history
	history := entry.StateHistory()
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].State == script.StateStarting {
			summary["last_started"] = history[i].Timestamp.Format(time.RFC3339)
			break
		}
	}

	// Check for recent alerts (last 5 minutes)
	if alertStore != nil {
		alerts := alertStore.Query(AlertStoreFilter{
			Since:     time.Now().Add(-5 * time.Minute),
			ProcessID: entry.ProcessID,
			Limit:     1,
		})
		summary["has_alerts"] = len(alerts) > 0
	}

	return summary
}

// hubHandleScriptList handles SCRIPT LIST.

func (d *Daemon) hubHandleScriptList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	projectPath := d.resolveScriptProjectPath(conn, cmd)
	if projectPath == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "directory required (via JSON data or session)")
	}

	scripts := d.buildScriptListSummaries(projectPath)

	data, _ := json.Marshal(map[string]interface{}{
		"scripts": scripts,
		"count":   len(scripts),
	})
	return conn.WriteJSON(data)
}

// buildScriptListSummaries merges process-kind (script.Entry) and
// proxy-kind (proxyScriptEntry) admin entries for the given project
// path into the SCRIPT LIST JSON shape. Extracted from
// hubHandleScriptList so tests can assert the merge contract without
// spinning up a full hub connection.
//
// Collision rule: if a script.Entry and a proxyScriptEntry share a
// name, the script.Entry wins. registerExplicitProxyEntry already
// enforces this on the write path, but the defensive fence here makes
// a name collision introduced by a future refactor render correctly
// (one row, not two) instead of producing a phantom status-bar
// indicator.
func (d *Daemon) buildScriptListSummaries(projectPath string) []map[string]interface{} {
	entries := d.scriptRegistry.List(projectPath)

	var proxyEntries []*proxyScriptEntry
	if d.proxyEntries != nil {
		proxyEntries = d.proxyEntries.List(projectPath)
	}

	scripts := make([]map[string]interface{}, 0, len(entries)+len(proxyEntries))
	scriptNames := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		scripts = append(scripts, scriptEntryToSummary(entry, d.alertStore))
		scriptNames[entry.Name] = struct{}{}
	}
	for _, pe := range proxyEntries {
		if _, clash := scriptNames[pe.name]; clash {
			continue
		}
		if summary := proxyEntryToSummary(pe); summary != nil {
			scripts = append(scripts, summary)
		}
	}
	return scripts
}

// hubHandleScriptGet handles SCRIPT GET <name>.

func (d *Daemon) hubHandleScriptGet(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "script name required")
	}

	name := cmd.Args[0]
	projectPath := d.resolveScriptProjectPath(conn, cmd)
	if projectPath == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "directory required (via JSON data or session)")
	}

	entry, ok := d.scriptRegistry.Get(name, projectPath)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("script %q not found in %s", name, projectPath))
	}

	detail := scriptEntryToSummary(entry, d.alertStore)

	// Add output tail (last 100 lines)
	allLines := entry.OutputLines()
	if len(allLines) > 100 {
		allLines = allLines[len(allLines)-100:]
	}
	detail["output"] = allLines

	// Add state history
	history := entry.StateHistory()
	historyMaps := make([]map[string]interface{}, len(history))
	for i, h := range history {
		historyMaps[i] = map[string]interface{}{
			"state":     h.State.String(),
			"timestamp": h.Timestamp.Format(time.RFC3339),
		}
	}
	detail["history"] = historyMaps

	data, _ := json.Marshal(detail)
	return conn.WriteJSON(data)
}

// hubHandleScriptOutput handles SCRIPT OUTPUT <name>.

func (d *Daemon) hubHandleScriptOutput(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "script name required")
	}

	name := cmd.Args[0]
	projectPath := d.resolveScriptProjectPath(conn, cmd)
	if projectPath == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "directory required (via JSON data or session)")
	}

	entry, ok := d.scriptRegistry.Get(name, projectPath)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("script %q not found in %s", name, projectPath))
	}

	// Parse optional tail count from JSON data
	opts, _ := unmarshalCommand[struct {
		Tail int `json:"tail"`
	}](cmd)
	tail := opts.Tail

	allLines := entry.OutputLines()
	total := len(allLines)
	if tail > 0 && tail < total {
		allLines = allLines[total-tail:]
	}

	data, _ := json.Marshal(map[string]interface{}{
		"lines": allLines,
		"count": len(allLines),
		"total": total,
	})
	return conn.WriteJSON(data)
}

// hubHandleScriptRestart handles SCRIPT RESTART <name>.

func (d *Daemon) hubHandleScriptRestart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "script name required")
	}

	name := cmd.Args[0]
	projectPath := d.resolveScriptProjectPath(conn, cmd)
	if projectPath == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "directory required (via JSON data or session)")
	}

	entry, ok := d.scriptRegistry.Get(name, projectPath)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("script %q not found in %s", name, projectPath))
	}

	// Stop existing process if running
	if proc, err := d.hub.ProcessManager().Get(entry.ProcessID); err == nil && proc.IsRunning() {
		if d.autoRestarter != nil {
			d.autoRestarter.Unregister(entry.ProcessID)
		}
		// Mark this stop as daemon-initiated so the health.OutageClassifier
		// treats the upcoming outage as a Rebuild rather than a Crash.
		d.healthTracker.MarkDaemonInitiatedStop(entry.ProcessID)
		if stopErr := d.hub.ProcessManager().Stop(ctx, entry.ProcessID); stopErr != nil {
			return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to stop process: %v", stopErr))
		}
		d.retireIncidentProcessOwner(entry.ProcessID)
		// Remove the stopped process so StartScript creates a fresh one
		d.hub.ProcessManager().RemoveByPath(entry.ProcessID, entry.ProjectPath)
	}

	// Look up agnt-specific config stored during initial autostart
	cfgVal, ok := d.scriptConfigs.Load(entry.ProcessID)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("no config found for script %q", name))
	}
	scriptCfg := cfgVal.(*config.ScriptConfig)

	// Wait for declared ports to be free before restarting
	expectedPorts := d.getExpectedPortsForScript(name, scriptCfg, nil,
		resolveWorkingDir(entry.ProjectPath, scriptCfg.Cwd), "", nil)
	for _, port := range expectedPorts {
		d.waitForPortFree(port, 5*time.Second)
	}

	// Restart via autostartScript which handles resolution and StartScript
	entry.AddRestartMarker()
	entry.SetState(script.StateRestarting)

	if err := d.autostartScript(ctx, name, scriptCfg, entry.ProjectPath, nil); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to restart script: %v", err))
	}

	resp := map[string]interface{}{
		"name":       name,
		"process_id": entry.ProcessID,
		"state":      entry.State().String(),
		"success":    true,
		"message":    fmt.Sprintf("script %q restarted", name),
	}
	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleScriptStop handles SCRIPT STOP <name>.

func (d *Daemon) hubHandleScriptStop(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "script name required")
	}

	name := cmd.Args[0]
	projectPath := d.resolveScriptProjectPath(conn, cmd)
	if projectPath == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "directory required (via JSON data or session)")
	}

	entry, ok := d.scriptRegistry.Get(name, projectPath)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("script %q not found in %s", name, projectPath))
	}

	proc, err := d.hub.ProcessManager().Get(entry.ProcessID)
	if err != nil || !proc.IsRunning() {
		d.retireIncidentProcessOwner(entry.ProcessID)
		entry.SetState(script.StateStopped)
		resp := map[string]interface{}{
			"name":       name,
			"process_id": entry.ProcessID,
			"state":      entry.State().String(),
			"success":    true,
			"message":    fmt.Sprintf("script %q already stopped", name),
		}
		data, _ := json.Marshal(resp)
		return conn.WriteJSON(data)
	}

	// Unregister from auto-restart before stopping
	if d.autoRestarter != nil {
		d.autoRestarter.Unregister(entry.ProcessID)
	}

	if stopErr := d.hub.ProcessManager().Stop(ctx, entry.ProcessID); stopErr != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to stop: %v", stopErr))
	}
	d.retireIncidentProcessOwner(entry.ProcessID)

	entry.SetState(script.StateStopped)

	resp := map[string]interface{}{
		"name":       name,
		"process_id": entry.ProcessID,
		"state":      "stopped",
		"success":    true,
		"message":    fmt.Sprintf("script %q stopped", name),
	}
	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleAutostart dispatches AUTOSTART sub-verbs.
