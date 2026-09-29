package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/standardbeagle/agnt/internal/devoidc"
)

// tailnetProxy is an unstarted proxy whose live address is a tailnet IP, with
// the tailscale lookups stubbed: this node is build1.example.ts.net /
// 100.87.26.14, and the peer 100.78.9.121 belongs to andy@example.com.
func tailnetProxy(t *testing.T) (*ProxyServer, *atomic.Int32) {
	t.Helper()
	ps, err := NewProxyServer(ProxyConfig{ID: "tn", TargetURL: "http://127.0.0.1:1", MaxLogSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	ps.setBoundAddr("100.87.26.14:31536")
	ps.tailnetIdentities = func(context.Context) []string { return []string{"build1.example.ts.net", "100.87.26.14"} }
	var lookups atomic.Int32
	ps.devOIDC.whois = func(_ context.Context, ip string) string {
		lookups.Add(1)
		switch ip {
		case "100.78.9.121", "100.87.26.14":
			return "andy@example.com"
		}
		return "" // e.g. a tagged device
	}
	return ps, &lookups
}

func tailnetRequest(peer, host string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://"+host+"/__agnt/oidc/state", nil)
	r.RemoteAddr = peer + ":51234"
	r.Host = host
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func TestDevOIDCTailnetCaller(t *testing.T) {
	ps, lookups := tailnetProxy(t)
	const host = "build1.example.ts.net:31536"

	got := ps.devOIDCCaller(tailnetRequest("100.78.9.121", host, nil))
	if got.TailnetLogin != "andy@example.com" || got.Local || got.AccessEmail != "" {
		t.Fatalf("premise: a tailnet peer on the node's own name is identified: %+v", got)
	}
	if c := ps.devOIDCCaller(tailnetRequest("100.87.26.14", "100.87.26.14:31536", nil)); c.TailnetLogin != "andy@example.com" {
		t.Fatalf("this node's own backend calling its tailnet IP: %+v", c)
	}

	refusals := map[string]*http.Request{
		"LAN peer":                tailnetRequest("192.168.1.5", host, nil),
		"loopback peer":           tailnetRequest("127.0.0.1", host, nil),
		"rebinding Host":          tailnetRequest("100.78.9.121", "evil.test:31536", nil),
		"relayed public Host":     tailnetRequest("100.78.9.121", "random.trycloudflare.com", nil),
		"Cf-Ray":                  tailnetRequest("100.78.9.121", host, map[string]string{"Cf-Ray": "1"}),
		"X-Forwarded-For":         tailnetRequest("100.78.9.121", host, map[string]string{"X-Forwarded-For": "203.0.113.9"}),
		"tagged or unknown owner": tailnetRequest("100.99.0.7", host, nil),
	}
	for name, r := range refusals {
		if c := ps.devOIDCCaller(r); c.TailnetLogin != "" || c.Local {
			t.Errorf("%s: %+v, want no identity", name, c)
		}
	}

	// whois is cached per peer.
	before := lookups.Load()
	for i := 0; i < 5; i++ {
		ps.devOIDCCaller(tailnetRequest("100.78.9.121", host, nil))
	}
	if lookups.Load() != before {
		t.Fatalf("whois ran %d more times for a cached peer", lookups.Load()-before)
	}

	// A quick tunnel bound to the proxy disqualifies tailnet callers too.
	done := make(chan struct{})
	ps.SetTunnelURL("https://random.trycloudflare.com", done)
	if c := ps.devOIDCCaller(tailnetRequest("100.78.9.121", host, nil)); c.TailnetLogin != "" {
		t.Fatalf("with a quick tunnel bound: %+v", c)
	}
	close(done)

	if o := ps.ListenerOrigin(); o != "http://build1.example.ts.net:31536" {
		t.Fatalf("ListenerOrigin on a tailnet bind = %q, want the MagicDNS origin", o)
	}
}

func TestDevOIDCTailnetServesAllowListedPersonas(t *testing.T) {
	ps, _ := tailnetProxy(t)
	is, err := devoidc.New(devOIDCTestConfig()) // allow: andy@example.com -> admin
	if err != nil {
		t.Fatal(err)
	}
	ps.SetDevOIDC(is)

	rec := httptest.NewRecorder()
	ps.serveDevOIDC(rec, tailnetRequest("100.78.9.121", "build1.example.ts.net:31536", nil))
	var st devoidc.State
	_ = json.NewDecoder(rec.Body).Decode(&st)
	if rec.Code != http.StatusOK || len(st.Personas) != 1 || st.Personas[0].Name != "admin" {
		t.Fatalf("allow-listed tailnet login: %d %+v", rec.Code, st)
	}

	rec = httptest.NewRecorder()
	ps.serveDevOIDC(rec, tailnetRequest("100.99.0.7", "build1.example.ts.net:31536", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("peer without an identity: %d, want 403", rec.Code)
	}
}
