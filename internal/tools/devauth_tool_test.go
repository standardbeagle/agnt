package tools

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/devoidc"
)

// devAuthServer serves a real issuer with the MCP process as a local caller.
func devAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	is, err := devoidc.New(devoidc.Config{
		Clients:        map[string]devoidc.Client{"web": {ID: "web", RedirectURIs: []string{"http://localhost:*/cb"}}},
		Personas:       map[string]devoidc.Persona{"standard": {Name: "standard", Email: "s@x.com"}, "admin": {Name: "admin", Email: "a@x.com", Roles: []string{"admin"}}},
		DefaultPersona: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		is.Handler(devoidc.Mount{
			LocalOrigin: srv.URL,
			Caller:      func(*http.Request) devoidc.Caller { return devoidc.Caller{Local: true} },
			SameOrigin:  func(r *http.Request) bool { return r.Header.Get("Origin") == srv.URL },
		}).ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDevAuthPersonasAndToken(t *testing.T) {
	srv := devAuthServer(t)
	res, out, _ := devAuthPersonas(srv.URL)
	if res != nil || out.Current != "standard" || len(out.Personas) != 2 || out.Issuer != srv.URL+devoidc.Prefix {
		t.Fatalf("personas: %v %+v", res, out)
	}
	res, out, _ = devAuthToken(srv.URL, DevAuthInput{Persona: "admin", Client: "web"})
	if res != nil || strings.Count(out.AccessToken, ".") != 2 || out.ExpiresIn != 3600 {
		t.Fatalf("token: %v %+v", res, out)
	}
	res, _, _ = devAuthToken(srv.URL, DevAuthInput{Persona: "root", Client: "web"})
	if res == nil || !res.IsError || !strings.Contains(resultText(res), "unknown persona") {
		t.Fatalf("token for unknown persona must fail naming the persona: %+v", res)
	}
	res, _, _ = devAuthToken(srv.URL, DevAuthInput{Persona: "admin"})
	if res == nil || !res.IsError {
		t.Fatal("token without client must fail")
	}
}

func TestDevAuthNoIssuerIsReportedPlainly(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>the app</html>"))
	}))
	t.Cleanup(app.Close)
	res, _, _ := devAuthPersonas(app.URL)
	if res == nil || !res.IsError || !strings.Contains(resultText(res), "declare a dev-oidc block") {
		t.Fatalf("a proxied (issuer-less) prefix must say how to enable dev-oidc: %+v", res)
	}
}

func TestDevAuthOrigin(t *testing.T) {
	for addr, want := range map[string]string{"127.0.0.1:4242": "http://localhost:4242", "[::1]:80": "http://localhost:80"} {
		if got, err := devAuthOrigin(addr); err != nil || got != want {
			t.Errorf("devAuthOrigin(%q) = %q, %v", addr, got, err)
		}
	}
	if _, err := devAuthOrigin(""); err == nil {
		t.Error("empty listen address accepted")
	}
}
