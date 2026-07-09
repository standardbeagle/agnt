package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/standardbeagle/agnt/internal/proxy"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) overlayActions() map[string]handlerFn {
	return map[string]handlerFn{
		"SET":            noCtx(d.hubHandleOverlaySet),
		"GET":            connOnly(d.hubHandleOverlayGet),
		"":               connOnly(d.hubHandleOverlayGet),
		"CLEAR":          connOnly(d.hubHandleOverlayClear),
		"ACTIVITY":       noCtx(d.hubHandleOverlayActivity),
		"OUTPUT-PREVIEW": noCtx(d.hubHandleOverlayOutputPreview),
		"FORWARDING":     noCtx(d.hubHandleOverlayForwarding),
	}
}

func (d *Daemon) hubHandleOverlay(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("OVERLAY").dispatch(ctx, conn, cmd, d.overlayActions())
}

// hubHandleOverlayForwarding handles OVERLAY FORWARDING command.
// Args: <paused:true/false>. Pauses or resumes agent-inbound push (incident
// digest pings) for the connection's session. Pull surfaces are unaffected.
func (d *Daemon) hubHandleOverlayForwarding(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "OVERLAY FORWARDING requires: <paused:true/false>")
	}
	sessionCode := conn.SessionCode()
	if sessionCode == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "no session attached: OVERLAY FORWARDING is session-scoped")
	}
	// Anything that isn't exactly "true" used to mean "resume": a typo, or a
	// caller sending "1", silently un-paused agent-inbound push.
	paused, err := strconv.ParseBool(cmd.Args[0])
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("OVERLAY FORWARDING requires <paused:true/false>, got %q", cmd.Args[0]))
	}
	d.SetForwardingPaused(sessionCode, paused)

	data, _ := json.Marshal(map[string]interface{}{
		"status": "ok",
		"paused": paused,
	})
	return conn.WriteJSON(data)
}

// hubHandleOverlaySet handles OVERLAY SET command.

func (d *Daemon) hubHandleOverlaySet(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	config, _ := unmarshalCommand[struct {
		Endpoint string `json:"endpoint"`
	}](cmd)

	if config.Endpoint == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "endpoint is required")
	}

	d.SetOverlayEndpoint(config.Endpoint)
	return conn.WriteOK("overlay endpoint set")
}

// hubHandleOverlayGet handles OVERLAY GET command.

func (d *Daemon) hubHandleOverlayGet(conn *hubpkg.Connection) error {
	endpoint := d.OverlayEndpoint()

	resp := map[string]interface{}{
		"endpoint": endpoint,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleOverlayClear handles OVERLAY CLEAR command.

func (d *Daemon) hubHandleOverlayClear(conn *hubpkg.Connection) error {
	d.SetOverlayEndpoint("")
	return conn.WriteOK("overlay endpoint cleared")
}

// hubHandleOverlayActivity handles OVERLAY ACTIVITY command.
// Args: <active:true/false> [proxyID1 proxyID2 ...]
// Broadcasts activity state to specified proxies (or all proxies if none specified).

func (d *Daemon) hubHandleOverlayActivity(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "OVERLAY ACTIVITY requires: <active:true/false> [proxyIDs...]")
	}

	// Parse active state
	active := cmd.Args[0] == "true"

	// Get proxy IDs (if specified)
	proxyIDs := cmd.Args[1:]

	// Broadcast to specified proxies or all proxies
	var proxiesToBroadcast []*proxy.ProxyServer
	if len(proxyIDs) > 0 {
		// Broadcast to specific proxies
		for _, proxyID := range proxyIDs {
			p, err := d.proxym.Get(proxyID)
			if err != nil {
				debug.Warn("daemon", "Proxy %s not found for activity broadcast: %v", proxyID, err)
				continue
			}
			proxiesToBroadcast = append(proxiesToBroadcast, p)
		}
	} else {
		// No explicit proxy IDs: broadcast to this session's project proxies
		// only. Fail loud if the connection has no session — never silently
		// fan activity out across every project.
		sc, err := d.resolveScope(protocol.DirectoryFilter{}, conn.SessionCode())
		if err != nil {
			return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
		}
		proxiesToBroadcast = d.proxym.ListScoped(sc)
	}

	// Broadcast activity state to each proxy
	totalSent := 0
	for _, p := range proxiesToBroadcast {
		sentCount := p.BroadcastActivityState(active)
		totalSent += sentCount
	}

	data, _ := json.Marshal(map[string]interface{}{
		"status":       "ok",
		"active":       active,
		"proxies":      len(proxiesToBroadcast),
		"clients_sent": totalSent,
	})
	return conn.WriteJSON(data)
}

// hubHandleOverlayOutputPreview handles OVERLAY OUTPUT-PREVIEW command.
// Broadcasts output preview lines to connected browsers via proxies.

func (d *Daemon) hubHandleOverlayOutputPreview(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	payload, err := unmarshalCommand[struct {
		Lines    []string `json:"lines"`
		ProxyIDs []string `json:"proxy_ids"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid payload")
	}

	if len(payload.Lines) == 0 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "lines required")
	}

	// Get proxies to broadcast to
	var proxiesToBroadcast []*proxy.ProxyServer
	if len(payload.ProxyIDs) > 0 {
		for _, proxyID := range payload.ProxyIDs {
			p, err := d.proxym.Get(proxyID)
			if err != nil {
				// Best-effort UI broadcast: a requested proxy may have been torn
				// down between the caller's list and this send. Skip it, but log
				// so a consistently missing target is diagnosable.
				debug.Log("overlay", "broadcast skipped unknown proxy %s: %v", proxyID, err)
				continue
			}
			proxiesToBroadcast = append(proxiesToBroadcast, p)
		}
	} else {
		sc, err := d.resolveScope(protocol.DirectoryFilter{}, conn.SessionCode())
		if err != nil {
			return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
		}
		proxiesToBroadcast = d.proxym.ListScoped(sc)
	}

	// Broadcast to each proxy
	totalSent := 0
	for _, p := range proxiesToBroadcast {
		sentCount := p.BroadcastOutputPreview(payload.Lines)
		totalSent += sentCount
	}

	data, _ := json.Marshal(map[string]interface{}{
		"status":       "ok",
		"lines":        len(payload.Lines),
		"proxies":      len(proxiesToBroadcast),
		"clients_sent": totalSent,
	})
	return conn.WriteJSON(data)
}

// hubHandleTunnel handles the TUNNEL command.
