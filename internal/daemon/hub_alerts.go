package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

	// Normalise the reported project path the way the scope gate normalises
	// the query side (ALERTS QUERY compares ProjectPath by exact string). A
	// reporter that sends a raw cwd would otherwise store an entry no scoped
	// query can match on a case-insensitive filesystem.
	projectPath := payload.ProjectPath
	if projectPath != "" {
		projectPath = normalizePath(projectPath)
	}

	d.alertStore.Add(&AlertEntry{
		PatternID:   payload.PatternID,
		Severity:    payload.Severity,
		Category:    payload.Category,
		Description: payload.Description,
		Line:        payload.Line,
		ScriptID:    payload.ScriptID,
		ProjectPath: projectPath,
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

	// An externally reported failure (error_queue tool, `agnt errors`) has no
	// other route into the incident pipeline, and the inbox is the only
	// incident pull surface — so without this the report was stored where
	// get_incidents never looks.
	if strings.HasPrefix(payload.PatternID, externalAlertPatternPrefix) {
		n := d.publishExternalIncident(payload, projectPath)
		return conn.WriteOK(fmt.Sprintf("alert stored; incident queued for %d session inbox(es)", n))
	}

	return conn.WriteOK("alert stored")
}

// externalAlertPatternPrefix marks an ALERTS REPORT that originated outside
// agnt's own monitors. The error_queue tool and `agnt errors` both stamp it.
// PTY-scanned process/browser matches are NOT published from here: those
// signals already reach the bus from their own daemon-side sources, and
// publishing the report too would double-count them.
const externalAlertPatternPrefix = "external."

// publishExternalIncident publishes one incident per session of the reported
// project and returns how many inboxes it was queued for. Inboxes are
// per-session (numbered contract 1) and the report names a project, not a
// session, so every session working on that project receives its own copy; a
// report for a project with no session lands in no inbox (it stays in the
// alert store, visible through proc snapshot).
func (d *Daemon) publishExternalIncident(payload protocol.AlertReportPayload, projectPath string) int {
	if d.incidentBus == nil || d.sessionRegistry == nil || projectPath == "" {
		return 0
	}
	var sev incident.Severity
	switch payload.Severity {
	case "warning":
		sev = incident.SeverityWarning
	case "info":
		sev = incident.SeverityInfo
	default:
		sev = incident.SeverityError
	}
	queued := 0
	for _, s := range d.sessionRegistry.List("", true) {
		if s.ProjectPath == "" || normalizePath(s.ProjectPath) != projectPath || !d.incidentBus.HasSession(s.Code) {
			continue
		}
		d.incidentBus.Publish(incident.NewIncidentEvent(
			incident.SourceExternal, sev, payload.Category, payload.Line,
			incident.Context{SessionID: s.Code, ProjectPath: projectPath, ProcessID: payload.ScriptID},
			nil,
		))
		queued++
	}
	return queued
}

// hubHandleAlertsQuery handles ALERTS QUERY command.
// Receives an optional AlertQueryFilter and returns matching entries.

func (d *Daemon) hubHandleAlertsQuery(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	protoFilter, err := unmarshalCommand[protocol.AlertQueryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
	}
	scopeFilter, err := unmarshalCommand[protocol.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
	}

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

	f, err := unmarshalCommand[struct {
		Since       string `json:"since,omitempty"`
		ProcessID   string `json:"process_id,omitempty"`
		Level       string `json:"level,omitempty"`
		Limit       int    `json:"limit,omitempty"`
		Global      bool   `json:"global,omitempty"`
		SessionCode string `json:"session_code,omitempty"`
		Directory   string `json:"directory,omitempty"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
	}
	scopeFilter, err := unmarshalCommand[protocol.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
	}
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
