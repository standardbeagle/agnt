package proxy

import (
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/publish"
)

// public_subresource.go is the splice-time half of the guarded subresource route
// (design spec 2026-08-19 §4a, INV-16/INV-19). It turns the references the
// DAEMON found in a guarded fetch of a share's publisher-named upstream into
// signed same-origin URLs. Nothing here ever admits a viewer-composed URL: the
// route serves only what this file minted, and it mints only what it read out of
// bytes the INV-13 guard already fetched.
//
// JS IS EXCLUDED BY DESIGN, PERMANENTLY — not "for now". The public plane's
// script-src is hash-sources-only (INV-12) and must stay that way, so a proxied
// script would be bytes nothing can execute; rewriting it would manufacture the
// impression that upstream JS runs on a published artifact when it never does,
// and would create a route whose only purpose is to serve executable content to
// a context that refuses it. <script src> is therefore left untouched.
//
// SVG is likewise excluded (INV-17, serve side): it is a document format that
// can carry script and style, not an image for this purpose.

const (
	// subresourceSubPath is the sub-route under /s/{token}. The reference is
	// carried in the query, but it is not viewer input in any meaningful sense:
	// it must match a MAC this daemon minted for this share at this depth.
	subresourceSubPath = "/sub"

	// maxPublicSubresourceBytes caps one subresource body (§4d). An over-cap body
	// is REFUSED, never truncated: a half stylesheet or a half font served as
	// complete is a broken demo presented as a real one, which is the silent
	// failure this plane's whole posture exists to avoid.
	maxPublicSubresourceBytes = 2 << 20

	// maxSubresourceRefsPerDocument caps how many DISTINCT references one
	// document (or one stylesheet) has rewritten. Past the cap references are
	// left alone — they then fail under CSP exactly as they do today — and the
	// refusal is logged. The cap bounds amplification: it is the multiplier
	// between one inbound artifact GET and the outbound fetches it can provoke.
	maxSubresourceRefsPerDocument = 64

	// maxSubresourceDepth is the nesting cap (§4d, owner-resolved Q4 at depth 2):
	// document (0) → CSS (1) → font/image (2). A stylesheet reached at depth 2
	// still SERVES, but its own references are not rewritten and the refusal is
	// logged — the same "leave it un-rewritten, say so out loud" shape as the
	// reference cap, rather than a second refusal dialect.
	maxSubresourceDepth = 2
)

// Tag/attribute matchers. These are deliberately narrow: the rewriter replaces
// an attribute VALUE with a percent-encoded same-origin path and never inserts
// markup, so the worst case of a mis-parse is a reference left un-rewritten
// (today's behaviour) rather than injected content. A full HTML parse over
// hostile input would be strictly more parser surface for that same outcome
// (spec §3.3's reasoning, applied to the reference rewriter).
var (
	linkTagRe  = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	imgTagRe   = regexp.MustCompile(`(?is)<img\b[^>]*>`)
	styleTagRe = regexp.MustCompile(`(?is)(<style\b[^>]*>)(.*?)(</style>)`)

	hrefAttrRe = regexp.MustCompile(`(?is)(\bhref\s*=\s*)("[^"]*"|'[^']*'|[^\s>]+)`)
	srcAttrRe  = regexp.MustCompile(`(?is)(\bsrc\s*=\s*)("[^"]*"|'[^']*'|[^\s>]+)`)
	relAttrRe  = regexp.MustCompile(`(?is)\brel\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)

	cssURLRe    = regexp.MustCompile(`(?is)url\(\s*("[^"]*"|'[^']*'|[^)'"\s]*)\s*\)`)
	cssImportRe = regexp.MustCompile(`(?is)(@import\s+)("[^"]*"|'[^']*')`)
)

// subresourceRewriter mints signed references for one document or one
// stylesheet. It is single-use and not safe for concurrent use.
type subresourceRewriter struct {
	base    *url.URL
	token   string
	shareID string
	signer  *publish.SubresourceSigner
	// depth is the depth of the references being MINTED (parent depth + 1).
	depth   int
	seen    map[string]string
	refused int
}

// newSubresourceRewriter returns a rewriter for references found in a resource
// fetched from baseURL, or ok=false when references must NOT be rewritten:
// no signer (fail closed — an unsigned reference would be an unbound relay), an
// unusable base, or a depth past the nesting cap.
func newSubresourceRewriter(baseURL, token, shareID string, signer *publish.SubresourceSigner, depth int) (*subresourceRewriter, bool) {
	if signer == nil || token == "" || shareID == "" {
		return nil, false
	}
	if depth < 1 || depth > maxSubresourceDepth {
		return nil, false
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, false
	}
	return &subresourceRewriter{
		base:    base,
		token:   token,
		shareID: shareID,
		signer:  signer,
		depth:   depth,
		seen:    make(map[string]string),
	}, true
}

// ref resolves one raw reference against the base and returns the signed
// same-origin path to serve it through, or ok=false to leave it untouched.
//
// Only https survives: that is what CheckUpstreamOrigin will demand at fetch
// time anyway, and it means data:, blob:, http:, mailto:, and fragment-only
// references are never turned into a daemon fetch.
func (rw *subresourceRewriter) ref(raw string) (string, bool) {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" || strings.HasPrefix(raw, "#") {
		return "", false
	}
	u, err := rw.base.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", false
	}
	u.Fragment = ""
	abs := u.String()
	if got, ok := rw.seen[abs]; ok {
		return got, true
	}
	if len(rw.seen) >= maxSubresourceRefsPerDocument {
		rw.refused++
		return "", false
	}
	q := url.Values{}
	q.Set("u", abs)
	q.Set("d", strconv.Itoa(rw.depth))
	q.Set("sig", rw.signer.Sign(rw.shareID, abs, rw.depth))
	signed := sharePrefix + rw.token + subresourceSubPath + "?" + q.Encode()
	rw.seen[abs] = signed
	return signed, true
}

