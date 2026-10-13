package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/publish"
)

// WebSocket keepalive timings. A ping is sent every wsPingPeriod; the peer's
// pong (or any inbound frame) extends the read deadline by wsPongWait. A peer
// that goes silent for wsPongWait is treated as dead and the read loop exits.
const (
	wsPongWait   = 60 * time.Second
	wsPingPeriod = (wsPongWait * 9) / 10
	wsWriteWait  = 10 * time.Second
)

// Per-connection resource bounds. The connection cap alone does not bound
// memory: gorilla buffers a whole message before returning it, and some message
// types run on their own goroutine.
const (
	// wsMaxMessageBytes caps one inbound message. The largest legitimate frames
	// are full-page screenshot PNGs; a larger frame closes the connection.
	wsMaxMessageBytes = 16 << 20
	// wsMaxHandlers caps the handler goroutines one connection may have in
	// flight. At the cap the read loop waits for a slot, so a client that
	// floods spawned requests is slowed instead of multiplying goroutines.
	wsMaxHandlers = 8

	// Frame-rate cap for one connection (see wsFrameLimiter). A frame costs one
	// token plus one per wsFrameCostUnit bytes, so a 16 MiB screenshot frame
	// spends most of a burst and a flood of them is held to a few MB/s. The
	// numbers leave ordinary telemetry (batched interactions, voice audio at
	// tens of frames a second, an occasional screenshot) untouched.
	wsFrameRatePerSecond = 100
	wsFrameBurst         = 400
	wsFrameCostUnit      = 64 << 10
)

// wsFrameLimiter is a token bucket for the frames of one control WebSocket
// connection. The HTTP request-rate cap counts the upgrade once; after that a
// connection could send frames — each up to wsMaxMessageBytes, many of them
// written to disk — at whatever rate the socket allowed. Used only from the
// connection's read loop, so it needs no locking.
type wsFrameLimiter struct {
	tokens float64
	last   time.Time
}

func newWSFrameLimiter(now time.Time) *wsFrameLimiter {
	return &wsFrameLimiter{tokens: wsFrameBurst, last: now}
}

// reserve charges one frame of size bytes and returns how long the caller must
// wait before handling it (0 when the frame is within budget). The charge is
// always taken, so the bucket may go negative; the wait is what repays it.
func (l *wsFrameLimiter) reserve(now time.Time, size int) time.Duration {
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens += elapsed.Seconds() * wsFrameRatePerSecond
		if l.tokens > wsFrameBurst {
			l.tokens = wsFrameBurst
		}
		l.last = now
	}
	l.tokens -= float64(1 + size/wsFrameCostUnit)
	if l.tokens >= 0 {
		return 0
	}
	return time.Duration(-l.tokens / wsFrameRatePerSecond * float64(time.Second))
}

// controlCallerTrusted reports whether the caller behind a control WebSocket
// upgrade may use the privileged frames: the ones that reach the agent
// (panel_message, sketch, the design_* and responsive_* handoffs), write
// secrets or project data (secret_submit, store_request), drive sessions
// (session_request) or spend the operator's API keys (voice_start). It is
// evaluated once, on the upgrade request, and pinned on the connection; no
// later frame can change it.
//
// Trusted, in order:
//
//  1. a verified Cloudflare Access identity (named-tunnel ingress);
//  2. the local developer's own browser — isLocalControlCaller;
//  3. whoever the dev OIDC issuer would serve (devOIDCCaller): an identified
//     tailnet peer, or its own notion of a local request.
//
// A request from the named tunnel's ingress listener is only ever (1): that
// listener carries nothing but tunnel traffic, so no property of the request
// can make it local. Everything else is refused — above all an
// unauthenticated tunnel visitor, who can open this socket because
// checkWSOrigin trusts the bound public URL.
func (ps *ProxyServer) controlCallerTrusted(r *http.Request) bool {
	if _, ok := AccessIdentityFrom(r.Context()); ok {
		return true
	}
	if tunnelled, _ := r.Context().Value(tunnelledKey{}).(bool); tunnelled {
		return false
	}
	if ps.isLocalControlCaller(r) {
		return true
	}
	caller := ps.devOIDCCaller(r)
	return caller.Local || caller.TailnetLogin != ""
}

