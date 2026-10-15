package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every overlay route that reads a request body caps it, not just /inject: a
// body over the cap is refused with 413 instead of being buffered whole by
// the JSON decoder. The body is generated lazily so the test itself holds
// almost nothing in memory.
func TestOverlayRoutesCapRequestBody(t *testing.T) {
	o, _ := makeTestOverlay(&recordingWriter{})
	for _, tc := range []struct {
		route   string
		handler http.HandlerFunc
		prefix  string
		limit   int64
	}{
		{"/type", o.handleType, `{"text":"`, maxOverlayControlBody},
		{"/key", o.handleKey, `{"sequence":"`, maxOverlayControlBody},
		{"/toast", o.handleToast, `{"message":"`, maxOverlayControlBody},
		{"/event", o.handleEvent, `{"type":"panel_message","data":"`, maxOverlayEventBody},
	} {
		body := io.MultiReader(
			strings.NewReader(tc.prefix),
			io.LimitReader(repeatByte('a'), tc.limit),
			strings.NewReader(`"}`),
		)
		rec := httptest.NewRecorder()
		tc.handler(rec, httptest.NewRequest(http.MethodPost, tc.route, body))
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s with a body over %d bytes: got %d, want 413", tc.route, tc.limit, rec.Code)
		}
	}
}

// A body within the cap still works.
func TestOverlayToastAcceptsBodyUnderCap(t *testing.T) {
	o, _ := makeTestOverlay(&recordingWriter{})
	rec := httptest.NewRecorder()
	o.handleToast(rec, httptest.NewRequest(http.MethodPost, "/toast",
		bytes.NewReader([]byte(`{"message":"built"}`))))
	if rec.Code != http.StatusOK {
		t.Fatalf("/toast: got %d, want 200", rec.Code)
	}
}

type repeatByte byte

func (b repeatByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}
