package proxy

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/devoidc"
	"github.com/standardbeagle/agnt/internal/platform"
)

// devOIDCMount is the issuer handler built for one proxy origin. It is
// rebuilt when the issuer is replaced or the proxy's port changes.
type devOIDCMount struct {
	issuer  *devoidc.Issuer
	origin  string
	handler http.Handler
}

// devOIDCState holds the project's dev issuer for this proxy (nil = none).
type devOIDCState struct {
	issuer atomic.Pointer[devoidc.Issuer]
	mount  atomic.Pointer[devOIDCMount]

	// whois resolves a tailnet peer IP to its owner's login. The zero value
	// is the production path (platform.TailscaleWhois); tests inject a stub.
	whois      func(ctx context.Context, ip string) string
	whoisCache sync.Map // peer IP -> whoisEntry
}

type whoisEntry struct {
	login   string
	expires time.Time
}

// whoisTTL bounds how long a peer IP keeps its resolved owner. Tailscale
// addresses are stable per node, so a minute only delays noticing that a
// node was re-assigned, while sparing a `tailscale whois` per request.
const whoisTTL = time.Minute

// SetDevOIDC installs the project's dev OIDC issuer on this proxy, or removes
// it with nil. Safe to call while serving.
func (ps *ProxyServer) SetDevOIDC(is *devoidc.Issuer) {
	ps.devOIDC.issuer.Store(is)
	ps.devOIDC.mount.Store(nil)
}

// DevOIDCIssuer returns the installed issuer, nil when the project has none.
func (ps *ProxyServer) DevOIDCIssuer() *devoidc.Issuer { return ps.devOIDC.issuer.Load() }

// ListenerOrigin is the origin the proxy's own listener answers on:
// http://localhost:<port> on loopback; on a tailnet bind the MagicDNS name,
// https://<name>:<port> when it serves the tailnet certificate and
// http://<name>:<port> when it does not. It is also the default dev-oidc
// issuer origin.
func (ps *ProxyServer) ListenerOrigin() string {
	port := strconv.Itoa(ps.BoundPort())
	if ps.tailnetTLS != nil {
		return "https://" + net.JoinHostPort(ps.tailnetTLS.domain, port)
	}
	if ps.boundToTailnet() {
		host, _, _ := net.SplitHostPort(ps.liveAddr())
		for id := range ps.tailnetIdentitySet(context.Background()).hosts {
			if net.ParseIP(id) == nil {
				host = id // the MagicDNS name, stable across address changes
				break
			}
		}
		return "http://" + net.JoinHostPort(host, port)
	}
	return "http://localhost:" + port
}

// boundToTailnet reports whether the live listener is on a tailnet address.
// It checks the bound address itself, never the configured token.
func (ps *ProxyServer) boundToTailnet() bool {
	host, _, err := net.SplitHostPort(ps.liveAddr())
	return err == nil && platform.IsTailnetAddress(host)
}

// serveDevOIDC routes /__agnt/oidc/ to the issuer. A project without a
// dev-oidc block keeps the path proxied to the app, so the prefix is only
// reserved when the feature is on.
func (ps *ProxyServer) serveDevOIDC(w http.ResponseWriter, r *http.Request) {
	is := ps.devOIDC.issuer.Load()
	if is == nil {
		ps.handleProxy(w, r)
		return
	}
	origin := ps.ListenerOrigin()
	m := ps.devOIDC.mount.Load()
	if m == nil || m.issuer != is || m.origin != origin {
		m = &devOIDCMount{issuer: is, origin: origin, handler: is.Handler(devoidc.Mount{
			LocalOrigin: origin,
			Caller:      ps.devOIDCCaller,
			SameOrigin:  ps.checkWSOrigin,
		})}
		ps.devOIDC.mount.Store(m)
	}
	m.handler.ServeHTTP(w, r)
}

