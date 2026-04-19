package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/standardbeagle/agnt/internal/tunnel"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleTunnel(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("TUNNEL").dispatch(ctx, conn, cmd, map[string]handlerFn{
		"START":  d.hubHandleTunnelStart,
		"STOP":   d.hubHandleTunnelStop,
		"STATUS": noCtx(d.hubHandleTunnelStatus),
		"LIST":   noCtx(d.hubHandleTunnelList),
	})
}

// hubHandleTunnelStart handles TUNNEL START command.

func (d *Daemon) hubHandleTunnelStart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "TUNNEL START requires: <id>")
	}

	tunnelID := cmd.Args[0]

	var config struct {
		Provider   string `json:"provider"`
		LocalPort  int    `json:"local_port"`
		LocalHost  string `json:"local_host"`
		ProxyID    string `json:"proxy_id"`
		BinaryPath string `json:"binary_path"`
	}

	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &config)
	}

	if config.Provider == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "provider is required")
	}
	if config.LocalPort == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "local_port is required")
	}

	// Get project path from session for session scoping
	projectPath := d.getSessionProjectPath(conn)

	tunnelConfig := tunnel.Config{
		Provider:   tunnel.Provider(config.Provider),
		LocalPort:  config.LocalPort,
		LocalHost:  config.LocalHost,
		BinaryPath: config.BinaryPath,
		Path:       projectPath,
	}

	t, err := d.tunnelm.Start(ctx, tunnelID, tunnelConfig)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	// Wait for public URL
	publicURL, err := t.WaitForURL(ctx)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("tunnel started but failed to get URL: %v", err))
	}

	// Update proxy public URL if proxy_id specified
	if config.ProxyID != "" {
		if p, err := getSessionScoped(d, conn, config.ProxyID, d.proxym.GetWithPathFilter); err == nil {
			p.SetPublicURL(publicURL)
		}
	}

	resp := map[string]interface{}{
		"id":         tunnelID,
		"provider":   config.Provider,
		"local_port": config.LocalPort,
		"public_url": publicURL,
		"status":     "running",
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleTunnelStop handles TUNNEL STOP command.

func (d *Daemon) hubHandleTunnelStop(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "TUNNEL STOP requires: <id>")
	}

	tunnelID := cmd.Args[0]

	// Use session-scoped lookup to find the tunnel
	t, err := getSessionScoped(d, conn, tunnelID, d.tunnelm.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Stop using the resolved full ID
	if err := d.tunnelm.Stop(ctx, t.ID()); err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	return conn.WriteOK("tunnel stopped")
}

// hubHandleTunnelStatus handles TUNNEL STATUS command.

func (d *Daemon) hubHandleTunnelStatus(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "TUNNEL STATUS requires: <id>")
	}

	tunnelID := cmd.Args[0]

	// Use session-scoped lookup to find the tunnel
	t, err := getSessionScoped(d, conn, tunnelID, d.tunnelm.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	info := t.Info()
	resp := map[string]interface{}{
		"id":         info.ID,
		"provider":   string(info.Provider),
		"state":      info.State,
		"public_url": info.PublicURL,
		"local_addr": info.LocalAddr,
		"path":       info.Path,
	}
	if info.Error != "" {
		resp["error"] = info.Error
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleTunnelList handles TUNNEL LIST command.

func (d *Daemon) hubHandleTunnelList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	// Parse filter from command data
	var dirFilter hubproto.DirectoryFilter
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &dirFilter)
	}

	var infos []tunnel.TunnelInfo
	if dirFilter.Global {
		// Global: list all tunnels
		infos = d.tunnelm.List()
	} else {
		// Session-scoped: filter by project path
		projectPath := d.getSessionProjectPath(conn)
		if projectPath != "" {
			infos = d.tunnelm.ListByPath(projectPath)
		} else {
			// No session, return all (fallback for non-session connections)
			infos = d.tunnelm.List()
		}
	}

	entries := make([]map[string]interface{}, len(infos))
	for i, info := range infos {
		entry := map[string]interface{}{
			"id":         info.ID,
			"provider":   string(info.Provider),
			"state":      info.State,
			"public_url": info.PublicURL,
			"local_addr": info.LocalAddr,
			"path":       info.Path,
		}
		if info.Error != "" {
			entry["error"] = info.Error
		}
		entries[i] = entry
	}

	data, _ := json.Marshal(map[string]interface{}{"tunnels": entries})
	return conn.WriteJSON(data)
}

// hubHandleBrowser handles the BROWSER command.
