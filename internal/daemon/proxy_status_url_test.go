//go:build unix

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/require"
)

func TestProxyStatusURLConfigNameRoundTrip(t *testing.T) {
	d, client, _, dir := newHubProxyTestDaemon(t)
	path := filepath.Join(dir, config.AgntConfigFileName)
	require.NoError(t, os.WriteFile(path, []byte("proxies {\n web {\n port 5173\n }\n}\n"), 0600))
	cfg, err := config.LoadAgntConfig(dir)
	require.NoError(t, err)
	id := makeProcessID(dir, "web")
	d.handleExplicitStart(ProxyEvent{Type: ExplicitStart, ProxyID: id, ProxyName: "web", Config: cfg.Proxies["web"], Path: dir})
	list, err := client.ProxyList(protocol.DirectoryFilter{Directory: dir})
	require.NoError(t, err)
	entry := findListEntry(t, list, id)
	require.Equal(t, "web", entry["config_name"])

	const statusURL = "http://box.tail1234.ts.net:19191"
	require.NoError(t, config.SetProxyStatusURL(path, entry["config_name"].(string), statusURL))
	plan, err := d.ReconcileProjectConfig(context.Background(), dir)
	require.NoError(t, err)
	require.True(t, plan.IsEmpty(), "a display edit must not restart the proxy")
	list, err = client.ProxyList(protocol.DirectoryFilter{Directory: dir})
	require.NoError(t, err)
	require.Equal(t, statusURL, findListEntry(t, list, id)["status_url"])

	require.NoError(t, d.proxym.Stop(context.Background(), id))
	d.restoreProxies()
	list, err = client.ProxyList(protocol.DirectoryFilter{Directory: dir})
	require.NoError(t, err)
	entry = findListEntry(t, list, id)
	require.Equal(t, "web", entry["config_name"])
	require.Equal(t, statusURL, entry["status_url"])
}
