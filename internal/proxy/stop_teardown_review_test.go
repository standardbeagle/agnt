package proxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/publish"
	"github.com/standardbeagle/agnt/internal/tunnel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func countDiagnostics(ps *ProxyServer, event string) int {
	n := 0
	for _, e := range diagnosticEvents(ps) {
		if e == event {
			n++
		}
	}
	return n
}

// Stop must tear a proxy down whatever `running` says. A proxy whose Serve
// returned unexpectedly has running=false but still owns its listener context
// and its hijacked WebSocket connections; the old Stop returned "not running"
// and left them all alive behind a proxy the manager had already deregistered.
func TestStop_TearsDownProxyThatIsNotRunning(t *testing.T) {
	backend := newWrapBackend(t)
	ps := startWrapProxy(t, backend)
	addr := ps.ListenAddr

	conn := &stubWSConn{}
	ps.wsConns.Store("conn-crashed", conn)

	// What runServer does when Serve returns an unexpected error.
	ps.running.Store(false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := ps.Stop(ctx)
	require.Error(t, err, "Stop still reports that the proxy was not running")
	assert.Contains(t, err.Error(), "not running")

	conn.mu.Lock()
	closed := conn.closed
	conn.mu.Unlock()
	assert.True(t, closed, "a hijacked WebSocket connection must be closed by Stop")
	_, still := ps.wsConns.Load("conn-crashed")
	assert.False(t, still, "the connection must be retired from the registry")

	// The listener is gone: its context was cancelled and the server shut down.
	require.Eventually(t, func() bool {
		c, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if dialErr == nil {
			_ = c.Close()
			return false
		}
		return true
	}, 5*time.Second, 20*time.Millisecond, "the proxy listener must be closed after Stop")

	// A second Stop is safe and does not announce the stop twice.
	require.Error(t, ps.Stop(ctx))
	assert.Equal(t, 1, countDiagnostics(ps, "proxy_stopped"))
}

// The health loop must outlive a tick that lands while the proxy is not
// serving (the auto-restart backoff). It used to return on that tick, ending
// health checks for good. Gated on the probe having happened, not on timing.
func TestRunHealthCheck_SurvivesTickWhileNotRunning(t *testing.T) {
	backend := newWrapBackend(t)
	ps, err := NewProxyServer(ProxyConfig{ID: "health-backoff", TargetURL: backend.URL, ListenPort: 0, MaxLogSize: 100})
	require.NoError(t, err)
	t.Cleanup(func() { ps.pageTracker.Stop(); ps.StopChaos() })
	ps.healthCheckInterval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { ps.runHealthCheck(ctx); close(done) }()

	// Not running: ticks pass, no probe is made, and the loop stays alive.
	time.Sleep(60 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("health loop exited on a tick that landed while the proxy was not running")
	default:
	}
	_, probed := ps.lastHealthCheck.Load().(time.Time)
	assert.False(t, probed, "no probe should run while the proxy is not serving")

	// Serving again: the same loop resumes probing.
	ps.running.Store(true)
	require.Eventually(t, func() bool {
		_, ok := ps.lastHealthCheck.Load().(time.Time)
		return ok
	}, 5*time.Second, 5*time.Millisecond, "health checks must resume once the proxy is running again")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("health loop did not exit with its context")
	}
}

