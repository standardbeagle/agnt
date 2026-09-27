package proxy

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/standardbeagle/agnt/internal/devoidc"
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
}

// SetDevOIDC installs the project's dev OIDC issuer on this proxy, or removes
// it with nil. Safe to call while serving.
func (ps *ProxyServer) SetDevOIDC(is *devoidc.Issuer) {
	ps.devOIDC.issuer.Store(is)
	ps.devOIDC.mount.Store(nil)
}

// DevOIDCIssuer returns the installed issuer, nil when the project has none.
func (ps *ProxyServer) DevOIDCIssuer() *devoidc.Issuer { return ps.devOIDC.issuer.Load() }

// DevOIDCLocalOrigin is the proxy's loopback origin, the default issuer
// origin: http://localhost:<port>.
func (ps *ProxyServer) DevOIDCLocalOrigin() string {
	return "http://localhost:" + strconv.Itoa(ps.BoundPort())
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
	origin := ps.DevOIDCLocalOrigin()
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
	return devoidc.Caller{Local: ps.isLocalDevOIDCRequest(r)}
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
	for _, h := range reverseProxyFingerprints {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "ngrok-") {
			return false
		}
	}
	return true
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
