package proxy

import (
	"context"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/publish"
)

// public_subresource_test.go covers the guarded subresource route (design spec
// 2026-08-19, INV-16..19). The guard is OBSERVED, never disabled
// (.claude/rules/publish-security-review-lessons.md §7): every end-to-end case
// runs through testUpstream, whose resolver answers with a genuinely public
// address and whose dialer asserts it was handed exactly that address. Refusal
// cases assert the guard's own verdict AND that the dialer was never reached
// (§8) — "it errored" is not evidence that the control refused it.

const subPrefixPath = "/sub"

// subShareID matches the id fakeVerifier hands back in upstreamHandler.
const subShareID = "share-1"

// signedSubPath builds the route the daemon itself would have minted, using the
// handler's own signer. A test that hand-rolls a signature is testing its own
// arithmetic; a test that borrows the handler's signer is testing the route.
func signedSubPath(t *testing.T, h *PublicHandler, absURL string, depth int) string {
	t.Helper()
	if h.signer == nil {
		t.Fatal("handler has no subresource signer")
	}
	q := url.Values{}
	q.Set("u", absURL)
	q.Set("d", strconv.Itoa(depth))
	q.Set("sig", h.signer.Sign(subShareID, absURL, depth))
	return sharePrefix + validToken + subPrefixPath + "?" + q.Encode()
}

// upstreamServing returns a guarded fetcher whose upstream answers a fixed map
// of path → (content-type, body), and the recorded dial log.
func upstreamServing(t *testing.T, routes map[string]struct {
	ct   string
	body string
}) (*guardedUpstreamFetcher, *[]string) {
	t.Helper()
	_, f, dialed := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", route.ct)
		io.WriteString(w, route.body)
	}))
	return f, dialed
}

// TestProxiedDocumentRewritesSubresourceRefs is the splice-time half of INV-16:
// the daemon rewrites the references IT found in the fetched document into
// signed same-origin URLs. JS is deliberately not rewritten — script-src is
// hash-only (INV-12), so proxying it would serve bytes nothing can execute.
func TestProxiedDocumentRewritesSubresourceRefs(t *testing.T) {
	doc := `<!DOCTYPE html><html><head>` +
		`<link rel="stylesheet" href="/site.css">` +
		`<link rel="icon" href="/favicon.png">` +
		`<script src="/app.js"></script>` +
		`<style>body{background:url("/bg.png")}</style>` +
		`</head><body><img src="https://cdn.example.com/hero.png"><img src="data:image/gif;base64,AAAA"></body></html>`

	f := &countingFetcher{body: []byte(doc)}
	h := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), f)

	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("proxied artifact: got %d, want 200", w.Code)
	}
	body := w.Body.String()

	// Every admitted reference became a signed same-origin path.
	for _, want := range []string{
		"https://demo.example.com/site.css",
		"https://demo.example.com/favicon.png",
		"https://demo.example.com/bg.png",
		"https://cdn.example.com/hero.png",
	} {
		if !strings.Contains(body, url.QueryEscape(want)) {
			t.Fatalf("reference %q was not rewritten into a signed subresource URL:\n%s", want, body)
		}
	}
	// JS is untouched, by design and permanently (INV-12).
	if !strings.Contains(body, `<script src="/app.js">`) {
		t.Fatalf("the upstream script src was rewritten; JS must never be proxied:\n%s", body)
	}
	// data: URLs need no proxy and must not be signed into one.
	if strings.Contains(body, url.QueryEscape("data:image/gif")) {
		t.Fatalf("a data: URL was rewritten into a subresource fetch:\n%s", body)
	}
	// The rewritten refs must actually verify — a rewrite that emits an
	// unverifiable URL is a 404 generator, not a feature.
	for _, ref := range extractSubRefs(body) {
		u, err := url.Parse(ref)
		if err != nil {
			t.Fatalf("rewritten ref does not parse: %q", ref)
		}
		q := u.Query()
		if !h.signer.Verify(subShareID, q.Get("u"), 1, q.Get("sig")) {
			t.Fatalf("rewritten ref carries a signature that does not verify: %q", ref)
		}
		if q.Get("d") != "1" {
			t.Fatalf("document-level ref minted at depth %q, want 1", q.Get("d"))
		}
	}
}

// extractSubRefs pulls the signed subresource paths out of a served document.
func extractSubRefs(body string) []string {
	var out []string
	needle := sharePrefix + validToken + subPrefixPath + "?"
	for i := 0; ; {
		j := strings.Index(body[i:], needle)
		if j < 0 {
			return out
		}
		start := i + j
		end := start
		for end < len(body) && body[end] != '"' && body[end] != '\'' && body[end] != ')' && body[end] != ' ' {
			end++
		}
		// HTML attribute values carry &amp; between query parameters; a browser
		// unescapes before requesting, so the test does too.
		out = append(out, html.UnescapeString(body[start:end]))
		i = end
	}
}

