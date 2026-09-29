package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/alert"
	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) alertsActions() map[string]handlerFn {
	return map[string]handlerFn{
		"REPORT":         noCtx(d.hubHandleAlertsReport),
		"QUERY":          noCtx(d.hubHandleAlertsQuery),
		"":               noCtx(d.hubHandleAlertsQuery),
		"STARTUP-LOG":    noCtx(d.hubHandleStartupLog),
		"STARTUP-ERRORS": noCtx(d.hubHandleStartupLog), // legacy alias
	}
}

func (d *Daemon) hubHandleAlerts(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("ALERTS").dispatch(ctx, conn, cmd, d.alertsActions())
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
	// tracker so health.OutageClassifier biases its decision toward Rebuild.
	if payload.Category == "rebuild" && payload.ScriptID != "" {
		d.healthTracker.RecordRebuildSignal(payload.ScriptID)
	}

	// Retention trigger 1 (PTY-reported path): a clean build retires the
	// process's stale errors, same as the daemon-scanner path.
	d.maybeRetireOnBuildSuccess(payload.PatternID, payload.ScriptID, ts)

	// Mirror into the incident pipeline (get_incidents). REPORT used to write
	// only alertStore, so PTY-reported alerts (agnt run) and the error_queue
	// MCP tool, whose description promises get_incidents visibility, never
	// arrived there. Same dual-write ingestProcessAlert does for the daemon
	// scanner. Dedup applies per session (fingerprint = source, category,
	// canonical message, location).
	if d.incidentBus != nil {
		m := &alert.AlertMatch{
			Pattern: &alert.AlertPattern{
				ID:          payload.PatternID,
				Severity:    alert.AlertSeverity(payload.Severity),
				Category:    payload.Category,
				Description: payload.Description,
			},
			Line:      payload.Line,
			Timestamp: ts,
			ScriptID:  payload.ScriptID,
		}
		// One fresh event per target so sessions never share an incident ID.
		newEvent := func(sessionID string) incident.IncidentEvent {
			ev := incident.FromAlertMatch(m, payload.ScriptID)
			ev.Ctx.ProjectPath = payload.ProjectPath
			ev.Ctx.SessionID = sessionID
			return ev
		}
		if sc := conn.SessionCode(); sc != "" {
			// agnt run reports over its own session-bound connection.
			d.incidentBus.Publish(newEvent(sc))
		} else if payload.ProjectPath != "" && d.sessionRegistry != nil {
			// MCP connections (error_queue) are not session-bound; the bus
			// drops sessionless events, so fan out to every session attached
			// to the reported project. No attached session: nothing live to
			// notify; alertStore above still records it.
			for _, s := range d.sessionRegistry.List(payload.ProjectPath, false) {
				d.incidentBus.Publish(newEvent(s.Code))
			}
		}
	}

	return conn.WriteOK("alert stored")
}

// hubHandleAlertsQuery handles ALERTS QUERY command.
// Receives an optional AlertQueryFilter and returns matching entries.

func (d *Daemon) hubHandleAlertsQuery(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	protoFilter, _ := unmarshalCommand[protocol.AlertQueryFilter](cmd)
	scopeFilter, _ := unmarshalCommand[protocol.DirectoryFilter](cmd)

	// Route through the mandatory session-scope chokepoint. A non-global
	// query that can't resolve a project is rejected fail-loud (mirrors
	// INCIDENTS QUERY) rather than leaking every project's alerts.
	projectPath, global, err := d.resolveProjectScope(scopeFilter, conn.SessionCode())
	if err != nil {
		return d.writeScopeErr(conn, err, "session_code")
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func tagSuffix(tag string) string {
	if tag == "" {
		return ""
	}
	return fmt.Sprintf(" [%s]", tag)
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
	scopeFilter, _ := unmarshalCommand[protocol.DirectoryFilter](cmd)
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
	projectPath, global, err := d.resolveProjectScope(scopeFilter, conn.SessionCode())
	if err != nil {
		return d.writeScopeErr(conn, err, "session_code")
	}
	if !global {
		filter.ProjectPath = projectPath
	}

	entries := d.startupErrorStore.Query(filter)

	data, _ := json.Marshal(map[string]interface{}{
		"entries": entries,
		"count":   len(entries),
		"notices": buildNotices(entries),
	})
	return conn.WriteJSON(data)
}

// hubHandleScript handles the SCRIPT command and its sub-verbs.
