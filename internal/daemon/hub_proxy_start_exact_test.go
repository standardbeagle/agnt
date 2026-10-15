package daemon

import (
	"context"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProxyStart_ShortIDDoesNotResolveToAnotherProjectsProxy is the regression
// for PROXY START using the fuzzy ProxyManager.Get as its existence check. A
// short id ("dev") matched a component of another project's compound id
// ("projb-1a2b3c4d:dev"), so the start was skipped and that project's proxy
// was reported back as the one just started.
func TestProxyStart_ShortIDDoesNotResolveToAnotherProjectsProxy(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)
	projectA := t.TempDir()
	projectB := t.TempDir()

	other, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         "projb-1a2b3c4d:dev",
		TargetURL:  ephemeralTargetURL(t),
		ListenPort: -1,
		Path:       normalizePath(projectB),
	})
	require.NoError(t, err)

	target := ephemeralTargetURL(t)
	result, err := c.ProxyStartWithConfig("dev", target, -1, 0, daemonclient.ProxyStartConfig{Path: projectA})
	require.NoError(t, err)

	assert.Equal(t, "dev", result["id"], "PROXY START must answer with the proxy it started, not another project's")
	assert.NotEqual(t, other.ListenAddr, result["listen_addr"])
	assert.Nil(t, result["already_running"])

	started, ok := d.exactProxy("dev")
	require.True(t, ok, "a proxy must exist under the exact requested id")
	assert.Equal(t, normalizePath(projectA), started.Path)

	// Starting the same id again is idempotent and says so.
	again, err := c.ProxyStartWithConfig("dev", target, -1, 0, daemonclient.ProxyStartConfig{Path: projectA})
	require.NoError(t, err)
	assert.Equal(t, "dev", again["id"])
	assert.Equal(t, true, again["already_running"])
}
