package daemon

import (
	"context"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/require"
)

// TestWireProxyLogger_WiresSessionClientFactory pins the fix for
// SetSessionClientFactory having no production caller: every proxy created
// by the daemon must come out of wireProxyLogger (the single chokepoint
// every proxy-creation path funnels through) with a session client factory
// attached, or `__devtool.session.*` / `__devtool.store.*` always fail with
// "no session client factory configured".
func TestWireProxyLogger_WiresSessionClientFactory(t *testing.T) {
	d := &Daemon{config: DaemonConfig{SocketPath: "/tmp/does-not-matter.sock"}}

	server, err := proxy.NewProxyServer(proxy.ProxyConfig{ID: "session-client-test", TargetURL: "http://127.0.0.1:1", Path: "/project/a"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Stop(context.Background()) })

	require.False(t, server.HasSessionClientFactory(), "sanity: a freshly constructed ProxyServer starts with no factory")

	d.wireProxyLogger(server)

	require.True(t, server.HasSessionClientFactory(), "wireProxyLogger must wire a session client factory")
}
