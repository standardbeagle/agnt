package daemon

import (
	"context"
	"encoding/json"

	"github.com/standardbeagle/agnt/internal/debug"

	"github.com/standardbeagle/agnt/internal/proxy"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) hubHandleOverlay(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("OVERLAY").dispatch(ctx, conn, cmd, map[string]handlerFn{
		"SET":            noCtx(d.hubHandleOverlaySet),
		"GET":            connOnly(d.hubHandleOverlayGet),
		"":               connOnly(d.hubHandleOverlayGet),
		"CLEAR":          connOnly(d.hubHandleOverlayClear),
		"ACTIVITY":       noCtx(d.hubHandleOverlayActivity),
		"OUTPUT-PREVIEW": noCtx(d.hubHandleOverlayOutputPreview),
	})
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
		// Broadcast to all proxies
		proxiesToBroadcast = d.proxym.List()
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
				continue
			}
			proxiesToBroadcast = append(proxiesToBroadcast, p)
		}
	} else {
		proxiesToBroadcast = d.proxym.List()
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
