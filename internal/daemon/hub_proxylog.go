package daemon

import (
	"context"
	"encoding/json"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleProxyLog(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("PROXYLOG").dispatch(ctx, conn, cmd, map[string]handlerFn{
		"QUERY":   noCtx(d.hubHandleProxyLogQuery),
		"":        noCtx(d.hubHandleProxyLogQuery),
		"SUMMARY": noCtx(d.hubHandleProxyLogSummary),
		"CLEAR":   noCtx(d.hubHandleProxyLogClear),
		"STATS":   noCtx(d.hubHandleProxyLogStats),
	})
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

	filter := convertLogQueryFilter(cmd.Data)

	entries := p.Logger().Query(filter)
	stats := p.Logger().Stats()

	data, _ := json.Marshal(map[string]interface{}{
		"entries":         entries,
		"count":           len(entries),
		"total_available": stats.AvailableEntries,
	})
	return conn.WriteJSON(data)
}

// convertLogQueryFilter unmarshals protocol LogQueryFilter data into a proxy.LogFilter,
// parsing string Since/Until values into *time.Time.

func convertLogQueryFilter(data []byte) proxy.LogFilter {
	if len(data) == 0 {
		return proxy.LogFilter{}
	}

	var pf protocol.LogQueryFilter
	json.Unmarshal(data, &pf)

	filter := proxy.LogFilter{
		Methods:     pf.Methods,
		URLPattern:  pf.URLPattern,
		StatusCodes: pf.StatusCodes,
		Limit:       pf.Limit,
		Since:       parseTimeString(pf.Since),
		Until:       parseTimeString(pf.Until),
	}

	for _, t := range pf.Types {
		filter.Types = append(filter.Types, proxy.LogEntryType(t))
	}

	return filter
}

// parseTimeString parses a time string as either RFC3339 or a Go duration.
// Duration strings like "5m" are interpreted as that duration ago from now.

func parseTimeString(s string) *time.Time {
	if s == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t
	}
	if d, err := time.ParseDuration(s); err == nil {
		t := time.Now().Add(-d)
		return &t
	}
	return nil
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
