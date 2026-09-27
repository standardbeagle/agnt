//go:build !windows

package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/tunnel"
)

// fakeCloudflared writes a stand-in cloudflared that records its argv, logs
// the named-tunnel registration line and then idles until killed. It is
// written by a child shell so no fork of this test process can hold the file
// open for write when it is exec'd (ETXTBSY).
func fakeCloudflared(t *testing.T) (bin, argvFile string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "cloudflared")
	argvFile = filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\n" +
		"echo 'INF Registered tunnel connection connIndex=0' >&2\nexec sleep 60\n"
	w := exec.Command("/bin/sh", "-c", `cat > "$0" && chmod 755 "$0"`, bin)
	w.Stdin = strings.NewReader(script)
	if out, err := w.CombinedOutput(); err != nil {
		t.Fatalf("writing fake cloudflared: %v: %s", err, out)
	}
	return bin, argvFile
}

// ingressURL returns the --url cloudflared was pointed at.
func ingressURL(t *testing.T, argvFile string) string {
	t.Helper()
	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("reading recorded argv: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i, a := range args {
		if a == "--url" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("cloudflared argv has no --url: %q", args)
	return ""
}

func startNamedTunnelProxy(t *testing.T, allowUnauth bool) (ps *ProxyServer, f *accessFixture, ingress string) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Seen-Proto", r.Header.Get("X-Forwarded-Proto"))
		io.WriteString(w, "backend-ok")
	}))
	t.Cleanup(backend.Close)

	bin, argvFile := fakeCloudflared(t)
	cred := filepath.Join(t.TempDir(), "tunnel.json")
	if err := os.WriteFile(cred, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &NamedTunnelConfig{
		Tunnel:     tunnel.NamedCloudflare{TunnelID: "tid-1", Hostname: "dev.example.com", CredentialsFile: cred},
		BinaryPath: bin,
	}
	if allowUnauth {
		cfg.AllowUnauthenticated = true
	} else {
		cfg.AccessTeamDomain, cfg.AccessAUD = testTeam, testAUD
	}
	ps, err := NewProxyServer(ProxyConfig{ID: "named-tunnel-proxy", TargetURL: backend.URL, MaxLogSize: 50, NamedTunnel: cfg})
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	f = newAccessFixture(t, "k1")
	if a := ps.namedTunnelSetup.access; a != nil {
		a.certsURL, a.client, a.now = f.access.certsURL, f.access.client, f.access.now
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	if err := ps.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { ps.Stop(context.Background()) })

	deadline := time.Now().Add(10 * time.Second)
	for !ps.IsTunnelRunning() {
		if time.Now().After(deadline) {
			t.Fatal("named tunnel never reported connected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ps, f, ingressURL(t, argvFile)
}

func get(t *testing.T, url, jwt string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url+"/api/thing", nil)
	req.Host = "dev.example.com"
	if jwt != "" {
		req.Header.Set(AccessJWTHeader, jwt)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestNamedTunnelIngressRequiresAccess(t *testing.T) {
	ps, f, ingress := startNamedTunnelProxy(t, false)

	if got := ps.TunnelURL(); got != "https://dev.example.com" {
		t.Fatalf("TunnelURL = %q", got)
	}
	if got := ps.GetPublicURL(); got != "https://dev.example.com" {
		t.Fatalf("public URL not bound to the tunnel hostname: %q", got)
	}
	if !strings.HasPrefix(ingress, "http://127.0.0.1:") || strings.HasSuffix(ingress, ":"+strings.Split(ps.ListenAddr, ":")[1]) {
		t.Fatalf("cloudflared must target a dedicated loopback ingress, got %q (proxy on %s)", ingress, ps.ListenAddr)
	}

	if code, body := get(t, ingress, ""); code != http.StatusForbidden || strings.Contains(body, "backend-ok") {
		t.Fatalf("ingress without token: %d %q, want 403 and no backend body", code, body)
	}
	bad := f.sign("k1", f.claims(map[string]any{"aud": "someone-else"}), nil)
	if code, _ := get(t, ingress, bad); code != http.StatusForbidden {
		t.Fatalf("ingress with wrong-audience token: %d, want 403", code)
	}
	good := f.sign("k1", f.claims(nil), nil)
	if code, body := get(t, ingress, good); code != http.StatusOK || body != "backend-ok" {
		t.Fatalf("ingress with valid token: %d %q, want 200 backend-ok", code, body)
	}
	// Local browsing on the proxy's own port is not behind Access.
	if code, body := get(t, "http://"+ps.ListenAddr, ""); code != http.StatusOK || body != "backend-ok" {
		t.Fatalf("local proxy port: %d %q, want 200 backend-ok", code, body)
	}

	ps.Stop(context.Background())
	if ps.TunnelURL() != "" || ps.IsTunnelRunning() {
		t.Fatal("tunnel still reported after Stop")
	}
	host := strings.TrimPrefix(ingress, "http://")
	if c, err := net.DialTimeout("tcp", host, time.Second); err == nil {
		c.Close()
		t.Fatalf("ingress %s still accepting after Stop", host)
	}
}

// The backend must see the scheme the browser used: https through the
// tunnel, http on the proxy's own port, whatever the client claims.
func TestNamedTunnelForwardedProto(t *testing.T) {
	ps, f, ingress := startNamedTunnelProxy(t, false)
	seen := func(url, jwt, claimed string) string {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, url+"/", nil)
		if jwt != "" {
			req.Header.Set(AccessJWTHeader, jwt)
		}
		if claimed != "" {
			req.Header.Set("X-Forwarded-Proto", claimed)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d", url, resp.StatusCode)
		}
		return resp.Header.Get("X-Seen-Proto")
	}
	if got := seen(ingress, f.sign("k1", f.claims(nil), nil), ""); got != "https" {
		t.Errorf("tunnelled request forwarded proto %q, want https", got)
	}
	if got := seen(ingress, f.sign("k1", f.claims(nil), nil), "http"); got != "https" {
		t.Errorf("tunnelled request with a client-set proto forwarded %q, want https", got)
	}
	if got := seen("http://"+ps.ListenAddr, "", ""); got != "http" {
		t.Errorf("local request forwarded proto %q, want http", got)
	}
	if got := seen("http://"+ps.ListenAddr, "", "https"); got != "http" {
		t.Errorf("local request claiming https forwarded %q, want http", got)
	}
}

func TestNamedTunnelAllowUnauthenticated(t *testing.T) {
	ps, _, ingress := startNamedTunnelProxy(t, true)
	if ps.namedTunnelSetup.access != nil {
		t.Fatal("premise: allow-unauthenticated must not build a verifier")
	}
	if code, body := get(t, ingress, ""); code != http.StatusOK || body != "backend-ok" {
		t.Fatalf("unauthenticated ingress: %d %q, want 200", code, body)
	}
	if cfg := ps.NamedTunnelConfig(); cfg == nil || !cfg.AllowUnauthenticated || cfg.Tunnel.Hostname != "dev.example.com" {
		t.Fatalf("NamedTunnelConfig does not round-trip for restart: %+v", cfg)
	}
}

func TestNamedTunnelRefusedWithoutAccessOrOptOut(t *testing.T) {
	for name, cfg := range map[string]*NamedTunnelConfig{
		"no access":   {Tunnel: tunnel.NamedCloudflare{Hostname: "dev.example.com"}},
		"bad team":    {Tunnel: tunnel.NamedCloudflare{Hostname: "dev.example.com"}, AccessTeamDomain: "evil.com", AccessAUD: "a"},
		"missing aud": {Tunnel: tunnel.NamedCloudflare{Hostname: "dev.example.com"}, AccessTeamDomain: testTeam},
	} {
		if _, err := NewProxyServer(ProxyConfig{ID: "p", TargetURL: "http://127.0.0.1:1", NamedTunnel: cfg}); err == nil {
			t.Errorf("%s: NewProxyServer accepted a named tunnel with no usable Access check", name)
		}
	}
}
