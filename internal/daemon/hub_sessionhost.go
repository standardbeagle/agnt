package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/sessionhost"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// hubHandleSessionHost dispatches SESSION-HOST sub-verbs. See
// docs/superpowers/specs/2026-07-03-remote-ssh-design.md §1-2.
//
// Transport note: the go-cli-server hub processes one command at a time per
// connection (Connection.Handle reads, dispatches, and only reads the next
// command after the handler returns), so a single connection cannot both
// block inside ATTACH's streaming loop and simultaneously accept STDIN/
// RESIZE/DETACH requests. This implementation therefore splits the spec's
// single bidirectional "frame" stream into: ATTACH (one connection, blocking,
// server→client only, mirroring hubHandleStreamEvents) plus separate
// request/response STDIN/RESIZE/DETACH commands a client issues over another
// connection, keyed by the attach_id returned in ATTACH's first frame. This
// preserves every numbered invariant in the spec (single-writer-per-attach,
// primary-only stdin, detach-is-not-kill, full-ring-then-live replay) while
// fitting the existing transport rather than inventing a new one.
func (d *Daemon) hubHandleSessionHost(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("SESSION-HOST").dispatch(ctx, conn, cmd, map[string]handlerFn{
		"CREATE": noCtx(d.hubHandleSessionHostCreate),
		"LIST":   noCtx(d.hubHandleSessionHostList),
		"KILL":   noCtx(d.hubHandleSessionHostKill),
		"RESIZE": noCtx(d.hubHandleSessionHostResize),
		"STDIN":  noCtx(d.hubHandleSessionHostStdin),
		"DETACH": noCtx(d.hubHandleSessionHostDetach),
		"ATTACH": func(c context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
			return d.hubHandleSessionHostAttach(c, conn, cmd)
		},
	})
}

// hubHandleSessionHostCreate handles SESSION-HOST CREATE.
// SESSION-HOST CREATE -- <json SessionHostCreateConfig>
func (d *Daemon) hubHandleSessionHostCreate(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	cfg, err := unmarshalCommand[protocol.SessionHostCreateConfig](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid SESSION-HOST CREATE payload: %v", err))
	}
	if cfg.Command == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION-HOST CREATE requires a command")
	}

	s, err := sessionhost.Create(sessionhost.CreateConfig{
		Name:        cfg.Name,
		ProjectPath: normalizePath(cfg.ProjectPath),
		Command:     cfg.Command,
		Args:        cfg.Args,
		Cols:        cfg.Cols,
		Rows:        cfg.Rows,
		Env:         cfg.Env,
	})
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to create session-host session: %v", err))
	}
	d.sessionHosts.Add(s)

	// Also register into the shared SessionRegistry (spec §2.3) so project
	// scoping helpers (hasOtherSessions, FindByDirectory) see it, tagged
	// Kind=session-host so doCleanup's guard (daemon_session_cleanup.go)
	// never reaps its pgid on a classic-session disconnect path.
	_ = d.sessionRegistry.Register(&Session{
		Code:        s.ID,
		ProjectPath: s.ProjectPath,
		Command:     s.Command,
		Args:        s.Args,
		StartedAt:   s.StartedAt,
		Status:      SessionStatusActive,
		LastSeen:    s.StartedAt,
		SessionPGID: s.SessionPGID,
		Kind:        SessionKindSessionHost,
	})

	debug.Log("daemon", "SESSION-HOST CREATE: %s (pgid=%d, cmd=%s)", s.ID, s.SessionPGID, s.Command)

	resp := protocol.SessionHostCreateResult{
		SessionID:   s.ID,
		SessionPGID: s.SessionPGID,
	}
	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionHostList handles SESSION-HOST LIST.
