package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) incidentsActions() map[string]handlerFn {
	return map[string]handlerFn{
		"QUERY": noCtx(d.hubHandleIncidentsQuery),
		"":      noCtx(d.hubHandleIncidentsQuery),
	}
}

func (d *Daemon) hubHandleIncidents(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("INCIDENTS").dispatch(ctx, conn, cmd, d.incidentsActions())
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

	qf, err := incidentQueryFilterToInternal(filter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}
	var entries []incident.InboxEntry
	var stats incident.Stats
	if filter.MarkRead {
		entries, stats = d.incidentBus.QueryAndMarkSession(sessionCode, qf, func(snapshot []incident.InboxEntry) []string {
			returned := returnedIncidentEntries(snapshot, filter)
			fingerprints := make([]string, len(returned))
			for i := range returned {
				fingerprints[i] = returned[i].Fingerprint
			}
			return fingerprints
		}, true)
	} else {
		entries, stats = d.incidentBus.QuerySession(sessionCode, qf)
	}

	result := buildIncidentQueryResultWithHydrator(entries, stats, filter, func(hash string) ([]byte, error) {
		payload, _, err := d.incidentBus.ReadSessionBlob(sessionCode, hash)
		return payload, err
	})
	// HasSession distinguishes an empty registered inbox from an unavailable
	// session pipeline (for example during teardown). Project config never
	// disables incident recording; alerts.push controls interrupts only.
	result.PipelineEnabled = d.incidentBus.HasSession(sessionCode)

	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}

// incidentQueryFilterToInternal converts the wire filter to the internal query filter.
// When the caller bounds the page (Limit > 0) we over-fetch by one so the hub can
// detect truncation itself and then truncate + compute the cursor + mark-read over
// exactly the page the caller will see. Doing the over-fetch here (rather than the
// tool inflating Limit and truncating client-side) is what keeps the dropped record
// from being marked read and swept past the cursor unseen.
func incidentQueryFilterToInternal(f protocol.IncidentQueryFilter) (incident.QueryFilter, error) {
	limit := f.Limit
	if limit > 0 {
		limit++
	}
	qf := incident.QueryFilter{Limit: limit}
	for _, s := range f.Severities {
		qf.Severities = append(qf.Severities, incident.Severity(s))
	}
	if f.Since != "" {
		// A `since` we cannot parse must not be dropped: silently ignoring it
		// returns the whole inbox to a caller who asked for a slice of it.
		t, fingerprint, hasFingerprint, err := decodeIncidentCursor(f.Since)
		if err != nil {
			return incident.QueryFilter{}, fmt.Errorf("invalid since %q: want incident cursor or RFC3339/RFC3339Nano", f.Since)
		}
		qf.Since = t
		qf.SinceFingerprint = fingerprint
		qf.HasSinceFingerprint = hasFingerprint
	}
	return qf, nil
}

// buildIncidentQueryResult maps inbox entries to the wire result type.
func buildIncidentQueryResult(entries []incident.InboxEntry, stats incident.Stats, filter protocol.IncidentQueryFilter) protocol.IncidentQueryResult {
	return buildIncidentQueryResultWithHydrator(entries, stats, filter, nil)
}

func buildIncidentQueryResultWithHydrator(entries []incident.InboxEntry, stats incident.Stats, filter protocol.IncidentQueryFilter,
	hydrate func(string) ([]byte, error),
) protocol.IncidentQueryResult {
	// The query over-fetched by one (see incidentQueryFilterToInternal) so the
	// hub can detect truncation itself, then truncate + compute the cursor +
	// mark-read over exactly the page the caller will see.
	//
	// Entries are the OLDEST matching page, newest-first, so the surplus is
	// entries[0] and the page to keep is the tail. Keeping the head instead
	// would strand the oldest incident below the cursor published here, and a
	// `since=cursor` pull would never return it again.
	//
	// Truncate before the secondary filters run: they can drop the surplus and
	// hide the fact that the inbox had more matching entries.
	examined, truncated := examinedIncidentEntries(entries, filter)
	filtered := returnedIncidentEntries(entries, filter)

	records := make([]protocol.IncidentRecord, 0, len(filtered))
	for _, e := range filtered {
		records = append(records, incidentEntryToRecord(e, filter.Detail, hydrate))
	}

	// Cursor = newest entry examined, not newest returned. Everything older was
	// either returned or rejected by a secondary filter, and everything newer is
	// still ahead of the cursor — so successive `since=cursor` pulls sweep the
	// inbox gap-free. Deriving it from `records` instead would stall a filtered
	// query forever on a page where nothing matched.
	var cursor string
	if len(examined) > 0 {
		cursor = encodeIncidentCursor(examined[0].LastSeenAt, examined[0].Fingerprint)
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
		Cursor:    cursor,
		Truncated: truncated,
	}
}

type incidentCursor struct {
	Time        string `json:"t"`
	Fingerprint string `json:"f"`
}

func encodeIncidentCursor(at time.Time, fingerprint string) string {
	payload, _ := json.Marshal(incidentCursor{Time: at.Format(time.RFC3339Nano), Fingerprint: fingerprint})
	return "v1." + base64.RawURLEncoding.EncodeToString(payload)
}

func decodeIncidentCursor(cursor string) (time.Time, string, bool, error) {
	if !strings.HasPrefix(cursor, "v1.") {
		at, err := time.Parse(time.RFC3339Nano, cursor)
		return at, "", false, err
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(cursor, "v1."))
	if err != nil {
		return time.Time{}, "", false, err
	}
	var decoded incidentCursor
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return time.Time{}, "", false, err
	}
	if decoded.Fingerprint == "" {
		return time.Time{}, "", false, fmt.Errorf("cursor fingerprint is empty")
	}
	at, err := time.Parse(time.RFC3339Nano, decoded.Time)
	if err != nil {
		return time.Time{}, "", false, err
	}
	return at, decoded.Fingerprint, true, nil
}

func examinedIncidentEntries(entries []incident.InboxEntry, filter protocol.IncidentQueryFilter) ([]incident.InboxEntry, bool) {
	examined := entries
	truncated := false
	if filter.Limit > 0 && len(examined) > filter.Limit {
		truncated = true
		examined = examined[len(examined)-filter.Limit:]
	}
	return examined, truncated
}

func returnedIncidentEntries(entries []incident.InboxEntry, filter protocol.IncidentQueryFilter) []incident.InboxEntry {
	examined, _ := examinedIncidentEntries(entries, filter)
	return applySecondaryFilters(examined, filter)
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
			if len(sourceSet) == 0 && filter.ProxyID == "" && filter.ProcessID == "" {
				out = append(out, e)
			}
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
func incidentEntryToRecord(e incident.InboxEntry, detail string, hydrate func(string) ([]byte, error)) protocol.IncidentRecord {
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
			if hydrate != nil {
				if payload, err := hydrate(e.Sample.PayloadRef.Hash); err == nil {
					full := string(payload)
					r.Payload = &full
				}
			}
		}
	}

	if r.ID == "" {
		r.ID = e.Fingerprint
	}

	return r
}
