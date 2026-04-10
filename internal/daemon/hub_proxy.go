package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"

	"github.com/standardbeagle/agnt/internal/proxy"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleProxy(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "PROXY %s: args=%v", cmd.SubVerb, cmd.Args)
	switch cmd.SubVerb {
	case "START":
		return d.hubHandleProxyStart(ctx, conn, cmd)
	case "STOP":
		return d.hubHandleProxyStop(ctx, conn, cmd)
	case "RESTART":
		return d.hubHandleProxyRestart(ctx, conn, cmd)
	case "STATUS":
		return d.hubHandleProxyStatus(conn, cmd)
	case "LIST":
		return d.hubHandleProxyList(conn, cmd)
	case "EXEC":
		return d.hubHandleProxyExec(conn, cmd)
	case "TOAST":
		return d.hubHandleProxyToast(conn, cmd)
	default:
		return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
			Code:         hubproto.ErrInvalidArgs,
			Message:      "unknown PROXY sub-command",
			Command:      "PROXY",
			ValidActions: []string{"START", "STOP", "RESTART", "STATUS", "LIST", "EXEC", "TOAST"},
		})
	}
}

// hubHandleProxyStart handles PROXY START command.
// PROXY START <id> <target_url> <port> [max_log_size]

func (d *Daemon) hubHandleProxyStart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 3 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY START requires: <id> <target_url> <port>")
	}

	proxyID := cmd.Args[0]
	targetURL := cmd.Args[1]
	port, err := strconv.Atoi(cmd.Args[2])
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid port")
	}

	maxLogSize := 1000
	if len(cmd.Args) > 3 {
		maxLogSize, _ = strconv.Atoi(cmd.Args[3])
	}

	// Parse extended config from JSON data (optional)
	path := "."
	bindAddress := ""
	allowExternal := false
	publicURL := ""
	skipTLSVerify := false
	if len(cmd.Data) > 0 {
		var data struct {
			Path          string `json:"path"`
			BindAddress   string `json:"bind_address"`
			AllowExternal bool   `json:"allow_external"`
			PublicURL     string `json:"public_url"`
			SkipTLSVerify bool   `json:"skip_tls_verify"`
		}
		if err := json.Unmarshal(cmd.Data, &data); err == nil {
			if data.Path != "" {
				path = data.Path
			}
			bindAddress = data.BindAddress
			allowExternal = data.AllowExternal
			publicURL = data.PublicURL
			skipTLSVerify = data.SkipTLSVerify
		}
	}

	// Create proxy config
	proxyConfig := proxy.ProxyConfig{
		ID:            proxyID,
		TargetURL:     targetURL,
		ListenPort:    port,
		MaxLogSize:    maxLogSize,
		AutoRestart:   true,
		Path:          normalizePath(path),
		BindAddress:   bindAddress,
		AllowExternal: allowExternal,
		PublicURL:     publicURL,
		SkipTLSVerify: skipTLSVerify,
	}

	proxyServer, err := d.proxym.Create(ctx, proxyConfig)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	d.wireProxyLogger(proxyServer)

	// Find session for this project to get session-specific overlay endpoint
	if path != "" {
		normalizedPath := normalizePath(path)
		if session, ok := d.sessionRegistry.FindByDirectory(normalizedPath); ok && session.OverlayPath != "" {
			proxyServer.SetOverlayEndpoint(session.OverlayPath)
			debug.Log("daemon", "Set session-specific overlay endpoint for proxy %s: %s", proxyID, session.OverlayPath)
		} else if endpoint := d.OverlayEndpoint(); endpoint != "" {
			// Fallback to global overlay endpoint if no session found
			proxyServer.SetOverlayEndpoint(endpoint)
			debug.Log("daemon", "Set global overlay endpoint for proxy %s: %s", proxyID, endpoint)
		} else {
			debug.Log("daemon", "No overlay endpoint found for proxy %s (path=%q, normalized=%q) — proxy→agent messages will not work", proxyID, path, normalizedPath)
		}
	} else if endpoint := d.OverlayEndpoint(); endpoint != "" {
		// Fallback to global overlay endpoint if no path specified
		proxyServer.SetOverlayEndpoint(endpoint)
		debug.Log("daemon", "Set global overlay endpoint for proxy %s: %s", proxyID, endpoint)
	} else {
		debug.Log("daemon", "No overlay endpoint found for proxy %s (no path, no global endpoint) — proxy→agent messages will not work", proxyID)
	}

	if !proxyServer.HasOverlayEndpoint() {
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID:  proxyID,
			ScriptName: proxyID,
			Level:      "warning",
			EventType:  "proxy_no_overlay",
			Message:    fmt.Sprintf("proxy %s has no overlay endpoint — browser messages will not reach agent", proxyID),
			Timestamp:  time.Now(),
		})
	}

	// Persist proxy config
	if d.stateMgr != nil {
		d.stateMgr.AddProxy(PersistentProxyConfig{
			ID:         proxyID,
			TargetURL:  targetURL,
			Port:       port,
			MaxLogSize: maxLogSize,
			Path:       path,
		})
	}

	resp := map[string]interface{}{
		"id":          proxyServer.ID,
		"listen_addr": proxyServer.ListenAddr,
		"target_url":  proxyServer.TargetURL.String(),
		"status":      "running",
	}
	if proxyServer.BindAddress != "" {
		resp["bind_address"] = proxyServer.BindAddress
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProxyStop handles PROXY STOP command.

func (d *Daemon) hubHandleProxyStop(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY STOP requires: <id>")
	}

	proxyID := cmd.Args[0]

	// Use session-scoped lookup to resolve the proxy
	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Stop using the resolved full ID
	if err := d.proxym.Stop(ctx, p.ID); err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Remove from persisted state
	if d.stateMgr != nil {
		d.stateMgr.RemoveProxy(p.ID)
	}

	return conn.WriteOK("proxy stopped")
}

