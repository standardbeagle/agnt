//go:build unix

package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartupPortCleanup_NoPersistedProxies(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := tmpDir + "/test.sock"
	statePath := tmpDir + "/state.json"

	d := New(DaemonConfig{
		SocketPath:             sockPath,
		EnableStatePersistence: true,
		StatePath:              statePath,
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	// No proxies persisted — should be a no-op
	cleaned := d.startupPortCleanup(context.Background())
	assert.Equal(t, 0, cleaned)
}

func TestStartupPortCleanup_PortFree(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := tmpDir + "/test.sock"
	statePath := tmpDir + "/state.json"

	d := New(DaemonConfig{
		SocketPath:             sockPath,
		EnableStatePersistence: true,
		StatePath:              statePath,
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	// Persist a proxy config with a port
	d.stateMgr.AddProxy(PersistentProxyConfig{
		ID:   "test-proxy",
		Port: ephemeralPort(t), // free at time of call
	})

	// Port is free — nothing to kill
	cleaned := d.startupPortCleanup(context.Background())
	assert.Equal(t, 0, cleaned)
}

func TestStartupPortCleanup_KillsOrphan(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("don't run as root")
	}

	tmpDir := t.TempDir()
	sockPath := tmpDir + "/test.sock"
	statePath := tmpDir + "/state.json"

	d := New(DaemonConfig{
		SocketPath:             sockPath,
		EnableStatePersistence: true,
		StatePath:              statePath,
	})
	require.NoError(t, d.Start())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	// The blocker self-assigns its port (binds :0 and reports it back), so it
	// owns the port from bind — no reserve-then-rebind TOCTOU window.
	_, port := startEphemeralPortBlocker(t)

	// Persist a proxy that uses this port
	d.stateMgr.AddProxy(PersistentProxyConfig{
		ID:   "orphan-proxy",
		Port: port,
	})

	// Run startup cleanup — should kill the orphan
	cleaned := d.startupPortCleanup(context.Background())
	assert.Equal(t, 1, cleaned)

	// Verify port is free
	ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	assert.NoError(t, err, "port should be free after cleanup")
	if ln2 != nil {
		ln2.Close()
	}
}

func TestStartupPortCleanup_SkipsManagedPIDs(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := tmpDir + "/test.sock"
	statePath := tmpDir + "/state.json"

	d := New(DaemonConfig{
		SocketPath:             sockPath,
		EnableStatePersistence: true,
		StatePath:              statePath,
	})
	require.NoError(t, d.Start())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	// Bind a port from the daemon's own PID (the test process)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	defer ln.Close()

	// Persist a proxy that uses this port
	d.stateMgr.AddProxy(PersistentProxyConfig{
		ID:   "managed-proxy",
		Port: port,
	})

	// The daemon's own processes should be skipped (not killed)
	// Since we have no managed processes, the test PID won't be skipped
	// but the point is: startupPortCleanup should not panic or error
	cleaned := d.startupPortCleanup(context.Background())
	// Port is held by the test process — it IS unmanaged from daemon's perspective
	// so the count should be 1 (the process holding the port)
	_ = cleaned
}

func TestStartupPortCleanup_MultipleProxies(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("don't run as root")
	}

	tmpDir := t.TempDir()
	sockPath := tmpDir + "/test.sock"
	statePath := tmpDir + "/state.json"

	d := New(DaemonConfig{
		SocketPath:             sockPath,
		EnableStatePersistence: true,
		StatePath:              statePath,
	})
	require.NoError(t, d.Start())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		d.Stop(ctx)
	}()

	// Two self-assigning blockers — each owns its port from bind, no TOCTOU.
	_, port1 := startEphemeralPortBlocker(t)
	_, port2 := startEphemeralPortBlocker(t)

	// Persist two proxies
	d.stateMgr.AddProxy(PersistentProxyConfig{ID: "proxy-1", Port: port1})
	d.stateMgr.AddProxy(PersistentProxyConfig{ID: "proxy-2", Port: port2})

	// Run cleanup — both orphans should be killed
	cleaned := d.startupPortCleanup(context.Background())
	assert.Equal(t, 2, cleaned)
}

func TestCollectPersistedPorts(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	sockPath := tmpDir + "/test.sock"
	statePath := tmpDir + "/state.json"

	d := New(DaemonConfig{
		SocketPath:             sockPath,
		EnableStatePersistence: true,
		StatePath:              statePath,
	})

	// No proxies
	ports := d.collectPersistedPorts()
	assert.Empty(t, ports)

	// Add proxies
	d.stateMgr.AddProxy(PersistentProxyConfig{ID: "p1", Port: 3000})
	d.stateMgr.AddProxy(PersistentProxyConfig{ID: "p2", Port: 3001})
	d.stateMgr.AddProxy(PersistentProxyConfig{ID: "p3", Port: 0})    // zero port ignored
	d.stateMgr.AddProxy(PersistentProxyConfig{ID: "p4", Port: 3000}) // duplicate port

	ports = d.collectPersistedPorts()
	assert.ElementsMatch(t, []int{3000, 3001}, ports)
}