// TestSubresourceRouteServesGuardedCSS is the serve-time half: a signed
// reference is fetched through the SAME guard as the document (dial pinned to
// the validated address), served under the public header policy, and its own
// url() references are rewritten one level deeper.
func TestSubresourceRouteServesGuardedCSS(t *testing.T) {
	f, dialed := upstreamServing(t, map[string]struct{ ct, body string }{
		"/site.css": {"text/css; charset=utf-8", `@font-face{src:url("/f.woff2")}body{background:url(/bg.png)}`},
	})
	h := upstreamHandler(t, upstreamRevision("https://example.com/app"), f)

	w := do(h, http.MethodGet, signedSubPath(t, h, "https://example.com/site.css", 1), "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("subresource: got %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if len(*dialed) != 1 {
		t.Fatalf("dialed %v — the subresource fetch must go through the guarded, pinned dial exactly once", *dialed)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("subresource content-type %q", ct)
	}
	css := w.Body.String()
	for _, want := range []string{"https://example.com/f.woff2", "https://example.com/bg.png"} {
		if !strings.Contains(css, url.QueryEscape(want)) {
			t.Fatalf("nested CSS reference %q not rewritten:\n%s", want, css)
		}
	}
	for _, ref := range extractSubRefs(css) {
		u, _ := url.Parse(ref)
		if got := u.Query().Get("d"); got != "2" {
			t.Fatalf("CSS-level ref minted at depth %q, want 2", got)
		}
	}
	// INV-18: a subresource response keeps the bare style-src, and script-src
	// stays hash-only everywhere.
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'self';") {
		t.Fatalf("subresource style-src is not bare 'self': %s", csp)
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("subresource response carries unsafe-inline: %s", csp)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("subresource response is missing nosniff")
	}
}

// TestSubresourceRouteRefusesUnboundReferences is INV-16's refusal half. Each
// row is a viewer trying to compose a fetch the daemon never authorised. The
// assertion with teeth is the dial log: the refusal must precede the socket.
func TestSubresourceRouteRefusesUnboundReferences(t *testing.T) {
	f, dialed := upstreamServing(t, map[string]struct{ ct, body string }{
		"/site.css": {"text/css", "body{}"},
	})
	h := upstreamHandler(t, upstreamRevision("https://example.com/app"), f)
	good := signedSubPath(t, h, "https://example.com/site.css", 1)
	goodQ, _ := url.Parse(good)

	swap := func(key, val string) string {
		q := goodQ.Query()
		q.Set(key, val)
		return sharePrefix + validToken + subPrefixPath + "?" + q.Encode()
	}

	cases := map[string]string{
		"no signature":       sharePrefix + validToken + subPrefixPath + "?u=" + url.QueryEscape("https://example.com/site.css") + "&d=1",
		"forged signature":   swap("sig", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		"viewer-chosen url":  swap("u", "https://evil.example.com/x.css"),
		"metadata url":       swap("u", "https://169.254.169.254/latest/meta-data/"),
		"loopback url":       swap("u", "https://127.0.0.1/x.css"),
		"depth downgrade":    swap("d", "2"),
		"depth out of range": swap("d", "9"),
		"no query at all":    sharePrefix + validToken + subPrefixPath,
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			before := len(*dialed)
			w := do(h, http.MethodGet, path, "", nil)
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s: got %d, want 404", name, w.Code)
			}
			if len(*dialed) != before {
				t.Fatalf("%s: a socket was opened for an unauthorised reference (dialed %v)", name, *dialed)
			}
		})
	}
}

// TestSubresourceRouteRefusesDisallowedContentType is INV-17: the upstream
// answers a signed reference with a type the allowlist refuses. The body must
// never be relayed, and the refusal must be attributable to the allowlist.
func TestSubresourceRouteRefusesDisallowedContentType(t *testing.T) {
	const marker = "SHOULD-NEVER-BE-RELAYED"
	for _, ct := range []string{
		"text/html",
		"application/javascript",
		"text/javascript",
		"image/svg+xml",
		"application/json",
		"application/octet-stream",
		"",
	} {
		t.Run(ct, func(t *testing.T) {
			f, _ := upstreamServing(t, map[string]struct{ ct, body string }{
				"/thing": {ct, marker},
			})
			h := upstreamHandler(t, upstreamRevision("https://example.com/app"), f)
			w := do(h, http.MethodGet, signedSubPath(t, h, "https://example.com/thing", 1), "", nil)
			if w.Code != http.StatusBadGateway {
				t.Fatalf("content-type %q: got %d, want 502", ct, w.Code)
			}
			if strings.Contains(w.Body.String(), marker) {
				t.Fatalf("content-type %q: the refused body was relayed to the viewer", ct)
			}
		})
	}
	// Provenance: the refusal names the allowlist, so it cannot be confused with
	// a transport failure.
	f, _ := upstreamServing(t, map[string]struct{ ct, body string }{
		"/thing": {"text/html", marker},
	})
	if _, _, err := f.fetchSubresource(context.Background(), "https://example.com/thing"); err == nil ||
		!strings.Contains(err.Error(), "content-type") {
		t.Fatalf("refusal is not attributable to the content-type allowlist: %v", err)
	}
}

