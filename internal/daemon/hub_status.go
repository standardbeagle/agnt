package daemon

import (
	"context"
	"encoding/json"

	"github.com/standardbeagle/agnt/internal/debug"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleStatus(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "STATUS: args=%v", cmd.Args)
	info := d.Info()
	data, err := json.Marshal(info)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}
	return conn.WriteJSON(data)
}

// hubHandleAutomate handles the AUTOMATE command and its sub-verbs.
