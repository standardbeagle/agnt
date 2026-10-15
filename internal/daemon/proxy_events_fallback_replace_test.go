package daemon

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/scope"
)

// fallbackReplaceFixture starts a daemon, two fake upstreams on 127.0.0.1:0
// and an .agnt.kdl declaring proxy "dev" linked to script "dev" with
// fallback-port = the first upstream's port. It then runs the fallback check
// directly (no 30s wait), so a fallback proxy targeting the first upstream
// exists when it returns.
type fallbackReplaceFixture struct {
	d          *Daemon
	project    string
	scriptID   string
	fallbackID string
	fallback   *proxy.ProxyServer
	oldPort    int // fallback-port: the first upstream
	newURL     string
}

func newFallbackReplaceFixture(t *testing.T, registerOwner bool) *fallbackReplaceFixture {
	t.Helper()
	d, project := newFallbackTestDaemon(t)

	upstream := func(body string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, body)
		}))
		t.Cleanup(s.Close)
		return s
	}
	oldUp := upstream("old-upstream")
	newUp := upstream("new-upstream")
	var port int
	if _, err := fmt.Sscanf(oldUp.Listener.Addr().String(), "127.0.0.1:%d", &port); err != nil {
		t.Fatalf("parse upstream port: %v", err)
	}

	if err := writeFile(filepath.Join(project, ".agnt.kdl"), fmt.Sprintf(`
proxies {
    dev {
        script "dev"
        fallback-port %d
    }
}
`, port)); err != nil {
		t.Fatalf("write config: %v", err)
	}

	scriptID := makeProcessID(project, "dev")
	if registerOwner {
		d.registerIncidentProcessOwner(scriptID, "owned-session")
	}
	d.handleFallbackPortCheck(ProxyEvent{
		Type:      FallbackPortCheck,
		ScriptID:  scriptID,
		ProxyName: "dev",
		Config:    &config.ProxyConfig{Script: "dev", FallbackPort: port},
		Path:      project,
	})
	fallbackID := makeProcessID(project, "dev")
	fb, ok := d.exactProxy(fallbackID)
	if !ok {
		t.Fatalf("fallback proxy %q was not created", fallbackID)
	}
	return &fallbackReplaceFixture{
		d: d, project: project, scriptID: scriptID, fallbackID: fallbackID,
		fallback: fb, oldPort: port, newURL: newUp.URL,
	}
}

func (f *fallbackReplaceFixture) detect(url string) {
	f.d.handleURLDetected(ProxyEvent{
		Type:     URLDetected,
		ScriptID: f.scriptID,
		URL:      url,
		Path:     f.project,
	})
}

func (f *fallbackReplaceFixture) projectProxies() []*proxy.ProxyServer {
	return f.d.proxym.ListScoped(scope.Project(f.project))
}

// A URL detected after the fallback proxy was created replaces it: one proxy,
// on the port the fallback had, forwarding to the detected URL, and the swap
// is reported in the startup log.
func TestHandleURLDetected_ReplacesFallbackProxyOnSamePort(t *testing.T) {
	f := newFallbackReplaceFixture(t, false)
	port := f.fallback.BoundPort()
	if port <= 0 {
		t.Fatalf("fallback proxy has no bound port")
	}

	f.detect(f.newURL)

	proxies := f.projectProxies()
	if len(proxies) != 1 {
		ids := make([]string, 0, len(proxies))
		for _, p := range proxies {
			ids = append(ids, p.ID+"→"+p.TargetURL.String())
		}
		t.Fatalf("want exactly one proxy after detection, got %d: %v", len(proxies), ids)
	}
	got := proxies[0]
	wantID := makeProxyIDFromURL(f.project, "dev", f.newURL)
	if got.ID != wantID {
		t.Errorf("proxy id = %q, want %q", got.ID, wantID)
	}
	if got.BoundPort() != port {
		t.Errorf("replacement bound port %d, want the fallback's port %d", got.BoundPort(), port)
	}
	if got.TargetURL.String() != f.newURL {
		t.Errorf("replacement target = %q, want %q", got.TargetURL.String(), f.newURL)
	}
	if !got.AutoRestart() {
		t.Errorf("replacement lost the fallback proxy's auto-restart")
	}
	if tracked := f.d.getProxiesForScript(f.scriptID); len(tracked) != 1 || tracked[0] != wantID {
		t.Errorf("script tracks %v, want [%s]", tracked, wantID)
	}
	if n := countStartupEntriesByEvent(f.d, "startup_proxy_fallback_replaced"); n != 1 {
		t.Errorf("startup_proxy_fallback_replaced entries = %d, want 1", n)
	}
	for _, e := range f.d.startupErrorStore.Query(StartupLogFilter{}) {
		if e.EventType == "startup_proxy_fallback_replaced" &&
			(!strings.Contains(e.Message, f.fallback.TargetURL.String()) || !strings.Contains(e.Message, f.newURL)) {
			t.Errorf("replaced entry must name old and new targets, got %q", e.Message)
		}
	}

	// The browser's port now reaches the detected upstream.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("GET through replacement proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "new-upstream") {
		t.Errorf("proxy on port %d served %q, want the detected upstream", port, body)
	}
}

// Detection of the URL the fallback already targets changes nothing: same
// proxy instance, no second proxy, no replacement event.
func TestHandleURLDetected_SameTargetAsFallbackKeepsIt(t *testing.T) {
	f := newFallbackReplaceFixture(t, false)

	f.detect(fmt.Sprintf("http://localhost:%d/", f.oldPort))

	proxies := f.projectProxies()
	if len(proxies) != 1 {
		t.Fatalf("want exactly one proxy, got %d", len(proxies))
	}
	if proxies[0] != f.fallback {
		t.Errorf("fallback proxy was replaced (now %q) although the target is unchanged", proxies[0].ID)
	}
	if n := countStartupEntriesByEvent(f.d, "startup_proxy_fallback_replaced"); n != 0 {
		t.Errorf("startup_proxy_fallback_replaced entries = %d, want 0", n)
	}
}

// The replaced fallback's per-proxy tracking dies with it; the replacement
// carries the script's exact incident owner.
func TestHandleURLDetected_FallbackReplacementRetiresOldOwner(t *testing.T) {
	f := newFallbackReplaceFixture(t, true)
	processOwner, _ := f.d.incidentProcessOwner.Load(f.scriptID)
	if prev, ok := f.d.incidentProxyOwner.Load(f.fallbackID); !ok || prev != processOwner {
		t.Fatalf("precondition: fallback proxy should carry the process owner")
	}

	f.detect(f.newURL)

	if _, ok := f.d.incidentProxyOwner.Load(f.fallbackID); ok {
		t.Errorf("incident owner of replaced fallback %q was not retired", f.fallbackID)
	}
	if _, ok := f.d.proxyConfigs.Load(f.fallbackID); ok {
		t.Errorf("config snapshot of replaced fallback %q was not retired", f.fallbackID)
	}
	if s := f.d.linkedScriptForProxy(f.fallbackID); s != "" {
		t.Errorf("replaced fallback %q still linked to script %q", f.fallbackID, s)
	}
	newID := makeProxyIDFromURL(f.project, "dev", f.newURL)
	if owner, ok := f.d.incidentProxyOwner.Load(newID); !ok || owner != processOwner {
		t.Errorf("replacement %q owner = %v, want exact process owner", newID, owner)
	}
}
