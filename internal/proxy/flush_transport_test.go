package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roundTripOK sends one GET through the proxy's own transport chain.
func roundTripOK(t *testing.T, ps *ProxyServer, target string) error {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	require.NoError(t, err)
	resp, err := ps.proxy.Transport.RoundTrip(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// A flush must not strip the TLS fallback. The old FlushConnections replaced
// the whole chain under the chaos wrapper with a bare clone of the verifying
// transport, so the first transient upstream error left a self-signed backend
// failing certificate verification until the proxy was recreated.
func TestFlushConnections_KeepsTLSFallbackForSelfSignedBackend(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	ps, err := NewProxyServer(ProxyConfig{ID: "flush-tls", TargetURL: backend.URL, ListenPort: 0, MaxLogSize: 100})
	require.NoError(t, err)
	t.Cleanup(func() { ps.pageTracker.Stop(); ps.StopChaos() })

	chaos, ok := ps.proxy.Transport.(*ChaosTransport)
	require.True(t, ok)
	before, ok := chaos.underlying.(*TLSFallbackTransport)
	require.True(t, ok, "HTTPS target should wire a TLSFallbackTransport")

	// The backend's certificate is self-signed: this only succeeds through
	// the fallback, which is allowed here because the target is loopback.
	require.NoError(t, roundTripOK(t, ps, backend.URL))
	require.True(t, before.skipped.Load(), "fallback should have activated")
	securePool := before.secure.cur.Load()

	ps.FlushConnections()

	after, ok := chaos.underlying.(*TLSFallbackTransport)
	require.True(t, ok, "flush replaced the TLS fallback wrapper with %T", chaos.underlying)
	assert.Same(t, before, after, "the wrapper itself must survive a flush")
	assert.True(t, after.skipped.Load(), "skipped state must survive a flush")
	assert.NotSame(t, securePool, after.secure.cur.Load(), "the connection pool should have been replaced")

	require.NoError(t, roundTripOK(t, ps, backend.URL),
		"a self-signed backend must still be reachable after a flush")
}

// Flushes arrive concurrently with requests and with each other (a backend
// restart fails many requests at once, and each calls FlushConnections). Run
// under -race this pins that the swap is synchronised.
func TestFlushConnections_ConcurrentWithRoundTrips(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()

	ps, err := NewProxyServer(ProxyConfig{ID: "flush-race", TargetURL: backend.URL, ListenPort: 0, MaxLogSize: 100})
	require.NoError(t, err)
	t.Cleanup(func() { ps.pageTracker.Stop(); ps.StopChaos() })
	require.NotNil(t, ps.baseTransport, "an HTTP target flushes through baseTransport")

	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				ps.FlushConnections()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := roundTripOK(t, ps, backend.URL); err != nil {
					failures.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	assert.Zero(t, failures.Load(), "requests must keep succeeding across flushes")
}

// A target the bypass does not apply to keeps verification: the certificate
// error comes back, the refusal is reported once, and — the control itself —
// the non-verifying transport is never used, so the request never reaches the
// backend.
func TestTLSFallback_RefusedTargetIsNeverSentUnverified(t *testing.T) {
	var hits atomic.Int32
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer backend.Close()

	var skipped, refused atomic.Int32
	fb := NewTLSFallbackTransport(&http.Transport{}, func(error) { skipped.Add(1) })
	fb.allow = func(string) bool { return false }
	fb.onRefused = func(error) { refused.Add(1) }

	for i := 0; i < 3; i++ {
		req, err := http.NewRequest(http.MethodGet, backend.URL, nil)
		require.NoError(t, err)
		_, err = fb.RoundTrip(req)
		require.Error(t, err)
		assert.True(t, isCertError(err), "the certificate error itself must be returned, got %v", err)
	}

	assert.Zero(t, hits.Load(), "a refused target must never be reached without verification")
	assert.False(t, fb.skipped.Load(), "the bypass must not activate for a refused target")
	assert.Zero(t, skipped.Load())
	assert.EqualValues(t, 1, refused.Load(), "the refusal is reported once, not per request")
}

func TestIsLocalFallbackTarget(t *testing.T) {
	lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "dev.internal":
			return []net.IPAddr{{IP: net.ParseIP("10.1.2.3")}}, nil
		case "api.example.com":
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		case "mixed.example.com":
			return []net.IPAddr{{IP: net.ParseIP("10.1.2.3")}, {IP: net.ParseIP("93.184.216.34")}}, nil
		}
		return nil, errors.New("no such host")
	}
	cases := map[string]bool{
		"localhost":         true,
		"app.localhost":     true,
		"127.0.0.1":         true,
		"[::1]":             true,
		"192.168.1.10":      true,
		"10.0.0.5":          true,
		"100.101.102.103":   true, // tailnet
		"dev.internal":      true, // resolves private
		"8.8.8.8":           false,
		"api.example.com":   false, // resolves public
		"mixed.example.com": false, // every address must be local
		"unresolvable.test": false, // a failed lookup is not local
		"":                  false,
	}
	for host, want := range cases {
		assert.Equalf(t, want, isLocalFallbackTargetWith(host, lookup), "host %q", host)
	}
}