// isLocalControlCaller is the local-caller rule for the control WebSocket: it
// recognises the developer's own browser on this machine even while the proxy
// is also exposed (a quick tunnel bound to its listener, or a 0.0.0.0 bind) —
// postures in which devOIDCCaller, which must be far stricter because it mints
// identity tokens, calls nobody local. Without it, exposing a proxy took the
// panel, secrets, voice and the session/store API away from the developer too.
//
// ALL four must hold, and each is the only thing stopping some attacker:
//
//	(a) the TCP peer is loopback;
//	(b) Host is a loopback authority on the proxy's bound port;
//	(c) Origin is that same loopback origin (sameLoopbackOrigin — the very
//	    test checkWSOrigin's loopback branch applies);
//	(d) no reverse-proxy fingerprint header is present
//	    (hasReverseProxyFingerprint, the one list devOIDCCaller also uses).
//
// and one precondition on the posture:
//
//	(e) the public URL bound to this proxy, if any, belongs to a provider
//	    whose forwarding behaviour was captured live (localRuleVerifiedTunnel)
//	    or is the proxy's own named tunnel, which has a separate ingress
//	    listener. With any other public URL bound — ngrok, or a static
//	    public-url in front of something unknown — the rule does not apply
//	    and the caller falls through to devOIDCCaller, which refuses it. The
//	    rule's safety against a tunnel visitor rests on what that provider's
//	    edge does with Host and its own headers; for a provider nobody has
//	    observed, that is an assumption, and an assumption is not a control.
//
// Who is on the other side, and what stops them:
//
//   - A visitor through a tunnel. The tunnel agent (cloudflared, ngrok,
//     tailscaled) runs on this machine and dials the proxy over loopback, so
//     (a) is TRUE for every tunnelled request and proves nothing here. The
//     visitor controls the path, the Origin (a non-browser client sends any
//     value) and most headers, so (c) proves nothing either. What they do not
//     control is what the provider's edge decides: the Host it routes by and
//     forwards, and the headers it adds.
//     Cloudflare (captured live against a quick tunnel): every request
//     reaches the origin with Cf-Ray, Cf-Connecting-Ip and X-Forwarded-For/
//     -Proto; a client-sent X-Forwarded-For is appended to, a client-sent
//     Cf-Connecting-Ip is rejected at the edge. So (d) fails for every
//     visitor, whatever they send — and by default (b) fails too, since
//     cloudflared forwards the public hostname as Host.
//     tailscale serve (captured live, node to node): Host is always the
//     node's tailnet name, never loopback (a request by tailnet IP gets a 404
//     from tailscale and never arrives); X-Forwarded-For/-Host and
//     Tailscale-User-* are set by tailscaled, client-sent values overwritten.
//     So (b) and (d) each fail.
//     ngrok has NOT been captured. Its documentation says the same (public
//     Host unless --host-header=rewrite; X-Forwarded-* added), but this rule
//     does not lean on that: see (e).
//     agnt never starts a tunnel with a Host rewrite. An operator who runs
//     their own with one (cloudflared --http-host-header) gives up (b) and
//     is left with (d) alone.
//   - A reverse proxy the operator runs themselves on this machine, set to
//     rewrite Host to localhost AND add no forwarding header. Its visitors
//     satisfy all four. Nothing in the request distinguishes them; this rule
//     cannot help that setup, and neither could the one it supplements.
//   - A machine on the LAN, when the proxy is bound beyond loopback. It can
//     send Host, Origin and no fingerprints at will; (a) is what refuses it —
//     the peer address is the kernel's, not a header.
//   - A web page the developer happens to visit. A browser sets Origin
//     itself, so a page on another site that opens a socket to
//     localhost:<port> fails (c); and one that rebinds its own hostname to
//     127.0.0.1 has that hostname as Host and Origin, failing (b) and (c).
//     Both are refused the upgrade by checkWSOrigin before they get here.
//   - Another process on this machine. It can forge all four — and can
//     already read the project and type into the terminal. Out of scope, as
//     it is for every loopback listener.
//
// localRuleVerifiedTunnelSuffixes are the public-URL host suffixes of the
// tunnel providers whose origin-side behaviour (Host forwarded, headers added,
// client-sent copies overwritten or rejected) was captured live: Cloudflare
// quick tunnels and tailscale serve. Add a provider here only with a capture.
var localRuleVerifiedTunnelSuffixes = []string{".trycloudflare.com", ".ts.net"}

