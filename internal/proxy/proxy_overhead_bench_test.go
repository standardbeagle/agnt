package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startBenchProxy runs a real proxy in front of backend, for benchmarks.
func startBenchProxy(b *testing.B, backend *httptest.Server) *ProxyServer {
	b.Helper()
	ps, err := NewProxyServer(ProxyConfig{ID: "bench-overhead", TargetURL: backend.URL, ListenPort: 0, MaxLogSize: 100})
	if err != nil {
		b.Fatalf("NewProxyServer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	b.Cleanup(cancel)
	if err := ps.Start(ctx); err != nil {
		b.Fatalf("proxy start: %v", err)
	}
	b.Cleanup(func() { ps.Stop(ctx) })
	select {
	case <-ps.Ready():
	case <-ctx.Done():
		b.Fatal("proxy not ready")
	}
	return ps
}

// benchGet fetches target and drains the body, returning the byte count. Every
// failure is fatal: a benchmark that counts failed requests measures nothing.
func benchGet(b *testing.B, client *http.Client, target, fetchDest string) int64 {
	b.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		b.Fatal(err)
	}
	if fetchDest != "" {
		req.Header.Set("Sec-Fetch-Dest", fetchDest)
	}
	resp, err := client.Do(req)
	if err != nil {
		b.Fatalf("GET %s: %v", target, err)
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		b.Fatalf("read %s: %v", target, err)
	}
	if resp.StatusCode != http.StatusOK {
		b.Fatalf("GET %s: status %d", target, resp.StatusCode)
	}
	return n
}

// BenchmarkProxyOverhead compares one request to the upstream directly against
// the same request through a live proxy, per payload size:
//
//	direct          client → upstream
//	proxied_json    client → proxy → upstream, non-HTML body: forwarding and
//	                logging cost with no instrumentation
//	proxied_html    client → proxy → upstream, HTML content frame: forwarding
//	                plus decode/rewrite/inject
//
// proxied_json − direct is the proxy's price for being in the path;
// proxied_html − proxied_json is the price of instrumenting the page. The HTML
// run reports injected-B, the bytes added to every instrumented page.
func BenchmarkProxyOverhead(b *testing.B) {
	for _, size := range benchPayloadSizes {
		html := benchHTML(size.bytes)
		jsonBody := []byte(strings.Repeat("x", len(html)))
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, ctype := html, "text/html; charset=utf-8"
			if strings.HasPrefix(r.URL.Path, "/api") {
				body, ctype = jsonBody, "application/json"
			}
			w.Header().Set("Content-Type", ctype)
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
		}))
		b.Cleanup(backend.Close)
		ps := startBenchProxy(b, backend)
		proxyURL := "http://" + ps.ListenAddr

		client := &http.Client{Transport: &http.Transport{
			DisableCompression:  true,
			MaxIdleConnsPerHost: 4,
		}}
		b.Cleanup(client.CloseIdleConnections)

		// The content-frame URL gets the unwrapped, instrumented page; a plain
		// document request would get the (small) chrome shell instead.
		contentPath := "/?" + frameMarkerParam + "=f1"
		cases := []struct {
			name, url, dest string
			wantMin         int64
		}{
			{"direct/" + size.name, backend.URL + "/", "", int64(len(html))},
			{"proxied_json/" + size.name, proxyURL + "/api/data", "", int64(len(jsonBody))},
			{"proxied_html/" + size.name, proxyURL + contentPath, "iframe", int64(len(html)) + 1},
		}
		for _, c := range cases {
			b.Run(c.name, func(b *testing.B) {
				got := benchGet(b, client, c.url, c.dest) // warm the connection and check the response
				if got < c.wantMin {
					b.Fatalf("response is %d bytes, want at least %d (instrumented page not served?)", got, c.wantMin)
				}
				if strings.HasPrefix(c.name, "proxied_html") {
					b.ReportMetric(float64(got-int64(len(html))), "injected-B")
				}
				b.SetBytes(got)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					benchGet(b, client, c.url, c.dest)
				}
			})
		}
	}
}
