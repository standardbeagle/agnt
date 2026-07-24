//go:build unix

package daemon

import (
	"context"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/scope"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startSleeper registers a long-lived managed process under projectPath so the
// lifecycle handlers have a real process to (not) stop. No t.Parallel in any
// caller: these start real OS processes (AGENTS.md § Testing).
func startSleeper(t *testing.T, d *Daemon, id, projectPath string) *goprocess.ManagedProcess {
	t.Helper()
	proc, err := d.hub.ProcessManager().StartCommand(context.Background(), goprocess.ProcessConfig{
		ID:          id,
		ProjectPath: projectPath,
		Command:     "/bin/sh",
		Args:        []string{"-c", "sleep 30"},
	})
	require.NoError(t, err)
	return proc
}

func createProxy(t *testing.T, d *Daemon, id, projectPath string) *proxy.ProxyServer {
	t.Helper()
	p, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{
		ID:         id,
		TargetURL:  ephemeralTargetURL(t),
		ListenPort: -1,
		Path:       projectPath,
	})
	require.NoError(t, err)
	return p
}

// TestStopAll_ScopedToBoundSession_LeavesOtherProjectIntact is the core tenancy
// guard: STOP-ALL issued on a connection bound to project A must stop only A's
// processes and proxies, must leave project B's resources running, and must NOT
// clear the daemon-wide overlay endpoint. Before the scope split, the handler
// called StopAllResources unconditionally — killing B and blasting the overlay.
func TestStopAll_ScopedToBoundSession_LeavesOtherProjectIntact(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, c, _ := newBootedDaemonWithClient(t)

	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))

	// A is bound to THIS client connection (conn.SessionCode() == "sess-a").
	_, err := c.SessionRegister("sess-a", "http://127.0.0.1:19191", dirA, "test", nil)
	require.NoError(t, err)
	// B lives only in the registry (a different session on another connection).
	activeSession(t, d.sessionRegistry, "sess-b", dirB, "http://127.0.0.1:29292")

	// A shared daemon-wide overlay endpoint that a scoped stop must not clear.
	d.SetOverlayEndpoint("http://127.0.0.1:19191")

	procA := startSleeper(t, d, "proc-a", dirA)
	procB := startSleeper(t, d, "proc-b", dirB)
	proxyA := createProxy(t, d, "proxy-a", dirA)
	proxyB := createProxy(t, d, "proxy-b", dirB)
	_ = proxyA

	resp, err := c.StopAll()
	require.NoError(t, err)

	// Counts reflect only A's resources.
	assert.Equal(t, float64(1), resp["processes_stopped"], "STOP-ALL must count only bound project's processes")
	assert.Equal(t, float64(1), resp["proxies_stopped"], "STOP-ALL must count only bound project's proxies")

	// A stopped, B untouched.
	assert.False(t, procA.IsRunning(), "project A process must be stopped")
	assert.True(t, procB.IsRunning(), "project B process must survive A's STOP-ALL")

	assert.Empty(t, d.proxym.ListScoped(scope.Project(dirA)), "project A proxy must be stopped and removed")
	if bProxies := d.proxym.ListScoped(scope.Project(dirB)); assert.Len(t, bProxies, 1, "project B proxy must survive") {
		assert.Equal(t, "proxy-b", bProxies[0].ID)
		assert.True(t, bProxies[0].IsRunning())
	}
	assert.True(t, proxyB.IsRunning())

	// Overlay endpoint must be preserved — the cross-project leak was clearing it.
	assert.Equal(t, "http://127.0.0.1:19191", d.OverlayEndpoint(),
		"a project-scoped STOP-ALL must not clear the daemon-wide overlay endpoint")
}

// TestStopAll_UnattachedCallerFailsLoud verifies an unresolvable non-global
// STOP-ALL is rejected rather than silently stopping every project's resources.
func TestStopAll_UnattachedCallerFailsLoud(t *testing.T) {
	t.Parallel()
	c := newBootedClient(t)

	_, err := c.StopAll()
	require.Error(t, err, "unattached, non-global STOP-ALL must fail loud")
}

// TestStopAll_ExplicitGlobalStopsEverythingAndClearsOverlay covers the
// deliberate global path: global:true stops every project's resources and
// clears the daemon-wide overlay endpoint, exactly as the pre-split behavior.
func TestStopAll_ExplicitGlobalStopsEverythingAndClearsOverlay(t *testing.T) {
	// No t.Parallel: starts real processes.
	d, c, _ := newBootedDaemonWithClient(t)

	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))

	_, err := c.SessionRegister("sess-a", "http://127.0.0.1:19191", dirA, "test", nil)
	require.NoError(t, err)
	activeSession(t, d.sessionRegistry, "sess-b", dirB, "http://127.0.0.1:29292")
	d.SetOverlayEndpoint("http://127.0.0.1:19191")

	procA := startSleeper(t, d, "proc-a", dirA)
	procB := startSleeper(t, d, "proc-b", dirB)
	createProxy(t, d, "proxy-a", dirA)
	createProxy(t, d, "proxy-b", dirB)

	resp, err := c.Conn().Request("STOP-ALL").WithJSON(map[string]interface{}{"global": true}).JSON()
	require.NoError(t, err)

	assert.Equal(t, float64(2), resp["processes_stopped"])
	assert.Equal(t, float64(2), resp["proxies_stopped"])

	assert.False(t, procA.IsRunning())
	assert.False(t, procB.IsRunning())
	assert.Empty(t, d.proxym.ListScoped(scope.Project(dirA)))
	assert.Empty(t, d.proxym.ListScoped(scope.Project(dirB)))
	assert.Equal(t, "", d.OverlayEndpoint(), "explicit global STOP-ALL clears the daemon-wide overlay endpoint")
}

// TestRestartAll_ScopedToBoundSession_RestartsOnlyBoundProject verifies
// RESTART-ALL snapshots and restarts only the bound project's proxies; the
// other project's proxy is neither counted nor touched.
func TestRestartAll_ScopedToBoundSession_RestartsOnlyBoundProject(t *testing.T) {
	// No t.Parallel: touches shared daemon process/proxy state.
	d, c, _ := newBootedDaemonWithClient(t)

	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))

	_, err := c.SessionRegister("sess-a", "http://127.0.0.1:19191", dirA, "test", nil)
	require.NoError(t, err)
	activeSession(t, d.sessionRegistry, "sess-b", dirB, "http://127.0.0.1:29292")

	createProxy(t, d, "proxy-a", dirA)
	proxyB := createProxy(t, d, "proxy-b", dirB)

	resp, err := c.RestartAll()
	require.NoError(t, err)

	assert.Equal(t, float64(1), resp["proxies_restarted"], "RESTART-ALL must restart only the bound project's proxies")
	assert.Equal(t, float64(0), resp["proxies_failed"])

	// B's proxy is the SAME untouched instance; A's has been recreated.
	if bProxies := d.proxym.ListScoped(scope.Project(dirB)); assert.Len(t, bProxies, 1) {
		assert.Same(t, proxyB, bProxies[0], "project B proxy must not be restarted by A's RESTART-ALL")
	}
	if aProxies := d.proxym.ListScoped(scope.Project(dirA)); assert.Len(t, aProxies, 1) {
		assert.Equal(t, "proxy-a", aProxies[0].ID)
		assert.True(t, aProxies[0].IsRunning())
	}
}
