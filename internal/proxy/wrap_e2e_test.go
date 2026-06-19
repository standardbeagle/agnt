package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestAlwaysWrap_EndToEnd drives the always-wrap + frame model through a live
// proxy and a real backend, covering the full request lifecycle:
//   - top-level navigation  -> chrome shell wrapping a single content iframe
//   - content-frame request -> unwrapped page + content-role runtime
//   - foreign nested iframe  -> not wrapped (app owns it)
//   - page session tracking  -> shell + content coalesce to one clean-URL session
func TestAlwaysWrap_EndToEnd(t *testing.T) {
	backend := newWrapBackend(t)
	ps := startWrapProxy(t, backend)
	proxyURL := fmt.Sprintf("http://%s", ps.ListenAddr)

	// 1. Top-level navigation: a browser document request gets the chrome shell.
	shell := getWrap(t, proxyURL+"/app", "document")
	if c := strings.Count(shell, "<iframe"); c != 1 {
		t.Fatalf("top-level nav must yield a shell with one content iframe, got %d:\n%s", c, shell)
	}
	if !strings.Contains(shell, `window.__devtool_role="chrome"`) {
		t.Errorf("shell must declare the chrome role")
	}
	if strings.Contains(shell, "PAGE-BODY-MARKER") {
		t.Errorf("shell must not inline page content")
	}
	if !strings.Contains(shell, frameMarkerParam+"=") {
		t.Errorf("shell iframe src must carry the frame marker")
	}

	// 2. Content-frame request (carries the marker): unwrapped page + content role.
	content := getWrap(t, proxyURL+"/app?"+frameMarkerParam+"=f1", "iframe")
	if strings.Contains(content, "<iframe") {
		t.Errorf("content-frame response must not be re-wrapped")
	}
	if !strings.Contains(content, "PAGE-BODY-MARKER") {
		t.Errorf("content frame must serve the real page body")
	}
	if !strings.Contains(content, `window.__devtool_role="content"`) {
		t.Errorf("content frame must declare the content role")
	}

	// 3. Foreign app-embedded iframe (no marker, Sec-Fetch-Dest: iframe): NOT wrapped.
	foreign := getWrap(t, proxyURL+"/widget", "iframe")
	if strings.Contains(foreign, "<iframe") {
		t.Errorf("foreign app iframe must not be wrapped in a shell")
	}
	if !strings.Contains(foreign, "PAGE-BODY-MARKER") {
		t.Errorf("foreign iframe must serve its real content")
	}

	// 4. Page session tracking: the shell's top-level request and the content
	// frame's marked request coalesce into one session keyed on the clean URL.
	var sessions []*PageSession
	for i := 0; i < 50; i++ {
		sessions = ps.PageTracker().GetActiveSessions()
		if len(sessions) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Find the /app session(s) (there may also be a /widget session). Shell +
	// content document requests for /app must coalesce into exactly ONE session.
	appCount := 0
	for _, s := range sessions {
		if strings.Contains(s.URL, "/app") {
			appCount++
		}
		if strings.Contains(s.URL, frameMarkerParam) {
			t.Errorf("session URL must be marker-free, got %q", s.URL)
		}
	}
	if appCount != 1 {
		t.Fatalf("shell + content requests for /app must coalesce into exactly 1 session, got %d (total %d)", appCount, len(sessions))
	}
}

func newWrapBackend(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html><head><title>App</title></head><body>PAGE-BODY-MARKER</body></html>"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func startWrapProxy(t *testing.T, backend *httptest.Server) *ProxyServer {
	t.Helper()
	ps, err := NewProxyServer(ProxyConfig{
		ID:         "e2e-wrap",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 100,
	})
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if err := ps.Start(ctx); err != nil {
		t.Fatalf("proxy start: %v", err)
	}
	t.Cleanup(func() { ps.Stop(ctx) })
	select {
	case <-ps.Ready():
	case <-ctx.Done():
		t.Fatal("proxy not ready")
	}
	return ps
}

// getWrap issues a GET with the given Sec-Fetch-Dest and returns the body.
func getWrap(t *testing.T, url, dest string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if dest != "" {
		req.Header.Set("Sec-Fetch-Dest", dest)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
