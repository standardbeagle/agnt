package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/browser"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"

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

// autoStartBrowserProxy creates the proxy BROWSER START fronts a browser with
// when the named proxy does not exist yet.
//
// The incident owner is registered before wireProxyLogger, which captures it:
// stampIncidentOwner fails closed for a proxy with no captured owner, so
// without it every browser JS error and HTTP 5xx from this proxy was dropped
// before the incident bus and never reached the agent.
func (d *Daemon) autoStartBrowserProxy(ctx context.Context, proxyID, targetURL, projectPath, sessionCode string) (*proxy.ProxyServer, error) {
	server, err := d.proxym.Create(ctx, proxy.ProxyConfig{
		ID:          proxyID,
		TargetURL:   targetURL,
		ListenPort:  0, // Auto-assign
		AutoRestart: true,
		Path:        projectPath,
	})
	if err != nil {
		return nil, err
	}
	d.registerIncidentProxyOwner(server.ID, sessionCode)
	d.wireProxyLogger(server)

	// Bind to this project's session overlay, not a global default.
	if ep := d.overlayEndpointForProject(projectPath); ep != "" {
		server.SetOverlayEndpoint(ep)
	}
	return server, nil
}

// hubHandleBrowserStart handles BROWSER START command.

func (d *Daemon) hubHandleBrowserStart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	config, _ := unmarshalCommand[struct {
		ID         string `json:"id"`
		URL        string `json:"url"`
		ProxyID    string `json:"proxy_id"`
		Headless   *bool  `json:"headless"`
		BinaryPath string `json:"binary_path"`
		protocol.StartScope
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
	projectPath, err := d.startProjectPath(conn, config.StartScope)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

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

			owner := conn.SessionCode()
			if owner == "" {
				owner = config.SessionCode
			}
			if owner == "" && projectPath != "" {
				if s, ok := d.sessionRegistry.FindByDirectory(projectPath); ok {
					owner = s.Code
				}
			}
			proxyServer, err = d.autoStartBrowserProxy(ctx, config.ProxyID, url, projectPath, owner)
			if err != nil {
				return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to auto-start proxy: %v", err))
			}
			proxyStarted = true
		}

		proxyURL = proxyServer.URL()
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
		// A proxy started only to front this browser must not outlive the
		// failed start: nothing reports it and nothing would stop it.
		if proxyStarted {
			if stopErr := d.proxym.Stop(ctx, config.ProxyID); stopErr != nil && !errors.Is(stopErr, proxy.ErrProxyNotFound) {
				debug.Warn("daemon", "BROWSER START: stopping auto-started proxy %s: %v", config.ProxyID, stopErr)
			}
			d.retireIncidentProxyOwner(config.ProxyID)
		}
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
	dirFilter, err := unmarshalCommand[protocol.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
	}

	// Route through the mandatory session-scope chokepoint (see
	// resolveProjectScope). A non-global query with no resolvable project
	// fails loud rather than listing every project's browsers.
	projectPath, global, err := d.resolveProjectScope(dirFilter, conn.SessionCode())
	if err != nil {
		return d.writeScopeErr(conn, err, "session_code")
	}

	var infos []browser.Info
	if global {
		infos = d.browserm.List()
	} else {
		infos = d.browserm.ListByPath(projectPath)
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
