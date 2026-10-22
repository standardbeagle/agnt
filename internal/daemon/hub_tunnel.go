package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/tunnel"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleTunnel(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("TUNNEL").dispatch(ctx, conn, cmd, d.tunnelActions())
}

func (d *Daemon) tunnelActions() map[string]handlerFn {
	return map[string]handlerFn{
		"START":  d.hubHandleTunnelStart,
		"STOP":   d.hubHandleTunnelStop,
		"STATUS": noCtx(d.hubHandleTunnelStatus),
		"LIST":   noCtx(d.hubHandleTunnelList),
	}
}

// hubHandleTunnelStart handles TUNNEL START command.

func (d *Daemon) hubHandleTunnelStart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "TUNNEL START requires: <id>")
	}

	tunnelID := cmd.Args[0]

	config, cfgErr := unmarshalCommand[struct {
		Provider     string `json:"provider"`
		LocalPort    int    `json:"local_port"`
		LocalHost    string `json:"local_host"`
		ProxyID      string `json:"proxy_id"`
		BinaryPath   string `json:"binary_path"`
		URLTimeoutMs int    `json:"url_timeout_ms"`
	}](cmd)
	if cfgErr != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid TUNNEL START payload: %v", cfgErr))
	}

	if config.Provider == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "provider is required")
	}
	if config.LocalPort == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "local_port is required")
	}

	// Validate the provider at the boundary (S9 ParseProvider) rather than
	// casting the raw string straight to tunnel.Provider. The cast only
	// surfaced a typo later as Start's generic "unsupported tunnel provider";
	// ParseProvider fails loud here naming the legal set, and normalizes the
	// value the tunnel is actually started with.
	provider, err := tunnel.ParseProvider(config.Provider)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	urlTimeout, err := d.tunnelURLWait(config.URLTimeoutMs)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	// Resolve the proxy before anything starts. A proxy_id that names no
	// proxy used to be skipped after the tunnel was up, and the reply still
	// said running: the caller believed the proxy now knew its public URL,
	// and the tunnel was left running for nothing.
	var linked *proxy.ProxyServer
	if config.ProxyID != "" {
		p, err := getSessionScoped(d, conn, config.ProxyID, d.proxym.GetWithPathFilter)
		if err != nil {
			return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("proxy_id %q: %v; no tunnel was started", config.ProxyID, err))
		}
		linked = p
	}

	// Get project path from session for session scoping
	projectPath := tunnelOwnerPath(d.getSessionProjectPath(conn), linked)

	tunnelConfig := tunnel.Config{
		Provider:   provider,
		LocalPort:  config.LocalPort,
		LocalHost:  config.LocalHost,
		BinaryPath: config.BinaryPath,
		Path:       projectPath,
	}

	// The provider process lives on ctx, the hub's own context, for as long
	// as the tunnel runs. Only the wait for its URL is bounded.
	t, err := d.tunnelm.Start(ctx, tunnelID, tunnelConfig)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	waitCtx, cancel := context.WithTimeout(ctx, urlTimeout)
	publicURL, err := t.WaitForURL(waitCtx)
	cancel()
	timedOut := errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
	if err != nil {
		return d.failTunnelStart(ctx, conn, t, provider, urlTimeout, timedOut, err)
	}

	if linked != nil {
		// Tell the proxy what started this tunnel, not just its URL: the
		// control WebSocket's local-caller rule is granted per tunnel
		// kind, and a hostname cannot stand in for it (tailscale serve
		// and the public Funnel share *.ts.net).
		linked.SetTunnelURL(publicURL, t.Done(), tunnelKindFor(provider))
	}

	resp := map[string]interface{}{
		"id":         tunnelID,
		"provider":   string(provider),
		"local_port": config.LocalPort,
		"public_url": publicURL,
		"status":     "running",
		// The MCP tunnel tool reads state and local_addr from this reply.
		"state":      t.Info().State,
		"local_addr": t.Info().LocalAddr,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// tunnelOwnerPath is the project a started tunnel belongs to: the connection's
// bound session, else the project of the proxy it fronts. The MCP connection is
// never session-bound, so without the proxy fallback its tunnels carried no
// path and never appeared in their own project's scoped TUNNEL LIST.
func tunnelOwnerPath(sessionPath string, linked *proxy.ProxyServer) string {
	if sessionPath != "" {
		return sessionPath
	}
	if linked != nil && linked.Path != "" && linked.Path != "." {
		return normalizePath(linked.Path)
	}
	return ""
}

// defaultTunnelURLTimeout bounds how long TUNNEL START waits for a provider to
// print its public URL when the caller names no url_timeout_ms. A Cloudflare
// quick tunnel or ngrok normally reports one within seconds; a minute leaves
// room for a slow edge without holding the caller's connection indefinitely.
const defaultTunnelURLTimeout = 60 * time.Second

// maxTunnelURLTimeout is the longest url_timeout_ms a caller may ask for.
const maxTunnelURLTimeout = 10 * time.Minute

// tunnelURLWait returns how long TUNNEL START waits for the public URL: the
// caller's url_timeout_ms when given, else the daemon default.
func (d *Daemon) tunnelURLWait(requestedMs int) (time.Duration, error) {
	switch {
	case requestedMs < 0:
		return 0, fmt.Errorf("url_timeout_ms must be positive, got %d", requestedMs)
	case int64(requestedMs) > maxTunnelURLTimeout.Milliseconds():
		return 0, fmt.Errorf("url_timeout_ms %d exceeds the maximum of %d", requestedMs, maxTunnelURLTimeout.Milliseconds())
	case requestedMs > 0:
		return time.Duration(requestedMs) * time.Millisecond, nil
	case d.tunnelURLTimeout > 0:
		return d.tunnelURLTimeout, nil
	default:
		return defaultTunnelURLTimeout, nil
	}
}

// tunnelStopTimeout bounds stopping a tunnel whose URL never arrived.
const tunnelStopTimeout = 5 * time.Second

// failTunnelStart answers a TUNNEL START whose tunnel produced no URL. The
// tunnel is stopped and its own registry entry retired before the reply, so
// the caller is never told "failed" about a provider still running under the
// id, and a retry of the same id is not refused as already existing. The
// error carries the provider's state and its last words.
func (d *Daemon) failTunnelStart(ctx context.Context, conn *hubpkg.Connection, t *tunnel.Tunnel, provider tunnel.Provider, wait time.Duration, timedOut bool, waitErr error) error {
	info := t.Info()
	lastOutput := t.LastOutput()

	var msg string
	if timedOut {
		msg = fmt.Sprintf("tunnel %s: %s printed no public URL within %s (state %s", t.ID(), provider, wait, info.State)
	} else {
		msg = fmt.Sprintf("tunnel %s: %s failed before printing a public URL: %v (state %s", t.ID(), provider, waitErr, info.State)
	}
	if info.Error != "" && timedOut {
		msg += "; error: " + info.Error
	}
	if lastOutput != "" && !strings.Contains(msg, lastOutput) {
		msg += "; last output: " + lastOutput
	}
	msg += ")"

	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tunnelStopTimeout)
	defer cancel()
	if err := d.tunnelm.StopExact(stopCtx, t); err != nil {
		msg += fmt.Sprintf("; the tunnel could not be stopped and is still registered: %v", err)
	} else {
		msg += "; the tunnel was stopped"
	}

	code := hubproto.ErrInternal
	if timedOut {
		code = hubproto.ErrTimeout
	}
	return conn.WriteErr(code, msg)
}

