package overlay

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// fakeProxyController records what a palette command asked the daemon to do.
// Only the methods these commands use are meaningful; the rest satisfy the
// interface.
type fakeProxyController struct {
	projectPath string

	tunnelProvider string
	tunnelProxyID  string
	tunnelPort     int
	tunnelURL      string
	tunnelErr      error

	reconciled bool
	reconcile  error
}

func (f *fakeProxyController) StopScript(string) error    { return nil }
func (f *fakeProxyController) RestartScript(string) error { return nil }
func (f *fakeProxyController) StartScript(string) error   { return nil }
func (f *fakeProxyController) RunCommand(string) error    { return nil }
func (f *fakeProxyController) KillPort(int) error         { return nil }
func (f *fakeProxyController) CleanOrphans() error        { return nil }
func (f *fakeProxyController) RestartProxy(string) error  { return nil }
func (f *fakeProxyController) StopProxy(string) error     { return nil }
func (f *fakeProxyController) StopTunnel(string) error    { return nil }
func (f *fakeProxyController) ProjectPath() string        { return f.projectPath }

func (f *fakeProxyController) StartTunnel(provider, proxyID string, localPort int) (string, error) {
	f.tunnelProvider, f.tunnelProxyID, f.tunnelPort = provider, proxyID, localPort
	return f.tunnelURL, f.tunnelErr
}

func (f *fakeProxyController) ReconcileConfig() error {
	f.reconciled = true
	return f.reconcile
}

func routerWithProxies(t *testing.T, ctrl ScriptController, proxies ...ProxyInfo) *InputRouter {
	t.Helper()
	o := &Overlay{}
	o.UpdateStatus(Status{Proxies: proxies})
	return &InputRouter{overlay: o, scriptController: ctrl}
}

func TestResolveProxy(t *testing.T) {
	one := []ProxyInfo{{ID: "dev"}}
	two := []ProxyInfo{{ID: "api"}, {ID: "web"}}

	if got, err := resolveProxy(one, ""); err != nil || got.ID != "dev" {
		t.Errorf("single proxy with no id: got %q, %v", got.ID, err)
	}
	if got, err := resolveProxy(two, "web"); err != nil || got.ID != "web" {
		t.Errorf("named proxy: got %q, %v", got.ID, err)
	}

	// An ambiguous or missing target must name the candidates. A command that
	// says only "no" forces the developer into a second lookup for information
	// the overlay already has.
	_, err := resolveProxy(two, "")
	if err == nil {
		t.Fatal("two proxies with no id was accepted")
	}
	for _, id := range []string{"api", "web"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("ambiguity error does not name %q: %v", id, err)
		}
	}
	_, err = resolveProxy(two, "nope")
	if err == nil || !strings.Contains(err.Error(), "web") {
		t.Errorf("unknown id error should list the running proxies: %v", err)
	}
	if _, err := resolveProxy(nil, ""); err == nil {
		t.Error("no proxies at all was accepted")
	}
}

func TestResolveProxyLocalNames(t *testing.T) {
	proxies := []ProxyInfo{
		{ID: "project-1234:api", ConfigName: "api"},
		{ID: "project-1234:web:localhost-5173", ConfigName: "web"},
	}
	for _, name := range []string{"web", "web:localhost-5173", proxies[1].ID} {
		got, err := resolveProxy(proxies, name)
		if err != nil || got.ID != proxies[1].ID {
			t.Fatalf("resolve %q: got %+v, %v", name, got, err)
		}
	}
	proxies = append(proxies, ProxyInfo{ID: "project-1234:web:localhost-5174", ConfigName: "web"})
	if _, err := resolveProxy(proxies, "web"); err == nil || !strings.Contains(err.Error(), proxies[2].ID) {
		t.Fatalf("ambiguous config name must list choices: %v", err)
	}
	if got, err := resolveProxy(proxies, proxies[1].ID); err != nil || got.ID != proxies[1].ID {
		t.Fatalf("full ID must disambiguate: %+v, %v", got, err)
	}
}

