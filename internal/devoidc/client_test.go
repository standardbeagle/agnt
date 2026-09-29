package devoidc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSwitchScriptQuotesValues(t *testing.T) {
	js := SwitchScript(`adm'in"</script>`, "web")
	if !strings.Contains(js, `add('persona', "adm'in\"\u003c/script\u003e")`) {
		t.Fatalf("persona not emitted as a JSON string literal:\n%s", js)
	}
	if !strings.Contains(js, `f.action = "/__agnt/oidc/switch"`) || !strings.Contains(js, `add('client', "web")`) {
		t.Fatalf("switch script shape:\n%s", js)
	}
}

func TestFetchState(t *testing.T) {
	is := newWithKey(Config{
		Clients:        map[string]Client{"web": {ID: "web", RedirectURIs: []string{"http://localhost:*/cb"}}},
		Personas:       map[string]Persona{"standard": {Email: "s@x.com"}, "admin": {Email: "a@x.com"}},
		DefaultPersona: "admin",
	}, sharedKey(t))
	srv := httptest.NewServer(is.Handler(Mount{
		Caller:     func(*http.Request) Caller { return Caller{Local: true} },
		SameOrigin: func(*http.Request) bool { return true },
	}))
	t.Cleanup(srv.Close)
	st, err := FetchState(srv.URL)
	if err != nil || st.Persona != "admin" || !st.Has("standard") || st.Has("root") || strings.Join(st.Names(), ",") != "admin,standard" {
		t.Fatalf("FetchState: %+v %v", st, err)
	}

	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html>app</html>"))
	}))
	t.Cleanup(app.Close)
	if _, err := FetchState(app.URL); err == nil || !strings.Contains(err.Error(), "declare a dev-oidc block") {
		t.Fatalf("issuer-less proxy: %v", err)
	}
}

func TestOriginForProxyURL(t *testing.T) {
	for proxyURL, want := range map[string]string{
		"http://127.0.0.1:4242":             "http://localhost:4242",
		"http://[::1]:80":                   "http://localhost:80",
		"http://0.0.0.0:31536":              "http://localhost:31536",
		"http://100.87.26.14:31536":         "http://100.87.26.14:31536",
		"https://build1.tnet.ts.net:31536":  "https://build1.tnet.ts.net:31536",
		"https://build1.tnet.ts.net:31536/": "https://build1.tnet.ts.net:31536",
	} {
		if got, err := OriginForProxyURL(proxyURL); err != nil || got != want {
			t.Errorf("OriginForProxyURL(%q) = %q, %v; want %q", proxyURL, got, err, want)
		}
	}
	for _, bad := range []string{"", "127.0.0.1:4242", "http://", "ftp://x:1"} {
		if got, err := OriginForProxyURL(bad); err == nil {
			t.Errorf("OriginForProxyURL(%q) = %q, want an error", bad, got)
		}
	}
}

// A dev-oidc block written for an http tailnet proxy breaks when the proxy
// starts serving the tailnet certificate: the issuer and the callback both
// change scheme. Only URLs naming the proxy's own host are judged.
func TestSchemeMismatches(t *testing.T) {
	const https = "https://build1.tnet.ts.net:31536"
	got := SchemeMismatches(https,
		"http://build1.tnet.ts.net:31536/__agnt/oidc",
		[]string{
			"http://build1.tnet.ts.net:31536/api/auth/callback/dev-oidc",
			"http://localhost:*/api/auth/callback/dev-oidc",
			"https://build1.tnet.ts.net:31536/ok",
		})
	if len(got) != 2 {
		t.Fatalf("want the issuer and one redirect-uri, got %q", got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"issuer http://build1.tnet.ts.net:31536/__agnt/oidc", "redirect-uri http://build1.tnet.ts.net:31536/api/auth/callback/dev-oidc", "https"} {
		if !strings.Contains(joined, want) {
			t.Errorf("mismatches %q must mention %q", got, want)
		}
	}
	if m := SchemeMismatches(https, "", []string{"https://build1.tnet.ts.net:31536/cb"}); len(m) != 0 {
		t.Errorf("default issuer and matching callback: %q", m)
	}
	if m := SchemeMismatches("http://build1.tnet.ts.net:31536", "http://build1.tnet.ts.net:31536/__agnt/oidc", []string{"http://build1.tnet.ts.net:31536/cb"}); len(m) != 0 {
		t.Errorf("http proxy with http URLs: %q", m)
	}
	if m := SchemeMismatches(https, "https://dev.example.com/__agnt/oidc", []string{"http://other.host/cb"}); len(m) != 0 {
		t.Errorf("URLs for other hosts are not this proxy's to judge: %q", m)
	}
}