// TestSubresourceRouteRefusesOverCapBody is INV-19: a body past the per-
// subresource cap is REFUSED, never truncated. A truncated stylesheet or font
// served as complete is a broken demo presented as a real one.
func TestSubresourceRouteRefusesOverCapBody(t *testing.T) {
	big := strings.Repeat("a", int(maxPublicSubresourceBytes)+1024)
	f, _ := upstreamServing(t, map[string]struct{ ct, body string }{
		"/big.css": {"text/css", big},
	})
	h := upstreamHandler(t, upstreamRevision("https://example.com/app"), f)
	w := do(h, http.MethodGet, signedSubPath(t, h, "https://example.com/big.css", 1), "", nil)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("over-cap subresource: got %d, want 502", w.Code)
	}
	if w.Body.Len() >= 1024 {
		t.Fatalf("over-cap subresource returned a %d-byte body — a partial relay, not a refusal", w.Body.Len())
	}
}

// TestSubresourceRouteDiesWithRevoke is INV-4 on the new route: revoke kills the
// whole /s/{token} family atomically, and it does so BEFORE any outbound work.
func TestSubresourceRouteDiesWithRevoke(t *testing.T) {
	f, dialed := upstreamServing(t, map[string]struct{ ct, body string }{
		"/site.css": {"text/css", "body{}"},
	})
	h := upstreamHandler(t, upstreamRevision("https://example.com/app"), f)
	path := signedSubPath(t, h, "https://example.com/site.css", 1)

	if w := do(h, http.MethodGet, path, "", nil); w.Code != http.StatusOK {
		t.Fatalf("pre-revoke subresource: got %d, want 200", w.Code)
	}
	dialsBefore := len(*dialed)

	// Revoke: the verifier stops recognising the token, exactly as the store does.
	h.verifier = &fakeVerifier{token: "", rev: nil, id: ""}

	if w := do(h, http.MethodGet, path, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("post-revoke subresource: got %d, want 404", w.Code)
	}
	if len(*dialed) != dialsBefore {
		t.Fatalf("a revoked share still reached the network (dialed %v)", *dialed)
	}
}

// TestSubresourceRouteRejectsWrongMethod keeps the route inside the same
// method policy as the rest of the share family.
func TestSubresourceRouteRejectsWrongMethod(t *testing.T) {
	f, _ := upstreamServing(t, map[string]struct{ ct, body string }{
		"/site.css": {"text/css", "body{}"},
	})
	h := upstreamHandler(t, upstreamRevision("https://example.com/app"), f)
	w := do(h, http.MethodPost, signedSubPath(t, h, "https://example.com/site.css", 1), "", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST to subresource route: got %d, want 405", w.Code)
	}
}

// TestSubresourceRouteRefusesForSelfContainedShare: a share that names no
// upstream has no subresources to proxy, so the route must not exist for it —
// otherwise it is a relay attached to a share that never authorised any fetch.
func TestSubresourceRouteRefusesForSelfContainedShare(t *testing.T) {
	f, dialed := upstreamServing(t, map[string]struct{ ct, body string }{
		"/site.css": {"text/css", "body{}"},
	})
	h := upstreamHandler(t, &publish.PublishedWalkthrough{Version: publish.SchemaV1, Title: "no upstream"}, f)
	w := do(h, http.MethodGet, signedSubPath(t, h, "https://example.com/site.css", 1), "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("subresource on a self-contained share: got %d, want 404", w.Code)
	}
	if len(*dialed) != 0 {
		t.Fatalf("a self-contained share reached the network (dialed %v)", *dialed)
	}
}

