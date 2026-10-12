package proxy

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// startHeaderEchoProxy fronts a backend that reports what it received (as
// response headers) with a real ProxyServer and returns the proxy's address.
func startHeaderEchoProxy(t *testing.T) string {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Forwarded-Host", r.Header.Get("X-Forwarded-Host"))
		w.Header().Set("X-Seen-Forwarded-Proto", r.Header.Get("X-Forwarded-Proto"))
		w.Header().Set("X-Seen-Forwarded-For", strings.Join(r.Header.Values("X-Forwarded-For"), "|"))
		w.Header().Set("X-Seen-Host", r.Host)
		w.Header().Set("X-Seen-Query", r.URL.RawQuery)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)

	ps, err := NewProxyServer(ProxyConfig{
		ID:         "upstream-rewrite-test-proxy",
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
	return ps.ListenAddr
}

// rawHeaderGet writes one hand-built HTTP/1.1 request, so the test controls headers
// net/http would normalise (Connection above all), and returns the response.
func rawHeaderGet(t *testing.T, addr, target string, headers ...string) *http.Response {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\n", target, addr)
	for _, h := range headers {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := conn.Write([]byte(b.String())); err != nil {
		t.Fatalf("write request: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// A client that names the proxy's forwarding headers in Connection must not be
// able to have them stripped on the way upstream. Under the old Director hook
// ReverseProxy removed hop-by-hop headers after the hook ran, so it could.
func TestUpstreamRewrite_ClientCannotStripForwardedHeaders(t *testing.T) {
	addr := startHeaderEchoProxy(t)

	resp := rawHeaderGet(t, addr, "/",
		"Connection: X-Forwarded-Host, X-Forwarded-Proto, X-Forwarded-For")

	if got := resp.Header.Get("X-Seen-Forwarded-Host"); got != addr {
		t.Errorf("X-Forwarded-Host upstream = %q, want %q", got, addr)
	}
	if got := resp.Header.Get("X-Seen-Forwarded-Proto"); got != "http" {
		t.Errorf("X-Forwarded-Proto upstream = %q, want http", got)
	}
	if got := resp.Header.Get("X-Seen-Forwarded-For"); got != "127.0.0.1" {
		t.Errorf("X-Forwarded-For upstream = %q, want 127.0.0.1", got)
	}
}

func TestUpstreamRewrite_ForwardedHeaders(t *testing.T) {
	addr := startHeaderEchoProxy(t)

	resp := rawHeaderGet(t, addr, "/",
		"X-Forwarded-For: 203.0.113.9",
		"X-Forwarded-Host: spoofed.example",
		"X-Forwarded-Proto: https")

	// The inbound chain is kept and the client address appended exactly once.
	if got := resp.Header.Get("X-Seen-Forwarded-For"); got != "203.0.113.9, 127.0.0.1" {
		t.Errorf("X-Forwarded-For upstream = %q, want %q", got, "203.0.113.9, 127.0.0.1")
	}
	// Host and Proto describe this hop, never what the client claimed.
	if got := resp.Header.Get("X-Seen-Forwarded-Host"); got != addr {
		t.Errorf("X-Forwarded-Host upstream = %q, want %q", got, addr)
	}
	if got := resp.Header.Get("X-Seen-Forwarded-Proto"); got != "http" {
		t.Errorf("X-Forwarded-Proto upstream = %q, want http", got)
	}
	if got := resp.Header.Get("X-Seen-Host"); got == addr || got == "" {
		t.Errorf("upstream Host = %q, want the backend's own host", got)
	}
}

// A query net/url cannot parse is forwarded byte for byte rather than
// re-encoded, reordered or dropped.
func TestUpstreamRewrite_PreservesRawQuery(t *testing.T) {
	addr := startHeaderEchoProxy(t)

	const query = "b=2;c=3&a=%zz&z"
	resp := rawHeaderGet(t, addr, "/path?"+query)

	if got := resp.Header.Get("X-Seen-Query"); got != query {
		t.Errorf("upstream query = %q, want %q", got, query)
	}
}
