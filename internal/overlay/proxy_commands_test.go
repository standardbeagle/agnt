package overlay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
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

func TestTailscaleURLCommand_WritesConfigAndAppliesIt(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, config.AgntConfigFileName)
	if err := os.WriteFile(configPath, []byte("proxies {\n    dev {\n        url \"http://localhost:5173\"\n    }\n}\n"), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	ctrl := &fakeProxyController{projectPath: dir}
	r := routerWithProxies(t, ctrl, ProxyInfo{
		ID:           "dev",
		ListenAddr:   "127.0.0.1:19191",
		TailscaleURL: "http://box.tail1234.ts.net:19191",
	})

	if err := r.runTailscaleURLCommand(""); err != nil {
		t.Fatalf("runTailscaleURLCommand: %v", err)
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

func TestTailscaleURLCommand_NoTailnetAddressIsLoud(t *testing.T) {
	ctrl := &fakeProxyController{projectPath: t.TempDir()}
	r := routerWithProxies(t, ctrl, ProxyInfo{ID: "dev", ListenAddr: "127.0.0.1:19191"})

	err := r.runTailscaleURLCommand("")
	if err == nil {
		t.Fatal("pinning an empty address was accepted")
	}
	if !strings.Contains(err.Error(), "tailscale") {
		t.Errorf("error should say what is missing: %v", err)
	}
}

// TestTailscaleURLCommand_ReportsWhichHalfFailed: the file is written before
// the live apply, so a reconcile failure must not read as "nothing happened".
func TestTailscaleURLCommand_ReportsWhichHalfFailed(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, config.AgntConfigFileName)
	if err := os.WriteFile(configPath, []byte("proxies {\n    dev {\n        url \"http://localhost:5173\"\n    }\n}\n"), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	ctrl := &fakeProxyController{projectPath: dir, reconcile: fmt.Errorf("daemon is gone")}
	r := routerWithProxies(t, ctrl, ProxyInfo{ID: "dev", TailscaleURL: "http://box.ts.net:19191"})

	err := r.runTailscaleURLCommand("")
	if err == nil {
		t.Fatal("a failed live-apply was reported as success")
	}
	if !strings.Contains(err.Error(), "wrote status-url") {
		t.Errorf("error hides that the file was already written: %v", err)
	}
	cfg, cerr := config.LoadAgntConfigFile(configPath)
	if cerr != nil || cfg.Proxies["dev"].StatusURL == "" {
		t.Error("the write half should have survived the apply failure")
	}
}
