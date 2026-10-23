package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const budgetTestHTML = "<html><head><title>t</title></head><body>hello</body></html>"

func compressFor(t *testing.T, encoding string) []byte {
	t.Helper()
	var buf bytes.Buffer
	switch encoding {
	case "gzip":
		w := gzip.NewWriter(&buf)
		w.Write([]byte(budgetTestHTML))
		w.Close()
	case "deflate":
		w := zlib.NewWriter(&buf)
		w.Write([]byte(budgetTestHTML))
		w.Close()
	case "raw-deflate":
		w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
		w.Write([]byte(budgetTestHTML))
		w.Close()
	}
	return buf.Bytes()
}

// exhaustInjectBudget fills the shared budget and restores it on cleanup.
func exhaustInjectBudget(t *testing.T) {
	t.Helper()
	free := injectBudget.limit - injectBudget.used.Load()
	if !injectBudget.tryReserve(free) {
		t.Fatal("could not exhaust budget")
	}
	t.Cleanup(func() { injectBudget.release(free) })
}

// A compressed response shed for memory pressure must reach the client byte for
// byte: admission is decided before any decoder consumes the stream.
func TestModifyResponse_ShedCompressedBodyIsForwardedIntact(t *testing.T) {
	for _, enc := range []string{"gzip", "deflate", "raw-deflate"} {
		t.Run(enc, func(t *testing.T) {
			ps := newTestProxyServer("http://localhost:3000", ":8080")
			ps.logger = NewTrafficLogger(10)
			exhaustInjectBudget(t)
			payload := compressFor(t, enc)
			header := enc
			if enc == "raw-deflate" {
				header = "deflate"
			}
			resp := &http.Response{
				StatusCode:    http.StatusOK,
				Header:        http.Header{"Content-Type": {"text/html"}, "Content-Encoding": {header}},
				Body:          io.NopCloser(bytes.NewReader(payload)),
				ContentLength: int64(len(payload)),
				Request:       httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil),
			}
			if err := ps.modifyResponse(resp); err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(resp.Body)
			if !bytes.Equal(got, payload) {
				t.Fatalf("shed body altered: got %d bytes, want %d", len(got), len(payload))
			}
			if resp.Header.Get("Content-Encoding") != header {
				t.Fatalf("Content-Encoding dropped: %q", resp.Header.Get("Content-Encoding"))
			}
		})
	}
}

// A body that is not what its Content-Encoding claims is forwarded untouched
// when the decoder cannot open it.
func TestModifyResponse_UndecodableBodyIsForwardedIntact(t *testing.T) {
	ps := newTestProxyServer("http://localhost:3000", ":8080")
	payload := []byte("definitely not gzip")
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": {"text/html"}, "Content-Encoding": {"gzip"}},
		Body:          io.NopCloser(bytes.NewReader(payload)),
		ContentLength: int64(len(payload)),
		Request:       httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil),
	}
	if err := ps.modifyResponse(resp); err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %q, want %q", got, payload)
	}
}

// An unknown-length body reserves the per-response cap, and the reservation
// lasts until the downstream client closes the body.
func TestModifyResponse_ReservationHeldUntilBodyClosed(t *testing.T) {
	ps := newTestProxyServer("http://localhost:3000", ":8080")
	before := injectBudget.used.Load()
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": {"text/html"}},
		Body:          io.NopCloser(bytes.NewReader([]byte(budgetTestHTML))),
		ContentLength: -1,
		Request:       httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil),
	}
	if err := ps.modifyResponse(resp); err != nil {
		t.Fatal(err)
	}
	if got := injectBudget.used.Load() - before; got != maxInjectBodyBytes {
		t.Fatalf("held %d bytes while body unread, want %d", got, int64(maxInjectBodyBytes))
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body.Close() // idempotent
	if got := injectBudget.used.Load() - before; got != 0 {
		t.Fatalf("%d bytes still reserved after Close", got)
	}
}
