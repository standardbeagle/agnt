package daemon

import (
	"context"
	"encoding/json"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleCurrentPage(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("CURRENTPAGE").dispatch(ctx, conn, cmd, map[string]handlerFn{
		"LIST":    noCtx(d.hubHandleCurrentPageList),
		"":        noCtx(d.hubHandleCurrentPageList),
		"GET":     noCtx(d.hubHandleCurrentPageGet),
		"SUMMARY": noCtx(d.hubHandleCurrentPageSummary),
		"CLEAR":   noCtx(d.hubHandleCurrentPageClear),
	})
}

// hubHandleCurrentPageList handles CURRENTPAGE LIST command.

func (d *Daemon) hubHandleCurrentPageList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CURRENTPAGE LIST requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Return lightweight summaries instead of full sessions with massive arrays
	summaries := p.PageTracker().GetActiveSessionSummaries()

	resp := map[string]interface{}{
		"sessions": summaries,
		"count":    len(summaries),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleCurrentPageGet handles CURRENTPAGE GET command.

func (d *Daemon) hubHandleCurrentPageGet(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 2 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CURRENTPAGE GET requires: <proxy_id> <session_id>")
	}

	proxyID := cmd.Args[0]
	sessionID := cmd.Args[1]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	session, ok := p.PageTracker().GetSession(sessionID)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, "session not found")
	}

	// Project onto the lean wire schema: URL-only resources, per-kind counts,
	// no HTTP bodies/headers. The tool side honours `raw` to expose the
	// (body-free) detail arrays. See compactPageSession.
	data, _ := json.Marshal(compactPageSession(session))
	return conn.WriteJSON(data)
}

// hubHandleCurrentPageSummary handles CURRENTPAGE SUMMARY command.

func (d *Daemon) hubHandleCurrentPageSummary(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 2 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CURRENTPAGE SUMMARY requires: <proxy_id> <session_id>")
	}

	proxyID := cmd.Args[0]
	sessionID := cmd.Args[1]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	session, ok := p.PageTracker().GetSession(sessionID)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, "session not found")
	}

	// Same lean projection as GET; the tool side computes the rollups.
	data, _ := json.Marshal(compactPageSession(session))
	return conn.WriteJSON(data)
}

// hubHandleCurrentPageClear handles CURRENTPAGE CLEAR command.

func (d *Daemon) hubHandleCurrentPageClear(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CURRENTPAGE CLEAR requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	p.PageTracker().Clear()
	return conn.WriteOK("page sessions cleared")
}

// hubHandleOverlay handles the OVERLAY command.
