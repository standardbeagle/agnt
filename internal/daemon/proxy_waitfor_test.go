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

// TestRegisterProxyDependencies_NoWaitForIsNoOp verifies that a
// proxy without a `wait-for` declaration is left in the default
// open state — no waiter goroutines, gate open, no extra state.
func TestRegisterProxyDependencies_NoWaitForIsNoOp(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	d := newTestDaemon(t)
	server := newTestProxyServer(t, backend.URL)

	proxyCfg := &config.ProxyConfig{Script: "dev"}
	d.registerProxyDependencies(server, "test-proxy", proxyCfg, "/tmp/project")

	assert.True(t, server.IsReadyForForwarding())
	assert.Empty(t, server.PendingDependencies())
}

// TestRegisterProxyDependencies_GatesUntilAllDepsReady verifies
// that registering a proxy with two deps closes the gate and that
// firing both ready signals opens it exactly once.
func TestRegisterProxyDependencies_GatesUntilAllDepsReady(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	d := newTestDaemon(t)
	// The detached server is discarded below in favor of the manager-tracked
	// instance; the helper call stays for its t.Cleanup (stops the chaos
	// engine and page tracker it creates).
	_ = newTestProxyServer(t, backend.URL)

	// Register proxy in manager so the waiter's Get() lookup succeeds.
	d.proxym = proxy.NewProxyManager()
	// Intentionally do NOT call Create — that would spin up a real
	// listener. Instead, seed the manager by calling Get after a
	// manual insert via Stop/Create pattern. For this test the
	// waiter uses a monkey-patched lookup instead.
	//
	// The real daemon path uses d.proxym.Get(proxyID). We can reach
	// the same path by creating the proxy through the manager's
	// Create method pointing at the test backend.
	created, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         "test-proxy",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 50,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = d.proxym.Stop(context.Background(), "test-proxy")
	})
	// Use the manager-tracked instance so the assertions and the waiter
	// target the same server.
	server := created

	projectPath := "/tmp/project"
	proxyCfg := &config.ProxyConfig{
		Script:  "dev-frontend",
		WaitFor: []string{"dev-backend", "dev-lib"},
	}

	d.registerProxyDependencies(server, "test-proxy", proxyCfg, projectPath)

	// Gate should be closed with both deps pending (translated to
	// process IDs rooted at projectPath).
	assert.False(t, server.IsReadyForForwarding())
	assert.Len(t, server.PendingDependencies(), 2)

	// Fire ready for dev-backend → gate still closed.
	d.readySignaler.SignalReady(makeProcessID(projectPath, "dev-backend"))
	require.Eventually(t, func() bool {
		return len(server.PendingDependencies()) == 1
	}, 2*time.Second, 10*time.Millisecond, "expected one dep remaining after dev-backend ready")
	assert.False(t, server.IsReadyForForwarding())

	// Fire ready for dev-lib → gate opens.
	d.readySignaler.SignalReady(makeProcessID(projectPath, "dev-lib"))
	require.Eventually(t, func() bool {
		return server.IsReadyForForwarding()
	}, 2*time.Second, 10*time.Millisecond, "expected gate open after all deps ready")
	assert.Empty(t, server.PendingDependencies())
}

// TestRegisterProxyDependencies_SurvivesProxyTeardown verifies that
// if the proxy is stopped while a waiter is still blocked, the
// waiter exits cleanly when its dep eventually fires (no panic, no
// stuck goroutine).
func TestRegisterProxyDependencies_SurvivesProxyTeardown(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	d := newTestDaemon(t)

	created, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         "teardown-proxy",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 50,
	})
	require.NoError(t, err)

	projectPath := "/tmp/project"
	proxyCfg := &config.ProxyConfig{
		Script:  "dev-frontend",
		WaitFor: []string{"dev-backend"},
	}
	d.registerProxyDependencies(created, "teardown-proxy", proxyCfg, projectPath)

	// Stop the proxy before the dep fires. The waiter should not
	// panic when Get() returns ErrProxyNotFound.
	require.NoError(t, d.proxym.Stop(context.Background(), "teardown-proxy"))

	// Fire ready signal — waiter should exit without error.
	d.readySignaler.SignalReady(makeProcessID(projectPath, "dev-backend"))

	// Give the waiter a moment to observe and exit.
	time.Sleep(50 * time.Millisecond)
	// If we got here without panic, the test passes.
}

// newTestDaemon creates a minimal Daemon with only the fields the
// wait-for code path needs: proxym, readySignaler, eventHub, and a
// live ctx. It avoids the full New() path so tests don't need a
// socket, Hub, or filesystem.
func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := &Daemon{
		proxym:        proxy.NewProxyManager(),
		readySignaler: NewReadySignaler(),
		eventHub:      NewEventHub(),
		ctx:           ctx,
		cancel:        cancel,
	}
	return d
}

// newTestProxyServer creates a ProxyServer without starting it. Used
// by tests that only exercise the readiness-gate API surface; the
// server is not wired into a ProxyManager.
func newTestProxyServer(t *testing.T, targetURL string) *proxy.ProxyServer {
	t.Helper()
	ps, err := proxy.NewProxyServer(proxy.ProxyConfig{
		ID:         "detached",
		TargetURL:  targetURL,
		ListenPort: 0,
		MaxLogSize: 50,
	})
	require.NoError(t, err)
	// Stop chaos engine background goroutines and the page-tracker actor —
	// ProxyServer.Stop does this for started servers, but this test helper
	// never calls Start.
	t.Cleanup(func() {
		ps.StopChaos()
		ps.PageTracker().Stop()
	})
	return ps
}
