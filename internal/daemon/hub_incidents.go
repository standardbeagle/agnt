package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleIncidents(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("INCIDENTS").dispatch(ctx, conn, cmd, map[string]handlerFn{
		"QUERY": noCtx(d.hubHandleIncidentsQuery),
		"":      noCtx(d.hubHandleIncidentsQuery),
	})
}

func (d *Daemon) hubHandleIncidentsQuery(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if d.incidentBus == nil {
		return conn.WriteErr(hubproto.ErrInternal, "incident pipeline not initialized")
	}

	sessionCode := conn.SessionCode()
	if sessionCode == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "no session attached — call SESSION ATTACH first")
	}

	var filter protocol.IncidentQueryFilter
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &filter); err != nil {
			return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter: %v", err))
		}
	}

	qf := incidentQueryFilterToInternal(filter)
	entries, stats := d.incidentBus.QuerySession(sessionCode, qf)

	result := buildIncidentQueryResult(entries, stats, filter)
	// HasSession is true only when this session actually has a pipeline
	// (alerts.incident-pipeline enabled), so callers can tell "pipeline on but
	// inbox empty" from "pipeline off for this session".
	result.PipelineEnabled = d.incidentBus.HasSession(sessionCode)

	// Mark read only the records actually returned (after secondary filtering),
	// so entries filtered out of the response are not silently marked read and
	// the cursor advances to exactly the page the caller saw.
	if filter.MarkRead && len(result.Incidents) > 0 {
		fps := make([]string, len(result.Incidents))
		for i, r := range result.Incidents {
			fps[i] = r.Fingerprint
		}
		d.incidentBus.MarkReadSession(sessionCode, fps, true)
	}

	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}

// incidentQueryFilterToInternal converts the wire filter to the internal query filter.
func incidentQueryFilterToInternal(f protocol.IncidentQueryFilter) incident.QueryFilter {
	qf := incident.QueryFilter{Limit: f.Limit}
	for _, s := range f.Severities {
		qf.Severities = append(qf.Severities, incident.Severity(s))
	}
	if f.Since != "" {
		t, err := time.Parse(time.RFC3339, f.Since)
		if err == nil {
			qf.Since = t
		}
	}
	if f.MarkRead {
		qf.UnreadOnly = false
	}
	return qf
}

// buildIncidentQueryResult maps inbox entries to the wire result type.
func buildIncidentQueryResult(entries []incident.InboxEntry, stats incident.Stats, filter protocol.IncidentQueryFilter) protocol.IncidentQueryResult {
	// Apply secondary filters not supported by the inbox query.
	filtered := applySecondaryFilters(entries, filter)

	records := make([]protocol.IncidentRecord, 0, len(filtered))
	for _, e := range filtered {
		records = append(records, incidentEntryToRecord(e, filter.Detail))
	}

	// Records are newest-first; cursor = newest entry in the returned page. The
	// inbox returns the oldest unseen page when truncating, so advancing the
	// caller's `since` to this page's newest entry sweeps forward gap-free.
	var cursor string
	if len(records) > 0 {
		cursor = records[0].LastSeen
	}

	return protocol.IncidentQueryResult{
		Incidents: records,
		InboxStats: protocol.InboxStatsRecord{
			Critical: stats.Critical,
			Error:    stats.Error,
			Warning:  stats.Warning,
			Info:     stats.Info,
			Dropped:  stats.Dropped,
		},
		Cursor: cursor,
	}
}

// applySecondaryFilters narrows entries by Sources, ProxyID, ProcessID, and Fingerprints
// — fields not handled by the Inbox.Query() primitive.
func applySecondaryFilters(entries []incident.InboxEntry, filter protocol.IncidentQueryFilter) []incident.InboxEntry {
	if len(filter.Sources) == 0 && filter.ProxyID == "" && filter.ProcessID == "" && len(filter.Fingerprints) == 0 {
		return entries
	}

	sourceSet := make(map[string]bool, len(filter.Sources))
	for _, s := range filter.Sources {
		sourceSet[s] = true
	}
	fpSet := make(map[string]bool, len(filter.Fingerprints))
	for _, fp := range filter.Fingerprints {
		fpSet[fp] = true
	}

	out := entries[:0:0]
	for _, e := range entries {
		if len(fpSet) > 0 && !fpSet[e.Fingerprint] {
			continue
		}
		if e.Sample == nil {
			out = append(out, e)
			continue
		}
		if len(sourceSet) > 0 && !sourceSet[string(e.Sample.Source)] {
			continue
		}
		if filter.ProxyID != "" && e.Sample.Ctx.ProxyID != filter.ProxyID {
			continue
		}
		if filter.ProcessID != "" && e.Sample.Ctx.ProcessID != filter.ProcessID {
			continue
		}
		out = append(out, e)
	}
	return out
}

// incidentEntryToRecord converts an InboxEntry to the wire IncidentRecord.
func incidentEntryToRecord(e incident.InboxEntry, detail string) protocol.IncidentRecord {
	r := protocol.IncidentRecord{
		Fingerprint: e.Fingerprint,
		FirstSeen:   e.FirstSeenAt.Format(time.RFC3339),
		LastSeen:    e.LastSeenAt.Format(time.RFC3339),
		Count:       e.Count,
		Severity:    string(e.Severity),
		Read:        e.Read,
	}

	if e.Sample != nil {
		r.ID = e.Sample.ID
		r.Source = string(e.Sample.Source)
		r.Category = e.Sample.Category
		r.Summary = e.Sample.Summary
		r.Context = protocol.IncidentContext{
			ProcessID:   e.Sample.Ctx.ProcessID,
			ProxyID:     e.Sample.Ctx.ProxyID,
			SessionID:   e.Sample.Ctx.SessionID,
			ProjectPath: e.Sample.Ctx.ProjectPath,
			URL:         e.Sample.Ctx.URL,
			PID:         e.Sample.Ctx.PID,
			Port:        e.Sample.Ctx.Port,
		}
		r.Remediation = protocol.IncidentRemediation{
			PrimaryTool:  e.Sample.Remediation.PrimaryTool,
			PrimaryArgs:  e.Sample.Remediation.PrimaryArgs,
			FallbackTool: e.Sample.Remediation.FallbackTool,
			SkillHint:    e.Sample.Remediation.SkillHint,
		}
		if detail == "full" && e.Sample.PayloadRef != nil {
			// Blob payload hydration not yet implemented — PayloadRef carries
			// only the hash and MIME type; the in-memory BlobStore is held by
			// the session pipeline, not accessible here without a lookup path.
			_ = e.Sample.PayloadRef
		}
	}

	if r.ID == "" {
		r.ID = e.Fingerprint
	}

	return r
}
