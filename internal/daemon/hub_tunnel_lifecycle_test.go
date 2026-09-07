//go:build unix

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/require"
)

func TestTunnelStopRestoresProxyURLOverWire(t *testing.T) {
	// The provider is the external boundary. A local process emits its URL
	// and stays alive until stopped; no external tunnel service is contacted.
	tail, err := exec.LookPath("tail")
	require.NoError(t, err)
	provider := filepath.Join(t.TempDir(), "provider")
	require.NoError(t, os.WriteFile(provider, []byte(fmt.Sprintf("#!/bin/sh\nprintf 'https://review.trycloudflare.com\\n' >&2\nexec %q -f /dev/null\n", tail)), 0700))
	d, client := newRoutableTestClient(t)
	dir := t.TempDir()
	_, err = client.SessionRegister("tunnel-review", "", dir, "bash", nil)
	require.NoError(t, err)
	p, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{ID: "web", Path: dir, TargetURL: "http://127.0.0.1:5173", PublicURL: "https://configured.example"})
	require.NoError(t, err)
	_, err = client.TunnelStart(protocol.TunnelStartConfig{ID: "web-tunnel", Provider: "cloudflare", LocalPort: p.BoundPort(), ProxyID: "web", BinaryPath: provider})
	require.NoError(t, err)
	require.Equal(t, "https://review.trycloudflare.com", p.GetPublicURL())
	tun, err := d.tunnelm.Get("web-tunnel")
	require.NoError(t, err)
	require.NoError(t, client.TunnelStop("web-tunnel"))
	select {
	case <-tun.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("tunnel process did not exit")
	}
	require.Equal(t, "https://configured.example", p.GetPublicURL())
}
