package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// benchHTML returns a realistic HTML body of roughly the given size with no
// absolute target URLs (the common dev-server case, where the rewrite pass
// should allocate nothing).
func benchHTML(approxBytes int) []byte {
	const filler = "<p>Hello World, this is some representative page content.</p>"
	var b strings.Builder
	b.WriteString("<html><head><title>Test</title></head><body>")
	for b.Len() < approxBytes {
		b.WriteString(filler)
	}
	b.WriteString("</body></html>")
	return []byte(b.String())
}

// BenchmarkModifyResponse_Identity measures the production inject hot path for
// an uncompressed (identity) HTML response with Content-Length set and a
// target URL configured. It drains the result via io.Discard so the benchmark
// itself does not allocate (unlike a trailing io.ReadAll, which grows by
// doubling and dominates the numbers). This pins the three hot-path wins:
//   - readAllSized presizes from Content-Length (single read alloc)
//   - the rewrite pass allocates nothing when no absolute target URL is present
//   - the cached []byte script is not re-copied per request
func BenchmarkModifyResponse_Identity(b *testing.B) {
	target, _ := url.Parse("http://localhost:5173")
	ps := &ProxyServer{ListenAddr: "127.0.0.1:8080", TargetURL: target}

	body := benchHTML(64 * 1024)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp := &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"text/html"}},
			Body:          io.NopCloser(bytes.NewReader(body)),
			ContentLength: int64(len(body)),
		}
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		if err := ps.modifyResponse(resp); err != nil {
			b.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
	}
}
