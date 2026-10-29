//go:build unix

package daemon

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRestartAll_RebuildsScriptFromDeclaredConfig pins that RESTART-ALL
// rebuilds a config-declared script from its full .agnt.kdl config (env,
// ports, auto-restart) rather than a narrow manifest carrying only
// id/command/args/project-path/working-dir. Before this fix, procManifest
// dropped Env entirely (env=nil) and never carried ports or auto-restart, so
// a restarted script silently lost its declared environment.
func TestRestartAll_RebuildsScriptFromDeclaredConfig(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))

	writeConfig(t, dir, `
scripts {
    web {
        run "`+stayAliveCmd()+`"
        autostart true
        env {
            AGNT_TEST_MARKER "restart-all-keeps-env"
        }
    }
}
`)
	_, err := c.SessionRegister("sess-restart-all", "http://127.0.0.1:19191", dir, "test", nil)
	require.NoError(t, err)

	ctx := context.Background()
	plan, err := d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.Equal(t, []string{"web"}, plan.StartScripts)

	processID := makeProcessID(dir, "web")
	require.Eventually(t, func() bool {
		p, err := d.hub.ProcessManager().Get(processID)
		return err == nil && p.State().String() == "running"
	}, 3*time.Second, 20*time.Millisecond, "web never reached running before restart")

	resp, err := c.RestartAll()
	require.NoError(t, err)
	require.Equal(t, float64(1), resp["processes_restarted"])

	require.Eventually(t, func() bool {
		p, err := d.hub.ProcessManager().Get(processID)
		if err != nil || p.State().String() != "running" {
			return false
		}
		for _, kv := range p.Env {
			if kv == "AGNT_TEST_MARKER=restart-all-keeps-env" {
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond, "restarted process must still carry its declared env var")
}

// TestRestartAll_RebuildsProxyFromDeclaredConfig pins that RESTART-ALL
// rebuilds a config-declared proxy via the same construction helper
// (buildProxyServerConfig) the autostart/explicit-start event path uses,
// instead of a narrow manifest that always passed ListenPort: 0 (ephemeral)
// and dropped skip-tls-verify/bind/public-status URLs/the cloudflare-tunnel
// block. Asserts the explicit listen-port survives a restart.
func TestRestartAll_RebuildsProxyFromDeclaredConfig(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)
	dir := normalizePath(shortTempDir(t))
	listenPort := ephemeralPort(t)

	writeConfig(t, dir, `
proxies {
    api {
        url "http://127.0.0.1:1"
        listen-port `+strconv.Itoa(listenPort)+`
    }
}
`)
	_, err := c.SessionRegister("sess-restart-all-proxy", "http://127.0.0.1:19191", dir, "test", nil)
	require.NoError(t, err)

	ctx := context.Background()
	plan, err := d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.Equal(t, []string{"api"}, plan.StartProxies)

	proxyID := makeProcessID(dir, "api")
	require.Eventually(t, func() bool {
		_, err := d.proxym.Get(proxyID)
		return err == nil
	}, 3*time.Second, 20*time.Millisecond, "api proxy never came up before restart")

	resp, err := c.RestartAll()
	require.NoError(t, err)
	require.Equal(t, float64(1), resp["proxies_restarted"])

	require.Eventually(t, func() bool {
		p, err := d.proxym.Get(proxyID)
		return err == nil && p.BoundPort() == listenPort
	}, 3*time.Second, 20*time.Millisecond, "restarted proxy must keep its declared listen-port")
}
