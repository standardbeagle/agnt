package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The proxy under test listens on 127.0.0.1:8123 (newGateProxy). These are the
// literal values a browser on this machine sends for it.
const (
	gateLocalHost   = "localhost:8123"
	gateLocalOrigin = "http://localhost:8123"
	gateTunnelURL   = "https://abc.trycloudflare.com"
)

// upgradeShape describes a control-WebSocket upgrade request by the four
// things the local-caller rule reads.
type upgradeShape struct {
	peer    string            // TCP peer (r.RemoteAddr)
	host    string            // Host header
	origin  string            // Origin header ("" = absent)
	headers map[string]string // anything else
}

func (s upgradeShape) request() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://"+s.host+"/__devtool_metrics", nil)
	r.Host = s.host
	r.RemoteAddr = s.peer
	if s.origin != "" {
		r.Header.Set("Origin", s.origin)
	}
	for k, v := range s.headers {
		r.Header.Set(k, v)
	}
	return r
}

// startedTunnel binds url to ps the way a tunnel agnt started does: with the
// tunnel's lifetime and its kind.
func startedTunnel(t *testing.T, ps *ProxyServer, url string, kind TunnelKind) {
	t.Helper()
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	ps.SetTunnelURL(url, done, kind)
}

// localBrowser is the developer's own browser on this machine.
func localBrowser() upgradeShape {
	return upgradeShape{peer: "127.0.0.1:53120", host: gateLocalHost, origin: gateLocalOrigin}
}

// tunnelVisitor is a remote visitor arriving through a Cloudflare quick
// tunnel, as captured live at the origin: the peer is cloudflared on loopback,
// Host and Origin are the public hostname, and the edge has added its headers.
func tunnelVisitor() upgradeShape {
	return upgradeShape{
		peer:   "127.0.0.1:53121",
		host:   "abc.trycloudflare.com",
		origin: gateTunnelURL,
		headers: map[string]string{
			"Cf-Ray":            "8f2a1b3c4d5e6f70-SJC",
			"Cf-Connecting-Ip":  "203.0.113.9",
			"X-Forwarded-For":   "203.0.113.9",
			"X-Forwarded-Proto": "https",
		},
	}
}

// The local developer keeps control whatever else the proxy is exposed to,
// for the postures whose tunnel behaviour has been observed. This is the
// regression: a bound quick tunnel (or a 0.0.0.0 bind) used to take the
// panel, secrets, voice and session/store away from the local browser.
func TestControlCallerTrusted_LocalBrowserKeepsControl(t *testing.T) {
	t.Run("no tunnel", func(t *testing.T) {
		assert.True(t, newGateProxy(t).controlCallerTrusted(localBrowser().request()))
	})
	t.Run("cloudflare quick tunnel started by agnt", func(t *testing.T) {
		ps := newGateProxy(t)
		startedTunnel(t, ps, gateTunnelURL, TunnelKindCloudflareQuick)
		assert.True(t, ps.controlCallerTrusted(localBrowser().request()))
	})
	t.Run("tailscale serve started by agnt", func(t *testing.T) {
		ps := newGateProxy(t)
		startedTunnel(t, ps, "https://build1.example.ts.net", TunnelKindTailscaleServe)
		assert.True(t, ps.controlCallerTrusted(localBrowser().request()))

		// A tailnet peer through that serve, as captured at the origin:
		// loopback peer, the tailnet name as Host, tailscaled's headers.
		visitor := upgradeShape{
			peer: "127.0.0.1:53122", host: "build1.example.ts.net", origin: "https://build1.example.ts.net",
			headers: map[string]string{
				"X-Forwarded-For":      "100.101.102.103",
				"X-Forwarded-Host":     "build1.example.ts.net",
				"Tailscale-User-Login": "peer@example.com",
			},
		}
		assert.False(t, ps.controlCallerTrusted(visitor.request()))
	})
	t.Run("bound to every interface", func(t *testing.T) {
		ps, err := NewProxyServer(ProxyConfig{
			ID: "gate-lan", TargetURL: "http://localhost:3000", ListenPort: 8123,
			MaxLogSize: 100, BindAddress: "0.0.0.0", AllowExternal: true,
		})
		require.NoError(t, err)
		t.Cleanup(func() { ps.pageTracker.Stop(); ps.StopChaos() })
		assert.True(t, ps.controlCallerTrusted(localBrowser().request()))

		// The same headers from a machine on the LAN: only the peer differs,
		// and the peer is what refuses it.
		lan := localBrowser()
		lan.peer = "192.168.1.50:53120"
		assert.False(t, ps.controlCallerTrusted(lan.request()))
	})
	t.Run("by loopback address", func(t *testing.T) {
		ps := newGateProxy(t)
		startedTunnel(t, ps, gateTunnelURL, TunnelKindCloudflareQuick)
		s := upgradeShape{peer: "127.0.0.1:53120", host: "127.0.0.1:8123", origin: "http://127.0.0.1:8123"}
		assert.True(t, ps.controlCallerTrusted(s.request()))
	})
}

