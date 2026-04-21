package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
	d := New(cfg)
	require.NoError(t, d.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Stop(ctx)
	})
	return d
}

// newBootedDaemonWithClient boots a daemon and a connected Client. Used by
// hub-integration tests that drive the daemon through its protocol. The
// tmpDir return value is the working directory used for the socket — tests
// that write config files under it can reuse the same dir.
func newBootedDaemonWithClient(t *testing.T) (*Daemon, *Client, string) {
	t.Helper()
	sockPath := shortSockPath(t)
	tmpDir := filepath.Dir(sockPath)
	d := newBootedDaemonWithConfig(t, DaemonConfig{SocketPath: sockPath})
	c := NewClient(WithSocketPath(sockPath))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	return d, c, tmpDir
}

// newBootedClient is the zero-ceremony variant of newBootedDaemonWithClient
// for tests that only need a client. The daemon is still booted (required
// for the client to talk to) and is stopped via t.Cleanup.
func newBootedClient(t *testing.T) *Client {
	t.Helper()
	_, c, _ := newBootedDaemonWithClient(t)
	return c
}
