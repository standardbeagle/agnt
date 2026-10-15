package daemon

import (
	"context"
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