// rewriteDocument rewrites the subresource references of a fetched upstream
// HTML document: stylesheet/icon <link href>, <img src>, and the url()/@import
// references inside inline <style> blocks. Everything else — <script src> above
// all — is passed through byte-for-byte.
func (rw *subresourceRewriter) rewriteDocument(body []byte) []byte {
	out := linkTagRe.ReplaceAllFunc(body, func(tag []byte) []byte {
		if !linkRelIsSubresource(string(tag)) {
			return tag
		}
		return rw.rewriteAttr(tag, hrefAttrRe)
	})
	out = imgTagRe.ReplaceAllFunc(out, func(tag []byte) []byte {
		return rw.rewriteAttr(tag, srcAttrRe)
	})
	out = styleTagRe.ReplaceAllFunc(out, func(block []byte) []byte {
		m := styleTagRe.FindSubmatch(block)
		if m == nil {
			return block
		}
		// The contents of <style> are raw CSS, not HTML text, so the CSS
		// rewriter's unescaped output is correct here.
		var b []byte
		b = append(b, m[1]...)
		b = append(b, rw.rewriteCSS(m[2])...)
		b = append(b, m[3]...)
		return b
	})
	rw.logRefusals("document")
	return out
}

// rewriteCSS rewrites url() and quoted @import references in a stylesheet.
func (rw *subresourceRewriter) rewriteCSS(css []byte) []byte {
	out := cssImportRe.ReplaceAllFunc(css, func(m []byte) []byte {
		g := cssImportRe.FindSubmatch(m)
		if g == nil {
			return m
		}
		signed, ok := rw.ref(unquoteAttr(string(g[2])))
		if !ok {
			return m
		}
		return []byte(string(g[1]) + `"` + signed + `"`)
	})
	out = cssURLRe.ReplaceAllFunc(out, func(m []byte) []byte {
		g := cssURLRe.FindSubmatch(m)
		if g == nil {
			return m
		}
		signed, ok := rw.ref(unquoteAttr(string(g[1])))
		if !ok {
			return m
		}
		return []byte(`url("` + signed + `")`)
	})
	rw.logRefusals("stylesheet")
	return out
}

// rewriteAttr replaces one attribute's value inside a single tag.
func (rw *subresourceRewriter) rewriteAttr(tag []byte, attr *regexp.Regexp) []byte {
	return attr.ReplaceAllFunc(tag, func(m []byte) []byte {
		g := attr.FindSubmatch(m)
		if g == nil {
			return m
		}
		signed, ok := rw.ref(unquoteAttr(string(g[2])))
		if !ok {
			return m
		}
		// html.EscapeString because the signed path carries "&" between query
		// parameters; an unescaped "&" in an HTML attribute is a parse hazard even
		// where browsers tolerate it. The path itself is percent-encoded by
		// url.Values.Encode, so it can carry no quote or angle bracket.
		return []byte(string(g[1]) + `"` + html.EscapeString(signed) + `"`)
	})
}

// logRefusals surfaces a cap refusal loudly rather than dropping it (INV-19:
// no cap is enforced silently). It reports once per rewrite pass.
func (rw *subresourceRewriter) logRefusals(what string) {
	if rw.refused == 0 {
		return
	}
	debug.Log("publish", "public plane: %d %s subresource reference(s) left un-rewritten — the per-resource cap of %d distinct references was reached; they will be refused by CSP as before",
		rw.refused, what, maxSubresourceRefsPerDocument)
	rw.refused = 0
}

// unquoteAttr strips one layer of matching quotes from an attribute or CSS value.
func unquoteAttr(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// linkRelIsSubresource reports whether a <link> tag names a rel this route
// serves. Only stylesheets and icons: a <link rel="preload" as="script"> or a
// rel we do not recognise is left alone rather than speculatively proxied.
func linkRelIsSubresource(tag string) bool {
	m := relAttrRe.FindStringSubmatch(tag)
	if m == nil {
		return false
	}
	for _, tok := range strings.Fields(strings.ToLower(unquoteAttr(m[1]))) {
		switch tok {
		case "stylesheet", "icon", "apple-touch-icon", "mask-icon":
			return true
		}
	}
	return false
}

// rewriteUpstreamDocumentRefs is the entry point serveProxiedArtifact uses. A
// rewriter that cannot be built (no signer) means NO rewriting: the document is
// served exactly as it is today, with its subresources refused by CSP. That is a
// degraded demo, never an unbound relay.
func rewriteUpstreamDocumentRefs(body []byte, baseURL, token, shareID string, signer *publish.SubresourceSigner) []byte {
	rw, ok := newSubresourceRewriter(baseURL, token, shareID, signer, 1)
	if !ok {
		return body
	}
	return rw.rewriteDocument(body)
}

// rewriteUpstreamCSSRefs rewrites a served stylesheet's own references one level
// deeper. At the nesting cap the stylesheet is served unchanged and the refusal
// is logged: the resource itself is fine, only the chain stops.
func rewriteUpstreamCSSRefs(css []byte, baseURL, token, shareID string, signer *publish.SubresourceSigner, parentDepth int) []byte {
	rw, ok := newSubresourceRewriter(baseURL, token, shareID, signer, parentDepth+1)
	if !ok {
		debug.Log("publish", "public plane: stylesheet at depth %d serves without nested rewriting — the nesting cap of %d stops the chain here", parentDepth, maxSubresourceDepth)
		return css
	}
	return rw.rewriteCSS(css)
}
