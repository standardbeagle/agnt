//go:build unix

package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDepsIntegration_ClientStartsAfterServerURL verifies that a script with
// depends-on waits until its dependency's URL is detected before starting.
func TestDepsIntegration_ClientStartsAfterServerURL(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create .agnt.kdl with two scripts:
	// - "server" prints a URL after 1 second, then stays alive
	// - "client" depends on "server", prints a message, then stays alive
	//
	// Both must be long-running so the process manager does not treat a quick
	// exit as a startup failure.
	configContent := `
scripts {
    server {
        run "sleep 1 && echo Listening on http://localhost:9999 && sleep 60"
        autostart true
    }
    client {
        run "echo client-started && sleep 60"
        autostart true
        depends-on "server" timeout=10
    }
}
`
	configPath := filepath.Join(tmpDir, ".agnt.kdl")
	require.NoError(t, writeFile(configPath, configContent))

	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	require.NoError(t, daemon.Start())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	// Connect a client so we can query process state
	client := NewClient(WithSocketPath(sockPath))
	require.NoError(t, client.Connect())
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	startTime := time.Now()
	result := daemon.RunAutostart(ctx, tmpDir)

	elapsed := time.Since(startTime)
	t.Logf("RunAutostart completed in %v", elapsed)
	t.Logf("RunAutostart result: scripts=%v errors=%v", result.Scripts, result.Errors)

	// RunAutostart should have started both scripts
	assert.Len(t, result.Scripts, 2, "expected 2 scripts started")
	assert.Empty(t, result.Errors, "expected no errors")

	// The server sleeps 1s before printing the URL. The client depends on server.
	// So RunAutostart should take at least ~1 second (waiting for server URL).
	assert.GreaterOrEqual(t, elapsed.Seconds(), 0.8,
		"expected RunAutostart to take at least ~1s waiting for server URL")

	// Verify both processes are running
	serverID := makeProcessID(tmpDir, "server")
	clientID := makeProcessID(tmpDir, "client")

	serverStatus := waitForProcessState(t, client, serverID, "running", 3*time.Second)
	t.Logf("Server status: %v", serverStatus)

	clientStatus := waitForProcessState(t, client, clientID, "running", 3*time.Second)
	t.Logf("Client status: %v", clientStatus)

	// Verify ordering via runtime difference. Since both processes are still
	// running, the server (started first) should have a longer runtime than the
	// client (started after server URL was detected). The gap should be at least
	// ~1s (the server's sleep before printing URL).
	serverRuntimeMs, ok1 := serverStatus["runtime_ms"].(float64)
	clientRuntimeMs, ok2 := clientStatus["runtime_ms"].(float64)
	if ok1 && ok2 {
		gapMs := serverRuntimeMs - clientRuntimeMs
		t.Logf("Server runtime: %.0fms, Client runtime: %.0fms, Gap: %.0fms", serverRuntimeMs, clientRuntimeMs, gapMs)
		assert.GreaterOrEqual(t, gapMs, 800.0,
			"server should have been running at least ~1s longer than client")
	} else {
		t.Logf("Could not extract runtime_ms: server=%v client=%v", serverStatus["runtime_ms"], clientStatus["runtime_ms"])
	}

	// Clean up processes
	_, _ = client.ProcStop(serverID, false)
	_, _ = client.ProcStop(clientID, false)
}

// TestDepsIntegration_TimeoutDependencyNeverReady verifies that when a
// dependency never becomes ready (no URL output), the dependent script starts
// anyway after the timeout expires.
func TestDepsIntegration_TimeoutDependencyNeverReady(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Create .agnt.kdl:
	// - "server" sleeps for 60s (never prints a URL within timeout)
	// - "client" depends on "server" with a 2s timeout, then stays alive
	configContent := `
scripts {
    server {
        run "sleep 60"
        autostart true
    }
    client {
        run "echo client-started && sleep 60"
        autostart true
        depends-on "server" timeout=2
    }
}
`
	configPath := filepath.Join(tmpDir, ".agnt.kdl")
	require.NoError(t, writeFile(configPath, configContent))

	daemon := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	require.NoError(t, daemon.Start())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		daemon.Stop(ctx)
	}()

	client := NewClient(WithSocketPath(sockPath))
	require.NoError(t, client.Connect())
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	startTime := time.Now()
	result := daemon.RunAutostart(ctx, tmpDir)
	elapsed := time.Since(startTime)

	t.Logf("RunAutostart completed in %v", elapsed)
	t.Logf("RunAutostart result: scripts=%v errors=%v", result.Scripts, result.Errors)

	// Both scripts should still start (timeout = warning, not error)
	assert.Len(t, result.Scripts, 2, "expected 2 scripts started")
	assert.Empty(t, result.Errors, "expected no hard errors (timeout is a warning)")

	// The timeout is 2s, so RunAutostart should take at least ~2s
	assert.GreaterOrEqual(t, elapsed.Seconds(), 1.8,
		"expected RunAutostart to wait at least ~2s for timeout")

	// But it should NOT take 30s (which would mean it waited for sleep to finish)
	assert.Less(t, elapsed.Seconds(), 10.0,
		"expected RunAutostart to complete well before 60s sleep finishes")

	// Verify both processes are running
	serverID := makeProcessID(tmpDir, "server")
	clientID := makeProcessID(tmpDir, "client")

	waitForProcessState(t, client, serverID, "running", 3*time.Second)
	waitForProcessState(t, client, clientID, "running", 3*time.Second)

	// Clean up processes
	_, _ = client.ProcStop(serverID, false)
	_, _ = client.ProcStop(clientID, false)
}
