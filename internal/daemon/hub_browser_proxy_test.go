package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemon/health"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBrowserStart_AutoStartedProxyHasIncidentOwner is the regression for the
// proxy BROWSER START creates when its proxy_id does not exist yet. It was
// wired without an incident owner, and stampIncidentOwner fails closed for an
// owner-less proxy, so every browser JS error and HTTP 5xx it saw was dropped
// before the incident bus.
func TestBrowserStart_AutoStartedProxyHasIncidentOwner(t *testing.T) {
	d, _ := newBootedDaemon(t)
	project := normalizePath(t.TempDir())

	server, err := d.autoStartBrowserProxy(context.Background(), "browser-proxy", ephemeralTargetURL(t), project, "claude-1234abcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), server.ID) })

	v, ok := d.incidentProxyOwner.Load(server.ID)
	require.True(t, ok, "the auto-started proxy must carry an incident owner")
	assert.Equal(t, "claude-1234abcd", v.(*health.ResourceOwner).SessionCode)
}

// TestBrowserStart_FailedBrowserStopsAutoStartedProxy: when the browser fails
// to start, the proxy started only to front it was left running, unreported
// and with nothing that would ever stop it.
func TestBrowserStart_FailedBrowserStopsAutoStartedProxy(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)
	project := t.TempDir()

	_, err := c.BrowserStart(protocol.BrowserStartConfig{
		ProxyID:    "browser-proxy",
		URL:        ephemeralTargetURL(t),
		BinaryPath: filepath.Join(t.TempDir(), "no-such-chrome"),
		StartScope: protocol.StartScope{Directory: project},
	})
	require.Error(t, err, "a browser with a missing binary cannot start")

	_, running := d.exactProxy("browser-proxy")
	assert.False(t, running, "the auto-started proxy must not outlive the failed browser start")
	_, owned := d.incidentProxyOwner.Load("browser-proxy")
	assert.False(t, owned)
}
