package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/stretchr/testify/require"
)

// testPortCounter drives allocTestPort. Started below the base so the first
// allocation lands at the base.
var testPortCounter atomic.Int32

// allocTestPort returns a process-unique TCP port in the 20000–31999 range —
// deliberately BELOW the Linux ephemeral range (ip_local_port_range starts at
// 32768), so the kernel never hands one of these to a net.Listen("127.0.0.1:0")
// elsewhere in the suite. Each call returns a distinct number (atomic counter),
// so two parallel tests never receive the same port, and the value is verified
// bindable before return.
//
// This replaces the reserve-:0-then-close-then-rebind pattern, whose TOCTOU
// window let a parallel test's ephemeral allocation steal the just-freed port —
// the root cause of the suite's intermittent "bind: address already in use"
// flakes. Because the returned port is both unique and outside the ephemeral
// range, neither cross-test reuse nor :0 handoff can collide with it.
func allocTestPort(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 8000; attempt++ {
		p := 20000 + int(testPortCounter.Add(1))%12000
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue // held by something (or a prior wrap still bound); try next
		}
		_ = ln.Close()
		return p
	}
	t.Fatal("allocTestPort: no free port found in 20000-31999")
	return 0
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// rootPath returns the filesystem root for the current platform.
func rootPath() string {
	if runtime.GOOS == "windows" {
		return `C:\`
	}
	return "/"
}

// shortTempDir creates a temp directory with a short path to stay within
// the unix socket path length limit (~108 chars) on Windows.
func shortTempDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if runtime.GOOS == "windows" {
		// Use a short base to avoid exceeding socket path limits.
		base = `C:\tmp`
		os.MkdirAll(base, 0755)
	}
	dir, err := os.MkdirTemp(base, "d")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// shortSockPath returns a socket path short enough for Windows unix sockets.
func shortSockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(shortTempDir(t), "s.sock")
}

// defaultTestDaemonConfig returns the config shape every daemon test used
// to hand-roll inline. Tests override by mutating the returned struct
// before passing it to newBootedDaemon / newBootedDaemonWithClient.
func defaultTestDaemonConfig(t *testing.T) DaemonConfig {
	t.Helper()
	return DaemonConfig{
		SocketPath:   shortSockPath(t),
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	}
}

// newBootedDaemon boots a Daemon with default test config + a Cleanup that
// stops it, and returns the started instance along with the socket path it
// was configured with (so tests that need to reuse the path — e.g. to
// construct a second client — can without reaching into Daemon internals).
// Eliminates the 10-line boot/defer-stop boilerplate from individual tests.
// Callers needing a non-default config should call newBootedDaemonWithConfig.
func newBootedDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	sockPath := shortSockPath(t)
	d := newBootedDaemonWithConfig(t, DaemonConfig{SocketPath: sockPath})
	return d, sockPath
}

// newBootedDaemonWithConfig is like newBootedDaemon but lets the caller override
// fields. SocketPath is auto-filled via shortSockPath if empty so tests
// don't have to remember the Windows sockaddr_un length trap.
//
// Routes through NewForTest so the heavy production-only startup steps
// (cleanupOrphans, startupPortCleanup, startupOrphanPGIDScan, restoreProxies,
// updateChecker) are skipped. Those steps walk /proc and issue kill(2) on
// scan-discovered PIDs; running them from N parallel tests causes host-global
// contention that surfaces as hang/timeout flakes in tests unrelated to the
// startup ops themselves. Tests that specifically need one of those ops must
// call the method directly (e.g. d.cleanupOrphans()).
func newBootedDaemonWithConfig(t *testing.T, cfg DaemonConfig) *Daemon {
	t.Helper()
	if cfg.SocketPath == "" {
		cfg.SocketPath = shortSockPath(t)
	}
	if cfg.MaxClients == 0 {
		cfg.MaxClients = 10
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 5 * time.Second
	}
	return NewForTest(t, cfg)
}

// newBootedDaemonWithClient boots a daemon and a connected daemonclient.Client. Used by
// hub-integration tests that drive the daemon through its protocol. The
// tmpDir return value is the working directory used for the socket — tests
// that write config files under it can reuse the same dir.
func newBootedDaemonWithClient(t *testing.T) (*Daemon, *daemonclient.Client, string) {
	t.Helper()
	sockPath := shortSockPath(t)
	tmpDir := filepath.Dir(sockPath)
	d := newBootedDaemonWithConfig(t, DaemonConfig{SocketPath: sockPath})
	c := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	return d, c, tmpDir
}

// newBootedClient is the zero-ceremony variant of newBootedDaemonWithClient
// for tests that only need a client. The daemon is still booted (required
// for the client to talk to) and is stopped via t.Cleanup.
func newBootedClient(t *testing.T) *daemonclient.Client {
	t.Helper()
	_, c, _ := newBootedDaemonWithClient(t)
	return c
}

// ephemeralPort returns a port number that was free at the time of the call.
// Briefly binds to :0, reads the allocated port, then releases the listener.
// Use for proxy target URLs where no real listener is needed, not for ports
// you intend to bind yourself (small TOCTOU window).
func ephemeralPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// ephemeralTargetURL returns a unique "http://127.0.0.1:<port>" URL for use
// as a proxy target in tests. Each call returns a distinct URL, preventing
// hash-based proxy listen port collisions under -count>1 / -p parallel runs.
func ephemeralTargetURL(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("http://127.0.0.1:%d", ephemeralPort(t))
}
