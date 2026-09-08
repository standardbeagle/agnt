//go:build unix

package daemon

import (
	"context"
	"testing"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/require"
)

// A proxy is registered under a compound id -- project hash, name, host and
// port -- and every command accepts any single component of it, so a developer
// or an agent restarts "dev", not the whole string.
//
// Restarting by that short name must leave exactly one proxy, under the id it
// already had. Retiring the old server by the typed name removes nothing, so
// the list keeps the dead proxy alongside the new one; recreating under the
// typed name drops the project hash the list is scoped by. Both make `proxy
// list` describe a set of proxies that does not exist.
func TestProxyRestartByShortNameKeepsOneProxyUnderItsOwnID(t *testing.T) {
	d, client := newRoutableTestClient(t)
	dir := t.TempDir()
	_, err := client.SessionRegister("restart-identity", "", dir, "bash", nil)
	require.NoError(t, err)

	const fullID = "myapp-abc1:dev:localhost-3000"
	_, err = d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:        fullID,
		Path:      dir,
		TargetURL: "http://127.0.0.1:5173",
	})
	require.NoError(t, err)

	result, err := client.ProxyRestart("dev")
	require.NoError(t, err)
	require.Equal(t, fullID, result["id"], "the restart reported an id the proxy never had")

	listed, err := client.ProxyList(protocol.DirectoryFilter{Directory: dir})
	require.NoError(t, err)
	proxies, _ := listed["proxies"].([]interface{})
	require.Len(t, proxies, 1, "restarting by short name left more than one proxy in the list: %+v", proxies)

	entry, _ := proxies[0].(map[string]interface{})
	require.Equal(t, fullID, entry["id"], "the restarted proxy is listed under a different id")

	require.Equal(t, int64(1), d.proxym.ActiveCount(), "the daemon's active proxy count no longer matches the list")
}