// localRuleVerifiedTunnel reports whether publicURL is an https URL on one of
// those providers' hostnames.
func localRuleVerifiedTunnel(publicURL string) bool {
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	for _, suffix := range localRuleVerifiedTunnelSuffixes {
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return true
		}
	}
	return false
}

func (ps *ProxyServer) isLocalControlCaller(r *http.Request) bool {
	if pub := ps.GetPublicURL(); pub != "" && pub != ps.namedTunnelURL() && !localRuleVerifiedTunnel(pub) {
		return false // (e)
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(peer); ip == nil || !ip.IsLoopback() {
		return false // (a)
	}
	_, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != strconv.Itoa(ps.BoundPort()) || !isLoopbackAuthority(r.Host) {
		return false // (b)
	}
	if !sameLoopbackOrigin(r) {
		return false // (c)
	}
	return !hasReverseProxyFingerprint(r) // (d)
}

// errorHandler handles proxy errors.
func (ps *ProxyServer) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	seq := ps.requestSeq.Add(1)
	reqID := fmt.Sprintf("req-%d", seq)
	timestamp := time.Now()

	errStr := err.Error()

	// Distinguish downstream (client) cancellation from upstream transport failure.
	// A client disconnect (tab close, navigation, AbortController) surfaces here as
	// context.Canceled / context.DeadlineExceeded. Flushing the transport pool on
	// those would kill healthy shared HTTPS connections and force every concurrent
	// request to redo TLS — which on slow upstreams (e.g. haam-dev ~2-3s handshake)
	// cascades into 502s. Only flush on genuine upstream transport errors.
	isClientCanceled := errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		(r.Context().Err() != nil && strings.Contains(errStr, "context canceled"))

	// Check if this is a transient connection error (common during development)
	// These happen when dev servers restart, connections timeout, etc.
	isTransient := isTransientConnectionError(errStr)

	// Flush stale connections on transient upstream errors so the browser's immediate
	// reconnect (especially WebSocket/HMR) gets a fresh connection to the backend.
	// Never flush on client-side cancellation — that nukes the pool for innocent
	// concurrent requests and causes cascading 502s.
	if isTransient && !isClientCanceled {
		ps.FlushConnections()
	}

	ps.logger.LogHTTP(HTTPLogEntry{
		ID:         reqID,
		Timestamp:  timestamp,
		Method:     r.Method,
		URL:        publish.ScrubSharePath(r.URL.String()),
		StatusCode: http.StatusBadGateway,
		Error:      errStr,
	})

	// Provide helpful error message based on error type
	var userMsg string
	var diagEvent string
	var diagLevel ProxyDiagnosticLevel

	if isClientCanceled {
		// The downstream client went away (tab close, navigation, AbortController).
		// This is not a proxy or upstream failure — don't alarm the user and don't
		// touch the connection pool.
		userMsg = "Client canceled the request before the response completed."
		diagEvent = "client_canceled"
		diagLevel = DiagnosticWarning
	} else if strings.Contains(errStr, "context canceled") {
		userMsg = fmt.Sprintf("Proxy Error: Request canceled. The proxy may be shutting down, or the target server (%s) is unavailable.", ps.TargetURL.String())
		diagEvent = "context_canceled"
		diagLevel = DiagnosticWarning
	} else if strings.Contains(errStr, "connection refused") {
		userMsg = fmt.Sprintf("Proxy Error: Cannot connect to target server %s. Make sure the server is running.", ps.TargetURL.String())
		diagEvent = "connection_refused"
		diagLevel = DiagnosticError
	} else if strings.Contains(errStr, "no such host") {
		userMsg = fmt.Sprintf("Proxy Error: Cannot resolve target host %s. Check the target URL.", ps.TargetURL.String())
		diagEvent = "dns_error"
		diagLevel = DiagnosticError
	} else if isTransient {
		// Friendly message for transient errors - these are normal during development
		userMsg = fmt.Sprintf("Connection to %s was interrupted. This often happens when the dev server restarts. Refresh to retry.", ps.TargetURL.Host)
		diagEvent = "transient_error"
		diagLevel = DiagnosticWarning
	} else {
		userMsg = fmt.Sprintf("Proxy Error: %s (target: %s)", errStr, ps.TargetURL.String())
		diagEvent = "unknown_error"
		diagLevel = DiagnosticError
	}

	// Broadcast diagnostic to connected browser clients
	ps.BroadcastProxyDiagnostic(&ProxyDiagnostic{
		Timestamp: timestamp,
		Level:     diagLevel,
		Category:  "proxy",
		Event:     diagEvent,
		Message:   userMsg,
		RequestID: reqID,
		Method:    r.Method,
		URL:       r.URL.String(),
		Target:    ps.TargetURL.String(),
		Data: map[string]any{
			"raw_error":    errStr,
			"is_transient": isTransient,
			"status_code":  http.StatusBadGateway,
		},
	})

	// Record this connection attempt for the loading page history, and — for a
	// genuine upstream failure — against the loading page's outage budget. A
	// client that went away says nothing about the upstream.
	ps.recordConnAttempt(diagEvent, userMsg)
	if !isClientCanceled {
		ps.noteUpstreamFailure(timestamp)
	}

	// For browser requests hitting a server that isn't up yet, serve a friendly
	// loading page with auto-refresh instead of a cryptic error string.
	if (isTransient || diagEvent == "connection_refused") && acceptsHTML(r) {
		ps.serveLoadingPage(w, r, ps.TargetURL.String())
		return
	}

	http.Error(w, userMsg, http.StatusBadGateway)
}

