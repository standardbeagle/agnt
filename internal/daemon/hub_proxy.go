package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/tunnel"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleProxy(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("PROXY").dispatch(ctx, conn, cmd, d.proxyActions())
}

func (d *Daemon) proxyActions() map[string]handlerFn {
	return map[string]handlerFn{
		"START":   d.hubHandleProxyStart,
		"STOP":    d.hubHandleProxyStop,
		"RESTART": d.hubHandleProxyRestart,
		"STATUS":  noCtx(d.hubHandleProxyStatus),
		"LIST":    noCtx(d.hubHandleProxyList),
		"EXEC":    noCtx(d.hubHandleProxyExec),
		"TOAST":   noCtx(d.hubHandleProxyToast),
	}
}

// hubHandleProxyStart handles PROXY START command.
// PROXY START <id> <target_url> <port> [max_log_size]
//
// Thin wrapper over handleExplicitStart — the MCP tool path and the
// autostart event path share the same wire-up sequence (Create +
// wireProxyLogger + SetOverlayEndpoint + stateMgr.AddProxy +
// proxyEntryStore.Register + registerProxyDependencies). Before T3 the
// two paths had drifted: autostart did dependency gating + admin
// registry, the tool did state persistence. Delegating here is the
// single-source-of-truth move; the tool handler's only remaining job
// is parsing MCP args, normalizing the path, and shaping the JSON
// response.
//
// Proxy ID policy: the tool takes the user-supplied id verbatim (no
// makeProcessID rewrite). Autostart uses makeProcessID(projectPath,
// name) to project-scope collisions. This divergence is intentional
// for backward compat — tests and external callers pass raw IDs to
// the tool. Future work can unify, but T3 leaves it alone.
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
		maxLogSize, err = strconv.Atoi(cmd.Args[3])
		if err != nil {
			return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid max_log_size")
		}
	}

	// Parse extended config from JSON data (optional).
	path := "."
	bindAddress := ""
	allowExternal := false
	publicURL := ""
	skipTLSVerify := false
	if ext, err := unmarshalCommand[struct {
		Path          string `json:"path"`
		BindAddress   string `json:"bind_address"`
		AllowExternal bool   `json:"allow_external"`
		PublicURL     string `json:"public_url"`
		SkipTLSVerify bool   `json:"skip_tls_verify"`
	}](cmd); err == nil {
		if ext.Path != "" {
			path = ext.Path
		}
		bindAddress = ext.BindAddress
		allowExternal = ext.AllowExternal
		publicURL = ext.PublicURL
		skipTLSVerify = ext.SkipTLSVerify
	}

	normalizedPath := normalizePath(path)

	// Build a config.ProxyConfig that handleExplicitStart → buildProxyServerConfig
	// will translate into a proxy.ProxyConfig. The port MCP arg is the
	// proxy's listen port (not the target port); URL is the full target.
	proxyConfig := &config.ProxyConfig{
		URL:           targetURL,
		ListenPort:    port,
		MaxLogSize:    maxLogSize,
		Bind:          bindAddress,
		AllowExternal: allowExternal,
		PublicURL:     publicURL,
		SkipTLSVerify: skipTLSVerify,
	}

	// handleExplicitStart is synchronous — on return the proxy is fully
	// wired (or a startup_error entry explains the failure). Respect
	// ctx by bailing early if the caller already cancelled.
	select {
	case <-ctx.Done():
		return conn.WriteErr(hubproto.ErrInternal, ctx.Err().Error())
	default:
	}

	// A start under an id that is already running is idempotent and says so.
	// The lookups are exact: a fuzzy Get matched a short id ("dev") against
	// another project's compound id, skipped the start, and reported that
	// project's proxy back as this one.
	_, alreadyRunning := d.exactProxy(proxyID)
	if !alreadyRunning {
		d.handleExplicitStart(ProxyEvent{
			Type:         ExplicitStart,
			ProxyID:      proxyID,
			Config:       proxyConfig,
			Path:         normalizedPath,
			OwnerSession: conn.SessionCode(),
		})
	}

	// If the lookup fails, handleExplicitStart's Create() path hit an
	// error. The real cause is the proxy_creation_failed entry
	// handleExplicitStart recorded in the startup log. Inline that detail so
	// the MCP caller sees the root cause instead of a bare "not created", and
	// point at the durable surfaces for the full record rather than
	// swallowing it.
	proxyServer, found := d.exactProxy(proxyID)
	if !found {
		err := proxy.ErrProxyNotFound
		detail := ""
		if d.startupErrorStore != nil {
			for _, e := range d.startupErrorStore.Query(StartupLogFilter{ProcessID: proxyID, Level: "error", Limit: 5}) {
				if e.EventType == "proxy_creation_failed" {
					detail = e.Message // oldest→newest order: keep the most recent match
				}
			}
		}
		msg := fmt.Sprintf("proxy %s was not created: %v", proxyID, err)
		if detail != "" {
			msg += " — cause: " + detail
		}
		msg += " (see get_incidents or `daemon startup_log` for the full proxy_creation_failed record)"
		return conn.WriteErr(hubproto.ErrInternal, msg)
	}

	resp := map[string]interface{}{
		"id":          proxyServer.ID,
		"listen_addr": proxyServer.ListenAddr,
		"url":         proxyServer.URL(),
		"target_url":  proxyServer.TargetURL.String(),
		"status":      "running",
	}
	if alreadyRunning {
		resp["already_running"] = true
	}
	if proxyServer.BindAddress != "" {
		resp["bind_address"] = proxyServer.BindAddress
	}
	// Surface the public URL when one was configured (explicit public_url, or a
	// tunnel tool that later calls SetPublicURL). The MCP tool reads this to
	// render the "access at" URL — without it, proxyAccessURL fell back to the
	// bare loopback address even when the proxy was fronted by a public URL.
	if publicURL := proxyServer.GetPublicURL(); publicURL != "" {
		resp["public_url"] = publicURL
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
	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Stop using the resolved full ID. The manager deregisters the proxy even
	// when its shutdown reports an error, so the rest of the cleanup runs
	// either way: skipping it left the proxy in persisted state (back on the
	// next daemon start) with a stale admin entry and incident owner.
	stopErr := d.proxym.Stop(ctx, p.ID)
	d.retireIncidentProxyOwner(p.ID)
	d.retireExplicitProxyEntry(p.Path, p.ID)

	// Remove from persisted state
	if d.stateMgr != nil {
		d.stateMgr.RemoveProxy(p.ID)
	}

	// Not found here means a concurrent stop retired it first: the stop
	// intent is met.
	if stopErr != nil && !errors.Is(stopErr, proxy.ErrProxyNotFound) {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("proxy %s removed, but its shutdown failed: %v", p.ID, stopErr))
	}
	return conn.WriteOK("proxy stopped")
}

