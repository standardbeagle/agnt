package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/scope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// activeSession builds a registered, active session for a project + overlay.
func activeSession(t *testing.T, r *SessionRegistry, code, projectPath, overlay string) *Session {
	t.Helper()
	s := &Session{Code: code, ProjectPath: projectPath, OverlayPath: overlay}
	s.SetStatus(SessionStatusActive)
	require.NoError(t, r.Register(s))
	return s
}

// TestNoCrossProjectOverlayDelivery is the regression guard for the reported
// bug: a panel message sent from project A's browser surfaced in project B's
// agent. The leak was overlay-endpoint binding that fell back to a single
// global value, so the last session to register re-pointed every proxy at its
// own overlay. This test pins that each project's proxy binds only to its own
// session's overlay, and that registering a second project never re-points the
// first project's proxy.
func TestNoCrossProjectOverlayDelivery(t *testing.T) {
	t.Parallel()

	pm := proxy.NewProxyManager()
	reg := NewSessionRegistry(time.Minute)
	d := &Daemon{proxym: pm, sessionRegistry: reg}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pm.Shutdown(ctx)
	}()

	dirA := shortTempDir(t)
	dirB := shortTempDir(t)
	overlayA := "http://127.0.0.1:19191"
	overlayB := "http://127.0.0.1:29292"

	sessA := activeSession(t, reg, "sess-space", dirA, overlayA)
	sessB := activeSession(t, reg, "sess-rpg", dirB, overlayB)

	// overlayEndpointForProject must resolve each project to its OWN overlay,
	// never the other's — the core of the fix.
	assert.Equal(t, overlayA, d.overlayEndpointForProject(dirA))
	assert.Equal(t, overlayB, d.overlayEndpointForProject(dirB))

	// Create one proxy per project.
	proxyA, err := pm.Create(context.Background(), proxy.ProxyConfig{
		ID: "proxy-space", TargetURL: ephemeralTargetURL(t), ListenPort: -1, Path: dirA,
	})
	require.NoError(t, err)
	proxyB, err := pm.Create(context.Background(), proxy.ProxyConfig{
		ID: "proxy-rpg", TargetURL: ephemeralTargetURL(t), ListenPort: -1, Path: dirB,
	})
	require.NoError(t, err)

	// Bind via the production path. Order matters for the regression: bind B
	// LAST, which under the old global-blast bug would have clobbered A.
	d.rebindProxyOverlays(sessA)
	d.rebindProxyOverlays(sessB)

	assert.Equal(t, overlayA, proxyA.OverlayNotifier().GetEndpoint(),
		"project A proxy must keep A's overlay after B registers (no cross-project clobber)")
	assert.Equal(t, overlayB, proxyB.OverlayNotifier().GetEndpoint(),
		"project B proxy must bind to B's overlay")

	// ListScoped must isolate: A's scope sees only A's proxy.
	scopedA := pm.ListScoped(scope.Project(dirA))
	require.Len(t, scopedA, 1)
	assert.Equal(t, "proxy-space", scopedA[0].ID)

	scopedB := pm.ListScoped(scope.Project(dirB))
	require.Len(t, scopedB, 1)
	assert.Equal(t, "proxy-rpg", scopedB[0].ID)

	// Unscoped sees both — the audited escape hatch still works.
	assert.Len(t, pm.ListScoped(scope.Unscoped("test: see all")), 2)
}

// TestUnboundProxyFailsClosed verifies that a proxy whose session has not yet
// registered gets NO overlay endpoint rather than leaking onto another
// project's overlay (the old global fallback). It is wired up later when its
// own session connects.
func TestUnboundProxyFailsClosed(t *testing.T) {
	t.Parallel()

	pm := proxy.NewProxyManager()
	reg := NewSessionRegistry(time.Minute)
	d := &Daemon{proxym: pm, sessionRegistry: reg}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		pm.Shutdown(ctx)
	}()

	dirA := shortTempDir(t)
	dirB := shortTempDir(t)

	// Only project B has a session.
	activeSession(t, reg, "sess-rpg", dirB, "http://127.0.0.1:29292")

	// A proxy for project A (no session yet) must resolve to "" — fail closed.
	assert.Equal(t, "", d.overlayEndpointForProject(dirA),
		"project with no session must not inherit another project's overlay")
}
