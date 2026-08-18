package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleScope(_ context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	// RESOLVE is registered as a sub-verb for SCOPE (hub_run.go), so the wire
	// parser lifts it into cmd.SubVerb and leaves cmd.Args empty — reading
	// cmd.Args[0] here made this handler reject EVERY real "SCOPE RESOLVE" call
	// with "expected SCOPE RESOLVE" (the parser never leaves the token in Args
	// once it is registered). Dispatch on cmd.SubVerb like every other router
	// handler, tolerating a stray Args[0] spelling for a hand-built command.
	sub := cmd.SubVerb
	if sub == "" && len(cmd.Args) > 0 {
		sub = cmd.Args[0]
	}
	if sub != protocol.SubVerbResolve {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "expected SCOPE RESOLVE")
	}
	filter, err := unmarshalCommand[protocol.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
	}
	_, global, err := d.resolveProjectScope(filter, conn.SessionCode())
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}
	data, err := json.Marshal(map[string]bool{"global": global})
	if err != nil {
		return err
	}
	return conn.WriteJSON(data)
}
