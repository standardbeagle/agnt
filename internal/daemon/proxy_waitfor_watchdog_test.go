//go:build unix

package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// TestProxyWaitFor_WatchdogWarnsOnStalledGate verifies the Silent Failure
// Prohibition guard: a proxy gated on a dependency that never signals ready
// must surface a warning after the grace window instead of hanging mute behind
// an endless 503 agnt_proxy_not_ready.
func TestProxyWaitFor_WatchdogWarnsOnStalledGate(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	d := newTestDaemon(t)
	d.readinessWatchdogGrace = 150 * time.Millisecond
	d.startupErrorStore = NewStartupLogStore(50)

	created, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         "stalled-proxy",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 50,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "stalled-proxy") })

	projectPath := "/tmp/project"
	// never-ready is never signaled, so the gate stays closed forever.
	proxyCfg := &config.ProxyConfig{Script: "dev-frontend", WaitFor: []string{"never-ready"}}
	d.registerProxyDependencies(created, "stalled-proxy", proxyCfg, projectPath)

	require.False(t, created.IsReadyForForwarding(), "gate should be closed")

	require.Eventually(t, func() bool {
		for _, e := range d.startupErrorStore.Recent(time.Hour, 100) {
			if e.EventType == "proxy_readiness_stalled" {
				return true
			}
		}
		return false
	}, 2*time.Second, 25*time.Millisecond,
		"watchdog must emit a proxy_readiness_stalled warning for a gate that never opens")

	// The warning must name the pending dependency and be a warning level.
	var found *StartupLogEntry
	for _, e := range d.startupErrorStore.Recent(time.Hour, 100) {
		if e.EventType == "proxy_readiness_stalled" {
			found = e
			break
		}
	}
	require.NotNil(t, found)
	assert.Equal(t, "warning", found.Level)
	assert.Contains(t, found.Message, makeProcessID(projectPath, "never-ready"))
}

// TestProxyWaitFor_WatchdogSilentWhenGateOpens verifies the watchdog does NOT
// warn when the gate opens within the grace window — the healthy path must
// stay quiet.
func TestProxyWaitFor_WatchdogSilentWhenGateOpens(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	d := newTestDaemon(t)
	d.readinessWatchdogGrace = 300 * time.Millisecond
	d.startupErrorStore = NewStartupLogStore(50)

	created, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         "healthy-proxy",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 50,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "healthy-proxy") })

	projectPath := "/tmp/project"
	proxyCfg := &config.ProxyConfig{Script: "dev-frontend", WaitFor: []string{"backend"}}
	d.registerProxyDependencies(created, "healthy-proxy", proxyCfg, projectPath)

	// Signal ready well before the grace window elapses.
	d.readySignaler.SignalReady(makeProcessID(projectPath, "backend"))
	require.Eventually(t, func() bool {
		return created.IsReadyForForwarding()
	}, 1*time.Second, 10*time.Millisecond, "gate should open after signal")

	// Wait past the grace window; no warning must appear.
	time.Sleep(400 * time.Millisecond)
	for _, e := range d.startupErrorStore.Recent(time.Hour, 100) {
		assert.NotEqual(t, "proxy_readiness_stalled", e.EventType,
			"watchdog must stay silent when the gate opens in time")
	}
}
