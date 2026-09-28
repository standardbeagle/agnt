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

func TestOriginForListenAddr(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1:4242":     "http://localhost:4242",
		"[::1]:80":           "http://localhost:80",
		"0.0.0.0:31536":      "http://localhost:31536",
		":31536":             "http://localhost:31536",
		"100.87.26.14:31536": "http://100.87.26.14:31536",
	} {
		if got, err := OriginForListenAddr(addr); err != nil || got != want {
			t.Errorf("OriginForListenAddr(%q) = %q, %v; want %q", addr, got, err, want)
		}
	}
	if _, err := OriginForListenAddr(""); err == nil {
		t.Error("empty listen address accepted")
	}
}