func TestTailscaleCommandSavesConfigName(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(fmt.Sprintf("named=%t", named), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, config.AgntConfigFileName)
			if err := os.WriteFile(path, []byte("proxies {\n    web {\n        port 5173\n    }\n}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var dto proxyDTO
			if !decodeResult(map[string]interface{}{
				"id": "project-1234:web", "config_name": "web", "listen_addr": "127.0.0.1:19191",
			}, &dto) {
				t.Fatal("decode proxy list entry")
			}
			web := dto.toInfo()
			web.TailscaleURL = "http://box.tail1234.ts.net:19191"
			proxies := []ProxyInfo{web}
			name := ""
			if named {
				name = "web"
				proxies = append(proxies, ProxyInfo{ID: "project-1234:api", ConfigName: "api"})
			}
			ctrl := &fakeProxyController{projectPath: dir}
			if err := routerWithProxies(t, ctrl, proxies...).runTailscaleCommand(name); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadAgntConfigFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Proxies) != 1 || cfg.Proxies["web"].StatusURL != web.TailscaleURL || cfg.Proxies["web"].Port != 5173 {
				t.Fatalf("pin must update the original web node: %+v", cfg.Proxies)
			}
			if !ctrl.reconciled {
				t.Fatal("pin did not request a live update")
			}
			var buf bytes.Buffer
			web.StatusURL = cfg.Proxies["web"].StatusURL
			NewRenderer(&buf, 200, 24).DrawIndicator(Status{DaemonConnected: ConnectionConnected, Proxies: []ProxyInfo{web}})
			if !strings.Contains(buf.String(), web.StatusURL) || strings.Contains(buf.String(), "localhost") {
				t.Fatalf("status bar did not display the saved URL: %q", buf.String())
			}
		})
	}
}

func TestListenPortOf(t *testing.T) {
	cases := []struct {
		addr string
		want int
		ok   bool
	}{
		{"127.0.0.1:19191", 19191, true},
		{"[::]:8080", 8080, true},
		{"", 0, false},
		{"127.0.0.1:", 0, false},
	}
	for _, c := range cases {
		got, err := listenPortOf(ProxyInfo{ID: "dev", ListenAddr: c.addr})
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("listenPortOf(%q) = %d, %v; want %d", c.addr, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("listenPortOf(%q) accepted an unusable address", c.addr)
		}
	}
}

// TestTunnelCommand_UsesProxyIDAndPort pins the binding that makes the URL
// appear next to its proxy: PROXY LIST attaches tunnel_url by matching the
// tunnel's id to the proxy's, and the tunnel must front the PROXY's port, not
// the backend's — tunnelling to the backend would bypass the instrumentation.
func TestTunnelCommand_UsesProxyIDAndPort(t *testing.T) {
	ctrl := &fakeProxyController{tunnelURL: "https://x.trycloudflare.com"}
	r := routerWithProxies(t, ctrl, ProxyInfo{ID: "dev", ListenAddr: "127.0.0.1:19191", TargetURL: "http://localhost:5173"})

	if err := r.runTunnelCommand("cloudflare"); err != nil {
		t.Fatalf("runTunnelCommand: %v", err)
	}
	if ctrl.tunnelProxyID != "dev" {
		t.Errorf("tunnel id = %q, want the proxy id %q", ctrl.tunnelProxyID, "dev")
	}
	if ctrl.tunnelPort != 19191 {
		t.Errorf("tunnel port = %d, want the proxy's listen port 19191", ctrl.tunnelPort)
	}
	if ctrl.tunnelProvider != "cloudflare" {
		t.Errorf("provider = %q", ctrl.tunnelProvider)
	}
}

func TestTunnelCommand_RejectsBadProviderWithoutCallingDaemon(t *testing.T) {
	ctrl := &fakeProxyController{}
	r := routerWithProxies(t, ctrl, ProxyInfo{ID: "dev", ListenAddr: "127.0.0.1:19191"})

	err := r.runTunnelCommand("tailscal")
	if err == nil {
		t.Fatal("typo'd provider was accepted")
	}
	for _, p := range tunnelProviders {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("error does not offer %q: %v", p, err)
		}
	}
	if ctrl.tunnelProvider != "" {
		t.Error("a rejected provider still reached the daemon")
	}
}

func TestTunnelCommand_EmptyURLIsAFailure(t *testing.T) {
	// The daemon answers only once it has a URL, so an empty one means the
	// provider published nothing. Reporting success would hand the developer a
	// tunnel they cannot reach.
	ctrl := &fakeProxyController{tunnelURL: ""}
	r := routerWithProxies(t, ctrl, ProxyInfo{ID: "dev", ListenAddr: "127.0.0.1:19191"})

	if err := r.runTunnelCommand("ngrok"); err == nil {
		t.Fatal("a tunnel with no URL was reported as success")
	}
}

