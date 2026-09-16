package daemon

import (
	"context"
	"encoding/json"

	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) investigationActions() map[string]handlerFn {
	return map[string]handlerFn{
		"GET":   noCtx(d.hubHandleInvestigationGet),
		"":      noCtx(d.hubHandleInvestigationGet),
		"MERGE": noCtx(d.hubHandleInvestigationMerge),
	}
}

func (d *Daemon) hubHandleInvestigation(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("INVESTIGATION").dispatch(ctx, conn, cmd, d.investigationActions())
}

// investigationSession resolves which session's record the call addresses:
// an explicit session_code (the MCP daemon connection is never session-bound
// and must name one) or the connection's bound session. Records stay
// hard-isolated per session — the caller merely selects one, mirroring
// INCIDENTS QUERY's read selector (numbered contract 1).
func (d *Daemon) investigationSession(conn *hubpkg.Connection, sessionCode string) (*Session, error) {
	if sessionCode == "" {
		sessionCode = conn.SessionCode()
	}
	if sessionCode == "" {
		return nil, errNoSessionScope
	}
	session, ok := d.sessionRegistry.Get(sessionCode)
	if !ok {
		return nil, nil
	}
	return session, nil
}

func (d *Daemon) writeInvestigationResult(conn *hubpkg.Connection, sessionCode string, session *Session) error {
	result := protocol.InvestigationResult{SessionCode: sessionCode}
	if session != nil {
		if inv := session.Investigation(); inv != nil {
			result.Found = true
			result.Investigation = inv
		}
	}
	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}

// hubHandleInvestigationGet handles INVESTIGATION GET: reads the record for
// the named (or bound) session. A retired session reads empty — Found false,
// Investigation nil — never a stale record.
func (d *Daemon) hubHandleInvestigationGet(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	var req protocol.InvestigationGetRequest
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &req); err != nil {
			return conn.WriteErr(hubproto.ErrInvalidArgs, "INVESTIGATION GET: invalid payload")
		}
	}
	sessionCode := req.SessionCode
	if sessionCode == "" {
		sessionCode = conn.SessionCode()
	}
	session, err := d.investigationSession(conn, req.SessionCode)
	if err != nil {
		return d.writeScopeErrHint(conn, err,
			"attach one of these sessions (agnt auto-attaches by cwd) or pass session_code and retry")
	}
	return d.writeInvestigationResult(conn, sessionCode, session)
}

// hubHandleInvestigationMerge handles INVESTIGATION MERGE: applies the patch
// to the named (or bound) session's record replace-on-write and returns the
// merged record. Merging into a session that no longer exists fails loud
// rather than silently starting a record nowhere.
func (d *Daemon) hubHandleInvestigationMerge(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[protocol.InvestigationMergeRequest](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "INVESTIGATION MERGE requires {patch}")
	}
	session, err := d.investigationSession(conn, req.SessionCode)
	if err != nil {
		return d.writeScopeErrHint(conn, err,
			"attach one of these sessions (agnt auto-attaches by cwd) or pass session_code and retry")
	}
	if session == nil {
		code := req.SessionCode
		if code == "" {
			code = conn.SessionCode()
		}
		return conn.WriteErr(hubproto.ErrNotFound,
			"session "+code+" not found — investigation records die with their session")
	}
	merged := session.MergeInvestigation(req.Patch)
	data, _ := json.Marshal(protocol.InvestigationResult{
		SessionCode:   session.Code,
		Found:         true,
		Investigation: merged,
	})
	return conn.WriteJSON(data)
}
