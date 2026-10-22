package daemon

import (
	"context"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A malformed PROXY START payload used to be dropped, starting the proxy
// without the bind address / public URL / TLS setting asked for and reporting
// success.
func TestProxyStart_MalformedPayloadIsRefused(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)

	_, err := c.Conn().Request(protocol.VerbProxy, protocol.SubVerbStart, "badpayload", ephemeralTargetURL(t), "-1").
		WithData([]byte(`{"bind_address":`)).JSON()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid PROXY START payload")

	_, found := d.exactProxy("badpayload")
	assert.False(t, found, "a refused start must not leave a proxy behind")
}

// A malformed AUTORESTART enable payload used to enable auto-restart with the
// defaults, silently ignoring the limits the caller sent.
func TestProcAutoRestartEnable_MalformedPayloadIsRefused(t *testing.T) {
	d := newReviewBatchDaemon(t)
	client := daemonclient.NewClient(daemonclient.WithSocketPath(d.config.SocketPath))
	require.NoError(t, client.Connect())
	t.Cleanup(func() { _ = client.Close() })

	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "api")
	cmd, args := stayAliveExec()
	proc, startupErr := d.startScriptWithRetry(context.Background(), id, dir, dir, cmd, args, nil, nil, false)
	require.Nil(t, startupErr)
	t.Cleanup(func() { _ = d.hub.ProcessManager().StopProcess(context.Background(), proc) })

	_, err := client.Conn().Request(protocol.VerbProc, "AUTORESTART", id, "enable").
		WithData([]byte(`{"max_restarts":`)).JSON()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid AUTORESTART payload")

	_, ok := d.autoRestarter.GetConfig(id)
	assert.False(t, ok, "a refused enable must not register the process")
}

// BROWSER START, AUTOMATION START/NAVIGATE and OVERLAY SET discarded a
// malformed payload and ran with a zero config: a start with no project scope
// (invisible in its own project's listing), or a generic "required" error that
// hid the real cause.
func TestHub_MalformedPayloadIsRefused(t *testing.T) {
	_, c, _ := newBootedDaemonWithClient(t)
	bad := []byte(`{"id":`)
	cases := []struct {
		name string
		verb string
		sub  string
	}{
		{"browser start", protocol.VerbBrowser, protocol.SubVerbStart},
		{"automation start", protocol.VerbAutomation, protocol.SubVerbStart},
		{"automation navigate", protocol.VerbAutomation, protocol.SubVerbNavigate},
		{"overlay set", protocol.VerbOverlay, protocol.SubVerbSet},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Conn().Request(tc.verb, tc.sub).WithData(bad).JSON()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid payload")
		})
	}
}

// SESSION URL with an unreadable script payload used to fall back to the
// "dev" script and queue the URL against it.
func TestSessionURL_MalformedPayloadIsRefused(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))
	_, err := c.SessionRegister("urltest-1", "", dir, "claude", nil)
	require.NoError(t, err)

	_, err = c.Conn().Request(protocol.VerbSession, protocol.SubVerbURL, "urltest-1", "http://localhost:3000").
		WithData([]byte(`{"script":`)).JSON()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid SESSION URL payload")
	assert.Len(t, d.proxyEvents, 0, "no proxy event may be queued for a refused report")
}

func TestTunnelOwnerPath(t *testing.T) {
	proj := t.TempDir()
	linked := &proxy.ProxyServer{Path: proj}

	assert.Equal(t, "/sess", tunnelOwnerPath("/sess", linked), "a bound session wins")
	assert.Equal(t, normalizePath(proj), tunnelOwnerPath("", linked),
		"an unbound (MCP) connection takes the fronted proxy's project")
	assert.Equal(t, "", tunnelOwnerPath("", &proxy.ProxyServer{Path: "."}))
	assert.Equal(t, "", tunnelOwnerPath("", nil))
}

func TestTunnelStart_MalformedPayloadIsRefused(t *testing.T) {
	_, c, _ := newBootedDaemonWithClient(t)
	_, err := c.Conn().Request(protocol.VerbTunnel, protocol.SubVerbStart, "t1").
		WithData([]byte(`{"provider":`)).JSON()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid TUNNEL START payload")
}
