package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/go-cli-server/protocol"
)

// RegisterSubprocessCommands registers the SUBPROCESS command handlers with the hub.
func (r *SubprocessRouter) RegisterSubprocessCommands() {
	_ = r.hub.RegisterCommand(CommandDefinition{
		Verb:    protocol.VerbSubprocess,
		Handler: r.handleSubprocess,
	})
}

// handleSubprocess handles all SUBPROCESS commands.
func (r *SubprocessRouter) handleSubprocess(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	switch cmd.SubVerb {
	case protocol.SubVerbRegister:
		return r.handleRegister(ctx, conn, cmd)
	case protocol.SubVerbUnregister:
		return r.handleUnregister(ctx, conn, cmd)
	case protocol.SubVerbStart:
		return r.handleStart(ctx, conn, cmd)
	case protocol.SubVerbStop:
		return r.handleStop(ctx, conn, cmd)
	case protocol.SubVerbStatus:
		return r.handleStatus(ctx, conn, cmd)
	case protocol.SubVerbList:
		return r.handleList(ctx, conn, cmd)
	default:
		return conn.WriteInvalidAction("SUBPROCESS", cmd.SubVerb, []string{
			protocol.SubVerbRegister,
			protocol.SubVerbUnregister,
			protocol.SubVerbStart,
			protocol.SubVerbStop,
			protocol.SubVerbStatus,
			protocol.SubVerbList,
		})
	}
}

// handleRegister handles SUBPROCESS REGISTER command.
// Expects JSON data with SubprocessRegisterConfig.
func (r *SubprocessRouter) handleRegister(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Data) == 0 {
		return conn.WriteMissingParam("SUBPROCESS REGISTER", "data", "SUBPROCESS REGISTER requires JSON configuration data")
	}

	var config protocol.SubprocessRegisterConfig
	if err := json.Unmarshal(cmd.Data, &config); err != nil {
		return conn.WriteErr(protocol.ErrInvalidArgs, fmt.Sprintf("invalid JSON: %v", err))
	}

	// Validate required fields
	if config.ID == "" {
		return conn.WriteMissingParam("SUBPROCESS REGISTER", "id", "subprocess ID is required")
	}

	if len(config.Commands) == 0 {
		return conn.WriteMissingParam("SUBPROCESS REGISTER", "commands", "at least one command pattern is required")
	}

	if config.Transport.Type == "" {
		return conn.WriteMissingParam("SUBPROCESS REGISTER", "transport.type", "transport type is required")
	}

	// Convert protocol config to internal ManagedSubprocess
	sp := r.configToManagedSubprocess(config)

	// Register with the router
	if err := r.Register(sp); err != nil {
		return conn.WriteErr(protocol.ErrAlreadyExists, err.Error())
	}

	// Auto-start if requested
	if config.AutoStart {
		if err := sp.start(ctx); err != nil {
			// Registration succeeded but start failed - report but don't unregister
			return conn.WriteInternalErr(fmt.Sprintf("registered but failed to start: %v", err))
		}
	}

	// Return success with subprocess info
	info := r.subprocessToInfo(sp)
	data, _ := json.Marshal(info)
	return conn.WriteJSON(data)
}

// handleUnregister handles SUBPROCESS UNREGISTER command.
func (r *SubprocessRouter) handleUnregister(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Args) == 0 {
		return conn.WriteMissingParam("SUBPROCESS UNREGISTER", "id", "subprocess ID is required")
	}

	id := cmd.Args[0]
	if err := r.Unregister(id); err != nil {
		return conn.WriteErr(protocol.ErrNotFound, err.Error())
	}

	return conn.WriteOK(fmt.Sprintf("subprocess %s unregistered", id))
}

// handleStart handles SUBPROCESS START command.
func (r *SubprocessRouter) handleStart(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Args) == 0 {
		return conn.WriteMissingParam("SUBPROCESS START", "id", "subprocess ID is required")
	}

	id := cmd.Args[0]
	if err := r.Start(ctx, id); err != nil {
		return conn.WriteInternalErr(err.Error())
	}

	// Return updated subprocess info
	sp, ok := r.Get(id)
	if !ok {
		return conn.WriteOK(fmt.Sprintf("subprocess %s started", id))
	}

	info := r.subprocessToInfo(sp)
	data, _ := json.Marshal(info)
	return conn.WriteJSON(data)
}