// hubHandleProxyStatus handles PROXY STATUS command.

func (d *Daemon) hubHandleProxyStatus(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY STATUS requires: <id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	resp := map[string]interface{}{
		"id":          p.ID,
		"listen_addr": p.ListenAddr,
		"target_url":  p.TargetURL.String(),
		"status":      "running",
		"stats":       p.Stats(),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProxyList handles PROXY LIST command.

func (d *Daemon) hubHandleProxyList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	// Parse filter from command data
	var dirFilter hubproto.DirectoryFilter
	if len(cmd.Data) > 0 {
		json.Unmarshal(cmd.Data, &dirFilter)
	}

	// Resolve filter path from session code or directory
	filterPath := ""
	if !dirFilter.Global {
		if dirFilter.SessionCode != "" {
			// Look up session to get project path
			if session, ok := d.sessionRegistry.Get(dirFilter.SessionCode); ok {
				filterPath = normalizePath(session.ProjectPath)
			}
		} else if dirFilter.Directory != "" {
			filterPath = normalizePath(dirFilter.Directory)
		}
	}

	proxies := d.proxym.List()

	var result []map[string]interface{}
	for _, p := range proxies {
		proxyPath := normalizePath(p.Path)

		// Filter by path if not global and we have a filter path
		if !dirFilter.Global && filterPath != "" && filterPath != "." && proxyPath != filterPath {
			continue
		}

		result = append(result, map[string]interface{}{
			"id":          p.ID,
			"listen_addr": p.ListenAddr,
			"target_url":  p.TargetURL.String(),
			"status":      "running",
			"running":     true,
			"path":        p.Path,
		})
	}

	data, _ := json.Marshal(map[string]interface{}{
		"proxies": result,
		"count":   len(result),
	})
	return conn.WriteJSON(data)
}

// hubHandleProxyExec handles PROXY EXEC command.

func (d *Daemon) hubHandleProxyExec(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY EXEC requires: <id>")
	}

	proxyID := cmd.Args[0]

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Code is in the data payload
	if len(cmd.Data) == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY EXEC requires code")
	}

	code := string(cmd.Data)
	execID, resultChan, err := p.ExecuteJavaScript(code)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	// Wait for result with timeout
	timeout := 30 * time.Second
	select {
	case result := <-resultChan:
		if result == nil {
			return conn.WriteErr(hubproto.ErrInternal, "execution channel closed")
		}

		resp := map[string]interface{}{
			"execution_id": execID,
			"success":      result.Error == "",
			"result":       result.Result,
			"error":        result.Error,
			"duration":     result.Duration.String(),
		}

		// Include file path for large results
		if result.FilePath != "" {
			resp["file_path"] = result.FilePath
		}

		data, _ := json.Marshal(resp)
		return conn.WriteJSON(data)

	case <-time.After(timeout):
		return conn.WriteErr(hubproto.ErrTimeout, "execution timed out")
	}
}

// hubHandleProxyToast handles PROXY TOAST command.

