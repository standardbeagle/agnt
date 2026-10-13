package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/publish"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// publishActions maps the PUBLISH sub-verbs to their handlers. All five are
// control-plane, session-scoped (spec §2a): the public share-verify path
// (publish.VerifyToken) is deliberately NOT reachable here — it belongs to
// the separate public plane (P7) and carries no project scope (INV-1).
func (d *Daemon) publishActions() map[string]handlerFn {
	return map[string]handlerFn{
		protocol.SubVerbCreate:   noCtx(d.hubHandlePublishCreate),
		protocol.SubVerbStatus:   noCtx(d.hubHandlePublishStatus),
		protocol.SubVerbList:     noCtx(d.hubHandlePublishList),
		protocol.SubVerbRevoke:   noCtx(d.hubHandlePublishRevoke),
		protocol.SubVerbRotate:   noCtx(d.hubHandlePublishRotate),
		protocol.SubVerbFeedback: noCtx(d.hubHandlePublishFeedback),
	}
}

func (d *Daemon) hubHandlePublish(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	actions := d.publishActions()
	return newCommandRouter("PUBLISH").
		withDefault(func(_ context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
			return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
				Code:         hubproto.ErrInvalidAction,
				Message:      "unknown PUBLISH sub-command",
				Command:      "PUBLISH",
				ValidActions: routerSubVerbs(actions),
			})
		}).
		dispatch(ctx, conn, cmd, actions)
}

// requirePublishStore returns the store or writes a loud error. A nil store means
// the persisted store failed to load loud on boot (corruption): we surface that
// rather than silently serving an empty store.
func (d *Daemon) requirePublishStore(conn *hubpkg.Connection) (*publish.Store, bool) {
	if d.publishStore == nil {
		_ = conn.WriteErr(hubproto.ErrInvalidState, "publish store unavailable (failed to load on boot); check daemon startup log")
		return nil, false
	}
	return d.publishStore, true
}

// shareURL is the public path bearing the token. The public listener + routing
// live in P7; here we return the canonical relative path so the publisher knows
// where the share will be served.
func shareURL(token string) string { return "/s/" + token }

func (d *Daemon) hubHandlePublishCreate(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[protocol.PublishCreateRequest](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid request JSON: "+err.Error())
	}
	store, ok := d.requirePublishStore(conn)
	if !ok {
		return nil
	}
	projectPath, ok := d.publishProjectPath(conn)
	if !ok {
		return nil
	}
	if len(req.Walkthrough) == 0 {
		return conn.WriteErr(hubproto.ErrMissingParam, "walkthrough is required")
	}
	// Decode + fully validate the artifact through the P2 validators before it is
	// ever stored (immutable validated revision; reject invalid at create).
	pw, err := publish.DecodePublishedWalkthrough(req.Walkthrough)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid walkthrough: "+err.Error())
	}
	id, token, err := store.Create(pw, projectPath)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}
	info, _ := store.Status(id)
	data, _ := json.Marshal(protocol.PublishCreateResult{
		ID:       id,
		Token:    token, // returned exactly once
		ShareURL: shareURL(token),
		Digest:   info.Digest,
	})
	return conn.WriteJSON(data)
}

func (d *Daemon) hubHandlePublishRotate(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[protocol.PublishRotateRequest](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid request JSON: "+err.Error())
	}
	store, ok := d.requirePublishStore(conn)
	if !ok {
		return nil
	}
	if req.ID == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "id is required")
	}
	if !d.shareOwned(conn, store, req.ID) {
		return nil
	}
	token, err := store.Rotate(req.ID)
	if err != nil {
		return publishErr(conn, err)
	}
	data, _ := json.Marshal(protocol.PublishRotateResult{
		ID:       req.ID,
		Token:    token, // fresh token, returned once
		ShareURL: shareURL(token),
	})
	return conn.WriteJSON(data)
}

func (d *Daemon) hubHandlePublishRevoke(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[protocol.PublishRevokeRequest](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid request JSON: "+err.Error())
	}
	store, ok := d.requirePublishStore(conn)
	if !ok {
		return nil
	}
	if req.ID == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "id is required")
	}
	if !d.shareOwned(conn, store, req.ID) {
		return nil
	}
	if err := store.Revoke(req.ID); err != nil {
		return publishErr(conn, err)
	}
	return conn.WriteOK("share revoked")
}

func (d *Daemon) hubHandlePublishStatus(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[protocol.PublishStatusRequest](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid request JSON: "+err.Error())
	}
	store, ok := d.requirePublishStore(conn)
	if !ok {
		return nil
	}
	if req.ID == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "id is required")
	}
	if !d.shareOwned(conn, store, req.ID) {
		return nil
	}
	info, err := store.Status(req.ID)
	if err != nil {
		return publishErr(conn, err)
	}
	data, _ := json.Marshal(shareInfoToWire(info))
	return conn.WriteJSON(data)
}

func (d *Daemon) hubHandlePublishList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	store, ok := d.requirePublishStore(conn)
	if !ok {
		return nil
	}
	projectPath, ok := d.publishProjectPath(conn)
	if !ok {
		return nil
	}
	infos := store.List(projectPath)
	shares := make([]protocol.PublishShareInfo, 0, len(infos))
	for _, info := range infos {
		shares = append(shares, shareInfoToWire(info))
	}
	data, _ := json.Marshal(protocol.PublishListResult{Shares: shares})
	return conn.WriteJSON(data)
}

