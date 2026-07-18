package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleProxyLog(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("PROXYLOG").dispatch(ctx, conn, cmd, d.proxyLogActions())
}

func (d *Daemon) proxyLogActions() map[string]handlerFn {
	return map[string]handlerFn{
		"QUERY":   noCtx(d.hubHandleProxyLogQuery),
		"":        noCtx(d.hubHandleProxyLogQuery),
		"SUMMARY": noCtx(d.hubHandleProxyLogSummary),
		"CLEAR":   noCtx(d.hubHandleProxyLogClear),
		"STATS":   noCtx(d.hubHandleProxyLogStats),
	}
}

// hubHandleProxyLogQuery handles PROXYLOG QUERY command.

func (d *Daemon) hubHandleProxyLogQuery(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXYLOG requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	filter, err := convertLogQueryFilter(cmd.Data)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	entries := p.Logger().Query(filter)
	if filter.OmitBodies {
		entries = stripHTTPBodies(entries)
	}
	stats := p.Logger().Stats()

	data, _ := json.Marshal(map[string]interface{}{
		"entries":         entries,
		"count":           len(entries),
		"total_available": stats.AvailableEntries,
		"dropped":         stats.Dropped,
	})
	return conn.WriteJSON(data)
}

// convertLogQueryFilter unmarshals protocol LogQueryFilter data into a proxy.LogFilter,
// parsing string Since/Until values into *time.Time.

func convertLogQueryFilter(data []byte) (proxy.LogFilter, error) {
	if len(data) == 0 {
		return proxy.LogFilter{}, nil
	}

	var pf protocol.LogQueryFilter
	if err := json.Unmarshal(data, &pf); err != nil {
		// The filter is produced by marshaling a valid protocol.LogQueryFilter
		// on the client, so a failure here means wire corruption. Returning an
		// empty (unfiltered) filter would silently widen the query and mislead
		// the agent into reading the wrong result, so fail loud instead.
		debug.Log("proxylog", "PROXYLOG QUERY filter decode failed: %v", err)
		return proxy.LogFilter{}, fmt.Errorf("PROXYLOG QUERY filter decode failed: %w", err)
	}

	since, err := parseTimeString(pf.Since)
	if err != nil {
		return proxy.LogFilter{}, fmt.Errorf("PROXYLOG QUERY invalid 'since': %w", err)
	}
	until, err := parseTimeString(pf.Until)
	if err != nil {
		return proxy.LogFilter{}, fmt.Errorf("PROXYLOG QUERY invalid 'until': %w", err)
	}

	filter := proxy.LogFilter{
		Methods:          pf.Methods,
		URLPattern:       pf.URLPattern,
		StatusCodes:      pf.StatusCodes,
		Limit:            pf.Limit,
		Since:            since,
		Until:            until,
		ErrorsOnly:       pf.ErrorsOnly,
		DiagnosticLevels: pf.DiagnosticLevels,
		InteractionTypes: pf.InteractionTypes,
		MutationTypes:    pf.MutationTypes,
		Frames:           pf.Frames,
		MessagePattern:   pf.MessagePattern,
		MinDurationMs:    pf.MinDurationMs,
		OmitBodies:       pf.OmitBodies,
	}

	for _, t := range pf.Types {
		filter.Types = append(filter.Types, proxy.LogEntryType(t))
	}

	return filter, nil
}

// parseTimeString parses a time string as either an RFC3339 timestamp or a Go
// duration (e.g. "5m", "1h"), the latter interpreted as that long ago from now.
// An empty string yields (nil, nil) — no time bound.
//
// A non-empty value that parses as neither is a loud error rather than a
// silently-dropped filter: returning nil for unparseable input would widen the
// query without telling the agent its time bound was ignored, so it would then
// trust results from outside the window it asked for.
func parseTimeString(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		t := time.Now().Add(-d)
		return &t, nil
	}
	return nil, fmt.Errorf("%q is neither an RFC3339 timestamp nor a Go duration (e.g. '5m', '1h')", s)
}

// stripHTTPBodies returns a copy of entries with HTTP request/response bodies
// and headers cleared, so the summary path does not ship 10KB-per-request
// payloads over IPC only to discard them. It copies the pointed-to HTTPLogEntry
// before clearing (the Query result shares the ring buffer's payload pointers),
// so the stored traffic log is never mutated.
func stripHTTPBodies(entries []proxy.LogEntry) []proxy.LogEntry {
	out := make([]proxy.LogEntry, len(entries))
	for i, e := range entries {
		if e.Type == proxy.LogTypeHTTP && e.HTTP != nil {
			h := *e.HTTP
			h.RequestBody = ""
			h.ResponseBody = ""
			h.RequestHeaders = nil
			h.ResponseHeaders = nil
			e.HTTP = &h
		}
		out[i] = e
	}
	return out
}

// hubHandleProxyLogSummary handles PROXYLOG SUMMARY command.

func (d *Daemon) hubHandleProxyLogSummary(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXYLOG SUMMARY requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// For summary, we return stats plus recent entries
	stats := p.Logger().Stats()

	resp := map[string]interface{}{
		"stats": stats,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProxyLogClear handles PROXYLOG CLEAR command.

func (d *Daemon) hubHandleProxyLogClear(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXYLOG CLEAR requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	p.Logger().Clear()
	return conn.WriteOK("logs cleared")
}

// hubHandleProxyLogStats handles PROXYLOG STATS command.

func (d *Daemon) hubHandleProxyLogStats(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXYLOG STATS requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	stats := p.Logger().Stats()

	data, _ := json.Marshal(stats)
	return conn.WriteJSON(data)
}

// hubHandleCurrentPage handles the CURRENTPAGE command.
