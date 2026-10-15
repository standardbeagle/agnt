package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/testutil/sockdir"
	"github.com/stretchr/testify/require"
)

// OVERLAY ACTIVITY and OUTPUT-PREVIEW take proxy ids; a fuzzy id from a
// session-bound connection must resolve inside that session's project only,
// never to another project's proxy (ID-scoped verb contract).
func TestOverlayBroadcast_FuzzyProxyIDStaysInSessionProject(t *testing.T) {
	tmpDir := sockdir.Short(t)
	sockPath := filepath.Join(tmpDir, "test.sock")
	projectA := filepath.Join(tmpDir, "project-a")
	projectB := filepath.Join(tmpDir, "project-b")
	require.NoError(t, os.MkdirAll(projectA, 0755))
	require.NoError(t, os.MkdirAll(projectB, 0755))

	_ = NewForTest(t, DaemonConfig{SocketPath: sockPath})
	client := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
	require.NoError(t, client.Connect())
	t.Cleanup(func() { _ = client.Close() })

	// Only project B has a "web" proxy.
	proxyB := "proj-b:web:localhost-4000"
	_, err := client.ProxyStart(proxyB, "http://localhost:4000", 0, 100, projectB)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.ProxyStop(proxyB) })

	// The connection belongs to project A.
	_, err = client.Conn().Request("SESSION", "REGISTER", "overlay-scope-a", projectA).
		WithJSON(map[string]interface{}{"project_path": projectA, "command": "test"}).JSON()
	require.NoError(t, err)
	_, err = client.Conn().Request("SESSION", "ATTACH", projectA).JSON()
	require.NoError(t, err)

	res, err := client.Conn().Request("OVERLAY", "ACTIVITY", "true", "web").JSON()
	require.NoError(t, err)
	require.EqualValues(t, 0, res["proxies"], "OVERLAY ACTIVITY reached another project's proxy by fuzzy id")

	res, err = client.Conn().Request("OVERLAY", "OUTPUT-PREVIEW").
		WithJSON(map[string]interface{}{"lines": []string{"x"}, "proxy_ids": []string{"web"}}).JSON()
	require.NoError(t, err)
	require.EqualValues(t, 0, res["proxies"], "OVERLAY OUTPUT-PREVIEW reached another project's proxy by fuzzy id")

	// The exact id still resolves (exact ids always work).
	res, err = client.Conn().Request("OVERLAY", "ACTIVITY", "true", proxyB).JSON()
	require.NoError(t, err)
	require.EqualValues(t, 1, res["proxies"])
}
