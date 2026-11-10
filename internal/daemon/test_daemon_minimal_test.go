package daemon

import (
	"context"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/require"
)

// These helpers are untagged on purpose: untagged tests on every platform
// (devoidc_config_error_test.go) use them. They lived in the unix-only
// proxy_waitfor_test.go, which broke the Windows test build.

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