// devOIDCCaller classifies a request for the issuer by the listener it
// arrived on (see the dev OIDC spec §5).
func (ps *ProxyServer) devOIDCCaller(r *http.Request) devoidc.Caller {
	if id, ok := AccessIdentityFrom(r.Context()); ok {
		return devoidc.Caller{AccessEmail: id.Email}
	}
	if tunnelled, _ := r.Context().Value(tunnelledKey{}).(bool); tunnelled {
		// Tunnel ingress without Access (allow-unauthenticated): the issuer
		// never serves an unauthenticated public caller.
		return devoidc.Caller{}
	}
	if ps.boundToTailnet() {
		return devoidc.Caller{TailnetLogin: ps.tailnetDevOIDCLogin(r)}
	}
	return devoidc.Caller{Local: ps.isLocalDevOIDCRequest(r)}
}

// tailnetDevOIDCLogin identifies the person behind a request on a proxy bound
// to its tailnet address, or returns "". All must hold: the TCP peer is a
// tailnet address (the connection, not a header, says so); the Host is one of
// this node's own tailnet names, which rules out DNS rebinding and a relayed
// public hostname; no quick tunnel or static public-url is bound; no
// reverse-proxy header is present; and tailscaled names a person, not a
// tagged device, as the peer's owner.
func (ps *ProxyServer) tailnetDevOIDCLogin(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !platform.IsTailnetPeerAddress(peer) {
		return ""
	}
	if !ps.isTailnetAuthority(r.Context(), r.Host) {
		return ""
	}
	if pub := ps.activePublicURL(); pub != nil && pub.url != "" && pub.url != ps.namedTunnelURL() {
		return ""
	}
	if hasReverseProxyFingerprint(r) {
		return ""
	}
	return ps.tailnetWhois(r.Context(), peer)
}

func (ps *ProxyServer) tailnetWhois(ctx context.Context, ip string) string {
	if v, ok := ps.devOIDC.whoisCache.Load(ip); ok {
		if e := v.(whoisEntry); time.Now().Before(e.expires) {
			return e.login
		}
	}
	whois := ps.devOIDC.whois
	if whois == nil {
		whois = platform.TailscaleWhois
	}
	login := whois(ctx, ip)
	ps.devOIDC.whoisCache.Store(ip, whoisEntry{login: login, expires: time.Now().Add(whoisTTL)})
	return login
}

// reverseProxyFingerprints are headers a tunnel or reverse proxy adds. A
// request on the proxy's own listener carrying one was relayed from
// somewhere else, e.g. a hand-run `cloudflared --url` agnt does not know
// about. Using client-controlled headers is safe here: they can only take
// access away.
var reverseProxyFingerprints = []string{
	"Cf-Ray", "Cf-Connecting-Ip", "Tailscale-User-Login", "X-Forwarded-For", "Forwarded",
}

// isLocalDevOIDCRequest reports whether a request on the proxy's own
// listener can only have come from this machine.
func (ps *ProxyServer) isLocalDevOIDCRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(ps.liveAddr())
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return false
	}
	if !isLoopbackAuthority(r.Host) {
		return false
	}
	if pub := ps.activePublicURL(); pub != nil && pub.url != "" && pub.url != ps.namedTunnelURL() {
		return false
	}
	return !hasReverseProxyFingerprint(r)
}

// hasReverseProxyFingerprint reports whether a tunnel or reverse proxy relayed
// the request (see reverseProxyFingerprints).
func hasReverseProxyFingerprint(r *http.Request) bool {
	for _, h := range reverseProxyFingerprints {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "ngrok-") {
			return true
		}
	}
	return false
}

// namedTunnelURL is the public URL of the proxy's own named tunnel, or "".
// That tunnel targets a separate guarded ingress, so its public-URL binding
// does not make the proxy's own listener reachable from outside.
func (ps *ProxyServer) namedTunnelURL() string {
	if ps.namedTunnelSetup == nil {
		return ""
	}
	return "https://" + ps.namedTunnelSetup.cfg.Tunnel.Hostname
}
