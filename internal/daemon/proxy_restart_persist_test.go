package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An explicit start with no listen-port asks for -1 (allocate one); the state
// a later daemon restores from must hold the port actually bound, and a
// restart must keep the proxy's crash auto-restart setting.
func TestExplicitStartPersistsBoundPortAndRestartKeepsAutoRestart(t *testing.T) {
	d := NewForTest(t, DaemonConfig{
		SocketPath:             shortSockPath(t),
		MaxClients:             4,
		WriteTimeout:           2 * time.Second,
		EnableStatePersistence: true,
		StatePath:              filepath.Join(t.TempDir(), "state.json"),
	})

	dir := normalizePath(shortTempDir(t))
	id := makeProcessID(dir, "web")
	d.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: id,
		Config:  &config.ProxyConfig{URL: "http://127.0.0.1:65043"},
		Path:    dir,
	})
	p, err := d.proxym.Get(id)
	require.NoError(t, err)
	require.True(t, p.AutoRestart(), "precondition: autostart proxies auto-restart")

	pc, ok := d.stateMgr.GetProxy(id)
	require.True(t, ok)
	assert.Equal(t, p.BoundPort(), pc.Port, "persisted port must be the bound port, not the requested -1")

	restarted, err := d.restartProxy(context.Background(), p, "")
	require.NoError(t, err)
	assert.True(t, restarted.AutoRestart(), "a restart must keep auto-restart")
}

// A restart recreates the server, so every per-proxy setting the original was
// started with has to survive it: a declared listen-port stays strict ("this
// port or nothing", also for the crash auto-restart), the display-only
// status-url stays on the overlay, and the project's auth-breakout rules are
// installed again.
func TestRestartProxyKeepsStrictPortStatusURLAndAuthBreakout(t *testing.T) {
	d := NewForTest(t, DaemonConfig{
		SocketPath:   shortSockPath(t),
		MaxClients:   4,
		WriteTimeout: 2 * time.Second,
	})

	dir := normalizePath(shortTempDir(t))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agnt.kdl"),
		[]byte("auth-breakout {\n enabled true\n}\n"), 0o600))
	id := makeProcessID(dir, "web")
	const statusURL = "https://status.example.test/web"
	d.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: id,
		Config: &config.ProxyConfig{
			URL:        "http://127.0.0.1:65043",
			ListenPort: reserveFreePort(t),
			StatusURL:  statusURL,
		},
		Path: dir,
	})
	p, err := d.proxym.Get(id)
	require.NoError(t, err)
	require.True(t, p.StrictListenPort, "precondition: a declared listen-port is strict")
	require.Equal(t, statusURL, p.GetStatusURL(), "precondition: status-url applied")
	require.NotNil(t, p.AuthBreakoutRules(), "precondition: auth-breakout applied")

	restarted, err := d.restartProxy(context.Background(), p, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), restarted.ID) })
	assert.Equal(t, p.BoundPort(), restarted.BoundPort(), "a restart must come back on the declared port")
	assert.True(t, restarted.StrictListenPort, "a restart must keep a declared listen-port strict")
	assert.Equal(t, statusURL, restarted.GetStatusURL(), "a restart must keep the status-url")
	assert.NotNil(t, restarted.AuthBreakoutRules(), "a restart must keep the auth-breakout rules")
}