// hubHandleProxyStatus handles PROXY STATUS command.

func (d *Daemon) hubHandleProxyStatus(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY STATUS requires: <id>")
	}

	proxyID := cmd.Args[0]

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	stats := p.Stats()
	resp := map[string]interface{}{
		"id":             p.ID,
		"listen_addr":    p.ListenAddr,
		"url":            p.URL(),
		"target_url":     p.TargetURL.String(),
		"status":         proxyRuntimeStatus(stats),
		"uptime":         formatProxyUptime(stats.Uptime),
		"total_requests": stats.TotalRequests,
		"log_stats":      stats.LoggerStats,
		"stats":          stats,
	}
	if !stats.ReadyForForwarding {
		resp["waiting_for"] = stats.WaitingFor
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// proxyRuntimeStatus derives the `status` string exposed in
// PROXY STATUS / PROXY LIST responses. Distinguishes a bound-but-
// gated proxy (waiting for declared `wait-for` dependencies) from a
// plain running proxy so the AI agent and the overlay status bar
// can render the state distinctly.
func proxyRuntimeStatus(stats proxy.ProxyStats) string {
	if !stats.Running {
		return "stopped"
	}
	if !stats.ReadyForForwarding {
		return "waiting_for_dependencies"
	}
	return "running"
}

// formatProxyUptime renders a proxy uptime as the time.Duration string
// representation (e.g. "3m12s", "226ms"). The MCP tool layer reads this
// field via getString(result, "uptime") and Go's default time.Duration
// JSON marshaller emits a nanosecond integer — so the handler must
// pre-format before WriteJSON. Returns "" when Duration is non-positive
// (proxy never started, or Stats() called before Start()).
func formatProxyUptime(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

// hubHandleProxyList handles PROXY LIST command.

func (d *Daemon) hubHandleProxyList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	// Parse filter from command data
	dirFilter, err := unmarshalCommand[protocol.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
	}

	// Route through the mandatory session-scope chokepoint (see
	// resolveProjectScope). A non-global query with no resolvable project
	// fails loud rather than listing every project's proxies.
	sc, err := d.resolveScope(dirFilter, conn.SessionCode())
	if err != nil {
		return d.writeScopeErr(conn, err, "session_code")
	}

	proxies := d.proxym.ListScoped(sc)

	// Index tunnels by ID (tunnel ID is conventionally the proxy ID) so the
	// proxy UI can surface the public tunnel URL — PROXY LIST previously
	// omitted this, so the overlay's tunnel line never lit up.
	tunnelsByID := make(map[string]tunnel.TunnelInfo)
	for _, t := range d.tunnelm.List() {
		tunnelsByID[t.ID] = t
	}

	var result []map[string]interface{}
	for _, p := range proxies {
		stats := p.Stats()
		entry := map[string]interface{}{
			"id":             p.ID,
			"listen_addr":    p.ListenAddr,
			"url":            p.URL(),
			"target_url":     p.TargetURL.String(),
			"status":         proxyRuntimeStatus(stats),
			"running":        stats.Running,
			"path":           p.Path,
			"uptime":         formatProxyUptime(stats.Uptime),
			"total_requests": stats.TotalRequests,
		}
		if !stats.ReadyForForwarding {
			entry["waiting_for"] = stats.WaitingFor
		}
		if value, ok := d.proxyConfigs.Load(p.ID); ok {
			entry["config_name"] = value.(configuredProxy).name
		}
		if t, ok := tunnelsByID[p.ID]; ok {
			entry["tunnel_url"] = t.PublicURL
			entry["tunnel_running"] = t.State == "connected"
		}
		// Display-only address from .agnt.kdl (status-url). A live tunnel
		// still wins: that URL is the one currently serving traffic.
		if statusURL := p.GetStatusURL(); statusURL != "" {
			entry["status_url"] = statusURL
		}
		result = append(result, entry)
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

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Code is in the data payload
	if len(cmd.Data) == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY EXEC requires code")
	}

	code := string(cmd.Data)
	// Optional target content frame (always-wrap model). cmd.Args[0] is the
	// proxy id; cmd.Args[1], when present, is the frame id.
	frameID := ""
	if len(cmd.Args) >= 2 {
		frameID = cmd.Args[1]
	}
	execID, resultChan, err := p.ExecuteJavaScript(code, frameID)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	// Wait for result with timeout
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

	case <-time.After(proxyExecTimeout):
		// Browser is connected (ExecuteJavaScript already fails fast on zero
		// clients) but never posted a result. Reclaim the pending entry so it
		// does not leak, and return a remediable error instead of a bare timeout.
		p.CancelExecution(execID)
		return conn.WriteErr(hubproto.ErrTimeout, fmt.Sprintf(
			"execution timed out after %s: browser connected but no reply for exec=%s frame=%s — the page is likely navigating/reloading or the script is hung (a heavy full-page screenshot on a large page can approach this window). Wait for the page to settle (check currentpage/proxylog) and retry.",
			proxyExecTimeout, execID, p.DescribeExecTarget(frameID)))
	}
}

