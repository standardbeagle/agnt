package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"

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

	var payload protocol.AlertReportPayload
	if err := json.Unmarshal(cmd.Data, &payload); err != nil {
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
	var protoFilter protocol.AlertQueryFilter
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &protoFilter); err != nil {
			debug.Log("daemon", "ALERTS QUERY: invalid filter JSON: %v", err)
		}
	}

	filter := AlertStoreFilter{
		ProcessID: protoFilter.ProcessID,
		Severity:  protoFilter.Severity,
		Limit:     protoFilter.Limit,
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
		Since: time.Now().Add(-30 * time.Minute),
		Limit: 50,
	}

	if len(cmd.Data) > 0 {
		var f struct {
			Since     string `json:"since,omitempty"`
			ProcessID string `json:"process_id,omitempty"`
			Level     string `json:"level,omitempty"`
			Limit     int    `json:"limit,omitempty"`
		}
		if err := json.Unmarshal(cmd.Data, &f); err == nil {
			if f.ProcessID != "" {
				filter.ProcessID = f.ProcessID
			}
			if f.Level != "" {
				filter.Level = f.Level
			}
			if f.Limit > 0 {
				filter.Limit = f.Limit
			}
			filter.Since = parseSinceFilter(f.Since)
			if filter.Since.IsZero() {
				filter.Since = time.Now().Add(-30 * time.Minute)
			}
		}
	}

	entries := d.startupErrorStore.Query(filter)

	data, _ := json.Marshal(map[string]interface{}{
		"entries": entries,
		"count":   len(entries),
	})
	return conn.WriteJSON(data)
}

// hubHandleScript handles the SCRIPT command and its sub-verbs.
