package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/browser"

	"github.com/standardbeagle/agnt/internal/proxy"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) browserActions() map[string]handlerFn {
	return map[string]handlerFn{
		"START":  d.hubHandleBrowserStart,
		"STOP":   d.hubHandleBrowserStop,
		"STATUS": noCtx(d.hubHandleBrowserStatus),
		"LIST":   noCtx(d.hubHandleBrowserList),
	}
}

func (d *Daemon) hubHandleBrowser(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("BROWSER").dispatch(ctx, conn, cmd, d.browserActions())
}

// hubHandleBrowserStart handles BROWSER START command.

func (d *Daemon) hubHandleBrowserStart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	config, _ := unmarshalCommand[struct {
		ID         string `json:"id"`
		URL        string `json:"url"`
		ProxyID    string `json:"proxy_id"`
		Headless   *bool  `json:"headless"`
		BinaryPath string `json:"binary_path"`
	}](cmd)

	// Also accept ID from args for convenience
	if config.ID == "" && len(cmd.Args) > 0 {
		config.ID = cmd.Args[0]
	}

	// Generate ID if not provided
	if config.ID == "" {
		config.ID = fmt.Sprintf("browser-%d", time.Now().UnixNano()%10000)
	}

	// Get project path from session for session scoping
	projectPath := d.getSessionProjectPath(conn)

	// Determine URL to open
	url := config.URL
	proxyStarted := false
	var proxyURL string

	// If proxy_id is specified, use the proxy's URL
	if config.ProxyID != "" {
		proxyServer, err := getSessionScoped(d, conn, config.ProxyID, d.proxym.GetWithPathFilter)
		if err != nil {
			// Proxy doesn't exist - need URL to auto-start
			if url == "" {
				return conn.WriteErr(hubproto.ErrInvalidArgs, "proxy_id specified but proxy not found and no URL provided to auto-start")
			}

			// Auto-start proxy with the provided URL
			proxyConfig := proxy.ProxyConfig{
				ID:          config.ProxyID,
				TargetURL:   url,
				ListenPort:  0, // Auto-assign
				AutoRestart: true,
				Path:        projectPath,
			}

			proxyServer, err = d.proxym.Create(ctx, proxyConfig)
			if err != nil {
				return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to auto-start proxy: %v", err))
			}

			d.wireProxyLogger(proxyServer)

			// Bind to this project's session overlay, not a global default.
			if ep := d.overlayEndpointForProject(projectPath); ep != "" {
				proxyServer.SetOverlayEndpoint(ep)
			}

			proxyStarted = true
		}

		// Use proxy's listen address as the URL
		proxyURL = fmt.Sprintf("http://%s", proxyServer.ListenAddr)
		url = proxyURL
	}

	// Need at least a URL to open
	if url == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "url or proxy_id required")
	}

	// Default headless to true
	headless := true
	if config.Headless != nil {
		headless = *config.Headless
	}

	browserConfig := browser.Config{
		ID:         config.ID,
		URL:        url,
		Headless:   headless,
		BinaryPath: config.BinaryPath,
		ProxyURL:   proxyURL,
		Path:       projectPath,
	}

	b, err := d.browserm.Start(ctx, config.ID, browserConfig)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"id":            config.ID,
		"state":         b.State().String(),
		"pid":           b.PID(),
		"headless":      headless,
		"proxy_started": proxyStarted,
	}
	if proxyURL != "" {
		resp["proxy_url"] = proxyURL
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleBrowserStop handles BROWSER STOP command.

func (d *Daemon) hubHandleBrowserStop(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "BROWSER STOP requires: <id>")
	}

	browserID := cmd.Args[0]

	// Use session-scoped lookup to find the browser
	b, err := getSessionScoped(d, conn, browserID, d.browserm.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Stop using the resolved full ID
	if err := d.browserm.Stop(ctx, b.ID()); err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	return conn.WriteOK("browser stopped")
}

// hubHandleBrowserStatus handles BROWSER STATUS command.

func (d *Daemon) hubHandleBrowserStatus(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "BROWSER STATUS requires: <id>")
	}

	browserID := cmd.Args[0]

	// Use session-scoped lookup to find the browser
	b, err := getSessionScoped(d, conn, browserID, d.browserm.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	info := b.Info()
	resp := map[string]interface{}{
		"id":       info.ID,
		"state":    info.State,
		"pid":      info.PID,
		"url":      info.URL,
		"headless": info.Headless,
		"path":     info.Path,
	}
	if info.ProxyURL != "" {
		resp["proxy_url"] = info.ProxyURL
	}
	if info.BinaryPath != "" {
		resp["binary_path"] = info.BinaryPath
	}
	if info.StartedAt != "" {
		resp["started_at"] = info.StartedAt
	}
	if info.Error != "" {
		resp["error"] = info.Error
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleBrowserList handles BROWSER LIST command.

func (d *Daemon) hubHandleBrowserList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	// Parse filter from command data
	dirFilter, _ := unmarshalCommand[hubproto.DirectoryFilter](cmd)

	var infos []browser.Info
	if dirFilter.Global {
		// Global: list all browsers
		infos = d.browserm.List()
	} else {
		// Session-scoped: filter by project path
		projectPath := d.getSessionProjectPath(conn)
		if projectPath != "" {
			infos = d.browserm.ListByPath(projectPath)
		} else {
			// No session, return all (fallback for non-session connections)
			infos = d.browserm.List()
		}
	}

	entries := make([]map[string]interface{}, len(infos))
	for i, info := range infos {
		entry := map[string]interface{}{
			"id":       info.ID,
			"state":    info.State,
			"pid":      info.PID,
			"url":      info.URL,
			"headless": info.Headless,
			"path":     info.Path,
		}
		if info.ProxyURL != "" {
			entry["proxy_url"] = info.ProxyURL
		}
		if info.Error != "" {
			entry["error"] = info.Error
		}
		entries[i] = entry
	}

	data, _ := json.Marshal(map[string]interface{}{
		"count":    len(entries),
		"browsers": entries,
	})
	return conn.WriteJSON(data)
}

// hubHandleChaos handles the CHAOS command.
