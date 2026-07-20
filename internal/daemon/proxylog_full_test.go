//go:build unix

package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/require"
)

// TestProxyLogQueryFull_PreservesResponseBody is the regression guard for the
// full-fidelity client pull. The compacted ProxyLogQuery path drops
// ResponseBody/ResponseHeaders; ProxyLogQueryFull must round-trip them intact
// from the daemon's TrafficLogger over the wire.
func TestProxyLogQueryFull_PreservesResponseBody(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	d := NewForTest(t, DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	proxyID := makeProcessID(tmpDir, "dev")
	p, err := d.proxym.Create(d.ctx, proxy.ProxyConfig{
		ID:         proxyID,
		TargetURL:  "http://localhost:3000",
		ListenPort: -1,
		Path:       tmpDir,
	})
	require.NoError(t, err)

	// Inject two full HTTP entries with response bodies + headers — the exact
	// payload the tool-side compaction step strips.
	p.Logger().LogHTTP(proxy.HTTPLogEntry{
		ID:              "req-1",
		Timestamp:       time.Now(),
		Method:          "GET",
		URL:             "http://localhost:3000/api/items",
		StatusCode:      200,
		ResponseHeaders: map[string]string{"Content-Type": "application/json"},
		ResponseBody:    `{"items":[1,2,3]}`,
	})
	p.Logger().LogHTTP(proxy.HTTPLogEntry{
		ID:              "req-2",
		Timestamp:       time.Now(),
		Method:          "POST",
		URL:             "http://localhost:3000/api/order",
		StatusCode:      201,
		ResponseHeaders: map[string]string{"X-Order-Id": "abc"},
		ResponseBody:    `{"ok":true}`,
	})

	client := NewClient(WithSocketPath(sockPath))
	require.NoError(t, client.Connect())
	defer client.Close()
	attachProjectSession(t, client, tmpDir)

	entries, total, dropped, err := client.ProxyLogQueryFull(proxyID, protocol.LogQueryFilter{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, total, int64(2), "total includes the two injected HTTP entries and may include proxy lifecycle entries")
	require.Zero(t, dropped)

	// Collect the HTTP entries and assert full fidelity survived the wire.
	byID := map[string]*proxy.HTTPLogEntry{}
	for i := range entries {
		if entries[i].Type == proxy.LogTypeHTTP && entries[i].HTTP != nil {
			byID[entries[i].HTTP.ID] = entries[i].HTTP
		}
	}
	require.Len(t, byID, 2, "both HTTP entries should round-trip")

	h1 := byID["req-1"]
	require.NotNil(t, h1)
	require.Equal(t, `{"items":[1,2,3]}`, h1.ResponseBody, "ResponseBody must survive (compacted path loses it)")
	require.Equal(t, "application/json", h1.ResponseHeaders["Content-Type"], "ResponseHeaders must survive")
	require.Equal(t, 200, h1.StatusCode)

	h2 := byID["req-2"]
	require.NotNil(t, h2)
	require.Equal(t, `{"ok":true}`, h2.ResponseBody)
	require.Equal(t, "abc", h2.ResponseHeaders["X-Order-Id"])
}