func (d *Daemon) hubHandleProxyToast(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "PROXY TOAST: args=%v dataLen=%d", cmd.Args, len(cmd.Data))

	if len(cmd.Args) < 1 {
		debug.Log("daemon", "PROXY TOAST: missing proxy ID")
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY TOAST requires: <id>")
	}

	proxyID := cmd.Args[0]
	debug.Log("daemon", "PROXY TOAST: proxyID=%s", proxyID)

	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		debug.Log("daemon", "PROXY TOAST: proxy not found: %v", err)
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}
	debug.Log("daemon", "PROXY TOAST: found proxy %s at %s", p.ID, p.ListenAddr)

	// Toast config is in the data payload
	if len(cmd.Data) == 0 {
		debug.Log("daemon", "PROXY TOAST: no data payload")
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY TOAST requires toast config")
	}

	var toast struct {
		Message  string `json:"toast_message"`
		Type     string `json:"toast_type"`
		Title    string `json:"toast_title"`
		Duration int    `json:"toast_duration"`
	}
	if err := json.Unmarshal(cmd.Data, &toast); err != nil {
		debug.Log("daemon", "PROXY TOAST: failed to unmarshal: %v", err)
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid toast config: "+err.Error())
	}

	if toast.Type == "" {
		toast.Type = "info"
	}
	if toast.Message == "" {
		debug.Log("daemon", "PROXY TOAST: empty message")
		return conn.WriteErr(hubproto.ErrInvalidArgs, "toast_message is required")
	}

	debug.Log("daemon", "PROXY TOAST: sending type=%s title=%q message=%q", toast.Type, toast.Title, toast.Message)

	sentCount, err := p.BroadcastToast(toast.Type, toast.Title, toast.Message, toast.Duration)
	if err != nil {
		debug.Log("daemon", "PROXY TOAST: broadcast error: %v", err)
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	debug.Log("daemon", "PROXY TOAST: sent to %d clients", sentCount)

	resp := map[string]interface{}{
		"success":    true,
		"sent_count": sentCount,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleProxyLog handles the PROXYLOG command and its sub-verbs.

func (d *Daemon) hubHandleProxyRestart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY RESTART requires: <id>")
	}

	proxyID := cmd.Args[0]

	// Get the proxy to capture its config
	p, err := d.getSessionScopedProxy(conn, proxyID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Capture config before stopping
	targetURL := p.TargetURL.String()
	maxLogSize := int(p.Logger().Stats().MaxSize)
	projectPath := p.Path
	bindAddress := p.BindAddress
	allowExternal := p.AllowExternal

	// Stop the proxy
	if err := d.proxym.Stop(ctx, proxyID); err != nil {
		debug.Warn("daemon", "error stopping proxy %s: %v", proxyID, err)
		d.startupErrorStore.Add(&StartupLogEntry{
			ProcessID: proxyID,
			Level:     "warning",
			EventType: "proxy_stop_failed",
			Message:   fmt.Sprintf("PROXY RESTART: failed to stop proxy: %v", err),
			Timestamp: time.Now(),
		})
	}

	// Remove from persisted state
	if d.stateMgr != nil {
		d.stateMgr.RemoveProxy(proxyID)
	}

	// Wait for cleanup
	time.Sleep(100 * time.Millisecond)

	// Create new proxy with same config
	newProxy, err := d.proxym.Create(ctx, proxy.ProxyConfig{
		ID:            proxyID,
		TargetURL:     targetURL,
		ListenPort:    0, // Auto-assign port
		MaxLogSize:    maxLogSize,
		Path:          projectPath,
		BindAddress:   bindAddress,
		AllowExternal: allowExternal,
	})
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to restart proxy: %v", err))
	}

	d.wireProxyLogger(newProxy)

	// Persist the new proxy state
	if d.stateMgr != nil {
		d.stateMgr.AddProxy(PersistentProxyConfig{
			ID:         proxyID,
			TargetURL:  targetURL,
			Port:       0, // Auto-assigned
			MaxLogSize: maxLogSize,
			Path:       projectPath,
		})
	}

	resp := map[string]interface{}{
		"id":          proxyID,
		"target_url":  targetURL,
		"listen_addr": newProxy.ListenAddr,
		"restarted":   true,
		"success":     true,
		"message":     fmt.Sprintf("Proxy %q restarted successfully", proxyID),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// ============================================================================
// AUTOMATION Command Handlers (chromedp sessions)
// ============================================================================

// getSessionScopedAutomationSession retrieves an automation session with session-scoped fuzzy matching.
