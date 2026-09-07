package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/require"
)

func TestReconcileProxyOnlyChanges(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	dir := t.TempDir()
	path := filepath.Join(dir, config.AgntConfigFileName)
	write := func(target, status string) {
		t.Helper()
		require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("proxies {\n web {\n url %q\n status-url %q\n }\n}\n", target, status)), 0600))
	}
	ctx := context.Background()
	id := makeProcessID(dir, "web")
	write("http://127.0.0.1:5173", "https://old.example")
	plan, err := d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.Equal(t, []string{"web"}, plan.StartProxies)
	var first *proxy.ProxyServer
	require.Eventually(t, func() bool {
		first, err = d.proxym.Get(id)
		_, recorded := d.proxyConfigs.Load(id)
		return err == nil && recorded
	}, 3*time.Second, 10*time.Millisecond)

	write("http://127.0.0.1:5173", "https://new.example")
	plan, err = d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.True(t, plan.IsEmpty(), "display-only changes must not restart the proxy")
	require.Equal(t, "https://new.example", first.GetStatusURL())
	current, err := d.proxym.Get(id)
	require.NoError(t, err)
	require.Same(t, first, current)

	write("http://127.0.0.1:5174", "https://new.example")
	plan, err = d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.Equal(t, []string{"web"}, plan.RestartProxies)
	require.Eventually(t, func() bool {
		current, err = d.proxym.Get(id)
		_, recorded := d.proxyConfigs.Load(id)
		return err == nil && recorded && current != first && current.TargetURL.String() == "http://127.0.0.1:5174"
	}, 3*time.Second, 10*time.Millisecond)

	manual, err := d.proxym.Create(ctx, proxy.ProxyConfig{ID: "manual", Path: dir, TargetURL: "http://127.0.0.1:5175"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("proxies {\n}\n"), 0600))
	plan, err = d.ReconcileProjectConfig(ctx, dir)
	require.NoError(t, err)
	require.Equal(t, []string{"web"}, plan.StopProxies)
	_, err = d.proxym.Get(id)
	require.ErrorIs(t, err, proxy.ErrProxyNotFound)
	current, err = d.proxym.Get("manual")
	require.NoError(t, err)
	require.Same(t, manual, current)
}

func TestApplyProxyDisplayConfigUsesConfigName(t *testing.T) {
	d := NewForTest(t, DaemonConfig{})
	dir := t.TempDir()
	id := makeProxyIDFromURL(dir, "web", "http://127.0.0.1:5173")
	p, err := d.proxym.Create(context.Background(), proxy.ProxyConfig{ID: id, Path: dir, TargetURL: "http://127.0.0.1:5173"})
	require.NoError(t, err)
	d.proxyConfigs.Store(id, configuredProxy{name: "web"})
	cfg := config.DefaultAgntConfig()
	cfg.Proxies["web"] = &config.ProxyConfig{StatusURL: "https://display.example"}
	d.applyProxyDisplayConfig(dir, cfg)
	require.Equal(t, "https://display.example", p.GetStatusURL())
	cfg.Proxies["web"].StatusURL = ""
	d.applyProxyDisplayConfig(dir, cfg)
	require.Empty(t, p.GetStatusURL())
}

func TestRestoreConfiguredProxyUsesCurrentConfig(t *testing.T) {
	dir := t.TempDir()
	d := NewForTest(t, DaemonConfig{EnableStatePersistence: true, StatePath: filepath.Join(t.TempDir(), "state.json")})
	path := filepath.Join(dir, config.AgntConfigFileName)
	require.NoError(t, os.WriteFile(path, []byte("proxies {\n web {\n url \"http://127.0.0.1:5174\"\n status-url \"https://current.example\"\n }\n}\n"), 0600))
	id := makeProcessID(dir, "web")
	d.stateMgr.AddProxy(PersistentProxyConfig{ID: id, Path: dir, TargetURL: "http://127.0.0.1:5173", ConfigName: "web"})
	d.restoreProxies()
	p, err := d.proxym.Get(id)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:5174", p.TargetURL.String())
	require.Equal(t, "https://current.example", p.GetStatusURL())
	value, ok := d.proxyConfigs.Load(id)
	require.True(t, ok)
	require.Equal(t, "web", value.(configuredProxy).name)
	plan, err := d.ReconcileProjectConfig(context.Background(), dir)
	require.NoError(t, err)
	require.True(t, plan.IsEmpty())
}

func TestUntrackScriptProxyPreservesReaders(t *testing.T) {
	d := &Daemon{scriptProxies: make(map[string][]string)}
	d.trackScriptProxy("script", "first")
	d.trackScriptProxy("script", "second")
	d.trackScriptProxy("script", "third")
	snapshot := d.scriptProxies["script"]
	d.untrackScriptProxy("first")
	require.Equal(t, []string{"first", "second", "third"}, snapshot)
	require.Equal(t, []string{"second", "third"}, d.getProxiesForScript("script"))
	require.Empty(t, d.linkedScriptForProxy("first"))
}
