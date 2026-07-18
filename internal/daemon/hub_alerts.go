package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/alert"
	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) alertsActions() map[string]handlerFn {
	return map[string]handlerFn{
		"REPORT":         noCtx(d.hubHandleAlertsReport),
		"QUERY":          noCtx(d.hubHandleAlertsQuery),
		"":               noCtx(d.hubHandleAlertsQuery),
		"CLEAR":          noCtx(d.hubHandleAlertsClear),
		"PIN":            noCtx(d.hubHandleAlertsPin),
		"UNPIN":          noCtx(d.hubHandleAlertsUnpin),
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
	// tracker so OutageClassifier biases its decision toward Rebuild.
	if payload.Category == "rebuild" && payload.ScriptID != "" {
		d.healthTracker.RecordRebuildSignal(payload.ScriptID)
	}

	// Retention trigger 1 (PTY-reported path): a clean build retires the
	// process's stale errors, same as the daemon-scanner path.
	d.maybeRetireOnBuildSuccess(payload.PatternID, payload.ScriptID, ts)

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

	// Pinned errors ride along on every query: a pin means "keep showing me
	// this until I unpin it", independent of Since/severity filters.
	pinned := d.pinnedStore.List(projectPath, global)

	data, _ := json.Marshal(map[string]interface{}{
		"alerts": entries,
		"count":  len(entries),
		"pinned": pinned,
	})
	return conn.WriteJSON(data)
}

// hubHandleAlertsClear handles ALERTS CLEAR. The clear is project-scoped by
// default (session-scope chokepoint, same as QUERY); an optional process_id
// narrows it to one process and global:true widens it to every project.
// Pinned errors are never touched.
func (d *Daemon) hubHandleAlertsClear(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	clearFilter, _ := unmarshalCommand[protocol.AlertClearFilter](cmd)
	scopeFilter, _ := unmarshalCommand[protocol.DirectoryFilter](cmd)

	projectPath, global, err := d.resolveProjectScope(scopeFilter, conn.SessionCode())
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	var removed int
	switch {
	case clearFilter.ProcessID != "":
		removed = d.retireProcessErrors(clearFilter.ProcessID, time.Now(), "agent-clear")
	case global:
		removed = d.alertStore.Len()
		d.alertStore.Clear()
	default:
		removed = d.alertStore.ClearProject(projectPath)
	}

	// The agent's live view is the session incident inbox — sweep it too so
	// a clear actually clears what get_errors/get_incidents show. Session
	// resolution mirrors PIN: explicit session_code, else the connection's.
	if clearFilter.ProcessID == "" && d.incidentBus != nil {
		sessionID := clearFilter.SessionCode
		if sessionID == "" {
			sessionID = conn.SessionCode()
		}
		if sessionID != "" {
			d.incidentBus.ClearSessionBefore(sessionID, time.Now())
		}
	}

	data, _ := json.Marshal(map[string]interface{}{
		"cleared": removed,
		"message": fmt.Sprintf("%d alert(s) cleared (pinned errors kept)", removed),
	})
	return conn.WriteJSON(data)
}

// hubHandleAlertsPin handles ALERTS PIN <id>: copies the addressed error out
// of the ring buffers into the pinned store, where no retention trigger can
// touch it. The id is whatever the agent saw — an alert-store unified id or
// an incident-inbox fingerprint; both stores are searched.
func (d *Daemon) hubHandleAlertsPin(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	payload, err := unmarshalCommand[protocol.AlertPinPayload](cmd)
	if err != nil || payload.ID == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "ALERTS PIN requires {id}")
	}
	scopeFilter, _ := unmarshalCommand[protocol.DirectoryFilter](cmd)
	projectPath, _, err := d.resolveProjectScope(scopeFilter, conn.SessionCode())
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	pin, found := d.findErrorByID(payload.ID, projectPath, scopeFilter.SessionCode, conn.SessionCode())
	if !found {
		return conn.WriteErr(hubproto.ErrNotFound,
			fmt.Sprintf("no current error with id %q — only process errors and incident-inbox entries can be pinned", payload.ID))
	}
	pin.Tag = payload.Tag
	pin.ProjectPath = projectPath
	if err := d.pinnedStore.Pin(pin); err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	data, _ := json.Marshal(map[string]interface{}{
		"pinned":  pin,
		"message": fmt.Sprintf("error %s pinned%s — survives builds, restarts, and clears until unpinned", pin.ID, tagSuffix(pin.Tag)),
	})
	return conn.WriteJSON(data)
}

// hubHandleAlertsUnpin handles ALERTS UNPIN <id>.
func (d *Daemon) hubHandleAlertsUnpin(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	payload, err := unmarshalCommand[protocol.AlertPinPayload](cmd)
	if err != nil || payload.ID == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "ALERTS UNPIN requires {id}")
	}
	scopeFilter, _ := unmarshalCommand[protocol.DirectoryFilter](cmd)
	projectPath, _, err := d.resolveProjectScope(scopeFilter, conn.SessionCode())
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	if !d.pinnedStore.Unpin(projectPath, payload.ID) {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("no pinned error with id %q in this project", payload.ID))
	}
	return conn.WriteOK(fmt.Sprintf("error %s unpinned", payload.ID))
}

// findErrorByID searches the alert ring (by unified id) and the caller's
// session incident inbox (by fingerprint) for the addressed error, returning
// it as a PinnedError copy.
func (d *Daemon) findErrorByID(id, projectPath, explicitSession, connSession string) (alert.PinnedError, bool) {
	for _, e := range d.alertStore.Query(AlertStoreFilter{ProjectPath: projectPath}) {
		if e.ID == id {
			return alert.PinnedError{
				ID:        e.ID,
				Source:    "process:" + e.ScriptID,
				Severity:  e.Severity,
				Category:  e.Category,
				Message:   firstNonEmpty(e.Line, e.Description),
				FirstSeen: e.Timestamp,
			}, true
		}
	}

	sessionID := explicitSession
	if sessionID == "" {
		sessionID = connSession
	}
	if d.incidentBus != nil && sessionID != "" {
		if entry := d.incidentBus.FindFingerprintSession(sessionID, id); entry != nil {
			pin := alert.PinnedError{
				ID:        entry.Fingerprint,
				Severity:  string(entry.Severity),
				FirstSeen: entry.FirstSeenAt,
			}
			if entry.Sample != nil {
				pin.Source = string(entry.Sample.Source)
				pin.Category = entry.Sample.Category
				pin.Message = entry.Sample.Summary
				pin.Page = entry.Sample.Ctx.URL
			}
			return pin, true
		}
	}
	return alert.PinnedError{}, false
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
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
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
