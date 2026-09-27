//go:build !windows

package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/devoidc"
)

func tunnelState(t *testing.T, ingress, jwt string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ingress+"/__agnt/oidc/state", nil)
	req.Host = "dev.example.com"
	if jwt != "" {
		req.Header.Set(AccessJWTHeader, jwt)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestDevOIDCThroughAccessTunnel(t *testing.T) {
	ps, f, ingress := startNamedTunnelProxy(t, false)
	is, err := devoidc.New(devOIDCTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	ps.SetDevOIDC(is)

	code, body := tunnelState(t, ingress, f.sign("k1", f.claims(map[string]any{"email": "andy@example.com"}), nil))
	if code != http.StatusOK || !strings.Contains(body, `"admin"`) || strings.Contains(body, `"standard"`) {
		t.Fatalf("allow-listed Access user must see exactly their personas: %d %s", code, body)
	}
	if code, _ := tunnelState(t, ingress, f.sign("k1", f.claims(map[string]any{"email": "stranger@example.com"}), nil)); code != http.StatusForbidden {
		t.Fatalf("unlisted Access user: %d, want 403", code)
	}
	if code, _ := tunnelState(t, ingress, f.sign("k1", f.claims(nil), nil)); code != http.StatusForbidden {
		t.Fatalf("Access token without an email (service token): %d, want 403", code)
	}
	// The named tunnel's own public URL does not disqualify local requests on
	// the proxy port: cloudflared never reaches that listener.
	if code, _ := oidcGet(t, ps, "/__agnt/oidc/state", "localhost:"+portOf(ps), nil); code != http.StatusOK {
		t.Fatalf("local request while the named tunnel is up: %d, want 200", code)
	}
}

func TestDevOIDCRefusedOnUnauthenticatedTunnel(t *testing.T) {
	ps, _, ingress := startNamedTunnelProxy(t, true)
	is, err := devoidc.New(devOIDCTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	ps.SetDevOIDC(is)
	if code, _ := tunnelState(t, ingress, ""); code != http.StatusForbidden {
		t.Fatalf("allow-unauthenticated tunnel served the issuer: %d, want 403", code)
	}
}