// handleWebSocket handles WebSocket connections for frontend metrics.
func (ps *ProxyServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	debug.Log("proxy", "WebSocket connection attempt from %s to proxy %s", r.RemoteAddr, ps.ID)

	rawConn, err := ps.wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		debug.Log("proxy", "WebSocket upgrade failed for proxy %s: %v", ps.ID, err)
		return
	}
	defer rawConn.Close()

	// A counter makes the id unique per proxy; the clock alone does not (see
	// wsConnSeq). The timestamp keeps ids distinct across proxy restarts.
	connID := fmt.Sprintf("conn-%d-%d", time.Now().UnixNano(), ps.wsConnSeq.Add(1))

	// Cleanup voice session on disconnect. Registered before the handler
	// Wait below so it runs AFTER it: voice_start dials on a handler
	// goroutine, and a session stored after this cleanup had run would never
	// be closed.
	defer func() {
		if session, ok := ps.voiceSessions.LoadAndDelete(connID); ok {
			session.(*VoiceSession).Close()
		}
	}()

	// Track per-message handler goroutines so they cannot outlive the
	// connection: the deferred Wait (registered after the Close defers, so
	// it runs first) awaits them before asyncConn/rawConn are torn down.
	var handlerWG sync.WaitGroup
	handlerSlots := make(chan struct{}, wsMaxHandlers)
	spawnHandler := func(fn func()) {
		handlerSlots <- struct{}{}
		handlerWG.Add(1)
		go func() {
			defer handlerWG.Done()
			defer func() { <-handlerSlots }()
			fn()
		}()
	}

	// Wrap rawConn in async writer so broadcasts never block on slow clients.
	// asyncConn is stored in wsConns for broadcast use only; rawConn is used
	// for all per-connection reads and direct writes from this goroutine.
	asyncConn := newAsyncWSWriter(rawConn, websocket.TextMessage)
	defer asyncConn.Close()
	defer handlerWG.Wait()

	// Store connection for sending messages
	ps.wsConns.Store(connID, asyncConn)
	debug.Log("proxy", "WebSocket client connected: proxy=%s connID=%s remote=%s", ps.ID, connID, r.RemoteAddr)

	// Warn the browser if TLS cert verification was bypassed for this proxy.
	if ps.tlsCertSkipped.Load() {
		if warn, err := json.Marshal(map[string]interface{}{
			"type":     "toast",
			"toast":    "warning",
			"title":    "Self-Signed Certificate",
			"message":  "This proxy is bypassing TLS certificate verification. The backend is using a self-signed or invalid certificate.",
			"duration": 8000,
		}); err == nil {
			// Best-effort browser toast; if the control-lane write fails the
			// conn is already going away, so degrade quietly to the debug log.
			if werr := asyncConn.WriteSync(warn); werr != nil {
				debug.Log("proxy", "failed to send TLS-skip warning toast to browser: %v", werr)
			}
		}
	}

	defer func() {
		// By identity: this close must retire this connection's entry only.
		ps.wsConns.CompareAndDelete(connID, asyncConn)
		debug.Log("proxy", "WebSocket client disconnected: proxy=%s connID=%s", ps.ID, connID)
	}()

	// Per-connection dispatch state: captures map is written by the binary
	// screenshot handler and sketch_capture, read by panel_message.
	connState := &wsConnState{
		ps:           ps,
		asyncConn:    asyncConn,
		connID:       connID,
		captures:     make(map[string]string),
		spawnHandler: spawnHandler,
		// Decided once, from the listener and connection the upgrade arrived
		// on — never from anything a later frame says.
		trusted: ps.controlCallerTrusted(r),
	}
	frames := newWSFrameLimiter(time.Now())

	// Keepalive: a half-open connection (client vanished without a close frame)
	// would otherwise block ReadMessage forever, leaking this goroutine and the
	// wsConns entry. A read deadline plus a pong handler that extends it, driven
	// by a periodic ping, detects the dead peer. WriteControl is safe to call
	// concurrently with the asyncConn drain goroutine (gorilla guarantees this).
	rawConn.SetReadLimit(wsMaxMessageBytes)
	rawConn.SetReadDeadline(time.Now().Add(wsPongWait))
	rawConn.SetPongHandler(func(string) error {
		return rawConn.SetReadDeadline(time.Now().Add(wsPongWait))
	})
	pingStop := make(chan struct{})
	defer close(pingStop)
	go func() {
		ticker := time.NewTicker(wsPingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := rawConn.WriteControl(websocket.PingMessage, nil, time.Now().Add(wsWriteWait)); err != nil {
					return
				}
			case <-pingStop:
				return
			}
		}
	}()

	// Read messages from frontend
	for {
		messageType, rawMessage, err := rawConn.ReadMessage()
		if err != nil {
			break
		}
		// Active client → extend the read deadline so a steady telemetry stream is
		// not disconnected between pings.
		rawConn.SetReadDeadline(time.Now().Add(wsPongWait))

		// Per-connection frame-rate cap. Over the cap the read loop waits,
		// which pushes back on the sender through TCP instead of dropping a
		// frame or multiplying work; the first wait is reported so a throttled
		// page is not a silent failure.
		if wait := frames.reserve(time.Now(), len(rawMessage)); wait > 0 {
			if !connState.frameCapReported {
				connState.frameCapReported = true
				ps.logger.LogDiagnostic(ProxyDiagnostic{
					Timestamp: time.Now(),
					Level:     DiagnosticWarning,
					Category:  "proxy",
					Event:     "ws_frame_rate_capped",
					Message:   fmt.Sprintf("proxy %s is slowing control WebSocket frames from %s: the per-connection frame-rate cap was exceeded", ps.ID, r.RemoteAddr),
					Data:      map[string]any{"proxy_id": ps.ID, "conn_id": connID},
				})
			}
			select {
			case <-time.After(wait):
			case <-r.Context().Done():
			}
		}

		// Binary messages: voice audio or screenshot PNG bytes
		if messageType == websocket.BinaryMessage {
			connState.handleBinaryFrame(rawMessage)
			continue
		}

		// Parse JSON message
		var msg wsMessage
		if err := json.Unmarshal(rawMessage, &msg); err != nil {
			// Best-effort: malformed browser telemetry frame, skip it. The data
			// is untrusted browser input so a parse failure is not actionable
			// beyond a diagnostic trace.
			debug.Log("proxy", "failed to parse browser WS message for proxy %s: %v", ps.ID, err)
			continue
		}

		seq := ps.requestSeq.Add(1)
		connState.id = fmt.Sprintf("metric-%d", seq)
		connState.timestamp = time.Now()

		if handler, ok := wsMessageHandlers[msg.Type]; ok {
			handler(connState, &msg)
		}
	}
}