// With a tunnel bound, devOIDCCaller calls nobody local, so the local-caller
// rule is the only thing that can grant trust — and every case below breaks
// exactly one of its conditions (or matches a real attacker shape). Each must
// be refused. The premise is asserted first: the unmodified local shape IS
// trusted on this proxy, so a refusal below is the changed field's doing.
func TestControlCallerTrusted_NearMissesRefused(t *testing.T) {
	ps := newGateProxy(t)
	startedTunnel(t, ps, gateTunnelURL, TunnelKindCloudflareQuick)
	if !ps.controlCallerTrusted(localBrowser().request()) {
		t.Fatalf("premise broken: the local browser shape must be trusted before any field is changed")
	}

	with := func(mutate func(*upgradeShape)) upgradeShape {
		s := localBrowser()
		mutate(&s)
		return s
	}
	cases := map[string]upgradeShape{
		// The real thing.
		"tunnel visitor (public Host and Origin, edge headers)": tunnelVisitor(),

		// (a) peer
		"loopback Host and Origin from a non-loopback peer": with(func(s *upgradeShape) { s.peer = "192.168.1.50:40000" }),
		"peer that is not an address":                       with(func(s *upgradeShape) { s.peer = "not-an-address" }),

		// (b) Host — the wrong port keeps Origin equal to Host, so only the
		// bound-port check can refuse it.
		"loopback Host on another port": with(func(s *upgradeShape) {
			s.host, s.origin = "localhost:9999", "http://localhost:9999"
		}),
		"Host with no port": with(func(s *upgradeShape) { s.host, s.origin = "localhost", "http://localhost" }),
		"no edge headers but a public Host": with(func(s *upgradeShape) {
			s.host, s.origin = "abc.trycloudflare.com", gateTunnelURL
		}),
		"public Host with a loopback Origin": with(func(s *upgradeShape) { s.host = "abc.trycloudflare.com" }),

		// (c) Origin
		"Origin of another site":           with(func(s *upgradeShape) { s.origin = "http://evil.example" }),
		"Origin on another port":           with(func(s *upgradeShape) { s.origin = "http://localhost:9999" }),
		"Origin is the public URL":         with(func(s *upgradeShape) { s.origin = gateTunnelURL }),
		"no Origin (a non-browser client)": with(func(s *upgradeShape) { s.origin = "" }),
		"Origin that is not http(s)":       with(func(s *upgradeShape) { s.origin = "file://localhost:8123" }),
		"Origin carrying userinfo":         with(func(s *upgradeShape) { s.origin = "http://x@localhost:8123" }),
		"DNS rebinding (attacker name as Host and Origin)": with(func(s *upgradeShape) {
			s.host, s.origin = "rebind.evil.example:8123", "http://rebind.evil.example:8123"
		}),
		"lookalike loopback name": with(func(s *upgradeShape) {
			s.host, s.origin = "localhost.evil.example:8123", "http://localhost.evil.example:8123"
		}),
	}
	// (d) one fingerprint header at a time on an otherwise perfect local
	// shape. Spelled out here, not read from the production list: a header
	// dropped from that list must fail this test.
	for _, header := range []string{
		"Cf-Ray", "Cf-Connecting-Ip", "Cf-Visitor", "Cdn-Loop",
		"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-Port", "Forwarded",
		"Tailscale-User-Login", "Tailscale-User-Name", "Ngrok-Trace-Id", "Via", "X-Real-Ip",
	} {
		header := header
		cases["fingerprint header "+header] = with(func(s *upgradeShape) {
			s.headers = map[string]string{header: "1"}
		})
	}

	for name, shape := range cases {
		t.Run(name, func(t *testing.T) {
			assert.False(t, ps.controlCallerTrusted(shape.request()))
		})
	}
}

