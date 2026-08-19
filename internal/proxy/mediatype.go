package proxy

import (
	"mime"
	"strings"
)

// mediatype.go holds the ONE predicate family that decides what a content type
// means to this codebase. It exists because the previous shape — a
// strings.Contains(ct, "text/html") substring scan inside ShouldInject — was
// about to be duplicated onto more content-type surfaces by the public-plane
// subresource route (S6 review advisory 2). A substring scan admits
// "application/x-text/html-ish" and every other value that merely CONTAINS the
// token, which is tolerable for a dev-plane injection convenience and is not
// tolerable for a security allowlist on an anonymous route.
//
// Every decision here is an EXACT media-type comparison after
// mime.ParseMediaType, and every unknown value is refused.

// canonicalMediaType parses a Content-Type header value and returns its
// lower-cased media type with parameters stripped ("text/html; charset=utf-8" →
// "text/html"). An absent or unparseable header yields "" — deny-by-default, so
// every caller's switch refuses it.
//
// mime.ParseMediaType returns ErrInvalidMediaParameter together with a usable
// media type (e.g. duplicate charset parameters); that case keeps the media type
// because the parameters play no part in any decision made here. A parse failure
// that yields no media type at all is refused.
func canonicalMediaType(contentType string) string {
	if strings.TrimSpace(contentType) == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil && mt == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(mt))
}

// isHTMLDocumentMediaType reports whether a content type names an HTML document
// — the only thing this codebase will splice bytes into. Exact match: a value
// that merely contains "text/html" is not an HTML document.
func isHTMLDocumentMediaType(contentType string) bool {
	return canonicalMediaType(contentType) == "text/html"
}

// isAllowedSubresourceMediaType is the closed serve-side allowlist for the
// public-plane subresource route (spec §4c / INV-17): text/css, image/* except
// image/svg+xml, and font/* plus the legacy application/font-woff.
//
// Everything else — HTML, JS, JSON, and notably SVG — is refused loudly. SVG is
// excluded because it is a document format that can carry script and style;
// admitting it safely needs a forced Content-Disposition or a per-response
// sandboxing CSP, which is a decision, not a default.
//
// This function never admits a type into a script-executing context: the public
// plane's script-src is hash-sources-only (INV-12) and no branch below returns
// true for a JS media type.
func isAllowedSubresourceMediaType(contentType string) bool {
	mt := canonicalMediaType(contentType)
	switch {
	case mt == "text/css":
		return true
	case mt == "image/svg+xml":
		// Checked BEFORE the image/ prefix: SVG is not an image for this purpose.
		return false
	case strings.HasPrefix(mt, "image/"):
		return true
	case strings.HasPrefix(mt, "font/"):
		return true
	case mt == "application/font-woff":
		return true
	}
	return false
}
