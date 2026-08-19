package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestCanonicalMediaTypeIsExactNotSubstring pins the S6 advisory-2 fix: the
// predicate that decides "is this an HTML document" is an exact media-type
// comparison, so a value that merely CONTAINS "text/html" is refused. The old
// strings.Contains scan admitted every row marked false below.
func TestCanonicalMediaTypeIsExactNotSubstring(t *testing.T) {
	cases := []struct {
		ct   string
		html bool
	}{
		{"text/html", true},
		{"text/html; charset=utf-8", true},
		{"TEXT/HTML", true},
		{" text/html ", true},
		// Substring impostors — all admitted by the old scan, all refused now.
		{"application/x-text/html", false},
		{"text/html-fragment", false},
		{"multipart/mixed; boundary=text/html", false},
		{"", false},
		{";;;", false},
		{"application/xhtml+xml", false},
		{"application/javascript", false},
		{"image/png", false},
	}
	for _, c := range cases {
		if got := isHTMLDocumentMediaType(c.ct); got != c.html {
			t.Errorf("isHTMLDocumentMediaType(%q) = %v, want %v", c.ct, got, c.html)
		}
		// ShouldInject must stay in lockstep — it is the same question.
		if got := ShouldInject(c.ct); got != c.html {
			t.Errorf("ShouldInject(%q) = %v, want %v (must delegate to the shared predicate)", c.ct, got, c.html)
		}
	}
}

// TestSubresourceMediaTypeAllowlist is INV-17's closed allowlist. The refusals
// are the point: HTML and JS must never be servable through the subresource
// route, and SVG is excluded because it is a script-and-style-carrying document
// format, not an image for this purpose.
func TestSubresourceMediaTypeAllowlist(t *testing.T) {
	cases := []struct {
		ct    string
		allow bool
	}{
		{"text/css", true},
		{"text/css; charset=utf-8", true},
		{"image/png", true},
		{"image/jpeg", true},
		{"image/webp", true},
		{"font/woff2", true},
		{"application/font-woff", true},
		// Refused, loudly.
		{"image/svg+xml", false},
		{"IMAGE/SVG+XML; charset=utf-8", false},
		{"text/html", false},
		{"application/javascript", false},
		{"text/javascript", false},
		{"application/json", false},
		{"application/octet-stream", false},
		{"", false},
		{"text/css-ish", false},
	}
	for _, c := range cases {
		if got := isAllowedSubresourceMediaType(c.ct); got != c.allow {
			t.Errorf("isAllowedSubresourceMediaType(%q) = %v, want %v", c.ct, got, c.allow)
		}
	}
}

// TestUpstreamDocumentRefusesUnsolicitedContentEncoding is the S6 advisory-1
// fix. The upstream answers text/html with a Content-Encoding we never asked
// for; the bytes are therefore not HTML, and splicing our bundle tag into them
// would serve corrupt compressed bytes as a document. The refusal must be loud
// and must name the encoding, so the failure is attributable to this check
// rather than to some later parse.
func TestUpstreamDocumentRefusesUnsolicitedContentEncoding(t *testing.T) {
	for _, enc := range []string{"br", "deflate", "zstd", "BR"} {
		t.Run(enc, func(t *testing.T) {
			_, f, _ := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Content-Encoding", enc)
				io.WriteString(w, "\x1b\x2a\x00\x84not-really-html")
			}))
			body, err := f.fetchDocument(context.Background(), "https://example.com/app")
			if err == nil {
				t.Fatalf("Content-Encoding %q was accepted; encoded bytes would be spliced and served as HTML", enc)
			}
			if !strings.Contains(err.Error(), "Content-Encoding") {
				t.Fatalf("refusal is not attributable to the encoding check: %v", err)
			}
			if body != nil {
				t.Fatalf("refusal returned %d bytes alongside the error", len(body))
			}
		})
	}
}

// TestUpstreamDocumentAcceptsIdentityEncoding is the other half: the check must
// not refuse the normal case (absent or identity), or every proxied share breaks.
// Go's transport solicits gzip itself and deletes the header after decoding, so a
// gzip upstream also lands here as "no encoding".
func TestUpstreamDocumentAcceptsIdentityEncoding(t *testing.T) {
	for _, enc := range []string{"", "identity"} {
		_, f, _ := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if enc != "" {
				w.Header().Set("Content-Encoding", enc)
			}
			io.WriteString(w, upstreamDoc)
		}))
		if _, err := f.fetchDocument(context.Background(), "https://example.com/app"); err != nil {
			t.Fatalf("Content-Encoding %q was refused: %v", enc, err)
		}
	}
}
