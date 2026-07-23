package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/sessionhost"
	"github.com/standardbeagle/agnt/internal/shims"
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
	return newCommandRouter("SESSION-HOST").dispatch(ctx, conn, cmd, d.sessionHostActions())
}

func (d *Daemon) sessionHostActions() map[string]handlerFn {
	return map[string]handlerFn{
		"CREATE": noCtx(d.hubHandleSessionHostCreate),
		"LIST":   noCtx(d.hubHandleSessionHostList),
		"NOTICE": noCtx(d.hubHandleSessionHostNotice),
		"KILL":   noCtx(d.hubHandleSessionHostKill),
		"RESIZE": noCtx(d.hubHandleSessionHostResize),
		"STDIN":  noCtx(d.hubHandleSessionHostStdin),
		"DETACH": noCtx(d.hubHandleSessionHostDetach),
		"ATTACH": func(c context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
			return d.hubHandleSessionHostAttach(c, conn, cmd)
		},
	}
}

type sessionHostNoticePayload struct {
	SessionName string `json:"session_name"`
	ProjectPath string `json:"project_path"`
	Message     string `json:"message"`
}

func (d *Daemon) hubHandleSessionHostNotice(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	payload, err := unmarshalCommand[sessionHostNoticePayload](cmd)
	if err != nil || payload.SessionName == "" || payload.Message == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SESSION-HOST NOTICE requires session_name and message")
	}
	delivered, dispatchErrs := d.eventHub.BroadcastAgentNotice(AgentNotice(payload))
	if delivered == 0 {
		return conn.WriteErr(hubproto.ErrInvalidState, fmt.Sprintf("agent notice not delivered: %v", errors.Join(dispatchErrs...)))
	}
	return conn.WriteOK("agent notice delivered")
}

type sessionHostAgentNoticeSink struct {
	registry *sessionhost.Registry
}

func (s sessionHostAgentNoticeSink) DeliverAgentNotice(notice AgentNotice) error {
	for _, candidate := range s.registry.List(notice.ProjectPath, false) {
		if candidate.Name != notice.SessionName {
			continue
		}
		if err := candidate.WriteStdin([]byte(notice.Message + "\n")); err != nil {
			return fmt.Errorf("session-host notice PTY delivery: %w", err)
		}
		return nil
	}
	return fmt.Errorf("session-host notice dropped: session %q not found for project %q", notice.SessionName, notice.ProjectPath)
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

	// Record the shim install sessionhost.Create made (PATH injection) so
	// daemon shutdown / startup sweep / watcher cleanup can find the bin
	// dir. Without this the dir would leak — the comment in sessionhost.go
	// claiming a client-side REGISTER was wrong: session-host creates have
	// no client-side shim step.
	if s.ProjectPath != "" {
		if binDir := shims.BinDir(s.ProjectPath); binDir != "" {
			if _, err := os.Stat(binDir); err == nil {
				if err := shims.RecordInstall(s.ProjectPath, binDir, s.ID); err != nil {
					debug.Log("daemon", "session-host shim register failed for %s: %v", s.ProjectPath, err)
				} else {
					d.ensureShimWatcher()
				}
			}
		}
	}

	// Also register into the shared SessionRegistry (spec §2.3) so project
	// scoping helpers (hasOtherSessions, FindByDirectory) see it, tagged
	// Kind=session-host so doCleanupExact's guard (daemon_session_cleanup.go)
	// never reaps its pgid on a classic-session disconnect path.
	//
	// Route the Register through the per-code session lifecycle gate so registry
	// mutations on this id are serialized with any classic SESSION REGISTER /
	// reconnect ReplaceExact — the gate's job is to serialize lifecycle mutations.
	// A collision here IS reachable: session-host ids ("<cfg.Name>-<idCounter>")
	// and classic codes ("<command-base>-<seq>") come from two independent
	// counters over a user-supplied name, so `--name claude` can produce
	// `claude-3` on both sides. The structural defense is on the classic side:
	// hubHandleSessionRegister's cross-kind guard (hub_session.go) refuses to
	// re-register against a session-host owned code, so a classic REGISTER can
	// never ReplaceExact this entry. Scope the gate to the registry mutation
	// alone: sessionhost.Create above already spawned the PTY and touches no
	// gate, so there is no re-entrancy on this code's gate.
	func() {
		unlock := d.sessionLifecycle.lock(s.ID)
		defer unlock()
		err := d.sessionRegistry.Register(&Session{
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
		if err != nil {
			// The session-host PTY is already spawned and tracked in
			// d.sessionHosts, so CREATE still succeeds — but a failed shared-
			// registry Register leaves it invisible to project-scoping helpers
			// (SESSION LIST/FindByDirectory/hasOtherSessions). A code collision on
			// this id is reachable (session-host and classic ids are independent
			// counters over a user-supplied name), so a Register failure is a real
			// anomaly, not a benign no-op, and silently swallowing it would violate
			// the Silent Failure Prohibition (.claude/rules/daemon-architecture.md).
			// Surface it on the agent-visible startup log (queryable via
			// get_errors). Scope it to the project so the owning session's default
			// (project-scoped) query sees it; a daemon-wide (empty-ProcessID) entry
			// would be visible only to a global query. Fall back to the daemon-wide
			// log only when there is no project path to scope by.
			msg := fmt.Sprintf("session-host %s live but not project-scoped: shared-registry Register failed: %v", s.ID, err)
			debug.Warn("daemon", "%s", msg)
			if s.ProjectPath != "" {
				d.startupLog(s.ProjectPath).Warn(s.Name, "session_host_register_failed", msg)
			} else {
				d.daemonStartupLog("warning", "session_host_register_failed", msg)
			}
		}
	}()

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
	filter, _ := unmarshalCommand[protocol.DirectoryFilter](cmd)
	projectPath, global, err := d.resolveProjectScope(filter, conn.SessionCode())
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

	// Remove the shared-registry entry under the per-code lifecycle gate, using
	// the exact-identity form to match the classic retirement path
	// (finalizeSessionRetirement). The gate serializes lifecycle mutations on
	// this code, and the exact form guards the Get-to-delete window so a change
	// that lands between the Get and the delete cannot be clobbered. A cross-kind
	// collision on this id is reachable (the ids come from two independent
	// counters over a user-supplied name), but a classic REGISTER cannot hold or
	// replace this entry because hubHandleSessionRegister's cross-kind guard
	// (hub_session.go) rejects re-registering against a session-host owned code —
	// so UnregisterExact here only ever removes our own entry. The pgid kill above
	// stays deliberately OUTSIDE the gate: it mutates
	// no registry, and holding the gate across a SIGTERM→SIGKILL escalation (up to
	// the grace period) would needlessly block a same-code reconnect. Only the
	// registry mutation is gated — the narrowest possible scope, and no gated call
	// here re-enters this code's gate, so there is no deadlock.
	func() {
		unlock := d.sessionLifecycle.lock(id)
		defer unlock()
		if entry, ok := d.sessionRegistry.Get(id); ok {
			d.sessionRegistry.UnregisterExact(id, entry)
		}
	}()

	// Detach the killed session from the project's shim manifest entry and
	// drop the bin dir when no session (classic or session-host) remains.
	// Runs for EVERY kill, not just the last — see releaseProjectShims.
	d.releaseProjectShims(s.ProjectPath, id)

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
