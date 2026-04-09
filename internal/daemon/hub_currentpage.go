package daemon

import (
	"context"
	"encoding/json"

	"github.com/standardbeagle/agnt/internal/debug"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleCurrentPage(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "CURRENTPAGE %s: args=%v", cmd.SubVerb, cmd.Args)
	switch cmd.SubVerb {
	case "LIST", "":
		return d.hubHandleCurrentPageList(conn, cmd)
	case "GET":
		return d.hubHandleCurrentPageGet(conn, cmd)
	case "SUMMARY":
		return d.hubHandleCurrentPageSummary(conn, cmd)
	case "CLEAR":
		return d.hubHandleCurrentPageClear(conn, cmd)
	default:
		return conn.WriteStructuredErr(&hubproto.StructuredError{
			Code:         hubproto.ErrInvalidArgs,
			Message:      "unknown CURRENTPAGE sub-command",
			Command:      "CURRENTPAGE",
			ValidActions: []string{"LIST", "GET", "SUMMARY", "CLEAR"},
		})
	}
}

// hubHandleCurrentPageList handles CURRENTPAGE LIST command.

func (d *Daemon) hubHandleCurrentPageList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CURRENTPAGE LIST requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.getSessionScopedProxy(conn, proxyID)
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

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	session, ok := p.PageTracker().GetSession(sessionID)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, "session not found")
	}

	data, _ := json.Marshal(session)
	return conn.WriteJSON(data)
}

// hubHandleCurrentPageSummary handles CURRENTPAGE SUMMARY command.

func (d *Daemon) hubHandleCurrentPageSummary(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 2 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CURRENTPAGE SUMMARY requires: <proxy_id> <session_id>")
	}

	proxyID := cmd.Args[0]
	sessionID := cmd.Args[1]

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	session, ok := p.PageTracker().GetSession(sessionID)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, "session not found")
	}

	// Return a summary of the session
	data, _ := json.Marshal(session)
	return conn.WriteJSON(data)
}

// hubHandleCurrentPageClear handles CURRENTPAGE CLEAR command.

func (d *Daemon) hubHandleCurrentPageClear(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "CURRENTPAGE CLEAR requires: <proxy_id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	p.PageTracker().Clear()
	return conn.WriteOK("page sessions cleared")
}

// hubHandleOverlay handles the OVERLAY command.