// SESSION-HOST LIST [-- <directory_filter_json>]
// Gated: routes through resolveProjectScope like SESSION LIST (spec §2.2
// invariant 12).
func (d *Daemon) hubHandleSessionHostList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	filter, _ := unmarshalCommand[struct {
		Directory   string `json:"directory"`
		Global      bool   `json:"global"`
		SessionCode string `json:"session_code"`
	}](cmd)

	projectPath, global, err := d.resolveProjectScope(protocol.DirectoryFilter{
		Global:      filter.Global,
		SessionCode: filter.SessionCode,
		Directory:   filter.Directory,
	}, conn.SessionCode())
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	sessions := d.sessionHosts.List(projectPath, global)
	list := make([]map[string]interface{}, 0, len(sessions))
	for _, s := range sessions {
		list = append(list, map[string]interface{}{
			"session_id":     s.ID,
			"name":           s.Name,
			"project_path":   s.ProjectPath,
			"command":        s.Command,
			"args":           s.Args,
			"started_at":     s.StartedAt.Format(time.RFC3339),
			"status":         string(s.Status()),
			"session_pgid":   s.SessionPGID,
			"last_attached":  s.LastAttached().Format(time.RFC3339),
			"attached_count": s.AttachedCount(),
		})
	}

	resp := map[string]interface{}{
		"sessions":  list,
		"count":     len(list),
		"directory": projectPath,
		"global":    global,
	}
	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleSessionHostKill handles SESSION-HOST KILL.
// SESSION-HOST KILL <session_id>
// This is the explicit-kill path (spec §2.2 invariant 11a): reaps the PTY
// child's pgid via the same containment primitive classic sessions use, then
// closes the PTY and removes the session from both registries.
func (d *Daemon) hubHandleSessionHostKill(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION-HOST KILL requires: <session_id>")
	}
	id := cmd.Args[0]

	s, ok := d.sessionHosts.Get(id)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session-host session %q not found", id))
	}

	// Only reap the pgid while the child is still alive. Once waitLoop has
	// observed the child exit (StatusExited), cmd.Wait has already reaped the
	// leader and the whole process tree is gone — the spec's numbered
	// invariant (§ Session Containment) states an exited child needs no pgid
	// action. Critically, killing a stale pgid here is not merely redundant:
	// the kernel is free to REUSE the dead child's pid (== pgid, via setsid)
	// for an unrelated, same-uid process group, and syscall.Kill(-pgid,
	// SIGKILL) would then nuke that innocent group (e.g. the user's editor or
	// coding agent). Gate on liveness so a reused pgid is never signalled.
	if s.Status() == sessionhost.StatusRunning && s.SessionPGID > 1 {
		debug.Log("daemon", "SESSION-HOST KILL: reaping pgid %d for session %s", s.SessionPGID, id)
		if err := platform.KillSessionPGID(s.SessionPGID, os.Getpid(), sessionPGIDGracePeriod, false); err != nil {
			debug.Warn("daemon", "SESSION-HOST KILL: killpg(%d) failed: %v", s.SessionPGID, err)
		}
	}
	_ = s.Close()

	d.sessionHosts.Remove(id)
	_ = d.sessionRegistry.Unregister(id)

	return conn.WriteOK(fmt.Sprintf("session-host session %s killed", id))
}

// hubHandleSessionHostResize handles SESSION-HOST RESIZE.
// SESSION-HOST RESIZE <session_id> -- {"cols":N,"rows":N}
// Resize is session-wide (not attach-specific — spec §1.3 invariant 8: no
// partial re-render, applies to all current attaches).
func (d *Daemon) hubHandleSessionHostResize(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION-HOST RESIZE requires: <session_id>")
	}
	id := cmd.Args[0]

	s, ok := d.sessionHosts.Get(id)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session-host session %q not found", id))
	}

	size, err := unmarshalCommand[protocol.SessionHostResizeConfig](cmd)
	if err != nil || (size.Cols <= 0 && size.Rows <= 0) {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION-HOST RESIZE requires cols/rows")
	}

	if err := s.Resize(size.Cols, size.Rows); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("resize failed: %v", err))
	}
	return conn.WriteOK("resized")
}

// sessionHostStdinPayload is the JSON payload for SESSION-HOST STDIN.
type sessionHostStdinPayload struct {
	AttachID string `json:"attach_id"`
	Data     string `json:"data"` // base64-encoded raw bytes
}