// tunnelKindFor maps the provider TUNNEL START launched to the kind the proxy
// records with the URL. TUNNEL START runs each provider one way only: a
// Cloudflare quick tunnel (never a named one — those are declared on the
// proxy), `tailscale serve` (never Funnel), `ngrok http`. An unrecognised
// provider maps to "", which the proxy treats as unknown.
func tunnelKindFor(provider tunnel.Provider) proxy.TunnelKind {
	switch provider {
	case tunnel.ProviderCloudflare:
		return proxy.TunnelKindCloudflareQuick
	case tunnel.ProviderTailscale:
		return proxy.TunnelKindTailscaleServe
	case tunnel.ProviderNgrok:
		return proxy.TunnelKindNgrok
	}
	return ""
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
	dirFilter, err := unmarshalCommand[protocol.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
	}

	// Route through the mandatory session-scope chokepoint (see
	// resolveProjectScope). A non-global query with no resolvable project
	// fails loud rather than listing every project's tunnels.
	projectPath, global, err := d.resolveProjectScope(dirFilter, conn.SessionCode())
	if err != nil {
		return d.writeScopeErr(conn, err, "session_code")
	}

	var infos []tunnel.TunnelInfo
	if global {
		infos = d.tunnelm.List()
	} else {
		infos = d.tunnelm.ListByPath(projectPath)
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
