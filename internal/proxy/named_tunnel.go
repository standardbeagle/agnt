package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/standardbeagle/agnt/internal/httpcaps"
	"github.com/standardbeagle/agnt/internal/tunnel"
)

// NamedTunnelConfig declares a named Cloudflare tunnel owned by the proxy.
type NamedTunnelConfig struct {
	Tunnel tunnel.NamedCloudflare
	// AccessTeamDomain and AccessAUD identify the Cloudflare Access
	// application whose tokens the tunnel ingress accepts. Both are required
	// unless AllowUnauthenticated is set.
	AccessTeamDomain string
	AccessAUD        string
	// AllowUnauthenticated serves the tunnel with no Access check. It must be
	// set explicitly; an absent Access application never implies it.
	AllowUnauthenticated bool
	// BinaryPath overrides cloudflared on PATH (tests).
	BinaryPath string
}

// namedTunnelSetup is the validated form NewProxyServer derives from
// NamedTunnelConfig. access is nil only when the operator opted out.
type namedTunnelSetup struct {
	cfg    NamedTunnelConfig
	access *CloudflareAccess
}

func newNamedTunnelSetup(cfg *NamedTunnelConfig) (*namedTunnelSetup, error) {
	if cfg == nil {
		return nil, nil
	}
	setup := &namedTunnelSetup{cfg: *cfg}
	if cfg.AllowUnauthenticated {
		return setup, nil
	}
	access, err := NewCloudflareAccess(cfg.AccessTeamDomain, cfg.AccessAUD)
	if err != nil {
		return nil, fmt.Errorf("named tunnel %s: %w", cfg.Tunnel.Hostname, err)
	}
	setup.access = access
	return setup, nil
}

// NamedTunnelConfig returns a copy of the proxy's named-tunnel declaration,
// nil when it declares none. Restart paths carry it to the replacement
// proxy so a restart never silently drops the tunnel or its Access check.
func (ps *ProxyServer) NamedTunnelConfig() *NamedTunnelConfig {
	if ps.namedTunnelSetup == nil {
		return nil
	}
	cfg := ps.namedTunnelSetup.cfg
	return &cfg
}

// namedTunnel is a running named tunnel: cloudflared plus the loopback
// ingress listener it forwards to.
type namedTunnel struct {
	tun     *tunnel.Tunnel
	ingress *http.Server
}

func (n *namedTunnel) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = n.tun.Stop(ctx)
	_ = n.ingress.Close()
}

// startNamedTunnel opens a dedicated loopback ingress listener serving the
// proxy's handler behind the Access guard, then points cloudflared at it.
//
// The ingress is separate from the proxy's own listener on purpose: every
// request that arrives through the tunnel passes the guard by construction,
// while local browsing on the proxy port stays unauthenticated. Checking the
// guard only for requests that "look tunnelled" (by Host or Cf-* headers)
// would make the decision depend on attacker-supplied input.
//
// Failures are reported as proxy diagnostics; the proxy keeps serving
// locally without the tunnel.
func (ps *ProxyServer) startNamedTunnel(ctx context.Context, handler http.Handler) {
	setup := ps.namedTunnelSetup
	name := setup.cfg.Tunnel.Hostname

	if setup.access != nil {
		setup.access.SetOnDeny(func(err error) {
			ps.logNamedTunnel(DiagnosticWarning, "named_tunnel_access_denied",
				fmt.Sprintf("Cloudflare Access refused a request for %s: %v", name, err))
		})
		handler = setup.access.Guard(markTunnelled(handler))
	} else {
		handler = markTunnelled(handler)
		ps.logNamedTunnel(DiagnosticWarning, "named_tunnel_unauthenticated",
			fmt.Sprintf("named tunnel %s is public with no Access check (allow-unauthenticated)", name))
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		ps.logNamedTunnel(DiagnosticError, "named_tunnel_failed",
			fmt.Sprintf("named tunnel %s: opening ingress listener: %v", name, err))
		return
	}
	caps := httpcaps.Streaming()
	ingress := caps.Apply(&http.Server{
		Handler:     handler,
		BaseContext: func(net.Listener) context.Context { return ctx },
	})
	go func() {
		if err := ingress.Serve(caps.LimitListener(ln)); err != nil && !errors.Is(err, http.ErrServerClosed) {
			ps.logNamedTunnel(DiagnosticError, "named_tunnel_failed",
				fmt.Sprintf("named tunnel %s: ingress listener stopped: %v", name, err))
		}
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	named := setup.cfg.Tunnel
	tun := tunnel.New(tunnel.Config{
		Provider:   tunnel.ProviderCloudflare,
		LocalHost:  "127.0.0.1",
		LocalPort:  port,
		BinaryPath: setup.cfg.BinaryPath,
		ID:         ps.ID,
		Path:       ps.Path,
		Named:      &named,
	})
	tun.OnURL(func(url string) {
		ps.SetTunnelURL(url, tun.Done())
		ps.logNamedTunnel(DiagnosticInfo, "named_tunnel_connected",
			fmt.Sprintf("named tunnel connected: %s", url))
	})
	ps.tunnel.Store(&namedTunnel{tun: tun, ingress: ingress})

	if err := tun.Start(ctx); err != nil {
		_ = ingress.Close()
		ps.logNamedTunnel(DiagnosticError, "named_tunnel_failed",
			fmt.Sprintf("named tunnel %s: %v", name, err))
		return
	}
	go func() {
		<-tun.Done()
		_ = ingress.Close()
		if ctx.Err() == nil {
			ps.logNamedTunnel(DiagnosticError, "named_tunnel_failed",
				fmt.Sprintf("named tunnel %s exited: %s", name, tun.Info().Error))
		}
	}()
}

// tunnelledKey marks a request context as having arrived through the named
// tunnel's ingress listener.
type tunnelledKey struct{}

// markTunnelled tags every request served by the tunnel ingress. The tag is
// set by the listener the request arrived on, never derived from headers, so
// a local request cannot claim to be tunnelled.
func markTunnelled(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tunnelledKey{}, true)))
	})
}

// forwardedProto is the scheme the client used to reach the proxy: https
// through the named tunnel (TLS terminates at Cloudflare's edge), http on the
// proxy's own listener.
func forwardedProto(ctx context.Context) string {
	if tunnelled, _ := ctx.Value(tunnelledKey{}).(bool); tunnelled {
		return "https"
	}
	return "http"
}

func (ps *ProxyServer) logNamedTunnel(level ProxyDiagnosticLevel, event, msg string) {
	ps.logger.LogDiagnostic(ProxyDiagnostic{
		Timestamp: time.Now(),
		Level:     level,
		Category:  "tunnel",
		Event:     event,
		Message:   msg,
		Target:    ps.namedTunnelSetup.cfg.Tunnel.Hostname,
		Data:      map[string]any{"proxy_id": ps.ID},
	})
}
