package proxy

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

// TestModifyResponse_StackedContentEncodingPassesThrough pins RFC 9110 §8.4:
// Content-Encoding lists every coding applied, in order. The proxy has one
// decoder per response, so a stacked coding — in one header value or across
// several header lines — must reach the browser byte-for-byte with its header
// intact, uninjected. Before, "br, gzip" removed only the gzip layer, injected
// into Brotli bytes, and deleted the header (a corrupt page), and "gzip, br"
// ran gzip over Brotli bytes.
func TestModifyResponse_StackedContentEncodingPassesThrough(t *testing.T) {
	html := []byte("<html><head><title>T</title></head><body>hi</body></html>")
	brThenGzip := encodeGzip(t, encodeBrotli(t, html))
	gzipThenBr := encodeBrotli(t, encodeGzip(t, html))

	for _, tc := range []struct {
		name   string
		values []string
		body   []byte
	}{
		{"br then gzip, one value", []string{"br, gzip"}, brThenGzip},
		{"gzip then br, one value", []string{"gzip, br"}, gzipThenBr},
		{"br then gzip, two header lines", []string{"br", "gzip"}, brThenGzip},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := &ProxyServer{ListenAddr: ":8080"}
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type":     []string{"text/html"},
					"Content-Encoding": append([]string(nil), tc.values...),
				},
				Body: io.NopCloser(bytes.NewReader(tc.body)),
			}
			t.Cleanup(func() { resp.Body.Close() })
			if err := ps.modifyResponse(resp); err != nil {
				t.Fatalf("modifyResponse: %v", err)
			}
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.body) {
				t.Errorf("body changed: a stacked coding must pass through untouched")
			}
			if v := resp.Header.Values("Content-Encoding"); len(v) != len(tc.values) {
				t.Errorf("Content-Encoding = %q, want %q kept", v, tc.values)
			}
		})
	}
}

// TestContentCodings_ExactTokens: codings are compared as tokens, never as
// substrings ("brx" is not br), and identity is no coding at all.
func TestContentCodings_ExactTokens(t *testing.T) {
	for _, tc := range []struct {
		values []string
		want   string
		ok     bool
	}{
		{nil, "", true},
		{[]string{"identity"}, "", true},
		{[]string{" GZIP "}, "gzip", true},
		{[]string{"x-gzip"}, "gzip", true},
		{[]string{"br"}, "br", true},
		{[]string{"zstd"}, "zstd", true},
		{[]string{"deflate"}, "deflate", true},
		{[]string{"brx"}, "", false},
		{[]string{"compress"}, "", false},
		{[]string{"gzip, identity"}, "gzip", true},
		{[]string{"br, gzip"}, "", false},
	} {
		h := http.Header{}
		for _, v := range tc.values {
			h.Add("Content-Encoding", v)
		}
		got, ok := decodableContentCoding(h)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q: got (%q, %v), want (%q, %v)", tc.values, got, ok, tc.want, tc.ok)
		}
	}
}

// TestContentCodedBody_ReadsEveryHeaderLine: a coding on a second
// Content-Encoding line counts. Header.Get sees only the first line, so
// "identity" then "br" used to read as an identity body — captured as text
// by the dev proxy and spliced by the walkthrough plane.
func TestContentCodedBody_ReadsEveryHeaderLine(t *testing.T) {
	h := http.Header{}
	h.Add("Content-Type", "text/html")
	h.Add("Content-Encoding", "identity")
	h.Add("Content-Encoding", "br")
	if got := uncapturableBodyType(h); got == "" {
		t.Error("uncapturableBodyType: a br second line must mark the body content-encoded")
	}
	if err := checkUpstreamEncoding(&http.Response{Header: h}); err == nil {
		t.Error("checkUpstreamEncoding: a br second line must be refused")
	}
	plain := http.Header{"Content-Encoding": {"identity", " Identity "}}
	if got := uncapturableBodyType(plain); got != "" {
		t.Errorf("identity-only body marked %q", got)
	}
	if err := checkUpstreamEncoding(&http.Response{Header: plain}); err != nil {
		t.Errorf("identity-only body refused: %v", err)
	}
}