// (e): the rule is granted by what started the tunnel, never by what its URL
// looks like. Every binding below leaves the local browser unrecognised (the
// pre-existing behaviour): a URL bound without a tunnel of agnt's own, a
// provider that was not captured, or a kind whose URL is not what that kind
// produces. The premise — the same proxy with a captured kind IS trusted — is
// the LocalBrowserKeepsControl test above.
func TestControlCallerTrusted_PublicURLWithoutACapturedTunnelKindDoesNotGetLocalRule(t *testing.T) {
	cases := map[string]func(t *testing.T, ps *ProxyServer){
		// Bound some other way: no provider at all.
		"operator public-url on ts.net":           func(_ *testing.T, ps *ProxyServer) { ps.SetPublicURL("https://build1.example.ts.net") },
		"operator public-url on trycloudflare":    func(_ *testing.T, ps *ProxyServer) { ps.SetPublicURL(gateTunnelURL) },
		"operator public-url on an unknown host":  func(_ *testing.T, ps *ProxyServer) { ps.SetPublicURL("https://dev.example.com") },
		"tunnel of unknown kind on ts.net":        func(t *testing.T, ps *ProxyServer) { startedTunnel(t, ps, "https://build1.example.ts.net", "") },
		"tunnel of unknown kind on trycloudflare": func(t *testing.T, ps *ProxyServer) { startedTunnel(t, ps, gateTunnelURL, "") },
		// A provider that was not captured.
		"ngrok tunnel": func(t *testing.T, ps *ProxyServer) {
			startedTunnel(t, ps, "https://abc123.ngrok-free.app", TunnelKindNgrok)
		},
		"ngrok tunnel on a lookalike host": func(t *testing.T, ps *ProxyServer) {
			startedTunnel(t, ps, gateTunnelURL, TunnelKindNgrok)
		},
		// The second lock: a captured kind whose URL is not that kind's.
		"quick tunnel kind, http URL": func(t *testing.T, ps *ProxyServer) {
			startedTunnel(t, ps, "http://abc.trycloudflare.com", TunnelKindCloudflareQuick)
		},
		"quick tunnel kind, bare suffix": func(t *testing.T, ps *ProxyServer) {
			startedTunnel(t, ps, "https://trycloudflare.com", TunnelKindCloudflareQuick)
		},
		"quick tunnel kind, suffix in the middle": func(t *testing.T, ps *ProxyServer) {
			startedTunnel(t, ps, "https://abc.trycloudflare.com.evil.test", TunnelKindCloudflareQuick)
		},
		"quick tunnel kind, ts.net URL": func(t *testing.T, ps *ProxyServer) {
			startedTunnel(t, ps, "https://build1.example.ts.net", TunnelKindCloudflareQuick)
		},
		"tailscale serve kind, trycloudflare URL": func(t *testing.T, ps *ProxyServer) {
			startedTunnel(t, ps, gateTunnelURL, TunnelKindTailscaleServe)
		},
	}
	for name, bind := range cases {
		t.Run(name, func(t *testing.T) {
			ps := newGateProxy(t)
			bind(t, ps)
			assert.False(t, ps.controlCallerTrusted(localBrowser().request()))
		})
	}
}

// When the tunnel exits, its binding goes with it and the proxy is an ordinary
// loopback proxy again.
func TestControlCallerTrusted_TunnelBindingEndsWithTheTunnel(t *testing.T) {
	ps := newGateProxy(t)
	done := make(chan struct{})
	ps.SetTunnelURL("https://abc123.ngrok-free.app", done, TunnelKindNgrok)
	require.False(t, ps.controlCallerTrusted(localBrowser().request()), "premise: refused while the ngrok tunnel is up")
	close(done)
	assert.True(t, ps.controlCallerTrusted(localBrowser().request()))
}

// The named tunnel's ingress listener carries only tunnel traffic: nothing
// about a request arriving there makes it local, and Access still does.
func TestControlCallerTrusted_NamedTunnelIngressIsNeverLocal(t *testing.T) {
	ps := newGateProxy(t)
	r := localBrowser().request()
	tunnelled := context.WithValue(r.Context(), tunnelledKey{}, true)
	assert.False(t, ps.controlCallerTrusted(r.WithContext(tunnelled)))

	verified := context.WithValue(tunnelled, accessIdentityKey{}, AccessIdentity{Email: "dev@example.com"})
	assert.True(t, ps.controlCallerTrusted(r.WithContext(verified)))
}

// agentInputFrames is every frame type whose handler hands browser content to
// the agent, with a payload its parser accepts and a marker to look for.
const agentInputMarker = "TYPED-BY-THE-VISITOR"