// publishProjectPath resolves the project a PUBLISH verb acts for, through the
// session-scope chokepoint (resolveProjectScope). On failure it writes the
// error — for a session-less connection, the list of active sessions to attach
// to (writeScopeErrHint), not a bare "no session" — and returns ok=false.
//
// Two deliberate departures from a list/query verb:
//
//   - global is forced OFF. A share has exactly one owning project; "every
//     project" is not an owner, and a scope.default-global config must not turn
//     publish into a cross-project read of every share's feedback.
//   - the path returned is the session's ProjectPath AS REGISTERED, not the
//     normalized form the gate computes. It is the key shares are persisted
//     under (Share.ProjectPath, compared exactly), so normalizing here would
//     orphan every share created before this gate existed.
//
// The PUBLISH wire requests carry no session_code/directory selector, so the
// only scope source is the connection's bound session; the hint says how to
// bind one.
func (d *Daemon) publishProjectPath(conn *hubpkg.Connection) (string, bool) {
	filter := protocol.DirectoryFilter{GlobalOverride: protocol.Bool(false)}
	if _, _, err := d.resolveProjectScope(filter, conn.SessionCode()); err != nil {
		_ = d.writeScopeErrHint(conn, err,
			"publish acts for one project: attach this connection to one of these sessions (SESSION ATTACH <directory>; the MCP server attaches by its working directory) and re-issue")
		return "", false
	}
	projectPath := d.getSessionProjectPath(conn)
	if projectPath == "" {
		_ = conn.WriteErr(hubproto.ErrInvalidState, "no active session with project path")
		return "", false
	}
	return projectPath, true
}

// shareOwned enforces control-plane project scoping: a share may only be
// addressed by id from the session that owns its project. On a scope failure it
// writes the appropriate error to conn and returns false (a share belonging to
// another project is reported as not-found — no cross-project leak of existence).
// It returns false, not an error, because conn.WriteErr itself returns nil.
func (d *Daemon) shareOwned(conn *hubpkg.Connection, store *publish.Store, id string) bool {
	projectPath, ok := d.publishProjectPath(conn)
	if !ok {
		return false
	}
	for _, info := range store.List(projectPath) {
		if info.ID == id {
			return true
		}
	}
	_ = conn.WriteErr(hubproto.ErrNotFound, "share not found")
	return false
}

// hubHandlePublishFeedback is the owner-scoped control-plane read of anonymous
// viewer feedback for a share (spec §2a "publish feedback list"). It enforces
// the SAME project ownership check as status/revoke/rotate (shareOwned): a
// session scoped to project B cannot read project A's feedback — a foreign share
// is reported not-found, no cross-project leak. The response carries feedback
// rows + observability counts (total, rate-limit-dropped) and NEVER a token
// (feedback records structurally hold none).
func (d *Daemon) hubHandlePublishFeedback(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[protocol.PublishFeedbackRequest](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid request JSON: "+err.Error())
	}
	store, ok := d.requirePublishStore(conn)
	if !ok {
		return nil
	}
	if req.ID == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "id is required")
	}
	// Ownership gate: identical to status/revoke/rotate. A share the session's
	// project does not own is not-found here (no existence oracle, no leak).
	if !d.shareOwned(conn, store, req.ID) {
		return nil
	}
	// Existence check only. Status does NOT error on a revoked share — it
	// reports it with revoked=true — so a revoked share's feedback stays readable
	// by its owner, which is the audit trail the tombstone is kept for.
	// shareOwned already proved the caller owns this share.
	if _, err := store.Status(req.ID); err != nil {
		return publishErr(conn, err)
	}

	result := protocol.PublishFeedbackResult{Rows: []protocol.PublishFeedbackRow{}}
	if d.feedbackStore != nil {
		// Read share-scoped, NOT digest-scoped. Two projects publishing the same
		// walkthrough content share a digest; a digest-keyed read would union their
		// feedback and leak project A's rows to project B (INV-1 / spec §2a). The
		// share's feedback lives only in its own ring, so this cannot cross projects.
		rows, next, rerr := d.feedbackStore.ReadByShare(req.ID, req.Cursor, req.Limit)
		if errors.Is(rerr, publish.ErrFeedbackCursorUnknown) {
			// The caller's cursor, not a daemon fault: say so, so it restarts.
			return conn.WriteErr(hubproto.ErrInvalidArgs, rerr.Error())
		}
		if rerr != nil {
			return conn.WriteErr(hubproto.ErrInternal, rerr.Error())
		}
		wire := make([]protocol.PublishFeedbackRow, 0, len(rows))
		for _, r := range rows {
			wire = append(wire, protocol.PublishFeedbackRow{
				ID:        r.ID,
				CreatedAt: r.CreatedAt.Format(time.RFC3339),
				Body:      r.Body,
			})
		}
		result.Rows = wire
		result.NextCursor = next
		result.Total = d.feedbackStore.Count(req.ID)
		result.Dropped = d.feedbackStore.DroppedByShare(req.ID)
	}
	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}

func publishErr(conn *hubpkg.Connection, err error) error {
	switch {
	case errors.Is(err, publish.ErrNotFound):
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	case errors.Is(err, publish.ErrRevoked):
		return conn.WriteErr(hubproto.ErrInvalidState, err.Error())
	default:
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}
}

func rfc3339Ptr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

func shareInfoToWire(info publish.ShareInfo) protocol.PublishShareInfo {
	return protocol.PublishShareInfo{
		ID:              info.ID,
		Title:           info.Title,
		Steps:           info.Steps,
		Digest:          info.Digest,
		TokenHashPrefix: info.TokenHashPrefix,
		Revoked:         info.Revoked,
		CreatedAt:       info.CreatedAt.Format(time.RFC3339),
		RotatedAt:       rfc3339Ptr(info.RotatedAt),
		RevokedAt:       rfc3339Ptr(info.RevokedAt),
	}
}
