package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// originCapture is a stand-in for a backend whose CORS middleware inspects the
// inbound Origin header. It records the Origin value the backend actually
// received, so a test can assert what the proxy forwarded.
type originCapture struct {
	mu   sync.Mutex
	seen string
	got  bool
}

func (o *originCapture) record(r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = r.Header.Get("Origin")
	o.got = true
}

func (o *originCapture) origin() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.seen, o.got
}

// startOriginProxy stands up a backend that captures the Origin it receives,
// fronted by a real ProxyServer, and returns the capture plus the proxy URL.
func startOriginProxy(t *testing.T) (*originCapture, string, string) {
	t.Helper()
	cap := &originCapture{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	ps, err := NewProxyServer(ProxyConfig{
		ID:         "origin-test-proxy",
		TargetURL:  backend.URL,
		ListenPort: 0,
		MaxLogSize: 50,
	})
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	if err := ps.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { ps.Stop(ctx) })
	select {
	case <-ps.Ready():
	case <-ctx.Done():
		t.Fatal("proxy not ready")
	}
	return cap, fmt.Sprintf("http://%s", ps.ListenAddr), backend.URL
}

// requestWithOrigin sends one GET through the proxy carrying the given Origin
// header (empty string = no Origin) and returns the Origin the backend saw.
func requestWithOrigin(t *testing.T, proxyURL, origin string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, proxyURL+"/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	return proxyURL
}

// TestDirectorRewritesProxyOwnOrigin covers the reported bug: when the inbound
// Origin is the proxy's own listen origin (the origin agnt itself introduced),
// the Director rewrites it to the backend's own origin so the backend's CORS
// middleware sees a same-origin request and stops logging a mismatch.
func TestDirectorRewritesProxyOwnOrigin(t *testing.T) {
	cap, proxyURL, backendURL := startOriginProxy(t)

	// Browser served from the proxy sends the proxy's own origin.
	requestWithOrigin(t, proxyURL, proxyURL)

	got, ok := cap.origin()
	if !ok {
		t.Fatal("backend never received the request")
	}
	if got != backendURL {
		t.Fatalf("proxy-own Origin was not rewritten to the backend origin:\n  got:  %q\n  want: %q", got, backendURL)
	}
}

// TestDirectorPreservesThirdPartyOrigin is the CSRF-safety test: a genuinely
// third-party Origin (a real cross-site request) must be forwarded unchanged so
// the backend still sees it as cross-origin and its CSRF/CORS checks fire. A
// blanket rewrite would make this cross-site request look same-origin — the
// exact security hole this scoping avoids.
func TestDirectorPreservesThirdPartyOrigin(t *testing.T) {
	cap, proxyURL, _ := startOriginProxy(t)

	const thirdParty = "http://evil.example.com"
	requestWithOrigin(t, proxyURL, thirdParty)

	got, ok := cap.origin()
	if !ok {
		t.Fatal("backend never received the request")
	}
	if got != thirdParty {
		t.Fatalf("third-party Origin must be forwarded verbatim (CSRF safety):\n  got:  %q\n  want: %q", got, thirdParty)
	}
}

// TestDirectorLeavesAbsentOriginAbsent confirms a request with no Origin is
// forwarded with no Origin — the rewrite only fires on the proxy's own origin.
func TestDirectorLeavesAbsentOriginAbsent(t *testing.T) {
	cap, proxyURL, _ := startOriginProxy(t)

	requestWithOrigin(t, proxyURL, "")

	got, ok := cap.origin()
	if !ok {
		t.Fatal("backend never received the request")
	}
	if got != "" {
		t.Fatalf("absent Origin must stay absent, got %q", got)
	}
}
