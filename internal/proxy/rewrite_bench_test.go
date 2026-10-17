package proxy

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
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

// benchPayloadSizes are the response sizes the proxy benchmarks sweep: a small
// fragment, a typical SPA shell or server-rendered page, and a heavy document.
// One size says little about a cost that mixes a fixed part (headers, script
// splice) with a per-byte part (decode, rewrite scan, copy).
var benchPayloadSizes = []struct {
	name  string
	bytes int
}{
	{"4KB", 4 << 10},
	{"64KB", 64 << 10},
	{"512KB", 512 << 10},
}

func encodeIdentity(_ testing.TB, raw []byte) []byte { return raw }

func encodeGzip(tb testing.TB, raw []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		tb.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		tb.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func encodeBrotli(tb testing.TB, raw []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		tb.Fatalf("brotli write: %v", err)
	}
	if err := w.Close(); err != nil {
		tb.Fatalf("brotli close: %v", err)
	}
	return buf.Bytes()
}

func encodeZstd(tb testing.TB, raw []byte) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		tb.Fatalf("zstd writer: %v", err)
	}
	if _, err := w.Write(raw); err != nil {
		tb.Fatalf("zstd write: %v", err)
	}
	if err := w.Close(); err != nil {
		tb.Fatalf("zstd close: %v", err)
	}
	return buf.Bytes()
}

// newBenchProxy is the proxy configuration every ModifyResponse benchmark uses:
// the production inject path with a target URL configured.
func newBenchProxy() *ProxyServer {
	target, _ := url.Parse("http://localhost:5173")
	return &ProxyServer{ListenAddr: "127.0.0.1:8080", TargetURL: target}
}

// benchResponse builds the upstream response a dev server would send for an
// already-encoded body: Content-Length set to the bytes on the wire.
func benchResponse(encoding string, wire []byte) *http.Response {
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": []string{"text/html"}},
		Body:          io.NopCloser(bytes.NewReader(wire)),
		ContentLength: int64(len(wire)),
	}
	resp.Header.Set("Content-Length", strconv.Itoa(len(wire)))
	if encoding != "" {
		resp.Header.Set("Content-Encoding", encoding)
	}
	return resp
}

// benchModifyEncoded measures ModifyResponse on an HTML response of each size in
// benchPayloadSizes, sent with the given Content-Encoding. Before timing it
// checks that one response comes out decoded and instrumented, so a benchmark
// that silently measures the "cannot decode, pass through" path fails instead.
// The result is drained with io.Copy(io.Discard) like the identity benchmark, and
// every error is fatal. MB/s is relative to the decoded HTML, so encodings
// compare directly.
func benchModifyEncoded(b *testing.B, encoding string, encode func(testing.TB, []byte) []byte) {
	for _, size := range benchPayloadSizes {
		b.Run(size.name, func(b *testing.B) {
			ps := newBenchProxy()
			raw := benchHTML(size.bytes)
			wire := encode(b, raw)

			check := benchResponse(encoding, wire)
			if err := ps.modifyResponse(check); err != nil {
				b.Fatalf("modifyResponse: %v", err)
			}
			got, err := io.ReadAll(check.Body)
			if err != nil {
				b.Fatalf("read modified body: %v", err)
			}
			if !bytes.Contains(got, []byte("__devtool")) || check.Header.Get("Content-Encoding") != "" {
				b.Fatalf("%q response was not decoded and instrumented (encoding header %q)", encoding, check.Header.Get("Content-Encoding"))
			}

			b.SetBytes(int64(len(raw)))
			b.ReportMetric(float64(len(wire)), "wire-B")
			b.ReportMetric(float64(len(got)-len(raw)), "injected-B")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				resp := benchResponse(encoding, wire)
				if err := ps.modifyResponse(resp); err != nil {
					b.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, resp.Body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
