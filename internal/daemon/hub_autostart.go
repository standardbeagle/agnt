package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleAutostart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	switch cmd.SubVerb {
	case protocol.SubVerbClearPorts:
		return d.hubHandleAutostartClearPorts(ctx, conn, cmd)
	case protocol.SubVerbContinue:
		return d.hubHandleAutostartContinue(ctx, conn, cmd)
	default:
		return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
			Code:         hubproto.ErrInvalidAction,
			Message:      "unknown action",
			Command:      "AUTOSTART",
			Action:       cmd.SubVerb,
			ValidActions: []string{protocol.SubVerbClearPorts, protocol.SubVerbContinue},
		})
	}
}

// hubHandleAutostartClearPorts handles AUTOSTART CLEAR-PORTS <projectPath>.
// Kills processes blocking declared ports, then resumes the pending autostart.

func (d *Daemon) hubHandleAutostartClearPorts(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "project path required")
	}
	projectPath := normalizePath(cmd.Args[0])

	val, ok := d.pendingAutostarts.Load(projectPath)
	if !ok {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "no pending autostart for this project")
	}
	pending := val.(*pendingAutostart)

	// Kill blockers using ProcessManager's full escalation path.
	// Use context.Background() since kill+resume may outlive the IPC request.
	log := d.startupErrorStore
	killResults := killPortBlockers(context.Background(), d.hub.ProcessManager(), pending.conflicts)
	var cleared []PortConflict
	for _, kr := range killResults {
		if kr.Killed {
			cleared = append(cleared, kr.PortConflict)
			log.Info(makeProcessID(projectPath, kr.ScriptName), kr.ScriptName,
				"port_conflict_killed",
				fmt.Sprintf("cleared port %d (was: %s PIDs %v)", kr.Port, kr.ProcessName, kr.PIDs))
		} else {
			log.Error(makeProcessID(projectPath, kr.ScriptName), kr.ScriptName,
				"port_conflict_failed", kr.Error)
		}
	}

	// Resume autostart with background context
	result := d.resumeAutostart(context.Background(), projectPath)
	result.PortsCleared = cleared

	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}

// hubHandleAutostartContinue handles AUTOSTART CONTINUE <projectPath>.
// Resumes the pending autostart without killing port blockers.

func (d *Daemon) hubHandleAutostartContinue(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrMissingParam, "project path required")
	}
	projectPath := normalizePath(cmd.Args[0])

	// Log that user declined to kill blockers
	if val, ok := d.pendingAutostarts.Load(projectPath); ok {
		pending := val.(*pendingAutostart)
		for _, c := range pending.conflicts {
			d.startupErrorStore.Add(&StartupLogEntry{
				ProcessID:  makeProcessID(projectPath, c.ScriptName),
				ScriptName: c.ScriptName,
				Level:      "warning",
				EventType:  "port_conflict_skipped",
				Message:    fmt.Sprintf("user declined to kill port %d blocker", c.Port),
				Port:       c.Port,
				Timestamp:  time.Now(),
			})
		}
	}

	// Resume autostart without killing — background context for longevity
	result := d.resumeAutostart(context.Background(), projectPath)

	data, _ := json.Marshal(result)
	return conn.WriteJSON(data)
}
