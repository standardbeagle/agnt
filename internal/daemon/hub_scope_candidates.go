package daemon

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// sessionCandidates returns the active sessions a scope-failing query could be
// re-issued against, newest first. It is METADATA ONLY: session code, project
// path, command, start time, kind — never any inbox / incident content — so
// handing this list back on a scope failure never crosses the per-session
// isolation boundary (numbered contract 1, .claude/rules/daemon-architecture.md).
//
// The MCP daemon connection is not session-bound, so a caller with no attached
// session legitimately owns every local session; the list is therefore the
// global active set rather than a project-scoped slice — there is no project to
// scope to at the point this is called.
func (d *Daemon) sessionCandidates() []protocol.SessionCandidate {
	if d.sessionRegistry == nil {
		return nil
	}
	sessions := d.sessionRegistry.ListActive("", true)
	out := make([]protocol.SessionCandidate, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, protocol.SessionCandidate{
			SessionCode: s.Code,
			ProjectPath: s.ProjectPath,
			Command:     s.Command,
			StartedAt:   s.StartedAt.Format(time.RFC3339),
			Kind:        string(s.kindOrClassic()),
		})
	}
	// Newest first: the most recently started session is the most likely intent.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartedAt > out[j].StartedAt
	})
	return out
}

// renderScopeCandidates turns a candidate list into an actionable one-line-per
// message a scope-failing handler returns instead of a bare "no session
// attached". The caller reads the sessions to pick from in the SAME round trip —
// the whole point of progressive disclosure. The `hint` is the "how to pick one"
// instruction (e.g. "re-issue with session_code:<code>" for a read verb, or
// "attach one of these sessions and retry" for a write verb that takes no
// selector).
func renderScopeCandidates(candidates []protocol.SessionCandidate, hint string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(
		"no session attached — %s. %d active session(s):",
		hint, len(candidates)))
	for _, c := range candidates {
		line := "\n  - " + c.SessionCode
		if c.ProjectPath != "" {
			line += "  (" + c.ProjectPath + ")"
		}
		if c.Command != "" {
			line += "  [" + c.Command + "]"
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// noSessionsMessage is the genuine "no valid choice exists" answer: there is no
// session at all to scope to, so an error is correct. It still says how to fix
// it (start a session) rather than dead-ending.
const noSessionsMessage = "no session attached and no active sessions to scope to — start one with `agnt run <cmd>` (or pass global:true to span all projects)"

// writeScopeErr is the progressive-disclosure replacement for the uniform
//
//	return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
//
// that every session-scoped list/query handler used to write after
// resolveProjectScope / resolveScope. When the failure is the "no resolvable
// session" chokepoint error (errNoSessionScope), it returns the candidate list
// to pick from instead of an unactionable error — unless there genuinely is no
// session, in which case an error is still correct (and says how to fix it).
// Any OTHER resolution error (unknown session_code, config load failure) is not
// a disambiguation case and passes through verbatim.
//
// selector names the argument the caller sets to pick a session (typically
// "session_code"); use writeScopeErrHint for a write verb that takes no selector.
func (d *Daemon) writeScopeErr(conn *hubpkg.Connection, err error, selector string) error {
	return d.writeScopeErrHint(conn, err,
		fmt.Sprintf("re-issue with %s:<code> (or global:true to span all)", selector))
}

// writeScopeErrHint is writeScopeErr with a caller-supplied "how to pick"
// instruction, for verbs whose fix is not simply setting a selector.
func (d *Daemon) writeScopeErrHint(conn *hubpkg.Connection, err error, hint string) error {
	if !errors.Is(err, errNoSessionScope) {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}
	candidates := d.sessionCandidates()
	if len(candidates) == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, noSessionsMessage)
	}
	return conn.WriteErr(hubproto.ErrInvalidArgs, renderScopeCandidates(candidates, hint))
}
