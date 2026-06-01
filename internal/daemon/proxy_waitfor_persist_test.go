//go:build unix

package daemon

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// TestProxyWaitFor_ReadyPersistsPastAutostart is the regression guard for the
// lost-wakeup race: a script signals ready DURING autostart, autostart
// completes (cancelling its context and previously running a ready-signal
// cleanup loop), and only THEN does the async proxy-creation path register a
// readiness gate on that script. The ready fact must survive autostart
// completion or the gate hangs forever on a 503 `agnt_proxy_not_ready`.
//
// `dev-lib` has no port → setupReadinessSignal fires SignalReady the instant
// the process starts. The proxy registers afterwards, mirroring the real
// timeline where Vite's URL detection event lands after the backend/lib layer.
func TestProxyWaitFor_ReadyPersistsPastAutostart(t *testing.T) {
	clusterDir := t.TempDir()
	d := newDaemon(t, clusterDir)

	dir := t.TempDir()
	writeConfig(t, dir, `
scripts {
    dev-lib {
        run "sleep 60"
        autostart true
    }
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := d.RunAutostartAsync(ctx, dir, nil)
	require.Empty(t, result.Errors, "autostart should start cleanly: %v", result.Errors)
	require.Contains(t, result.Scripts, "dev-lib")

	// Autostart has now returned: its internal context is cancelled. Simulate
	// the async URL-detection event that creates the proxy late, gated on
	// dev-lib. registerProxyDependencies must observe dev-lib's already-fired
	// ready signal and open the gate.
	projectPath := normalizePath(dir)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	created, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         "late-proxy",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 50,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "late-proxy") })

	proxyCfg := &config.ProxyConfig{Script: "dev-frontend", WaitFor: []string{"dev-lib"}}
	d.registerProxyDependencies(created, "late-proxy", proxyCfg, projectPath)

	require.Eventually(t, func() bool {
		return created.IsReadyForForwarding()
	}, 3*time.Second, 20*time.Millisecond,
		"gate must open: dev-lib readiness signaled during autostart must persist past autostart completion")
	assert.Empty(t, created.PendingDependencies())
}

// TestProxyWaitFor_PortProbeSurvivesAutostart guards the second half of the
// fix: a proxy-only dependency (a backend nothing else depends-on) whose port
// is not yet bound when autostart completes. The port probe must keep running
// on the daemon context, not the autostart context that is cancelled the
// moment autostart returns — otherwise the dep never signals and the gate
// hangs.
func TestProxyWaitFor_PortProbeSurvivesAutostart(t *testing.T) {
	clusterDir := t.TempDir()
	d := newDaemon(t, clusterDir)

	// Reserve a port, then free it so the script's probe target is initially
	// unbound. We bind it ourselves AFTER autostart completes.
	probePort := ephemeralPort(t)

	dir := t.TempDir()
	writeConfig(t, dir, `
scripts {
    backend {
        run "sleep 60"
        autostart true
        ports `+strconv.Itoa(probePort)+`
    }
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := d.RunAutostartAsync(ctx, dir, nil)
	require.Empty(t, result.Errors, "autostart should start cleanly: %v", result.Errors)
	require.Contains(t, result.Scripts, "backend")

	// Autostart returned; its ctx is cancelled. The probe must still be alive
	// on the daemon ctx. Register the proxy gate, then bind the port.
	projectPath := normalizePath(dir)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	created, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         "gated-proxy",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 50,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.proxym.Stop(context.Background(), "gated-proxy") })

	proxyCfg := &config.ProxyConfig{Script: "dev-frontend", WaitFor: []string{"backend"}}
	d.registerProxyDependencies(created, "gated-proxy", proxyCfg, projectPath)
	require.False(t, created.IsReadyForForwarding(), "gate should start closed: port not bound yet")

	// Bind the probe port now — the surviving probe must detect it and open
	// the gate.
	ln, err := net.Listen("tcp", "localhost:"+strconv.Itoa(probePort))
	require.NoError(t, err, "failed to bind probe port %d", probePort)
	defer ln.Close()

	require.Eventually(t, func() bool {
		return created.IsReadyForForwarding()
	}, 4*time.Second, 50*time.Millisecond,
		"gate must open: port probe must survive autostart completion and detect the late bind")
}

// TestHandleScriptStopped_CleansReadyState verifies the teardown counterpart:
// when a script stops, its readiness fact is cleared so the ready state's
// lifecycle matches the process lifecycle (and does not leak across restarts).
func TestHandleScriptStopped_CleansReadyState(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(t)
	d.startupErrorStore = NewStartupLogStore(50)

	projectPath := "/tmp/project"
	procID := makeProcessID(projectPath, "backend")

	// Signal ready, then confirm a waiter sees it immediately.
	d.readySignaler.SignalReady(procID)
	require.NoError(t, d.readySignaler.WaitReady(procID, 200*time.Millisecond),
		"ready signal should be observable before script stop")

	// Script stops → ready state must be cleared.
	d.handleScriptStopped(ProxyEvent{Type: ScriptStopped, ScriptID: procID})

	// A fresh wait must now time out: the latched closed channel is gone, so
	// the process is treated as not-yet-ready again (a restart re-arms it).
	err := d.readySignaler.WaitReady(procID, 200*time.Millisecond)
	require.Error(t, err, "ready state must be cleared after script stop")
}
