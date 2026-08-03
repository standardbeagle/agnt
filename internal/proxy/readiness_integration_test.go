package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProxyServer_ReadinessGate_BlocksWhileWaiting verifies a proxy
// with pending dependencies returns a 503 with the sentinel body
// instead of forwarding to the backend.
func TestProxyServer_ReadinessGate_BlocksWhileWaiting(t *testing.T) {
	backendHits := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendHits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("from backend"))
	}))
	t.Cleanup(func() { backend.Close() })

	ps, err := NewProxyServer(ProxyConfig{
		ID:         "gated",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 100,
	})
	require.NoError(t, err)

	// Close the gate BEFORE starting so the first request hits it.
	ps.SetDependencies([]string{"dev-backend"})
	assert.False(t, ps.IsReadyForForwarding())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	require.NoError(t, ps.Start(ctx))
	t.Cleanup(func() { ps.Stop(ctx) })

	<-ps.Ready()

	resp, err := http.Get("http://" + ps.ListenAddr + "/")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "1", resp.Header.Get("Retry-After"))
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.Equal(t, "waiting-for-dependencies", resp.Header.Get("X-Agnt-Readiness"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var parsed ReadinessBody
	require.NoError(t, json.Unmarshal(body, &parsed))
	assert.Equal(t, ReadinessSentinel, parsed.Error)
	assert.Equal(t, []string{"dev-backend"}, parsed.Pending)

	assert.Equal(t, 0, backendHits, "backend should not receive any requests while gated")
}

// TestProxyServer_ReadinessGate_OpensOnMarkDependency verifies that
// marking the last pending dependency ready atomically allows
// subsequent requests to forward normally.
func TestProxyServer_ReadinessGate_OpensOnMarkDependency(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("from backend"))
	}))
	t.Cleanup(func() { backend.Close() })

	ps, err := NewProxyServer(ProxyConfig{
		ID:         "gated-then-open",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 100,
	})
	require.NoError(t, err)

	ps.SetDependencies([]string{"dev-backend", "dev-lib"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	require.NoError(t, ps.Start(ctx))
	t.Cleanup(func() { ps.Stop(ctx) })

	<-ps.Ready()

	// First request: blocked.
	resp1, err := http.Get("http://" + ps.ListenAddr + "/")
	require.NoError(t, err)
	resp1.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp1.StatusCode)

	// Mark one dep ready — still closed.
	openedNow := ps.MarkDependencyReady("dev-backend")
	assert.False(t, openedNow)
	resp2, err := http.Get("http://" + ps.ListenAddr + "/")
	require.NoError(t, err)
	resp2.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp2.StatusCode)

	// Mark the last dep — gate opens.
	openedNow = ps.MarkDependencyReady("dev-lib")
	assert.True(t, openedNow)
	assert.True(t, ps.IsReadyForForwarding())

	resp3, err := http.Get("http://" + ps.ListenAddr + "/")
	require.NoError(t, err)
	body, err := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp3.StatusCode)
	assert.Equal(t, "from backend", string(body))
}

// TestProxyServer_ReadinessGate_NoDepsRegression verifies that a
// proxy with no `wait-for` configured behaves identically to today:
// requests forward immediately, no 503s.
func TestProxyServer_ReadinessGate_NoDepsRegression(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("no wait-for"))
	}))
	t.Cleanup(func() { backend.Close() })

	ps, err := NewProxyServer(ProxyConfig{
		ID:         "unguarded",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 100,
	})
	require.NoError(t, err)

	// Gate is open by default.
	assert.True(t, ps.IsReadyForForwarding())
	assert.Empty(t, ps.PendingDependencies())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	require.NoError(t, ps.Start(ctx))
	t.Cleanup(func() { ps.Stop(ctx) })

	<-ps.Ready()

	resp, err := http.Get("http://" + ps.ListenAddr + "/")
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "no wait-for", string(body))
}

// TestProxyServer_ReadinessGate_StatsExposeWaitState verifies that
// ProxyStats reports the gating state so `proxy status` / `proxy list`
// can distinguish "waiting for dependencies" from plain "running".
func TestProxyServer_ReadinessGate_StatsExposeWaitState(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(func() { backend.Close() })

	ps, err := NewProxyServer(ProxyConfig{
		ID:         "stats-test",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 100,
	})
	require.NoError(t, err)

	ps.SetDependencies([]string{"dev-backend"})

	stats := ps.Stats()
	assert.False(t, stats.ReadyForForwarding)
	assert.Equal(t, []string{"dev-backend"}, stats.WaitingFor)

	ps.MarkDependencyReady("dev-backend")
	stats = ps.Stats()
	assert.True(t, stats.ReadyForForwarding)
	assert.Empty(t, stats.WaitingFor)
}

// TestProxyServer_ReadinessGate_LogsGatedRequests verifies the
// gate-generated 503 is recorded in the traffic log. `proxylog query`
// should still surface these on demand even though the incident adapter
// filters them.
func TestProxyServer_ReadinessGate_LogsGatedRequests(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be reached while gated")
	}))
	t.Cleanup(func() { backend.Close() })

	ps, err := NewProxyServer(ProxyConfig{
		ID:         "log-gated",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 100,
	})
	require.NoError(t, err)

	ps.SetDependencies([]string{"dev-backend"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, ps.Start(ctx))
	t.Cleanup(func() { ps.Stop(ctx) })
	<-ps.Ready()

	resp, _ := http.Get(fmt.Sprintf("http://%s/api/data", ps.ListenAddr))
	if resp != nil {
		resp.Body.Close()
	}

	entries := ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypeHTTP}, Limit: 10})
	require.Len(t, entries, 1)
	require.NotNil(t, entries[0].HTTP)
	assert.Equal(t, http.StatusServiceUnavailable, entries[0].HTTP.StatusCode)
	assert.Equal(t, ReadinessSentinel, entries[0].HTTP.Error)
}