// StopByProjectPath compares normalised paths, as the tunnel manager does.
func TestStopByProjectPath_NormalizesTrailingSlash(t *testing.T) {
	pm := NewProxyManager()
	ps, err := NewProxyServer(ProxyConfig{ID: "proj:dev", TargetURL: "http://localhost:3000", ListenPort: 0, MaxLogSize: 100, Path: "/work/app/"})
	require.NoError(t, err)
	t.Cleanup(ps.StopChaos)
	pm.proxies.Store(ps.ID, ps)
	pm.activeCount.Add(1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The proxy was never started, so its Stop reports "not running"; what is
	// under test is that it was selected and retired at all.
	_, _ = pm.StopByProjectPath(ctx, "/work/app")

	_, err = pm.Get(ps.ID)
	assert.ErrorIs(t, err, ErrProxyNotFound, "a trailing-slash difference must not leave the project's proxy registered")
	assert.Zero(t, pm.ActiveCount())
}

// On the named-tunnel ingress the request-rate cap sits outside the Access
// guard, so a request Access refuses is still counted. With the cap inside
// the guard, refused requests never reached it: every one of them got a 403.
func TestNamedTunnelIngress_RateCapCountsAccessRefusals(t *testing.T) {
	access, err := NewCloudflareAccess("team.cloudflareaccess.com", "aud-tag")
	require.NoError(t, err)

	ps, err := NewProxyServer(ProxyConfig{ID: "ingress-cap", TargetURL: "http://localhost:3000", ListenPort: 0, MaxLogSize: 100})
	require.NoError(t, err)
	t.Cleanup(func() { ps.pageTracker.Stop(); ps.StopChaos() })
	ps.namedTunnelSetup = &namedTunnelSetup{
		cfg:    NamedTunnelConfig{Tunnel: tunnel.NamedCloudflare{Hostname: "dev.example.com"}},
		access: access,
	}
	// One token, and a frozen clock so it never refills.
	now := time.Unix(1_700_000_000, 0)
	ps.requestLimiter = publish.NewRateLimiter(1, 1, func() time.Time { return now })

	reached := 0
	handler := ps.namedTunnelIngressHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ }))

	serve := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "http://dev.example.com/", nil) // no Access token
		handler.ServeHTTP(w, r)
		return w
	}

	first := serve()
	assert.Equal(t, http.StatusForbidden, first.Code, "the first request is inside the cap and refused by Access")
	second := serve()
	assert.Equal(t, http.StatusTooManyRequests, second.Code, "the Access refusal must have spent the rate budget")
	assert.Equal(t, "1", second.Header().Get("Retry-After"))
	assert.Equal(t, 1, countDiagnostics(ps, "request_rate_capped"), "the 429 must come from the rate cap")
	assert.Zero(t, reached, "neither request may reach the proxy")
}

// The loading page's give-up budget runs from the first failure of the
// current outage. Measured from proxy start, any outage after the proxy's
// first minute went straight to the non-refreshing error page.
func TestLoadingPage_BudgetRunsFromOutageNotProxyStart(t *testing.T) {
	ps := &ProxyServer{startTime: time.Now().Add(-2 * time.Hour)}
	now := time.Now()

	serve := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		ps.serveLoadingPage(w, nil, "http://localhost:3000")
		return w
	}

	// A fresh outage on a long-running proxy: the auto-refreshing page.
	ps.noteUpstreamFailure(now.Add(-5 * time.Second))
	w := serve()
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `meta http-equiv="refresh" content="3"`)

	// Failures keep arriving (the page refreshes every 3s) and the outage
	// outlasts the budget: the error page, with an age that is true.
	ps.outageStart.Store(now.Add(-90 * time.Second).UnixNano())
	ps.noteUpstreamFailure(now.Add(-time.Second))
	w = serve()
	require.Equal(t, http.StatusBadGateway, w.Code)
	// "1m3": the age is about ninety seconds, never the two hours of uptime.
	assert.Contains(t, w.Body.String(), "unreachable for 1m3")
	assert.NotContains(t, w.Body.String(), "since startup")

	// The upstream answers: the outage is over, and the next one starts fresh.
	ps.noteUpstreamSuccess()
	assert.Zero(t, ps.outageStart.Load())
	ps.noteUpstreamFailure(now)
	require.Equal(t, http.StatusServiceUnavailable, serve().Code)

	// An outage nobody has been waiting on (last failure long ago) reopens
	// with a fresh budget rather than giving up on the first request.
	ps.outageStart.Store(now.Add(-time.Hour).UnixNano())
	ps.lastUpstreamFailure.Store(now.Add(-time.Hour).UnixNano())
	ps.noteUpstreamFailure(now)
	require.Equal(t, http.StatusServiceUnavailable, serve().Code)
}

// rewriteFor builds the upstream request modifyResponse sees: what the Rewrite
// hook recorded about the inbound one.
func rewriteFor(forwardedHost, forwardedProto string, tunnelled bool) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://localhost:3000/", nil)
	if forwardedHost != "" {
		req.Header.Set("X-Forwarded-Host", forwardedHost)
	}
	if forwardedProto != "" {
		req.Header.Set("X-Forwarded-Proto", forwardedProto)
	}
	if tunnelled {
		req = req.WithContext(context.WithValue(req.Context(), tunnelledKey{}, true))
	}
	return req
}