func agentInputFrames() map[string]*wsMessage {
	text := map[string]interface{}{"message": agentInputMarker, "selector": "#" + agentInputMarker, "description": agentInputMarker}
	return map[string]*wsMessage{
		"panel_message": {Type: "panel_message", Data: map[string]interface{}{
			"payload": map[string]interface{}{"message": agentInputMarker},
		}},
		"sketch":             {Type: "sketch", Data: map[string]interface{}{"description": agentInputMarker, "payload": text}},
		"design_state":       {Type: "design_state", Data: text},
		"design_request":     {Type: "design_request", Data: text},
		"design_chat":        {Type: "design_chat", Data: text},
		"design_edit":        {Type: "design_edit", Data: text},
		"responsive_request": {Type: "responsive_request", Data: text},
		"responsive_state":   {Type: "responsive_state", Data: text},
	}
}

func dispatch(c *wsConnState, msg *wsMessage) {
	wsMessageHandlers[msg.Type](c, msg)
}

// Every agent-input frame from an untrusted connection is dropped whole:
// nothing reaches the overlay (the agent's terminal), nothing but the refusal
// diagnostic reaches the traffic log (which channel mode forwards), and the
// sender is shown why. The same frame on a trusted connection is delivered —
// which is also the premise that the overlay capture would have seen it.
func TestAgentInputFrames_RefusedForUntrustedCaller(t *testing.T) {
	for msgType, msg := range agentInputFrames() {
		msg := msg
		t.Run(msgType, func(t *testing.T) {
			// Trusted first: the frame does reach the overlay.
			ps := newGateProxy(t)
			oc, socketPath := startOverlayCapture(t)
			ps.OverlayNotifier().SetEndpoint(socketPath)
			trusted, _ := newGateConn(t, ps, true)
			dispatch(trusted, msg)
			if len(oc.all()) == 0 {
				t.Fatalf("premise broken: a trusted %s must reach the overlay, or the refusal below proves nothing", msgType)
			}

			// Untrusted, on a fresh proxy and overlay.
			ps = newGateProxy(t)
			oc, socketPath = startOverlayCapture(t)
			ps.OverlayNotifier().SetEndpoint(socketPath)
			untrusted, stub := newGateConn(t, ps, false)
			dispatch(untrusted, msg)

			assert.Empty(t, oc.all(), "an untrusted %s must not reach the overlay", msgType)
			for _, e := range ps.Logger().Query(LogFilter{Limit: 1000}) {
				assert.Equalf(t, LogTypeDiagnostic, e.Type, "an untrusted %s must leave nothing in the traffic log but the refusal", msgType)
				raw, err := json.Marshal(e)
				require.NoError(t, err)
				assert.NotContains(t, string(raw), agentInputMarker)
			}
			assert.Contains(t, diagnosticEvents(ps), "ws_privileged_frame_refused")

			// The sender is told, on its own connection.
			require.Eventually(t, func() bool { return stub.count() == 1 }, 3*time.Second, 5*time.Millisecond,
				"the refused sender must be shown a toast")
			toast := stub.frames(t)[0]
			assert.Equal(t, "toast", toast["type"])
			payload := toast["payload"].(map[string]interface{})
			assert.Equal(t, "error", payload["type"])
			assert.Equal(t, "Not sent to the agent", payload["title"])
			assert.Contains(t, payload["message"], "requires a local, tailnet or Cloudflare Access-verified caller")
		})
	}
}

// A browser error from an untrusted connection stays telemetry: it is logged,
// but its text is not typed into the agent's terminal.
func TestErrorFrame_NotForwardedToOverlayForUntrustedCaller(t *testing.T) {
	msg := &wsMessage{Type: "error", Data: map[string]interface{}{"message": agentInputMarker}}

	ps := newGateProxy(t)
	oc, socketPath := startOverlayCapture(t)
	ps.OverlayNotifier().SetEndpoint(socketPath)
	trusted, _ := newGateConn(t, ps, true)
	dispatch(trusted, msg)
	if len(oc.all()) != 1 {
		t.Fatalf("premise broken: a trusted error frame must be forwarded to the overlay")
	}

	ps = newGateProxy(t)
	oc, socketPath = startOverlayCapture(t)
	ps.OverlayNotifier().SetEndpoint(socketPath)
	untrusted, _ := newGateConn(t, ps, false)
	dispatch(untrusted, msg)
	assert.Empty(t, oc.all(), "an untrusted error frame must not be forwarded to the overlay")
	assert.Len(t, ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypeError}}), 1, "it is still logged as telemetry")
}