// hubHandleSessionHostStdin handles SESSION-HOST STDIN.
// SESSION-HOST STDIN <session_id> -- {"attach_id":"...","data":"<base64>"}
// Rejects stdin from a non-primary attach (spec §1.3 invariant 3) with a
// structured error rather than a silent drop.
func (d *Daemon) hubHandleSessionHostStdin(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION-HOST STDIN requires: <session_id>")
	}
	id := cmd.Args[0]

	s, ok := d.sessionHosts.Get(id)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session-host session %q not found", id))
	}

	payload, err := unmarshalCommand[sessionHostStdinPayload](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid SESSION-HOST STDIN payload: %v", err))
	}
	if !s.IsPrimary(payload.AttachID) {
		return conn.WriteErr(hubproto.ErrInvalidState, "stdin rejected: attach is not primary")
	}

	raw, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid base64 stdin data: %v", err))
	}
	if err := s.WriteStdin(raw); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("stdin write failed: %v", err))
	}
	return conn.WriteOK("stdin written")
}

// sessionHostDetachPayload is the JSON payload for SESSION-HOST DETACH.
type sessionHostDetachPayload struct {
	AttachID string `json:"attach_id"`
}

// hubHandleSessionHostDetach handles SESSION-HOST DETACH.
// SESSION-HOST DETACH <session_id> -- {"attach_id":"..."}
// A detach never touches the PTY child (spec §1.3 invariant 4) — it only
// unregisters this attach's subscriber, which causes its blocked ATTACH
// stream to observe a closed channel and end cleanly.
func (d *Daemon) hubHandleSessionHostDetach(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION-HOST DETACH requires: <session_id>")
	}
	id := cmd.Args[0]

	s, ok := d.sessionHosts.Get(id)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session-host session %q not found", id))
	}

	payload, err := unmarshalCommand[sessionHostDetachPayload](cmd)
	if err != nil || payload.AttachID == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION-HOST DETACH requires attach_id")
	}
	s.Detach(payload.AttachID)
	return conn.WriteOK("detached")
}

// sessionHostAttachedData is the payload of the "attached" frame sent once
// at the start of every ATTACH — before replay-marker — so the client learns
// its attach_id and primary status for subsequent STDIN/RESIZE/DETACH calls.
type sessionHostAttachedData struct {
	AttachID  string `json:"attach_id"`
	IsPrimary bool   `json:"is_primary"`
}

// hubHandleSessionHostAttach handles SESSION-HOST ATTACH.
// SESSION-HOST ATTACH <session_id>
// Long-lived stream, modeled on hubHandleStreamEvents (spec §1.3): one
// goroutine, single writer, replay-then-live, 30s keepalive. Ends when the
// session exits, the attach is explicitly detached (SESSION-HOST DETACH from
// another connection), or the connection drops — none of which touch the PTY
// child (that's the entire point of session-host).
func (d *Daemon) hubHandleSessionHostAttach(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION-HOST ATTACH requires: <session_id>")
	}
	id := cmd.Args[0]

	s, ok := d.sessionHosts.Get(id)
	if !ok {
		return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("session-host session %q not found", id))
	}

	frames, attachID, isPrimary := s.Attach(64)

	if err := conn.WriteOK("streaming"); err != nil {
		s.Detach(attachID)
		return err
	}

	attachedData, _ := json.Marshal(sessionHostAttachedData{AttachID: attachID, IsPrimary: isPrimary})
	attachedFrame, _ := json.Marshal(sessionhost.Frame{Type: "attached", Data: attachedData})
	if err := conn.WriteChunk(attachedFrame); err != nil {
		s.Detach(attachID)
		return nil
	}

	keepalive := time.NewTicker(streamKeepaliveInterval)
	defer keepalive.Stop()
	defer s.Detach(attachID)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.Done():
			// PTY child exited; the "exit" frame was already broadcast by
			// waitLoop and will be delivered via frames below before this
			// case can win a race in practice, but guard anyway.
			return conn.WriteEnd()
		case payload, ok := <-frames:
			if !ok {
				// Detached (by us via defer, or by a DETACH command on
				// another connection).
				return conn.WriteEnd()
			}
			if err := conn.WriteChunk(payload); err != nil {
				debug.Log("sessionhost", "attach %s: write failed, client disconnected: %v", attachID, err)
				return nil
			}
			keepalive.Reset(streamKeepaliveInterval)
		case <-keepalive.C:
			if err := conn.WriteChunk([]byte(`{"type":"keepalive"}`)); err != nil {
				return nil
			}
		}
	}
}