func TestTailscaleCommand_WritesConfigAndAppliesIt(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, config.AgntConfigFileName)
	if err := os.WriteFile(configPath, []byte("proxies {\n    dev {\n        url \"http://localhost:5173\"\n    }\n}\n"), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	ctrl := &fakeProxyController{projectPath: dir}
	r := routerWithProxies(t, ctrl, ProxyInfo{
		ID:           "dev",
		ConfigName:   "dev",
		ListenAddr:   "127.0.0.1:19191",
		TailscaleURL: "http://box.tail1234.ts.net:19191",
	})

	if err := r.runTailscaleCommand(""); err != nil {
		t.Fatalf("runTailscaleCommand: %v", err)
	}

	cfg, err := config.LoadAgntConfigFile(configPath)
	if err != nil {
		t.Fatalf("config no longer parses: %v", err)
	}
	if got := cfg.Proxies["dev"].StatusURL; got != "http://box.tail1234.ts.net:19191" {
		t.Errorf("status-url = %q, want the tailnet address", got)
	}
	// Persisted AND applied: writing only the file would leave the overlay
	// unchanged until the next restart.
	if !ctrl.reconciled {
		t.Error("config was written but never applied live")
	}
}

func TestTailscaleCommand_NoTailnetAddressIsLoud(t *testing.T) {
	ctrl := &fakeProxyController{projectPath: t.TempDir()}
	r := routerWithProxies(t, ctrl, ProxyInfo{ID: "dev", ConfigName: "dev", ListenAddr: "127.0.0.1:19191"})

	err := r.runTailscaleCommand("")
	if err == nil {
		t.Fatal("pinning an empty address was accepted")
	}
	if !strings.Contains(err.Error(), "tailscale") {
		t.Errorf("error should say what is missing: %v", err)
	}
}

// TestTailscaleCommand_ReportsWhichHalfFailed: the file is written before
// the live apply, so a reconcile failure must not read as "nothing happened".
func TestTailscaleCommand_ReportsWhichHalfFailed(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, config.AgntConfigFileName)
	if err := os.WriteFile(configPath, []byte("proxies {\n    dev {\n        url \"http://localhost:5173\"\n    }\n}\n"), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	ctrl := &fakeProxyController{projectPath: dir, reconcile: fmt.Errorf("daemon is gone")}
	r := routerWithProxies(t, ctrl, ProxyInfo{ID: "dev", ConfigName: "dev", TailscaleURL: "http://box.ts.net:19191"})

	err := r.runTailscaleCommand("")
	if err == nil {
		t.Fatal("a failed live-apply was reported as success")
	}
	if !strings.Contains(err.Error(), "wrote") {
		t.Errorf("error hides that the file was already written: %v", err)
	}
	cfg, cerr := config.LoadAgntConfigFile(configPath)
	if cerr != nil || cfg.Proxies["dev"].StatusURL == "" {
		t.Error("the write half should have survived the apply failure")
	}
}

// The point of the command is the rebind: pinning the address without binding
// there hands the developer a URL no other device can reach.
func TestTailscaleCommand_BindsTheProxyToTheTailnet(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, config.AgntConfigFileName)
	if err := os.WriteFile(configPath, []byte("proxies {\n    dev {\n        url \"http://localhost:5173\"\n    }\n}\n"), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	ctrl := &fakeProxyController{projectPath: dir}
	r := routerWithProxies(t, ctrl, ProxyInfo{
		ID:           "dev",
		ConfigName:   "dev",
		ListenAddr:   "127.0.0.1:19191",
		TailscaleURL: "http://box.tail1234.ts.net:19191",
	})

	if err := r.runTailscaleCommand(""); err != nil {
		t.Fatalf("runTailscaleCommand: %v", err)
	}

	cfg, err := config.LoadAgntConfigFile(configPath)
	if err != nil {
		t.Fatalf("config no longer parses: %v", err)
	}
	if got := cfg.Proxies["dev"].Bind; got != proxy.BindTailscale {
		t.Errorf("bind = %q, want %q — without it the proxy stays on loopback", got, proxy.BindTailscale)
	}
}

// A proxy started by hand has no .agnt.kdl node, so a config reconcile has
// nothing to rebind. Writing the keys anyway would look like it worked.
func TestTailscaleCommand_RefusesAProxyThatIsNotInTheConfig(t *testing.T) {
	dir := t.TempDir()
	ctrl := &fakeProxyController{projectPath: dir}
	r := routerWithProxies(t, ctrl, ProxyInfo{
		ID:           "manual",
		ListenAddr:   "127.0.0.1:19191",
		TailscaleURL: "http://box.tail1234.ts.net:19191",
	})

	err := r.runTailscaleCommand("")
	if err == nil {
		t.Fatal("a proxy with no config node was accepted")
	}
	if ctrl.reconciled {
		t.Error("a refused command still asked the daemon to reconcile")
	}
	if _, statErr := os.Stat(filepath.Join(dir, config.AgntConfigFileName)); statErr == nil {
		t.Error("a refused command still wrote a config file")
	}
}
