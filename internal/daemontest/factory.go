// Package daemontest provides test helpers for creating daemon instances.
package daemontest

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/stretchr/testify/require"
)

// Option is a functional option for New.
type Option func(*daemon.DaemonConfig)

// WithStatePersistence enables state persistence in a temp dir.
func WithStatePersistence() Option {
	return func(cfg *daemon.DaemonConfig) {
		cfg.EnableStatePersistence = true
	}
}

// New boots a Daemon with an ephemeral socket path and registers a
// t.Cleanup that stops it. The daemon uses default test-safe settings:
// OrphanScanEnabled=false, StatePath=t.TempDir(), 5s WriteTimeout.
//
// Construction is routed through daemon.NewForTest so the heavy
// production-only startup steps (orphan scans, port preflight,
// proxy restoration, update checker) are skipped. See the
// "Test startup contract" section of .claude/rules/daemon-architecture.md
// for what's included vs. excluded and why.
func New(t *testing.T, opts ...Option) *daemon.Daemon {
	t.Helper()
	cfg := daemon.DaemonConfig{
		SocketPath:        ephemeralSockPath(t),
		MaxClients:        10,
		WriteTimeout:      5 * time.Second,
		OrphanScanEnabled: false,
		StatePath:         t.TempDir(),
	}
	for _, o := range opts {
		o(&cfg)
	}
	return daemon.NewForTest(t, cfg)
}

// EphemeralPort returns a port number that was free at the time of the call.
// It briefly binds to :0 to let the OS allocate a port, then releases it.
// There is a small TOCTOU window — use this for proxy target URLs where an
// actual listener is not required, not for ports you intend to bind yourself.
func EphemeralPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// EphemeralTargetURL returns a unique "http://127.0.0.1:<port>" URL
// suitable for use as a proxy target URL in tests. The port is allocated
// via :0 so each call returns a distinct URL, preventing hash-based listen
// port collisions across parallel test runs.
func EphemeralTargetURL(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("http://127.0.0.1:%d", EphemeralPort(t))
}

// ephemeralSockPath creates a short temp directory and returns a socket
// path inside it. Short path is required to stay within the unix socket
// path length limit (~108 chars) on Linux/macOS.
func ephemeralSockPath(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if runtime.GOOS == "windows" {
		base = `C:\tmp`
		os.MkdirAll(base, 0755)
	}
	dir, err := os.MkdirTemp(base, "dt")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir + "/s.sock"
}
