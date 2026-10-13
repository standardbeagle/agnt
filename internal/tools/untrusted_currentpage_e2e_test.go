package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// End to end: telemetry sent over real control-WebSocket upgrades — one in the
// shape of a visitor through a quick tunnel, one from the local browser — is
// recorded by the proxy, attributed to a page session, and rendered by the
// currentpage converters. The untrusted connection's errors, interactions and
// mutations arrive at the rendered output marked and labelled; the trusted
// connection's arrive unchanged; the same error text from each is two groups.
func TestCurrentPage_E2E_UntrustedMarkerSurvivesToRenderedOutput(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><head><title>App</title></head><body>app</body></html>"))
	}))
	t.Cleanup(backend.Close)

	ps, err := proxy.NewProxyServer(proxy.ProxyConfig{ID: "cp-e2e", TargetURL: backend.URL, ListenPort: 0, MaxLogSize: 200})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := ps.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Stop(context.Background()) })
	<-ps.Ready()

	const tunnelHost = "abc.trycloudflare.com"
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	ps.SetTunnelURL("https://"+tunnelHost, done, proxy.TunnelKindCloudflareQuick)

	// A content-frame document request creates the page session (frame f1).
	req, err := http.NewRequest(http.MethodGet, "http://"+ps.ListenAddr+"/app?__devtool_frame=f1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	sessionID := ""
	waitFor(t, "the content-frame request to create a page session", func() bool {
		for _, s := range ps.PageTracker().GetActiveSessions() {
			sessionID = s.ID
			return true
		}
		return false
	})

	dial := func(header http.Header) *websocket.Conn {
		u := url.URL{Scheme: "ws", Host: ps.ListenAddr, Path: "/__devtool_metrics"}
		conn, _, err := websocket.DefaultDialer.Dial(u.String(), header)
		if err != nil {
			t.Fatalf("control websocket upgrade: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	send := func(conn *websocket.Conn, typ string, data map[string]interface{}) {
		if err := conn.WriteJSON(map[string]interface{}{"type": typ, "data": data, "url": "/app", "frame_id": "f1"}); err != nil {
			t.Fatal(err)
		}
	}
	const boom = "TypeError: cart is undefined"

	visitor := dial(http.Header{
		"Host":             {tunnelHost},
		"Origin":           {"https://" + tunnelHost},
		"Cf-Ray":           {"8f2a1b3c4d5e6f70-SJC"},
		"Cf-Connecting-Ip": {"203.0.113.9"},
		"X-Forwarded-For":  {"203.0.113.9"},
	})
	send(visitor, "error", map[string]interface{}{"message": boom})
	send(visitor, "interactions", map[string]interface{}{"events": []interface{}{
		map[string]interface{}{"event_type": "click", "target": map[string]interface{}{"selector": "#visitor"}},
	}})
	send(visitor, "mutations", map[string]interface{}{"events": []interface{}{
		map[string]interface{}{"mutation_type": "added"},
	}})

	local := dial(http.Header{"Origin": {"http://" + ps.ListenAddr}})
	send(local, "error", map[string]interface{}{"message": boom})
	send(local, "interactions", map[string]interface{}{"events": []interface{}{
		map[string]interface{}{"event_type": "click", "target": map[string]interface{}{"selector": "#local"}},
	}})

	var session map[string]interface{}
	waitFor(t, "both connections' telemetry to reach the page session", func() bool {
		s, ok := ps.PageTracker().GetSession(sessionID)
		if !ok || len(s.Errors) != 2 || s.InteractionCount != 2 || s.MutationCount != 1 {
			return false
		}
		raw, err := json.Marshal(s) // what the daemon sends the tool
		if err != nil {
			t.Fatal(err)
		}
		return json.Unmarshal(raw, &session) == nil
	})

	render := func(v interface{}) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	all := map[string]bool{"errors": true, "interactions": true, "mutations": true}
	summary := render(convertToPageSummary(session, all, 20))
	full := render(convertToPageSessionOutput(session))

	label, _ := json.Marshal(untrustedLabelLiteral + boom)
	bare, _ := json.Marshal(boom)
	for name, out := range map[string]string{"summary": summary, "full": full} {
		if !strings.Contains(out, `"untrusted_note"`) {
			t.Errorf("%s output carries no untrusted note:\n%s", name, out)
		}
		if !strings.Contains(out, "#visitor") || !strings.Contains(out, "#local") {
			t.Errorf("%s output dropped an interaction (nothing is hidden from a pull):\n%s", name, out)
		}
	}
	// Errors: the visitor's is labelled and grouped apart from the page's own.
	if !strings.Contains(summary, `"message":`+string(label)) {
		t.Errorf("summary does not show the untrusted error labelled:\n%s", summary)
	}
	if !strings.Contains(summary, `"message":`+string(bare)) {
		t.Errorf("summary lost or relabelled the trusted error:\n%s", summary)
	}
	// By-type counts keep the two apart, and the mutation is the visitor's.
	for _, want := range []string{`"click (untrusted)":1`, `"click":1`, `"added (untrusted)":1`} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary is missing %s:\n%s", want, summary)
		}
	}
	// Exactly the visitor's three items are noted in the full view.
	if got := strings.Count(full, `"untrusted_note"`); got != 3 {
		t.Errorf("full output notes %d items as untrusted, want 3 (error, interaction, mutation):\n%s", got, full)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
