package daemon

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTunnelProviderEnv makes a re-exec of this test binary act as a tunnel
// provider (see TestMain), so TUNNEL START runs its real Start → launch →
// WaitForURL path on every OS without cloudflared or ngrok.
const fakeTunnelProviderEnv = "AGNT_DAEMON_TEST_FAKE_TUNNEL"

const fakeTunnelURL = "https://fake-review.trycloudflare.com"

func runFakeTunnelProvider(mode string) int {
	switch mode {
	case "silent":
		// Runs and logs, but never prints a public URL.
		fmt.Fprintln(os.Stderr, "INF still registering with the edge")
		time.Sleep(60 * time.Second)
		return 0
	case "url":
		fmt.Fprintln(os.Stderr, "INF your quick tunnel: "+fakeTunnelURL)
		time.Sleep(60 * time.Second)
		return 0
	}
	return 2
}

// fakeTunnelProvider returns a binary_path that runs this test binary as a
// provider in the given mode.
func fakeTunnelProvider(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	t.Setenv(fakeTunnelProviderEnv, mode)
	return self
}

// tunnelStartWithin issues TUNNEL START and fails the test, rather than
// hanging it, when no reply arrives within ceiling.
func tunnelStartWithin(t *testing.T, c *daemonclient.Client, cfg protocol.TunnelStartConfig, ceiling time.Duration) (map[string]interface{}, error) {
	t.Helper()
	type reply struct {
		res map[string]interface{}
		err error
	}
	ch := make(chan reply, 1)
	go func() {
		res, err := c.TunnelStart(cfg)
		ch <- reply{res, err}
	}()
	select {
	case r := <-ch:
		return r.res, r.err
	case <-time.After(ceiling):
		t.Fatalf("TUNNEL START gave no reply within %s", ceiling)
		return nil, nil
	}
}

// A provider that never prints a URL used to hold TUNNEL START on the hub's
// own context — until daemon shutdown — and blocked every later command on
// the caller's connection. The wait is bounded now: the reply is a loud
// timeout carrying the provider's state and last output, and the tunnel is
// stopped and retired before the reply, so the same id can be started again.
func TestTunnelStart_URLWaitTimesOutAndRetiresTunnel(t *testing.T) {
	d, c := newRoutableTestClient(t)
	// Long enough for the re-exec'd provider to have printed its line on a
	// loaded host (the error quotes it), far short of the 60s default.
	d.tunnelURLTimeout = 5 * time.Second

	silent := fakeTunnelProvider(t, "silent")
	_, err := tunnelStartWithin(t, c, protocol.TunnelStartConfig{ID: "stuck", Provider: "cloudflare", LocalPort: 3000, BinaryPath: silent}, 20*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[timeout]")
	assert.Contains(t, err.Error(), "printed no public URL within 5s")
	assert.Contains(t, err.Error(), "state starting")
	assert.Contains(t, err.Error(), "still registering with the edge", "the provider's last output is quoted")
	assert.Contains(t, err.Error(), "the tunnel was stopped")

	_, getErr := d.tunnelm.Get("stuck")
	assert.Error(t, getErr, "a timed-out tunnel must not stay registered")
	assert.Equal(t, 0, d.tunnelm.ActiveCount())

	// Retired before the reply: the same id starts again at once.
	t.Setenv(fakeTunnelProviderEnv, "url")
	res, err := tunnelStartWithin(t, c, protocol.TunnelStartConfig{ID: "stuck", Provider: "cloudflare", LocalPort: 3000, BinaryPath: silent}, 20*time.Second)
	require.NoError(t, err)
	assert.Equal(t, fakeTunnelURL, res["public_url"])
	require.NoError(t, c.TunnelStop("stuck"))
}

// A url_timeout_ms the caller sends is the bound, whatever the default.
func TestTunnelStart_HonoursCallerURLTimeout(t *testing.T) {
	d, c := newRoutableTestClient(t)
	d.tunnelURLTimeout = time.Hour // the default would never fire here

	silent := fakeTunnelProvider(t, "silent")
	_, err := tunnelStartWithin(t, c, protocol.TunnelStartConfig{ID: "stuck", Provider: "cloudflare", LocalPort: 3000, BinaryPath: silent, URLTimeoutMs: 250}, 20*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[timeout]")
	assert.Contains(t, err.Error(), "within 250ms")
	assert.Contains(t, err.Error(), "the tunnel was stopped")
	assert.Empty(t, d.tunnelm.List())
}

func TestTunnelStart_RejectsBadURLTimeout(t *testing.T) {
	d, c := newRoutableTestClient(t)
	provider := fakeTunnelProvider(t, "url")
	for _, ms := range []int{-1, int(maxTunnelURLTimeout.Milliseconds()) + 1, math.MaxInt} {
		_, err := tunnelStartWithin(t, c, protocol.TunnelStartConfig{ID: "bad", Provider: "cloudflare", LocalPort: 3000, BinaryPath: provider, URLTimeoutMs: ms}, 20*time.Second)
		require.Error(t, err, "url_timeout_ms %d", ms)
		assert.Contains(t, err.Error(), "[invalid_args]")
		assert.Contains(t, err.Error(), "url_timeout_ms")
	}
	assert.Empty(t, d.tunnelm.List(), "no tunnel starts on a rejected timeout")
}

// A proxy_id that resolves to no proxy used to be skipped after the tunnel was
// up, and the reply still said running. It is an error now, raised before any
// provider starts.
func TestTunnelStart_UnknownProxyIDFailsBeforeStarting(t *testing.T) {
	d, c := newRoutableTestClient(t)
	provider := fakeTunnelProvider(t, "url")

	_, err := tunnelStartWithin(t, c, protocol.TunnelStartConfig{ID: "orphan", Provider: "cloudflare", LocalPort: 3000, BinaryPath: provider, ProxyID: "no-such-proxy"}, 20*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[not_found]")
	assert.Contains(t, err.Error(), `proxy_id "no-such-proxy"`)
	assert.Contains(t, err.Error(), "no tunnel was started")
	assert.Empty(t, d.tunnelm.List(), "no tunnel may start for an unresolvable proxy")
}

// The resolved proxy still receives the URL once the tunnel reports it.
func TestTunnelStart_ResolvedProxyGetsURL(t *testing.T) {
	d, c := newRoutableTestClient(t)
	provider := fakeTunnelProvider(t, "url")
	p, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{ID: "web", TargetURL: "http://127.0.0.1:5173"})
	require.NoError(t, err)

	res, err := tunnelStartWithin(t, c, protocol.TunnelStartConfig{ID: "web-tunnel", Provider: "cloudflare", LocalPort: p.BoundPort(), BinaryPath: provider, ProxyID: "web"}, 20*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "running", res["status"])
	assert.Equal(t, fakeTunnelURL, p.GetPublicURL())
	require.NoError(t, c.TunnelStop("web-tunnel"))
}
