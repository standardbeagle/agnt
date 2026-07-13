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
	if len(cmd.Args) == 0 || cmd.Args[0] != protocol.SubVerbResolve {
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
