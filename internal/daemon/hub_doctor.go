package daemon

import (
	"context"
	"encoding/json"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleDoctor(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "DOCTOR: args=%v", cmd.Args)

	projectPath := ""
	if len(cmd.Args) > 0 {
		projectPath = cmd.Args[0]
	}
	if projectPath == "" {
		projectPath = d.getSessionProjectPath(conn)
	}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	report := d.RunDoctor(checkCtx, projectPath)
	data, err := json.Marshal(report)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}
	return conn.WriteJSON(data)
}

// hubHandleStatus handles the STATUS command.
// Returns full daemon info (Hub's built-in INFO only returns minimal data).
