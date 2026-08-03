package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
		"PIN":   noCtx(d.hubHandleIncidentsPin),
		"UNPIN": noCtx(d.hubHandleIncidentsUnpin),
		"CLEAR": noCtx(d.hubHandleIncidentsClear),
	}
}

// incidentSession resolves the inbox the caller may operate on. Retention is a
// per-session inbox operation, so the ONLY addressable inbox is the one bound
// to this connection — mirroring INCIDENTS QUERY and preserving numbered
// contract 1 (per-session isolation). A session-less call fails loud rather
// than falling back to some other session's inbox.
func (d *Daemon) incidentSession(conn *hubpkg.Connection) (string, error) {
	if d.incidentBus == nil {
		return "", fmt.Errorf("incident pipeline not initialized")
	}
	sessionCode := conn.SessionCode()
	if sessionCode == "" {
		return "", fmt.Errorf("no session attached — call SESSION ATTACH first")
	}
	return sessionCode, nil
}

// hubHandleIncidentsPin handles INCIDENTS PIN: marks one inbox entry exempt
// from band eviction and from every retention clear until it is unpinned.
func (d *Daemon) hubHandleIncidentsPin(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	sessionCode, err := d.incidentSession(conn)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}
	payload, err := unmarshalCommand[protocol.IncidentPinPayload](cmd)
	if err != nil || payload.Fingerprint == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "INCIDENTS PIN requires {fingerprint}")
	}

	entry, err := d.incidentBus.PinSession(sessionCode, payload.Fingerprint, payload.Tag)
	switch {
	case errors.Is(err, incident.ErrPinLimitReached):
		// Fail loud with the bound named: silently evicting an older pin to make
		// room would destroy a record the agent explicitly asked to keep.
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf(
			"pin limit reached (%d pinned entries) — unpin one before pinning another", incident.MaxPinnedEntries))
	case errors.Is(err, incident.ErrPinTargetNotFound):
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf(
			"no incident with fingerprint %q in this session's inbox", payload.Fingerprint))
	case err != nil:
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	data, _ := json.Marshal(protocol.IncidentPinResult{
		Fingerprint: entry.Fingerprint,
		Pinned:      true,
		Tag:         entry.Tag,
		PinnedCount: d.incidentBus.PinnedCountSession(sessionCode),
		PinLimit:    incident.MaxPinnedEntries,
		Message: fmt.Sprintf("incident %s pinned%s — survives eviction and every retention clear until unpinned",
			entry.Fingerprint, tagSuffix(entry.Tag)),
	})
	return conn.WriteJSON(data)
}

// hubHandleIncidentsUnpin handles INCIDENTS UNPIN: the entry becomes evictable
// and clearable again.
func (d *Daemon) hubHandleIncidentsUnpin(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	sessionCode, err := d.incidentSession(conn)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}
	payload, err := unmarshalCommand[protocol.IncidentPinPayload](cmd)
	if err != nil || payload.Fingerprint == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "INCIDENTS UNPIN requires {fingerprint}")
	}
	if !d.incidentBus.UnpinSession(sessionCode, payload.Fingerprint) {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf(
			"no pinned incident with fingerprint %q in this session's inbox", payload.Fingerprint))
	}
	data, _ := json.Marshal(protocol.IncidentPinResult{
		Fingerprint: payload.Fingerprint,
		Pinned:      false,
		PinnedCount: d.incidentBus.PinnedCountSession(sessionCode),
		PinLimit:    incident.MaxPinnedEntries,
		Message:     fmt.Sprintf("incident %s unpinned — normal retention applies again", payload.Fingerprint),
	})
	return conn.WriteJSON(data)
}

