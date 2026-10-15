package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRebindProxyOverlays_NoOverlayPath(t *testing.T) {
	t.Parallel()
	pm := proxy.NewProxyManager()
	d := &Daemon{proxym: pm}

	// Session with empty overlay path — should be a no-op.
	session := &Session{
		Code:        "test-session",
		ProjectPath: "/home/user/project",
		OverlayPath: "",
	}
	d.rebindProxyOverlays(session)
	// No panic, no proxies changed — nothing to assert beyond survival.
}

func TestRebindProxyOverlays_BindsUnboundProxy(t *testing.T) {
	t.Parallel()
	pm := proxy.NewProxyManager()
	d := &Daemon{proxym: pm}

	tmpDir := shortTempDir(t)

	// Create a proxy with no overlay endpoint.
	ps, err := pm.Create(context.Background(), proxy.ProxyConfig{
		ID:         "test-proxy",
		TargetURL:  ephemeralTargetURL(t),
		ListenPort: -1,
		Path:       tmpDir,
	})
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pm.Shutdown(ctx)
	}()

	assert.False(t, ps.HasOverlayEndpoint(), "proxy should start without overlay")

	session := &Session{
		Code:        "sess-1",
		ProjectPath: tmpDir,
		OverlayPath: "http://127.0.0.1:19191",
	}
	d.rebindProxyOverlays(session)

	assert.True(t, ps.HasOverlayEndpoint(), "proxy should have overlay after rebind")
}

func TestRebindProxyOverlays_SkipsAlreadyBound(t *testing.T) {
	t.Parallel()
	pm := proxy.NewProxyManager()
	d := &Daemon{proxym: pm, sessionRegistry: NewSessionRegistry(0)}

	tmpDir := shortTempDir(t)

	ps, err := pm.Create(context.Background(), proxy.ProxyConfig{
		ID:         "bound-proxy",
		TargetURL:  ephemeralTargetURL(t),
		ListenPort: -1,
		Path:       tmpDir,
	})
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pm.Shutdown(ctx)
	}()

	// Pre-bind the overlay.
	originalEndpoint := "http://127.0.0.1:19191"
	ps.SetOverlayEndpoint(originalEndpoint)
	assert.True(t, ps.HasOverlayEndpoint())
	// A live session owns it, so the binding is not stale.
	require.NoError(t, d.sessionRegistry.Register(&Session{
		Code: "sess-owner", ProjectPath: tmpDir, OverlayPath: originalEndpoint, Status: SessionStatusActive,
	}))

	// rebindProxyOverlays should not overwrite.
	session := &Session{
		Code:        "sess-2",
		ProjectPath: tmpDir,
		OverlayPath: "http://127.0.0.1:29292",
	}
	d.rebindProxyOverlays(session)

	// Overlay should still be the original.
	assert.Equal(t, originalEndpoint, ps.OverlayEndpoint())
}

func TestRebindProxyOverlays_SkipsDifferentPath(t *testing.T) {
	t.Parallel()
	pm := proxy.NewProxyManager()
	d := &Daemon{proxym: pm}

	tmpDir := shortTempDir(t)
	otherDir := shortTempDir(t)

	ps, err := pm.Create(context.Background(), proxy.ProxyConfig{
		ID:         "other-proxy",
		TargetURL:  ephemeralTargetURL(t),
		ListenPort: -1,
		Path:       otherDir,
	})
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pm.Shutdown(ctx)
	}()

	session := &Session{
		Code:        "sess-3",
		ProjectPath: tmpDir,
		OverlayPath: "http://127.0.0.1:19191",
	}
	d.rebindProxyOverlays(session)

	assert.False(t, ps.HasOverlayEndpoint(), "proxy in different directory should not be bound")
}

// A proxy bound to a session's overlay must not keep pointing at that socket
// after the session leaves: it moves to the project's remaining session.
func TestSessionRetirement_RebindsProxyToRemainingSession(t *testing.T) {
	pm := proxy.NewProxyManager()
	d := &Daemon{proxym: pm, sessionRegistry: NewSessionRegistry(0)}
	tmpDir := shortTempDir(t)

	ps, err := pm.Create(context.Background(), proxy.ProxyConfig{
		ID: "retire-proxy", TargetURL: ephemeralTargetURL(t), ListenPort: -1, Path: tmpDir,
	})
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pm.Shutdown(ctx)
	}()

	a := &Session{Code: "sess-a", ProjectPath: tmpDir, OverlayPath: "/tmp/a.sock", Status: SessionStatusActive}
	b := &Session{Code: "sess-b", ProjectPath: tmpDir, OverlayPath: "/tmp/b.sock", Status: SessionStatusActive}
	require.NoError(t, d.sessionRegistry.Register(a))
	d.rebindProxyOverlays(a)
	require.NoError(t, d.sessionRegistry.Register(b))
	d.rebindProxyOverlays(b)
	require.Equal(t, "/tmp/a.sock", ps.OverlayEndpoint(), "a live binding is not stolen")

	d.finalizeSessionRetirement(a.Code, a)
	assert.Equal(t, "/tmp/b.sock", ps.OverlayEndpoint(), "proxy follows the remaining session")

	d.finalizeSessionRetirement(b.Code, b)
	assert.Equal(t, "", ps.OverlayEndpoint(), "no session left: proxy is unbound")
}

// A reconnect re-registers the session on a new overlay socket; the proxy's
// binding to the old socket is stale and must be replaced.
func TestRebindProxyOverlays_ReplacesStaleEndpoint(t *testing.T) {
	pm := proxy.NewProxyManager()
	d := &Daemon{proxym: pm, sessionRegistry: NewSessionRegistry(0)}
	tmpDir := shortTempDir(t)

	ps, err := pm.Create(context.Background(), proxy.ProxyConfig{
		ID: "stale-proxy", TargetURL: ephemeralTargetURL(t), ListenPort: -1, Path: tmpDir,
	})
	require.NoError(t, err)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pm.Shutdown(ctx)
	}()

	ps.SetOverlayEndpoint("/tmp/old.sock") // no registered session owns it
	s := &Session{Code: "sess-r", ProjectPath: tmpDir, OverlayPath: "/tmp/new.sock", Status: SessionStatusActive}
	require.NoError(t, d.sessionRegistry.Register(s))
	d.rebindProxyOverlays(s)

	assert.Equal(t, "/tmp/new.sock", ps.OverlayEndpoint())
}
