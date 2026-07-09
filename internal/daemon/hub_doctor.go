package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleDoctor(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	// projectPath is a filesystem path, carried in the JSON data frame. A
	// malformed frame must not silently degrade to the session's project — the
	// caller asked to diagnose a specific directory.
	meta, err := unmarshalCommand[struct {
		Directory string `json:"directory"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid DOCTOR payload: %v", err))
	}
	projectPath := meta.Directory
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
