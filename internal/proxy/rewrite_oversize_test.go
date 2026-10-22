package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// closeCounter records Close calls on the origin body.
type closeCounter struct {
	io.Reader
	closed int
}

func (c *closeCounter) Close() error { c.closed++; return nil }

// An unknown-length HTML body past maxInjectBodyBytes is handed through
// uninjected. Closing the replacement body must close the origin body once and
// return; it used to call itself (the closure read resp.Body after it was
// reassigned) and overflow the stack.
func TestModifyResponse_OversizedChunkedBodyClosesOrigin(t *testing.T) {
	ps := newTestProxyServer("http://localhost:3000", ":8080")

	payload := bytes.Repeat([]byte("a"), maxInjectBodyBytes+1024)
	origin := &closeCounter{Reader: bytes.NewReader(payload)}
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:          origin,
		ContentLength: -1, // chunked: size unknown until read
		Request:       httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil),
	}

	if err := ps.modifyResponse(resp); err != nil {
		t.Fatalf("modifyResponse: %v", err)
	}

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read passthrough body: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("passthrough body altered: got %d bytes, want %d", len(got), len(payload))
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if origin.closed != 1 {
		t.Fatalf("origin body closed %d times, want 1", origin.closed)
	}
}
