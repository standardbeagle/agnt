package daemon

import (
	"context"
	"fmt"
	runtimedebug "runtime/debug"
	"sort"

	"github.com/standardbeagle/agnt/internal/debug"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// handlerFn is the canonical signature for commandRouter sub-verb handlers.
type handlerFn func(context.Context, *hubpkg.Connection, *hubproto.Command) error

// noCtx adapts a handler that does not need context.
func noCtx(fn func(*hubpkg.Connection, *hubproto.Command) error) handlerFn {
	return func(_ context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
		return fn(conn, cmd)
	}
}

// connOnly adapts a handler that only needs the connection.
func connOnly(fn func(*hubpkg.Connection) error) handlerFn {
	return func(_ context.Context, conn *hubpkg.Connection, _ *hubproto.Command) error {
		return fn(conn)
	}
}

// commandRouter dispatches hub commands to sub-handlers based on cmd.SubVerb.
type commandRouter struct {
	command        string
	defaultHandler handlerFn
}

// newCommandRouter creates a router for the given top-level command verb.
func newCommandRouter(command string) *commandRouter {
	return &commandRouter{command: command}
}

// withDefault sets a custom handler that is invoked when cmd.SubVerb does not
// match any entry in the handlers map.
func (r *commandRouter) withDefault(handler handlerFn) *commandRouter {
	r.defaultHandler = handler
	return r
}

// dispatch looks up the handler for cmd.SubVerb and executes it. If no handler
// is registered, it returns a structured error with a sorted list of valid actions.
//
// Panic isolation: if a handler panics, dispatch recovers, logs the stack, and
// writes an ErrInternal structured error to the client. This prevents a misbehaving
// handler from killing the connection's goroutine with no error envelope visible to
// the caller. Matches the per-sink panic policy in hub_hook.go.
func (r *commandRouter) dispatch(
	ctx context.Context,
	conn *hubpkg.Connection,
	cmd *hubproto.Command,
	handlers map[string]handlerFn,
) (retErr error) {
	debug.Log("daemon", "%s %s: args=%v", r.command, cmd.SubVerb, cmd.Args)

	defer func() {
		if rec := recover(); rec != nil {
			stack := runtimedebug.Stack()
			debug.Log("daemon", "%s %s: handler panic: %v\n%s", r.command, cmd.SubVerb, rec, stack)
			retErr = writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
				Code:    hubproto.ErrInternal,
				Message: fmt.Sprintf("handler panic: %v", rec),
				Command: r.command,
			})
		}
	}()

	if handler, ok := handlers[cmd.SubVerb]; ok {
		return handler(ctx, conn, cmd)
	}
	if r.defaultHandler != nil {
		return r.defaultHandler(ctx, conn, cmd)
	}

	valid := make([]string, 0, len(handlers))
	for action := range handlers {
		if action != "" {
			valid = append(valid, action)
		}
	}
	sort.Strings(valid)

	return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
		Code:         hubproto.ErrInvalidArgs,
		Message:      "unknown " + r.command + " sub-command",
		Command:      r.command,
		ValidActions: valid,
	})
}