// hubHandleIncidentsClear handles INCIDENTS CLEAR: retires the caller session's
// current incidents, keeping pinned entries.
//
// It routes through the bus's existing FIFO control-clear path (the same one
// the daemon's build-success / proc-stop retention triggers use) rather than
// touching the inbox directly, so an incident published just before the request
// cannot land after the clear and outlive a boundary it predates. There is
// exactly one clear mechanism.
func (d *Daemon) hubHandleIncidentsClear(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	sessionCode, err := d.incidentSession(conn)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	kept := d.incidentBus.PinnedCountSession(sessionCode)
	cleared, ok := d.incidentBus.ClearSessionBeforeSync(sessionCode, time.Now())
	if !ok {
		// Never report a clear that did not run.
		return conn.WriteErr(hubproto.ErrInternal,
			"incident clear was not applied (bus saturated or shutting down) — retry")
	}
	data, _ := json.Marshal(protocol.IncidentClearResult{
		Cleared: cleared,
		Kept:    kept,
		Message: fmt.Sprintf("%d incident(s) cleared, %d pinned kept", cleared, kept),
	})
	return conn.WriteJSON(data)
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

	// Bus overflow is the one loss the inbox itself cannot show: a dropped event
	// never reached a band, so it is absent from both the records and the band
	// stats. Reported as a collection warning so an overloaded pipeline does not
	// present as a complete view. It is cumulative since daemon start, which the
	// wording says outright rather than implying it describes this page.
	if dropped := d.incidentBus.Dropped(); dropped > 0 {
		result.CollectionWarnings = append(result.CollectionWarnings, fmt.Sprintf(
			"%d incident(s) dropped at the bus since daemon start (drop-newest under overflow) — some signals never reached any inbox",
			dropped))
	}

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

	var warnings []string
	records := make([]protocol.IncidentRecord, 0, len(filtered))
	for _, e := range filtered {
		records = append(records, incidentEntryToRecord(e, filter.Detail, hydrate, func(w string) {
			warnings = append(warnings, w)
		}))
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
			// New = unread entries. Left unset it always reported 0, so an agent
			// gating further pulls on `new` would stop polling a non-empty inbox.
			New:     stats.New,
			Dropped: stats.Dropped,
		},
		Cursor:             cursor,
		Truncated:          truncated,
		CollectionWarnings: warnings,
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
// A hydration failure is reported through onWarning rather than swallowed: a
// detail:"full" pull that silently returns no payload is indistinguishable from
// an incident that never had one.
func incidentEntryToRecord(e incident.InboxEntry, detail string, hydrate func(string) ([]byte, error),
	onWarning func(string),
) protocol.IncidentRecord {
	r := protocol.IncidentRecord{
		Fingerprint: e.Fingerprint,
		FirstSeen:   e.FirstSeenAt.Format(time.RFC3339),
		LastSeen:    e.LastSeenAt.Format(time.RFC3339),
		Count:       e.Count,
		Severity:    string(e.Severity),
		Read:        e.Read,
		Pinned:      e.Pinned,
		Tag:         e.Tag,
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
			Location:    e.Sample.Ctx.Location,
			FrameID:     e.Sample.Ctx.FrameID,
			PID:         e.Sample.Ctx.PID,
			Port:        e.Sample.Ctx.Port,
		}
		r.Remediation = protocol.IncidentRemediation{
			PrimaryTool:  e.Sample.Remediation.PrimaryTool,
			PrimaryArgs:  e.Sample.Remediation.PrimaryArgs,
			FallbackTool: e.Sample.Remediation.FallbackTool,
			SkillHint:    e.Sample.Remediation.SkillHint,
		}
		if detail == "full" && e.Sample.PayloadRef != nil && hydrate != nil {
			payload, err := hydrate(e.Sample.PayloadRef.Hash)
			switch {
			case err != nil:
				if onWarning != nil {
					onWarning(fmt.Sprintf("payload hydration failed for incident %s: %v", e.Fingerprint, err))
				}
			default:
				full := string(payload)
				r.Payload = &full
			}
		}
	}

	if r.ID == "" {
		r.ID = e.Fingerprint
	}

	return r
}
