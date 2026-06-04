package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleAlerts(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("ALERTS").dispatch(ctx, conn, cmd, map[string]handlerFn{
		"REPORT":         noCtx(d.hubHandleAlertsReport),
		"QUERY":          noCtx(d.hubHandleAlertsQuery),
		"":               noCtx(d.hubHandleAlertsQuery),
		"CLEAR":          connOnly(d.hubHandleAlertsClear),
		"STARTUP-LOG":    noCtx(d.hubHandleStartupLog),
		"STARTUP-ERRORS": noCtx(d.hubHandleStartupLog),
	})
}

// hubHandleAlertsReport handles ALERTS REPORT command.
// Receives an AlertReportPayload and stores it in the alert store.

func (d *Daemon) hubHandleAlertsReport(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Data) == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "ALERTS REPORT requires JSON payload")
	}

	payload, err := unmarshalCommand[protocol.AlertReportPayload](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid payload: %v", err))
	}

	ts, err := time.Parse(time.RFC3339, payload.Timestamp)
	if err != nil {
		ts = time.Now()
	}

	d.alertStore.Add(&AlertEntry{
		PatternID:   payload.PatternID,
		Severity:    payload.Severity,
		Category:    payload.Category,
		Description: payload.Description,
		Line:        payload.Line,
		ScriptID:    payload.ScriptID,
		ProjectPath: payload.ProjectPath,
		Timestamp:   ts,
	})

	// Rebuild-category alerts are evidence that the next process stop
	// is part of an intentional rebuild, not a crash. Stamp the health
	// tracker so OutageClassifier biases its decision toward Rebuild.
	if payload.Category == "rebuild" && payload.ScriptID != "" {
		d.healthTracker.RecordRebuildSignal(payload.ScriptID)
	}

	return conn.WriteOK("alert stored")
}

// hubHandleAlertsQuery handles ALERTS QUERY command.
// Receives an optional AlertQueryFilter and returns matching entries.

func (d *Daemon) hubHandleAlertsQuery(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	protoFilter, _ := unmarshalCommand[protocol.AlertQueryFilter](cmd)

	// Route through the mandatory session-scope chokepoint. A non-global
	// query that can't resolve a project is rejected fail-loud (mirrors
	// INCIDENTS QUERY) rather than leaking every project's alerts.
	projectPath, global, err := d.resolveProjectScope(protocol.DirectoryFilter{
		Global:      protoFilter.Global,
		SessionCode: protoFilter.SessionCode,
		Directory:   protoFilter.Directory,
	}, conn.SessionCode())
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	filter := AlertStoreFilter{
		ProcessID: protoFilter.ProcessID,
		Severity:  protoFilter.Severity,
		Limit:     protoFilter.Limit,
	}
	if !global {
		filter.ProjectPath = projectPath
	}

	filter.Since = parseSinceFilter(protoFilter.Since)

	entries := d.alertStore.Query(filter)

	data, _ := json.Marshal(map[string]interface{}{
		"alerts": entries,
		"count":  len(entries),
	})
	return conn.WriteJSON(data)
}

// hubHandleAlertsClear handles ALERTS CLEAR command.

func (d *Daemon) hubHandleAlertsClear(conn *hubpkg.Connection) error {
	d.alertStore.Clear()
	return conn.WriteOK("alerts cleared")
}

// parseSinceFilter parses a "since" string as RFC3339 or Go duration (e.g. "5m").

func parseSinceFilter(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if dur, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-dur)
	}
	return time.Time{}
}

// hubHandleStartupLog handles ALERTS STARTUP-LOG command.

func (d *Daemon) hubHandleStartupLog(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	filter := StartupLogFilter{
		Limit: 50,
	}

	f, _ := unmarshalCommand[struct {
		Since       string `json:"since,omitempty"`
		ProcessID   string `json:"process_id,omitempty"`
		Level       string `json:"level,omitempty"`
		Limit       int    `json:"limit,omitempty"`
		Global      bool   `json:"global,omitempty"`
		SessionCode string `json:"session_code,omitempty"`
		Directory   string `json:"directory,omitempty"`
	}](cmd)
	if f.ProcessID != "" {
		filter.ProcessID = f.ProcessID
	}
	if f.Level != "" {
		filter.Level = f.Level
	}
	if f.Limit > 0 {
		filter.Limit = f.Limit
	}
	// No wall-clock default Since. Autostart is a one-shot at session start, so
	// a 30-minute window left the log "always empty" once the session had been
	// running a while. The store is a capacity-bounded ring (100 entries), so an
	// unbounded-in-time query is safe and returns the whole ring up to Limit.
	// An explicit `since` still narrows the window.
	filter.Since = parseSinceFilter(f.Since)

	// Route through the mandatory session-scope chokepoint so STARTUP-LOG
	// cannot surface another project's startup events. Non-global queries
	// that can't resolve a project fail loud (mirrors ALERTS QUERY).
	projectPath, global, err := d.resolveProjectScope(protocol.DirectoryFilter{
		Global:      f.Global,
		SessionCode: f.SessionCode,
		Directory:   f.Directory,
	}, conn.SessionCode())
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}
	if !global {
		filter.ProjectPath = projectPath
	}

	entries := d.startupErrorStore.Query(filter)

	data, _ := json.Marshal(map[string]interface{}{
		"entries": entries,
		"count":   len(entries),
	})
	return conn.WriteJSON(data)
}

// hubHandleScript handles the SCRIPT command and its sub-verbs.
