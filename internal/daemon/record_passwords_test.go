package daemon

import (
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// proxies.<id>.record-passwords reaches the proxy it is declared on, and an
// edit to it restarts that proxy so the new value takes effect.
func TestRecordPasswords_ConfigReachesProxy(t *testing.T) {
	assert.False(t, buildProxyServerConfig("p", "http://localhost:3000", "/proj", &config.ProxyConfig{}).RecordPasswords)
	assert.False(t, buildProxyServerConfig("p", "http://localhost:3000", "/proj", nil).RecordPasswords)
	assert.True(t, buildProxyServerConfig("p", "http://localhost:3000", "/proj", &config.ProxyConfig{RecordPasswords: true}).RecordPasswords)

	off := &config.ProxyConfig{URL: "http://localhost:3000"}
	on := &config.ProxyConfig{URL: "http://localhost:3000", RecordPasswords: true}
	assert.NotEqual(t, proxySignature(off), proxySignature(on), "toggling record-passwords must restart the proxy")
}

// A proxy started with record-passwords on says so in the project's startup
// log; one without it says nothing.
func TestRecordPasswords_AnnouncedAtProxyStart(t *testing.T) {
	d := &Daemon{startupErrorStore: NewStartupLogStore(10)}
	dir := t.TempDir()
	newServer := func(id string, optIn bool) *proxy.ProxyServer {
		ps, err := proxy.NewProxyServer(proxy.ProxyConfig{
			ID: id, TargetURL: "http://localhost:3000", ListenPort: -1, Path: dir, RecordPasswords: optIn,
		})
		require.NoError(t, err)
		return ps
	}

	d.announceRecordPasswords(newServer("plain", false))
	assert.Empty(t, d.startupErrorStore.Query(StartupLogFilter{Limit: 10}), "no opt-in, no entry")

	d.announceRecordPasswords(newServer("login", true))
	entries := d.startupErrorStore.Query(StartupLogFilter{Limit: 10})
	require.Len(t, entries, 1)
	assert.Equal(t, "record_passwords_on", entries[0].EventType)
	assert.Equal(t, "warning", entries[0].Level)
	assert.True(t, strings.Contains(entries[0].Message, "login"), "the entry names the proxy: %s", entries[0].Message)
}