// TestSubresourceGuardRechecksEveryRedirectHop: the canonical SSRF bypass on the
// new route. A signed, public reference redirects to link-local space. Both
// assertions matter — the refusal names the guard's verdict, and the forbidden
// hop never reached the dialer.
func TestSubresourceGuardRechecksEveryRedirectHop(t *testing.T) {
	_, f, dialed := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	_, _, err := f.fetchSubresource(context.Background(), "https://example.com/site.css")
	if err == nil {
		t.Fatal("a subresource redirect into link-local space was followed")
	}
	if !strings.Contains(err.Error(), "upstream refused") {
		t.Fatalf("refusal is not attributable to the origin guard: %v", err)
	}
	if len(*dialed) != 1 {
		t.Fatalf("the forbidden hop reached the dialer: %v", *dialed)
	}
}

// TestSubresourceRefusesUnsolicitedEncoding: the S6 advisory-1 behaviour is
// inherited by the subresource reader, not just the document reader.
func TestSubresourceRefusesUnsolicitedEncoding(t *testing.T) {
	_, f, _ := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		w.Header().Set("Content-Encoding", "br")
		io.WriteString(w, "\x1b\x2a\x00\x84")
	}))
	_, _, err := f.fetchSubresource(context.Background(), "https://example.com/site.css")
	if err == nil || !strings.Contains(err.Error(), "Content-Encoding") {
		t.Fatalf("subresource reader accepted an unsolicited encoding: %v", err)
	}
}

// TestSubresourceReferenceCapLeavesExcessUnrewritten is INV-19's per-document
// cap. It is the multiplier between one inbound artifact GET and the outbound
// fetches it can provoke, so it must hold exactly: the first 64 distinct
// references are bound, the rest stay as the upstream wrote them (and are then
// refused by CSP exactly as they are today).
func TestSubresourceReferenceCapLeavesExcessUnrewritten(t *testing.T) {
	var doc strings.Builder
	doc.WriteString("<html><body>")
	const total = maxSubresourceRefsPerDocument + 6
	for i := 0; i < total; i++ {
		doc.WriteString(`<img src="/i` + strconv.Itoa(i) + `.png">`)
	}
	doc.WriteString("</body></html>")

	f := &countingFetcher{body: []byte(doc.String())}
	h := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), f)
	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("proxied artifact: got %d, want 200", w.Code)
	}
	body := w.Body.String()

	if got := len(extractSubRefs(body)); got != maxSubresourceRefsPerDocument {
		t.Fatalf("rewrote %d references, want exactly the cap of %d", got, maxSubresourceRefsPerDocument)
	}
	// The excess is left verbatim — not dropped, not truncated, not rewritten.
	for i := maxSubresourceRefsPerDocument; i < total; i++ {
		if !strings.Contains(body, `<img src="/i`+strconv.Itoa(i)+`.png">`) {
			t.Fatalf("reference #%d past the cap was altered; over-cap references must be left exactly as the upstream wrote them", i)
		}
	}
}

// TestSubresourceRepeatedReferenceCostsOneSlot: the cap counts DISTINCT
// references, so a page repeating one sprite 200 times still costs one slot and
// one outbound fetch.
func TestSubresourceRepeatedReferenceCostsOneSlot(t *testing.T) {
	doc := "<html><body>" + strings.Repeat(`<img src="/same.png">`, 200) + "</body></html>"
	f := &countingFetcher{body: []byte(doc)}
	h := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), f)
	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)

	refs := extractSubRefs(w.Body.String())
	if len(refs) != 200 {
		t.Fatalf("rewrote %d occurrences, want all 200", len(refs))
	}
	distinct := map[string]bool{}
	for _, r := range refs {
		distinct[r] = true
	}
	if len(distinct) != 1 {
		t.Fatalf("one repeated reference produced %d distinct signed URLs", len(distinct))
	}
}

// TestSubresourceNestingCapStopsTheChain is INV-19's depth bound: a stylesheet
// reached at the cap still serves, but its own references are NOT rewritten, so
// the chain document → CSS → asset cannot be walked deeper.
func TestSubresourceNestingCapStopsTheChain(t *testing.T) {
	f, _ := upstreamServing(t, map[string]struct{ ct, body string }{
		"/deep.css": {"text/css", `@import "https://example.com/deeper.css";body{background:url(/x.png)}`},
	})
	h := upstreamHandler(t, upstreamRevision("https://example.com/app"), f)

	w := do(h, http.MethodGet, signedSubPath(t, h, "https://example.com/deep.css", maxSubresourceDepth), "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("stylesheet at the nesting cap: got %d, want 200 (the resource serves; only the chain stops)", w.Code)
	}
	css := w.Body.String()
	if refs := extractSubRefs(css); len(refs) != 0 {
		t.Fatalf("a stylesheet at the nesting cap minted %d deeper references: %v", len(refs), refs)
	}
	if !strings.Contains(css, `url(/x.png)`) {
		t.Fatalf("the un-rewritten reference was altered anyway: %s", css)
	}
}