// proxyExecTimeout bounds how long the hub waits for a browser to post back a
// PROXY EXEC result. The page must reply within this window; slow first paints
// (heavy SPA loads) can approach it, but a genuine reply is near-instant once
// the exec harness runs.
const proxyExecTimeout = 30 * time.Second

// hubHandleProxyToast handles PROXY TOAST command.

func (d *Daemon) hubHandleProxyToast(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "PROXY TOAST: args=%v dataLen=%d", cmd.Args, len(cmd.Data))

	if len(cmd.Args) < 1 {
		debug.Log("daemon", "PROXY TOAST: missing proxy ID")
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PROXY TOAST requires: <id>")
	}

	proxyID := cmd.Args[0]
	debug.Log("daemon", "PROXY TOAST: proxyID=%s", proxyID)

	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
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

	// The wire payload is protocol.ToastConfig (client.ProxyToast sends it
	// via WithJSON, tags type/title/message/duration/input/name). The old
	// inline struct here read toast_-prefixed tags that no client ever sent,
	// so every hub-routed toast failed parse with "toast_message is required".
	toast, err := unmarshalCommand[protocol.ToastConfig](cmd)
	if err != nil {
		debug.Log("daemon", "PROXY TOAST: failed to unmarshal: %v", err)
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid toast config: "+err.Error())
	}

	if toast.Type == "" {
		toast.Type = "info"
	}
	if toast.Message == "" {
		debug.Log("daemon", "PROXY TOAST: empty message")
		return conn.WriteErr(hubproto.ErrInvalidArgs, "toast message is required")
	}

	debug.Log("daemon", "PROXY TOAST: sending type=%s title=%q message=%q input=%s", toast.Type, toast.Title, toast.Message, toast.Input)

	sentCount, err := p.BroadcastToastOpts(proxy.ToastOptions{
		Type:     toast.Type,
		Title:    toast.Title,
		Message:  toast.Message,
		Duration: toast.Duration,
		Input:    toast.Input,
		Name:     toast.Name,
	})
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
	p, err := getSessionScoped(d, conn, proxyID, d.proxym.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	newProxy, err := d.restartProxy(ctx, p, conn.SessionCode())
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("failed to restart proxy: %v", err))
	}

	resp := map[string]interface{}{
		"id":          newProxy.ID,
		"target_url":  newProxy.TargetURL.String(),
		"listen_addr": newProxy.ListenAddr,
		"url":         newProxy.URL(),
		"restarted":   true,
		"success":     true,
		"message":     fmt.Sprintf("Proxy %q restarted successfully", newProxy.ID),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// restartProxy stops p and recreates it with everything it was running with:
// target, bound port, bind address, public URL, TLS verification, named
// tunnel, pending readiness dependencies, config snapshot, admin entry, overlay
// binding, incident owner and persisted state. PROXY RESTART and RESTART-ALL
// both go through it so the two cannot drift.
//
// callerSession becomes the new server's incident owner; when the caller has
// none, the previous owner is kept.
func (d *Daemon) restartProxy(ctx context.Context, p *proxy.ProxyServer, callerSession string) (*proxy.ProxyServer, error) {
	// The proxy's own id is the only one that may be used. A caller's argument
	// may be a single component of a compound id -- "dev" for
	// "myapp-abc1:dev:localhost-3000" -- and carrying that through would
	// recreate the proxy under the short name, dropping the project hash its
	// scoping depends on and leaving persisted state under the wrong key.
	proxyID := p.ID

	// Capture config before stopping. The bound port is preserved across the
	// restart: clients (browsers, stored URLs, CORS origins) expect the proxy
	// to come back on the SAME port, not drift to a fresh auto-assignment.
	// PublicURL and SkipTLSVerify are captured too — a restart that dropped
	// them would silently change behavior (URL rewriting, TLS verification)
	// versus the original start.
	targetURL := p.TargetURL.String()
	maxLogSize := int(p.Logger().Stats().MaxSize)
	projectPath := p.Path
	bindAddress := p.BindAddress
	allowExternal := p.AllowExternal
	publicURL := p.GetPublicURL()
	skipTLSVerify := p.SkipTLSVerify
	autoRestart := p.AutoRestart()
	namedTunnel := p.NamedTunnelConfig()
	boundPort := p.BoundPort()
	// Capture any still-pending readiness dependencies so the gate is re-armed
	// on the new server. A ready proxy has none (empty), and coming back ready
	// is correct; a still-gated proxy keeps waiting on the same deps.
	pendingDeps := p.PendingDependencies()
	configSnapshot, configured := d.proxyConfigs.Load(p.ID)
	ownerSession := callerSession
	if ownerSession == "" {
		if prev, ok := d.incidentProxyOwner.Load(p.ID); ok {
			if owner := ownerAsIncidentResource(prev); owner != nil {
				ownerSession = owner.SessionCode
			}
		}
	}
	// A script-linked proxy stays linked: retireIncidentProxyOwner drops the
	// script↔proxy index along with the rest of the per-proxy tracking.
	linkedScript := d.linkedScriptForProxy(p.ID)

	// Stop the proxy
	if err := d.proxym.Stop(ctx, proxyID); err != nil {
		debug.Warn("daemon", "error stopping proxy %s: %v", proxyID, err)
		d.recordStartupEntry(proxyID, "", "warning", "proxy_stop_failed",
			fmt.Sprintf("proxy restart: failed to stop proxy: %v", err), 0)
	} else {
		d.retireIncidentProxyOwner(proxyID)
	}

	// The admin entry belongs to the server that is going away. It is retired
	// here and registered again below against the new one, because a restart
	// need not come back on the same port: the entry would otherwise keep
	// pointing the overlay and SCRIPT LIST at the old address.
	d.retireExplicitProxyEntry(projectPath, proxyID)

	// Remove from persisted state
	if d.stateMgr != nil {
		d.stateMgr.RemoveProxy(proxyID)
	}

	// Wait for the old listener to release the port before rebinding it,
	// rather than guessing with a fixed sleep. Stop() closes the listener
	// synchronously, so this returns almost immediately in the common case.
	if boundPort > 0 {
		waitPortFree(boundPort, 2*time.Second)
	}

	// Recreate on the SAME port (best-effort). Preserving the port is the
	// point of a restart, but it must NOT be strict: if the port cannot be
	// reclaimed in time (the OS is slow to release it, or something else
	// grabbed it during the restart window), drifting to a fresh port is far
	// better than hard-failing the restart and leaving the proxy down. Start()
	// falls back to auto-assign when boundPort is in use.
	newProxy, err := d.proxym.Create(ctx, proxy.ProxyConfig{
		ID:            proxyID,
		TargetURL:     targetURL,
		ListenPort:    boundPort,
		MaxLogSize:    maxLogSize,
		Path:          projectPath,
		BindAddress:   bindAddress,
		AllowExternal: allowExternal,
		PublicURL:     publicURL,
		SkipTLSVerify: skipTLSVerify,
		AutoRestart:   autoRestart,
		NamedTunnel:   namedTunnel,
	})
	if err != nil {
		return nil, err
	}

	d.registerIncidentProxyOwner(newProxy.ID, ownerSession)
	if linkedScript != "" {
		// Untrack first: a failed Stop above leaves the old link in place,
		// and trackScriptProxy appends.
		d.untrackScriptProxy(newProxy.ID)
		d.trackScriptProxy(linkedScript, newProxy.ID)
	}
	d.registerExplicitProxyEntry(projectPath, newProxy.ID, newProxy.ListenAddr)
	newProxy.CopyPublicURLFrom(p)
	if configured {
		d.proxyConfigs.Store(newProxy.ID, configSnapshot)
	}
	d.wireProxyLogger(newProxy)

	// Re-bind the owning session's overlay endpoint so browser→agent messages
	// keep flowing after a restart (handleExplicitStart does this on first
	// start; a restart that skipped it left the proxy overlay-less). Fail
	// closed with no project path; rebindProxyOverlays late-binds on connect.
	if projectPath != "" {
		if ep := d.overlayEndpointForProject(projectPath); ep != "" {
			newProxy.SetOverlayEndpoint(ep)
		}
	}

	// Re-arm the readiness gate on the new server with any dependencies that
	// were still pending at restart time. Mirrors handleExplicitStart's
	// registerProxyDependencies, but feeds already-resolved process IDs (not
	// script names) captured off the old server.
	d.applyProxyDependencies(newProxy, proxyID, pendingDeps)

	// Persist the new proxy state on its ACTUAL bound port. Start() may have
	// drifted off boundPort if the OS was slow to release it or something else
	// grabbed it during the restart window; persisting the requested port would
	// record a stale value the restore path could not honor.
	if d.stateMgr != nil {
		configName := ""
		if configured {
			configName = configSnapshot.(configuredProxy).name
		}
		d.stateMgr.AddProxy(PersistentProxyConfig{
			ID:         proxyID,
			ConfigName: configName,
			TargetURL:  targetURL,
			Port:       newProxy.BoundPort(),
			MaxLogSize: maxLogSize,
			Path:       projectPath,
		})
	}

	return newProxy, nil
}

// ============================================================================
// AUTOMATION Command Handlers (chromedp sessions)
// ============================================================================