// The refusal toast is spaced out per connection: a burst of refused frames
// shows one notice.
func TestAgentInputRefusalToastIsThrottled(t *testing.T) {
	ps := newGateProxy(t)
	c, stub := newGateConn(t, ps, false)
	for i := 0; i < 5; i++ {
		dispatch(c, agentInputFrames()["design_state"])
	}
	require.Eventually(t, func() bool { return stub.count() >= 1 }, 3*time.Second, 5*time.Millisecond)
	assert.Equal(t, 1, stub.count())
}

// dialControlWS opens the proxy's control WebSocket with exactly the given
// Host, Origin and extra headers.
func dialControlWS(t *testing.T, listenAddr, host, origin string, extra map[string]string) *websocket.Conn {
	t.Helper()
	u := url.URL{Scheme: "ws", Host: listenAddr, Path: "/__devtool_metrics"}
	header := http.Header{"Origin": {origin}, "Host": {host}}
	for k, v := range extra {
		header.Set(k, v)
	}
	var conn *websocket.Conn
	require.Eventually(t, func() bool {
		c, _, err := websocket.DefaultDialer.Dial(u.String(), header)
		conn = c
		return err == nil
	}, 3*time.Second, 20*time.Millisecond, "the control websocket must accept the upgrade")
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func overlaySaw(oc *overlayCapture, text string) bool {
	for _, body := range oc.all() {
		if strings.Contains(body, text) {
			return true
		}
	}
	return false
}

// End to end through real upgrades on one proxy with a quick tunnel bound:
// the local browser's panel message reaches the agent; a tunnel visitor's is
// refused with a toast and never reaches it. Both arrive from a loopback peer
// — which is why the peer alone decides nothing.
func TestAgentInput_E2E_LocalDeliveredTunnelVisitorRefused(t *testing.T) {
	backend := newWrapBackend(t)
	ps := startWrapProxy(t, backend)
	startedTunnel(t, ps, gateTunnelURL, TunnelKindCloudflareQuick)
	oc, socketPath := startOverlayCapture(t)
	ps.OverlayNotifier().SetEndpoint(socketPath)

	panel := func(text string) map[string]interface{} {
		return map[string]interface{}{
			"type": "panel_message",
			"data": map[string]interface{}{"payload": map[string]interface{}{"message": text}},
		}
	}

	// The tunnel visitor first, so a late delivery would be caught below.
	visitor := tunnelVisitor()
	remote := dialControlWS(t, ps.ListenAddr, visitor.host, visitor.origin, visitor.headers)
	require.NoError(t, remote.WriteJSON(panel("FROM-THE-TUNNEL-VISITOR")))
	var toast map[string]interface{}
	deadline := time.Now().Add(5 * time.Second)
	for toast == nil {
		require.True(t, time.Now().Before(deadline), "the refused visitor must be sent a toast")
		_ = remote.SetReadDeadline(deadline)
		_, raw, err := remote.ReadMessage()
		require.NoError(t, err)
		var frame map[string]interface{}
		if json.Unmarshal(raw, &frame) == nil && frame["type"] == "toast" {
			if payload, ok := frame["payload"].(map[string]interface{}); ok && payload["title"] == "Not sent to the agent" {
				toast = payload
			}
		}
	}
	assert.Equal(t, "error", toast["type"])

	// The local browser, on the same listener, with the tunnel still bound.
	local := dialControlWS(t, ps.ListenAddr, ps.ListenAddr, "http://"+ps.ListenAddr, nil)
	require.NoError(t, local.WriteJSON(panel("FROM-THE-LOCAL-DEVELOPER")))
	require.Eventually(t, func() bool { return overlaySaw(oc, "FROM-THE-LOCAL-DEVELOPER") },
		5*time.Second, 20*time.Millisecond, "the local browser's panel message must reach the agent while a tunnel is bound")

	// The visitor's frame was handled before the toast was sent and before
	// the local message was delivered; it reached neither surface.
	assert.False(t, overlaySaw(oc, "FROM-THE-TUNNEL-VISITOR"), "the tunnel visitor's text must never reach the agent")
	for _, e := range ps.Logger().Query(LogFilter{Limit: 1000}) {
		raw, err := json.Marshal(e)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "FROM-THE-TUNNEL-VISITOR")
	}
	assert.Contains(t, diagnosticEvents(ps), "ws_privileged_frame_refused")
}
