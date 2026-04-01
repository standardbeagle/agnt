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
	pm := proxy.NewProxyManager()
	d := &Daemon{proxym: pm}

	tmpDir := shortTempDir(t)

	// Create a proxy with no overlay endpoint.
	ps, err := pm.Create(context.Background(), proxy.ProxyConfig{
		ID:         "test-proxy",
		TargetURL:  "http://localhost:9999",
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
	pm := proxy.NewProxyManager()
	d := &Daemon{proxym: pm}

	tmpDir := shortTempDir(t)

	ps, err := pm.Create(context.Background(), proxy.ProxyConfig{
		ID:         "bound-proxy",
		TargetURL:  "http://localhost:9998",
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

	// rebindProxyOverlays should not overwrite.
	session := &Session{
		Code:        "sess-2",
		ProjectPath: tmpDir,
		OverlayPath: "http://127.0.0.1:29292",
	}
	d.rebindProxyOverlays(session)

	// Overlay should still be the original.
	assert.True(t, ps.HasOverlayEndpoint())
}

func TestRebindProxyOverlays_SkipsDifferentPath(t *testing.T) {
	pm := proxy.NewProxyManager()
	d := &Daemon{proxym: pm}

	tmpDir := shortTempDir(t)
	otherDir := shortTempDir(t)

	ps, err := pm.Create(context.Background(), proxy.ProxyConfig{
		ID:         "other-proxy",
		TargetURL:  "http://localhost:9997",
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
