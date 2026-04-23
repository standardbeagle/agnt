//go:build unix

package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDepsIntegration verifies dependency-wait behaviour with a shared daemon.
// Each subtest uses its own t.TempDir() for project-path isolation so script
// namespaces do not collide.
func TestDepsIntegration(t *testing.T) {
	tmpRoot := t.TempDir()
	sockPath := filepath.Join(tmpRoot, "test.sock")

	d := New(DaemonConfig{
		SocketPath:   sockPath,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})
	require.NoError(t, d.Start())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		d.Stop(ctx)
	})

	// client_starts_after_server_url: "server" prints a URL after 1 second,
	// then stays alive; "client" depends on "server" and must not start until
	// the URL is detected.
	t.Run("client_starts_after_server_url", func(t *testing.T) {
		tmpDir := t.TempDir()
		serverPort := ephemeralPort(t)
		writeConfig(t, tmpDir, fmt.Sprintf(`
scripts {
    server {
        run "sleep 1 && echo Listening on http://localhost:%d && sleep 60"
        autostart true
    }
    client {
        run "echo client-started && sleep 60"
        autostart true
        depends-on "server" timeout=10
    }
}
`, serverPort))

		client := NewClient(WithSocketPath(sockPath))
		require.NoError(t, client.Connect())
		defer client.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		startTime := time.Now()
		result := d.RunAutostart(ctx, tmpDir)
		elapsed := time.Since(startTime)

		t.Logf("RunAutostart completed in %v", elapsed)
		t.Logf("RunAutostart result: scripts=%v errors=%v", result.Scripts, result.Errors)

		assert.Len(t, result.Scripts, 2, "expected 2 scripts started")
		assert.Empty(t, result.Errors, "expected no errors")

		// The server sleeps 1s before printing the URL, so autostart must wait at
		// least ~1s for the dependency to be satisfied.
		assert.GreaterOrEqual(t, elapsed.Seconds(), 0.8,
			"expected RunAutostart to take at least ~1s waiting for server URL")

		serverID := makeProcessID(tmpDir, "server")
		clientID := makeProcessID(tmpDir, "client")

		serverStatus := waitForProcessState(t, client, serverID, "running", 3*time.Second)
		t.Logf("Server status: %v", serverStatus)

		clientStatus := waitForProcessState(t, client, clientID, "running", 3*time.Second)
		t.Logf("Client status: %v", clientStatus)

		// The server started first so its runtime must exceed the client's by at
		// least ~1s (the server's sleep before printing the URL).
		serverRuntimeMs, ok1 := serverStatus["runtime_ms"].(float64)
		clientRuntimeMs, ok2 := clientStatus["runtime_ms"].(float64)
		if ok1 && ok2 {
			gapMs := serverRuntimeMs - clientRuntimeMs
			t.Logf("Server runtime: %.0fms, Client runtime: %.0fms, Gap: %.0fms",
				serverRuntimeMs, clientRuntimeMs, gapMs)
			assert.GreaterOrEqual(t, gapMs, 800.0,
				"server should have been running at least ~1s longer than client")
		} else {
			t.Logf("Could not extract runtime_ms: server=%v client=%v",
				serverStatus["runtime_ms"], clientStatus["runtime_ms"])
		}

		_, _ = client.ProcStop(serverID, false)
		_, _ = client.ProcStop(clientID, false)
	})

	// timeout_dependency_never_ready: "server" sleeps 60s (never prints a URL
	// within the 2s timeout); "client" must still start after the timeout
	// expires — timeout is a warning, not a hard error.
	t.Run("timeout_dependency_never_ready", func(t *testing.T) {
		tmpDir := t.TempDir()
		writeConfig(t, tmpDir, `
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
`)

		client := NewClient(WithSocketPath(sockPath))
		require.NoError(t, client.Connect())
		defer client.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		startTime := time.Now()
		result := d.RunAutostart(ctx, tmpDir)
		elapsed := time.Since(startTime)

		t.Logf("RunAutostart completed in %v", elapsed)
		t.Logf("RunAutostart result: scripts=%v errors=%v", result.Scripts, result.Errors)

		assert.Len(t, result.Scripts, 2, "expected 2 scripts started")
		assert.Empty(t, result.Errors, "expected no hard errors (timeout is a warning)")

		// The 2s dep timeout must bound the wait.
		assert.GreaterOrEqual(t, elapsed.Seconds(), 1.8,
			"expected RunAutostart to wait at least ~2s for timeout")
		assert.Less(t, elapsed.Seconds(), 10.0,
			"expected RunAutostart to complete well before 60s sleep finishes")

		serverID := makeProcessID(tmpDir, "server")
		clientID := makeProcessID(tmpDir, "client")

		waitForProcessState(t, client, serverID, "running", 3*time.Second)
		waitForProcessState(t, client, clientID, "running", 3*time.Second)

		_, _ = client.ProcStop(serverID, false)
		_, _ = client.ProcStop(clientID, false)
	})
}
