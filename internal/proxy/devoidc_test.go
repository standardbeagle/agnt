package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/devoidc"
)

func devOIDCTestConfig() devoidc.Config {
	return devoidc.Config{
		Clients: map[string]devoidc.Client{
			"app": {ID: "app", RedirectURIs: []string{"http://localhost:*/cb"}, Secret: "dev", LoginPath: "/login", SessionCookies: []string{"app.sid"}},
		},
		Personas: map[string]devoidc.Persona{
			"standard": {Name: "standard", Email: "std@example.com", Roles: []string{"user"}},
			"admin":    {Name: "admin", Email: "admin@example.com", Roles: []string{"admin"}},
		},
		DefaultPersona: "standard",
		Allow:          map[string][]string{"andy@example.com": {"admin"}},
	}
}

// startDevOIDCProxy fronts a backend that records the paths it receives.
func startDevOIDCProxy(t *testing.T, cfg ProxyConfig, withIssuer bool) (*ProxyServer, *[]string) {
	t.Helper()
	var seen []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "backend")
	}))
	t.Cleanup(backend.Close)
	cfg.TargetURL = backend.URL
	cfg.MaxLogSize = 50
	if cfg.ID == "" {
		cfg.ID = "devoidc-proxy"
	}
	ps, err := NewProxyServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if withIssuer {
		is, err := devoidc.New(devOIDCTestConfig())
		if err != nil {
			t.Fatal(err)
		}
		ps.SetDevOIDC(is)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	if err := ps.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ps.Stop(context.Background()) })
	return ps, &seen
}

// oidcGet requests path on the proxy port with the given Host and headers.
func oidcGet(t *testing.T, ps *ProxyServer, path, host string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+ps.ListenAddr+path, nil)
	if host != "" {
		req.Host = host
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestDevOIDCLocalRequestClassification(t *testing.T) {
	ps, _ := startDevOIDCProxy(t, ProxyConfig{}, true)
	local := "localhost:" + portOf(ps)

	code, body := oidcGet(t, ps, "/__agnt/oidc/state", local, nil)
	if code != http.StatusOK || !strings.Contains(body, `"admin"`) || !strings.Contains(body, `"standard"`) {
		t.Fatalf("premise: a plain local request sees every persona: %d %s", code, body)
	}

	refusals := map[string]struct {
		host string
		hdr  map[string]string
	}{
		"non-loopback Host":    {"dev.example.com", nil},
		"rebinding-style Host": {"evil.test:" + portOf(ps), nil},
		"Cf-Ray":               {local, map[string]string{"Cf-Ray": "8a1b"}},
		"Cf-Connecting-IP":     {local, map[string]string{"Cf-Connecting-IP": "203.0.113.9"}},
		"X-Forwarded-For":      {local, map[string]string{"X-Forwarded-For": "203.0.113.9"}},
		"Forwarded":            {local, map[string]string{"Forwarded": "for=203.0.113.9"}},
		"Tailscale-User-Login": {local, map[string]string{"Tailscale-User-Login": "a@b"}},
		"ngrok header":         {local, map[string]string{"Ngrok-Skip-Browser-Warning": "1"}},
	}
	for name, tc := range refusals {
		if code, _ := oidcGet(t, ps, "/__agnt/oidc/state", tc.host, tc.hdr); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}

	// A quick tunnel bound to the proxy makes its own listener public.
	done := make(chan struct{})
	ps.SetTunnelURL("https://random.trycloudflare.com", done)
	if code, _ := oidcGet(t, ps, "/__agnt/oidc/state", local, nil); code != http.StatusForbidden {
		t.Errorf("with a quick tunnel bound: %d, want 403", code)
	}
	close(done)
	if code, _ := oidcGet(t, ps, "/__agnt/oidc/state", local, nil); code != http.StatusOK {
		t.Errorf("after the quick tunnel ended: %d, want 200", code)
	}
	ps.SetPublicURL("https://static.example.com")
	if code, _ := oidcGet(t, ps, "/__agnt/oidc/state", local, nil); code != http.StatusForbidden {
		t.Errorf("with a static public-url: %d, want 403", code)
	}
}

func TestDevOIDCRefusedOnExternalBind(t *testing.T) {
	ps, _ := startDevOIDCProxy(t, ProxyConfig{BindAddress: "0.0.0.0", AllowExternal: true}, true)
	if code, _ := oidcGet(t, ps, "/__agnt/oidc/state", "localhost:"+portOf(ps), nil); code != http.StatusForbidden {
		t.Fatalf("0.0.0.0-bound proxy served the issuer locally: %d, want 403", code)
	}
}

func TestDevOIDCPathProxiedWhenOff(t *testing.T) {
	ps, seen := startDevOIDCProxy(t, ProxyConfig{}, false)
	code, body := oidcGet(t, ps, "/__agnt/oidc/state", "", nil)
	if code != http.StatusOK || body != "backend" || len(*seen) != 1 || (*seen)[0] != "/__agnt/oidc/state" {
		t.Fatalf("without dev-oidc the prefix must reach the app: %d %q %v", code, body, *seen)
	}
	ps.SetDevOIDC(nil)
	if code, body := oidcGet(t, ps, "/__agnt/oidc/state", "", nil); code != http.StatusOK || body != "backend" {
		t.Fatalf("after SetDevOIDC(nil): %d %q", code, body)
	}
}

// TestDevOIDCFlowThroughProxy runs a relying party's code flow against the
// proxy: silent login as the default persona, then a same-origin switch.
func TestDevOIDCFlowThroughProxy(t *testing.T) {
	ps, _ := startDevOIDCProxy(t, ProxyConfig{}, true)
	origin := "http://localhost:" + portOf(ps)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	var disc map[string]any
	resp, err := http.Get(origin + "/__agnt/oidc/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&disc)
	resp.Body.Close()
	if disc["issuer"] != origin+"/__agnt/oidc" {
		t.Fatalf("default issuer must be the proxy's loopback origin: %v", disc["issuer"])
	}

	q := url.Values{"response_type": {"code"}, "client_id": {"app"}, "redirect_uri": {"http://localhost:3000/cb"}, "scope": {"openid"}, "state": {"s"}}
	resp, err = noFollow.Get(origin + "/__agnt/oidc/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	code := loc.Query().Get("code")
	if resp.StatusCode != http.StatusFound || code == "" {
		t.Fatalf("authorize: %d %s", resp.StatusCode, loc)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {"http://localhost:3000/cb"}}
	req, _ := http.NewRequest(http.MethodPost, origin+"/__agnt/oidc/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("app", "dev")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tok map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&tok)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || tok["id_token"] == nil {
		t.Fatalf("token: %d %v", resp.StatusCode, tok)
	}

	// Switch: cross-origin refused, same-origin lands on the app's login path.
	for _, tc := range []struct {
		origin string
		want   int
	}{{"https://evil.example", http.StatusForbidden}, {"", http.StatusForbidden}, {origin, http.StatusSeeOther}} {
		req, _ := http.NewRequest(http.MethodPost, origin+"/__agnt/oidc/switch", strings.NewReader("persona=admin"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("switch with Origin %q: %d, want %d", tc.origin, resp.StatusCode, tc.want)
		}
		if tc.want == http.StatusSeeOther && resp.Header.Get("Location") != "/login" {
			t.Errorf("switch landed on %q, want /login", resp.Header.Get("Location"))
		}
	}
}

func portOf(ps *ProxyServer) string {
	_, port, _ := strings.Cut(ps.ListenAddr, ":")
	for strings.Contains(port, ":") {
		_, port, _ = strings.Cut(port, ":")
	}
	return port
}