// handleStop handles SUBPROCESS STOP command.
func (r *SubprocessRouter) handleStop(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Args) == 0 {
		return conn.WriteMissingParam("SUBPROCESS STOP", "id", "subprocess ID is required")
	}

	id := cmd.Args[0]
	if err := r.Stop(ctx, id); err != nil {
		return conn.WriteInternalErr(err.Error())
	}

	return conn.WriteOK(fmt.Sprintf("subprocess %s stopped", id))
}

// handleStatus handles SUBPROCESS STATUS command.
func (r *SubprocessRouter) handleStatus(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	if len(cmd.Args) == 0 {
		return conn.WriteMissingParam("SUBPROCESS STATUS", "id", "subprocess ID is required")
	}

	id := cmd.Args[0]
	sp, ok := r.Get(id)
	if !ok {
		return conn.WriteNotFound("subprocess", id)
	}

	info := r.subprocessToInfo(sp)
	data, _ := json.Marshal(info)
	return conn.WriteJSON(data)
}

// handleList handles SUBPROCESS LIST command.
func (r *SubprocessRouter) handleList(ctx context.Context, conn *Connection, cmd *protocol.Command) error {
	subprocesses := r.List()

	infos := make([]protocol.SubprocessInfo, 0, len(subprocesses))
	for _, sp := range subprocesses {
		infos = append(infos, r.subprocessToInfo(sp))
	}

	data, _ := json.Marshal(infos)
	return conn.WriteJSON(data)
}

// configToManagedSubprocess converts a protocol config to a ManagedSubprocess.
func (r *SubprocessRouter) configToManagedSubprocess(config protocol.SubprocessRegisterConfig) *ManagedSubprocess {
	sp := &ManagedSubprocess{
		ID:          config.ID,
		Name:        config.Name,
		Description: config.Description,
		Commands:    config.Commands,
		Transport: SubprocessTransportConfig{
			Type:    config.Transport.Type,
			Address: config.Transport.Address,
			Command: config.Transport.Command,
			Args:    config.Transport.Args,
			Env:     config.Transport.Env,
			Timeout: time.Duration(config.Transport.TimeoutMs) * time.Millisecond,
		},
		HealthCheck: SubprocessHealthConfig{
			Enabled:          config.HealthCheck.Enabled,
			Interval:         time.Duration(config.HealthCheck.IntervalMs) * time.Millisecond,
			Timeout:          time.Duration(config.HealthCheck.TimeoutMs) * time.Millisecond,
			FailureThreshold: config.HealthCheck.FailureThreshold,
		},
		AutoStart:   config.AutoStart,
		AutoRestart: config.AutoRestart,
		MaxRestarts: config.MaxRestarts,
		RestartWait: time.Duration(config.RestartDelayMs) * time.Millisecond,
	}

	// Apply defaults
	if sp.HealthCheck.Interval == 0 {
		sp.HealthCheck.Interval = 10 * time.Second
	}
	if sp.HealthCheck.Timeout == 0 {
		sp.HealthCheck.Timeout = 5 * time.Second
	}
	if sp.HealthCheck.FailureThreshold == 0 {
		sp.HealthCheck.FailureThreshold = 3
	}
	if sp.Transport.Timeout == 0 {
		sp.Transport.Timeout = 10 * time.Second
	}
	if sp.RestartWait == 0 {
		sp.RestartWait = 1 * time.Second
	}

	return sp
}

// subprocessToInfo converts a ManagedSubprocess to protocol.SubprocessInfo.
func (r *SubprocessRouter) subprocessToInfo(sp *ManagedSubprocess) protocol.SubprocessInfo {
	info := protocol.SubprocessInfo{
		ID:              sp.ID,
		Name:            sp.Name,
		Description:     sp.Description,
		State:           string(sp.state.Load().(ManagedSubprocessState)),
		Healthy:         sp.healthy.Load(),
		Commands:        sp.Commands,
		CommandsHandled: sp.commandsHandled.Load(),
		CommandsFailed:  sp.commandsFailed.Load(),
		RestartCount:    int(sp.restartCount.Load()),
	}

	if t := sp.lastCommand.Load(); t != nil {
		info.LastCommandMs = t.UnixMilli()
	}
	if t := sp.lastHealthy.Load(); t != nil {
		info.LastHealthyMs = t.UnixMilli()
	}
	if t := sp.stateChanged.Load(); t != nil {
		info.StateChangedMs = t.UnixMilli()
	}

	return info
}
