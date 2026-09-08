package daemon

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"
)

// The admin surface -- the overlay status bar and SCRIPT LIST -- reads
// d.proxyEntries. Starting a proxy adds a row there; nothing on the stop path
// took it away, so the row outlived the proxy until the whole session was
// cleaned up. A developer looking at the status bar, and an agent reading
// SCRIPT LIST, were told a proxy was serving on an address nothing was
// listening on.
func TestProxyStopRetiresItsAdminEntry(t *testing.T) {
	t.Parallel()
	d, client, backend, tmpDir := newHubProxyTestDaemon(t)

	const proxyID = "entry-lifetime"
	if _, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, daemonclient.ProxyStartConfig{
		Path: tmpDir,
	}); err != nil {
		t.Fatalf("starting the proxy: %v", err)
	}
	if findProxyEntry(d, tmpDir, proxyID) == nil {
		t.Fatalf("premise broken: no admin entry after the proxy started, so the removal is untested")
	}

	if err := client.ProxyStop(proxyID); err != nil {
		t.Fatalf("stopping the proxy: %v", err)
	}

	if entry := findProxyEntry(d, tmpDir, proxyID); entry != nil {
		t.Errorf("the admin surface still lists %q at %q after the proxy was stopped",
			entry.Name(), entry.ListenAddr())
	}
}

// A restart may not come back on the same port: the handler asks for the old
// one and accepts a fresh one when the OS has not released it. The admin row
// has to follow the proxy to its new address, or it points the developer and
// the agent at a port nothing answers on.
//
// The row is given a wrong address before the restart rather than waiting for
// a port drift to produce one. A restart that reaches the same port -- the
// usual case -- would otherwise agree with a stale row as readily as with a
// refreshed one, and the test would pass without exercising anything.
func TestProxyRestartRefreshesItsAdminEntryAddress(t *testing.T) {
	t.Parallel()
	d, client, backend, tmpDir := newHubProxyTestDaemon(t)

	const proxyID = "entry-restart"
	if _, err := client.ProxyStartWithConfig(proxyID, backend.URL, 0, 100, daemonclient.ProxyStartConfig{
		Path: tmpDir,
	}); err != nil {
		t.Fatalf("starting the proxy: %v", err)
	}

	const staleAddr = "127.0.0.1:11111"
	d.retireExplicitProxyEntry(tmpDir, proxyID)
	d.registerExplicitProxyEntry(tmpDir, proxyID, staleAddr)
	if entry := findProxyEntry(d, tmpDir, proxyID); entry == nil || entry.ListenAddr() != staleAddr {
		t.Fatalf("premise broken: the row does not hold the stale address, so the refresh is untested")
	}

	result, err := client.ProxyRestart(proxyID)
	if err != nil {
		t.Fatalf("restarting the proxy: %v", err)
	}
	listenAddr, _ := result["listen_addr"].(string)
	if listenAddr == "" {
		t.Fatal("the restart reported no listen address")
	}

	entry := findProxyEntry(d, tmpDir, proxyID)
	if entry == nil {
		t.Fatal("the restarted proxy has no admin entry")
	}
	if entry.ListenAddr() != listenAddr {
		t.Errorf("the admin surface lists %q, the proxy is serving on %q", entry.ListenAddr(), listenAddr)
	}
}

func findProxyEntry(d *Daemon, projectPath, proxyID string) *proxyScriptEntry {
	for _, e := range d.proxyEntries.List(projectPath) {
		if e.ProxyID() == proxyID {
			return e
		}
	}
	return nil
}
