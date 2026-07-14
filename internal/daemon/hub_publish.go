package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon/publishstore"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/publish"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// publishActions maps the PUBLISH sub-verbs to their handlers. All five are
// control-plane, session-scoped (spec §2a): the public share-verify path
// (publishstore.VerifyToken) is deliberately NOT reachable here — it belongs to
// the separate public plane (P7) and carries no project scope (INV-1).
func (d *Daemon) publishActions() map[string]handlerFn {
	return map[string]handlerFn{
		protocol.SubVerbCreate: noCtx(d.hubHandlePublishCreate),
		protocol.SubVerbStatus: noCtx(d.hubHandlePublishStatus),
		protocol.SubVerbList:   noCtx(d.hubHandlePublishList),
		protocol.SubVerbRevoke: noCtx(d.hubHandlePublishRevoke),
		protocol.SubVerbRotate: noCtx(d.hubHandlePublishRotate),
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
func (d *Daemon) requirePublishStore(conn *hubpkg.Connection) (*publishstore.Store, bool) {
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
	projectPath := d.getSessionProjectPath(conn)
	if projectPath == "" {
		return conn.WriteErr(hubproto.ErrInvalidState, "no active session with project path")
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
	if err := d.assertShareOwned(conn, store, req.ID); err != nil {
		return err
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
	if err := d.assertShareOwned(conn, store, req.ID); err != nil {
		return err
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
	if err := d.assertShareOwned(conn, store, req.ID); err != nil {
		return err
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
	projectPath := d.getSessionProjectPath(conn)
	if projectPath == "" {
		return conn.WriteErr(hubproto.ErrInvalidState, "no active session with project path")
	}
	infos := store.List(projectPath)
	shares := make([]protocol.PublishShareInfo, 0, len(infos))
	for _, info := range infos {
		shares = append(shares, shareInfoToWire(info))
	}
	data, _ := json.Marshal(protocol.PublishListResult{Shares: shares})
	return conn.WriteJSON(data)
}

// assertShareOwned enforces control-plane project scoping: a share may only be
// addressed by id from the session that owns its project. A share belonging to
// another project is reported as not-found (no cross-project leak of existence).
func (d *Daemon) assertShareOwned(conn *hubpkg.Connection, store *publishstore.Store, id string) error {
	projectPath := d.getSessionProjectPath(conn)
	if projectPath == "" {
		return conn.WriteErr(hubproto.ErrInvalidState, "no active session with project path")
	}
	for _, info := range store.List(projectPath) {
		if info.ID == id {
			return nil
		}
	}
	return conn.WriteErr(hubproto.ErrNotFound, "share not found")
}

func publishErr(conn *hubpkg.Connection, err error) error {
	switch {
	case errors.Is(err, publishstore.ErrNotFound):
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	case errors.Is(err, publishstore.ErrRevoked):
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

func shareInfoToWire(info publishstore.ShareInfo) protocol.PublishShareInfo {
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
