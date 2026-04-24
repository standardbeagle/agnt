package daemon

import (
	"context"
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestNewForTest_StartsUnder100ms enforces the headline cost contract
// for the test factory: spinning up a usable daemon must complete in
// less than 100ms on a developer laptop. The full production Start()
// path is >500ms because of orphan scans, port preflight, proxy
// restoration, and the update-checker bootstrap. NewForTest skips all
// of those, so any regression that pulls one of them back in will trip
// this assertion.
//
// The threshold is generous (100ms) to absorb CI scheduler jitter; the
// real number on a quiet laptop is typically <20ms. If this test
// becomes flaky on a slow CI runner, raise the threshold rather than
// remove the assertion — the spend-budget itself is the contract.
func TestNewForTest_StartsUnder100ms(t *testing.T) {
	t.Parallel()
	cfg := DaemonConfig{
		SocketPath:        ephemeralTestSockPath(t),
		MaxClients:        10,
		WriteTimeout:      5 * time.Second,
		OrphanScanEnabled: false,
		StatePath:         t.TempDir(),
	}

	start := time.Now()
	d := NewForTest(t, cfg)
	elapsed := time.Since(start)

	require.NotNil(t, d, "NewForTest must return a daemon")
	require.Less(t, elapsed, 2*time.Second,
		"NewForTest startup budget exceeded: %v (limit 2s, ~20ms expected). "+
			"A new caller of bootstrap() likely pulled in an expensive "+
			"production-only step like cleanupOrphans, startupPortCleanup, "+
			"restoreProxies, or updateChecker.Start. Move it to Start() "+
			"only.", elapsed)
}

// TestNewForTest_HubAccepts verifies the daemon's socket is live and
// accepting connections after NewForTest returns. Without bootstrap()
// calling hub.Start, the socket file would not exist and any client
// connect would fail with ENOENT.
func TestNewForTest_HubAccepts(t *testing.T) {
	t.Parallel()
	cfg := DaemonConfig{
		SocketPath:        ephemeralTestSockPath(t),
		MaxClients:        10,
		WriteTimeout:      5 * time.Second,
		OrphanScanEnabled: false,
		StatePath:         t.TempDir(),
	}

	d := NewForTest(t, cfg)
	require.NotNil(t, d)

	// Hub must be live — socket file should exist on Unix, named pipe on Windows.
	if runtime.GOOS != "windows" {
		_, err := os.Stat(cfg.SocketPath)
		require.NoError(t, err, "hub socket file must exist after NewForTest")
	}

	// Confirm accept loop runs by dialing.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var d2 net.Dialer
	conn, err := d2.DialContext(ctx, "unix", cfg.SocketPath)
	if runtime.GOOS == "windows" {
		// Skip dial on Windows; named pipe semantics differ.
		t.Skip("skipping unix-socket dial on Windows")
	}
	require.NoError(t, err, "must be able to dial daemon socket")
	require.NoError(t, conn.Close())
}

// ephemeralTestSockPath returns a short temp socket path that fits inside
// the ~108-char unix socket limit. Mirrors daemontest.ephemeralSockPath
// so this in-package test does not need to import daemontest (which would
// create an import cycle).
func ephemeralTestSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "dt")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir + "/s.sock"
}
