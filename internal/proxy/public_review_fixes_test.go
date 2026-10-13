package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/publish"
)

// Regression tests for the publish / public-plane review findings. Refusal
// cases assert the control's own verdict AND that the dangerous side effect (an
// upstream fetch, a body read) was never reached; each carries a near miss that
// must still be served (.claude/skills/security-boundary-review).

// --- #3: reference decoding ---------------------------------------------------

// TestSubresourceRefsAreDecodedAsTheBrowserWould pins the exact URL the daemon
// signs and will fetch for each reference. The earlier rewriter ran the TEXT
// entity rule over every value, so "&timestamp=" became "×tamp=" and the daemon
// fetched a URL the page never named.
func TestSubresourceRefsAreDecodedAsTheBrowserWould(t *testing.T) {
	doc := `<!DOCTYPE html><html><head>` +
		`<link rel="stylesheet" href="/s.css?a=1&section=2&currency=usd">` +
		`<style>b{background:url(/bg.png?a=1&amp;b=2&times=3)}</style>` +
		`</head><body>` +
		`<img src="/img.png?w=100&timestamp=5&region=us&copy=1&param=2&notify=3">` +
		`<img src="/a.png?x=1&amp;y=2">` +
		`<img data-src="/lazy.png" src="/real.png">` +
		`<img data-src="/only-lazy.png">` +
		`<link rel="stylesheet" data-href="/lazy.css" href="/real.css">` +
		`</body></html>`

	f := &countingFetcher{body: []byte(doc)}
	h := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), f)
	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("proxied artifact: got %d, want 200", w.Code)
	}
	body := w.Body.String()

	got := mintedTargets(t, body)
	sort.Strings(got)
	want := []string{
		// Legacy entity names followed by "=" or a letter are NOT references in
		// an attribute: every parameter survives byte-for-byte.
		"https://demo.example.com/img.png?w=100&timestamp=5&region=us&copy=1&param=2&notify=3",
		"https://demo.example.com/s.css?a=1&section=2&currency=usd",
		// A real character reference in an attribute IS decoded.
		"https://demo.example.com/a.png?x=1&y=2",
		// CSS has no character references: "&amp;" is five literal characters.
		"https://demo.example.com/bg.png?a=1&amp;b=2&times=3",
		// Only the attribute named exactly src/href is a reference.
		"https://demo.example.com/real.png",
		"https://demo.example.com/real.css",
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("minted references:\n  got  %q\n  want %q", got, want)
	}
	for _, lazy := range []string{"lazy.png", "only-lazy.png", "lazy.css"} {
		for _, target := range got {
			if strings.Contains(target, lazy) {
				t.Fatalf("a data-* attribute was rewritten into a daemon fetch: %q", target)
			}
		}
	}
	// The data-* attributes themselves are left as the upstream wrote them.
	if !strings.Contains(body, `data-src="/only-lazy.png"`) {
		t.Fatalf("a tag with nothing to rewrite must pass through byte-for-byte:\n%s", body)
	}

	// §16: the MAC covers exactly the string the route will fetch. Every minted
	// reference verifies against its own "u" with no re-normalization.
	revision := revisionForToken(h, validToken)
	for _, ref := range extractSubRefs(body) {
		u, err := url.Parse(ref)
		if err != nil {
			t.Fatalf("minted reference does not parse: %q", ref)
		}
		q := u.Query()
		if !h.signer.Verify(subShareID, revision, q.Get("u"), 1, q.Get("sig")) {
			t.Fatalf("minted reference does not verify against the URL it carries: %q", ref)
		}
	}
}

// --- #4: revision binding, HEAD, origin key, concurrency ----------------------

// TestStaleRevisionSubresourceReferenceIsRefused: a reference minted for one
// revision must stop being fetchable the moment the share is re-published.
func TestStaleRevisionSubresourceReferenceIsRefused(t *testing.T) {
	f := &countingFetcher{body: []byte("a{}")}
	v := &fakeVerifier{token: validToken, rev: upstreamRevision("https://demo.example.com/app"), id: subShareID}
	h := NewPublicHandler(v, nil, 0)
	h.upstream = f

	ref := signedSubPath(t, h, "https://old.example.com/site.css", 1)
	if w := do(h, http.MethodGet, ref, "", nil); w.Code != http.StatusOK || f.calls != 1 {
		t.Fatalf("premise broken: the reference must be served for the revision it was minted for (code %d, fetches %d)", w.Code, f.calls)
	}

	// The publisher edits the share: same share id, same token, new content.
	v.rev = upstreamRevision("https://new.example.com/app")

	w := do(h, http.MethodGet, ref, "", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("a reference minted for the previous revision: got %d, want 404", w.Code)
	}
	if f.calls != 1 {
		t.Fatalf("a stale reference reached the fetcher (%d fetches, want 1): the refusal must come before any outbound work", f.calls)
	}
	// Near miss: a reference minted for the NEW revision is served.
	if w := do(h, http.MethodGet, signedSubPath(t, h, "https://new.example.com/site.css", 1), "", nil); w.Code != http.StatusOK {
		t.Fatalf("a reference for the current revision: got %d, want 200", w.Code)
	}
}

func TestHEADOnAProxiedShareMakesNoUpstreamRequest(t *testing.T) {
	f := &countingFetcher{body: []byte(upstreamDoc)}
	h := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), f)

	w := do(h, http.MethodHead, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("HEAD artifact: got %d, want 200", w.Code)
	}
	if f.calls != 0 {
		t.Fatalf("HEAD on the artifact made %d upstream fetch(es); it must make none", f.calls)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("HEAD artifact Content-Type = %q", ct)
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'sha256-") {
		t.Fatalf("HEAD artifact must still carry the public policy, got %q", csp)
	}

	ref := signedSubPath(t, h, "https://demo.example.com/site.css", 1)
	if w := do(h, http.MethodHead, ref, "", nil); w.Code != http.StatusOK {
		t.Fatalf("HEAD on a verified subresource reference: got %d, want 200", w.Code)
	}
	if f.calls != 0 {
		t.Fatalf("HEAD on a subresource made %d upstream fetch(es); it must make none", f.calls)
	}
	// HEAD is not a way round the reference check.
	forged := strings.Replace(ref, "site.css", "other.css", 1)
	if w := do(h, http.MethodHead, forged, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("HEAD on an unbound reference: got %d, want 404", w.Code)
	}
	// Near miss: GET still fetches.
	if w := do(h, http.MethodGet, sharePrefix+validToken, "", nil); w.Code != http.StatusOK || f.calls != 1 {
		t.Fatalf("GET artifact: code %d, fetches %d — want 200 and exactly 1", w.Code, f.calls)
	}
}

func TestUpstreamOriginKeyIsNormalized(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://example.com/a", "https://example.com"},
		{"https://EXAMPLE.com/a", "https://example.com"},
		{"HTTPS://Example.Com/a", "https://example.com"},
		{"https://example.com:443/a", "https://example.com"},
		{"https://example.com./a", "https://example.com"},
		{"https://EXAMPLE.com.:443/a?x=1", "https://example.com"},
		// A non-default port is a different origin and keeps its own budget.
		{"https://example.com:8443/a", "https://example.com:8443"},
		// 80 is not https's default port.
		{"https://example.com:80/a", "https://example.com:80"},
		// A sibling host is a different origin.
		{"https://cdn.example.com/a", "https://cdn.example.com"},
		{"https://[2001:DB8::1]:443/a", "https://[2001:db8::1]"},
		{"https://[2001:db8::1]:8443/a", "https://[2001:db8::1]:8443"},
	}
	for _, c := range cases {
		if got := upstreamOriginKey(c.in); got != c.want {
			t.Errorf("upstreamOriginKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestOutboundBudgetIsSharedAcrossOriginSpellings is the behaviour the key
// exists for: respelling the origin must not buy a fresh outbound budget.
func TestOutboundBudgetIsSharedAcrossOriginSpellings(t *testing.T) {
	h := newTestHandler(nil)
	h.WithRateLimits(nil, publish.NewRateLimiter(1, 2, nil))

	if !h.admitOutbound("https://example.com/a") || !h.admitOutbound("https://EXAMPLE.com.:443/b") {
		t.Fatal("premise broken: the first two fetches fit the burst of 2")
	}
	for _, spelling := range []string{
		"https://example.com/c", "https://example.com:443/c", "https://example.com./c", "https://Example.COM/c",
	} {
		if h.admitOutbound(spelling) {
			t.Fatalf("%q was admitted on a spent origin budget — a respelling got its own bucket", spelling)
		}
	}
	// Near miss: a genuinely different origin is unaffected.
	if !h.admitOutbound("https://example.com:8443/c") {
		t.Fatal("a different port is a different origin and must have its own budget")
	}
}

// blockingFetcher holds every fetch open until released, and counts entries.
type blockingFetcher struct {
	entered chan struct{}
	release chan struct{}
	calls   int
}

func (f *blockingFetcher) fetchDocument(_ context.Context, rawURL string) ([]byte, string, error) {
	f.calls++
	f.entered <- struct{}{}
	<-f.release
	return []byte(upstreamDoc), rawURL, nil
}

func (f *blockingFetcher) fetchSubresource(_ context.Context, rawURL string) ([]byte, string, string, error) {
	f.calls++
	f.entered <- struct{}{}
	<-f.release
	return []byte("a{}"), "text/css", rawURL, nil
}

func TestOutboundConcurrencyCeiling(t *testing.T) {
	f := &blockingFetcher{entered: make(chan struct{}, 4), release: make(chan struct{})}
	h := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), f)
	h.outboundSlots = make(chan struct{}, 1) // one slot, so the second request must wait

	first := make(chan int, 1)
	go func() { first <- do(h, http.MethodGet, sharePrefix+validToken, "", nil).Code }()
	<-f.entered // the first fetch now holds the only slot

	// A second request whose client gives up before a slot frees.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodGet, sharePrefix+validToken, nil).WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("a request past the concurrency ceiling: got %d, want 503", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != "upstream unavailable" {
		t.Fatalf("the saturation refusal must use the generic upstream message, got %q", w.Body.String())
	}
	if f.calls != 1 {
		t.Fatalf("the refused request reached the fetcher (%d calls, want 1)", f.calls)
	}

	close(f.release)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("the request holding the slot: got %d, want 200", code)
	}
	// The slot was returned: the next request is served.
	if w := do(h, http.MethodGet, sharePrefix+validToken, "", nil); w.Code != http.StatusOK {
		t.Fatalf("after the slot was released: got %d, want 200", w.Code)
	}
}

// --- #2: bundle placement and upstream meta CSP ------------------------------

const testBundleTagPrefix = `<script src="/__devtool/inject.`

func injectForTest(t *testing.T, doc string) (out string, tagAt int) {
	t.Helper()
	out = string(injectPublicBundleFirst([]byte(doc), PublicInstrumentationAssetPath(), PublicInstrumentationAssetCSPHash()))
	if n := strings.Count(out, testBundleTagPrefix); n != 1 {
		t.Fatalf("the bundle tag must appear exactly once, found %d in:\n%s", n, out)
	}
	return out, strings.Index(out, testBundleTagPrefix)
}

func TestPublicBundleIsFirstInHead(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		// after must end immediately before the tag; "" means offset 0.
		after string
	}{
		{"plain", `<!DOCTYPE html><html><head><title>x</title></head><body></body></html>`, `<head>`},
		{"upper-case tags", `<!DOCTYPE HTML><HTML><HEAD><TITLE>x</TITLE></HEAD><BODY></BODY></HTML>`, `<HEAD>`},
		{"head with attributes", `<!doctype html><html lang="en"><head data-x=">"><title>x</title></head>`, `<head data-x=">">`},
		{"</head> inside a comment", `<!doctype html><!-- </head> --><html><head><title>x</title></head>`, `<head>`},
		{"<head> inside a comment", `<!doctype html><!-- <head> --><html><head><title>x</title></head>`, `<html><head>`},
		{"</head> inside a template", `<!doctype html><html><head><template></head></template><title>x</title></head>`, `<html><head>`},
		{"</head> inside a script string", `<!doctype html><html><head><script>var s="</head>"</script></head>`, `<html><head>`},
		{"implied head", `<!doctype html><html><body><p>hi</p></body></html>`, `<html>`},
		{"no html tag", `<!doctype html><title>x</title><p>hi`, `<!doctype html>`},
		{"whitespace and comment before doctype", "\n<!-- c -->\n<!doctype html>\n<html>\n<head>\n<title>x</title>", "<head>"},
		{"BOM", "\xEF\xBB\xBF<!doctype html><html><head></head>", `<head>`},
		{"BOM only", "\xEF\xBB\xBFhello", "\xEF\xBB\xBF"},
		{"no markup at all", `hello`, ``},
		{"empty", ``, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, at := injectForTest(t, c.doc)
			if !strings.HasSuffix(out[:at], c.after) {
				t.Fatalf("the tag must come immediately after %q; it was inserted after %q in:\n%s", c.after, out[:at], out)
			}
			// Never ahead of a doctype that opens the document (one the browser
			// honours): a tag before it puts the page in quirks mode.
			if strings.HasPrefix(strings.ToLower(stripLeadingComments(c.doc)), "<!doctype") {
				if dt := strings.Index(strings.ToLower(out), "<!doctype"); at < dt {
					t.Fatalf("the tag was emitted before the doctype:\n%s", out)
				}
			}
			// Nothing but the tag was added.
			if strings.Replace(out, out[at:at+strings.Index(out[at:], "</script>")+len("</script>")], "", 1) != c.doc {
				t.Fatalf("injection changed bytes other than adding the tag:\n got %q\nfrom %q", out, c.doc)
			}
		})
	}
}

// stripLeadingComments drops leading whitespace and HTML comments, so the
// doctype-ordering check can tell "a doctype opens the document" from "a
// doctype appears somewhere later".
func stripLeadingComments(s string) string {
	for {
		s = strings.TrimLeft(strings.TrimPrefix(s, "\xEF\xBB\xBF"), " \t\r\n")
		if !strings.HasPrefix(s, "<!--") {
			return s
		}
		end := strings.Index(s, "-->")
		if end < 0 {
			return ""
		}
		s = s[end+3:]
	}
}

func TestUpstreamMetaCSPIsRemoved(t *testing.T) {
	const policy = `content="script-src 'none'; connect-src 'none'"`
	strip := []string{
		`<meta http-equiv="Content-Security-Policy" ` + policy + `>`,
		`<META HTTP-EQUIV="content-security-policy" ` + policy + `>`,
		`<meta http-equiv=Content-Security-Policy ` + policy + `>`,
		`<meta http-equiv='Content-Security-Policy' ` + policy + ` />`,
		`<meta ` + policy + ` http-equiv=" Content-Security-Policy ">`,
		// Entity-spelled: the browser decodes the attribute, so must the check (§3).
		`<meta http-equiv="Content-Security&#45;Policy" ` + policy + `>`,
		`<meta http-equiv="Content-Security-Policy-Report-Only" ` + policy + `>`,
	}
	for _, meta := range strip {
		doc := `<!doctype html><html><head>` + meta + `<title>x</title></head><body>ok</body></html>`
		out, at := injectForTest(t, doc)
		if strings.Contains(out, "script-src 'none'") {
			t.Errorf("an upstream meta CSP survived: %s\nin: %s", meta, out)
		}
		if !strings.HasSuffix(out[:at], "<head>") {
			t.Errorf("the bundle must precede everything the upstream put in its head, got prefix %q", out[:at])
		}
		if !strings.Contains(out, "<title>x</title></head><body>ok</body></html>") {
			t.Errorf("stripping the meta removed more than the meta: %s", out)
		}
	}

	// A browser reads "<!-->" as an EMPTY comment, so the meta after it is live
	// and must go. A scanner that waits for the next "-->" would skip it.
	tricky := `<!doctype html><html><head><!--><meta http-equiv="Content-Security-Policy" ` + policy + `><!-- --></head>`
	if out, _ := injectForTest(t, tricky); strings.Contains(out, "script-src 'none'") {
		t.Errorf("a meta CSP after an empty comment survived:\n%s", out)
	}

	// Near misses: none of these is a policy the browser would apply, and each
	// must pass through untouched.
	keep := []string{
		`<meta http-equiv="Content-Type" content="text/html; charset=utf-8">`,
		`<meta name="Content-Security-Policy" ` + policy + `>`,
		`<meta http-equiv="X-Content-Security-Policy-ish" ` + policy + `>`,
		`<!-- <meta http-equiv="Content-Security-Policy" ` + policy + `> -->`,
		`<script>var s='<meta http-equiv="Content-Security-Policy" ` + policy + `>'</script>`,
		`<textarea><meta http-equiv="Content-Security-Policy" ` + policy + `></textarea>`,
	}
	for _, fragment := range keep {
		doc := `<!doctype html><html><head><title>x</title></head><body>` + fragment + `</body></html>`
		if out, _ := injectForTest(t, doc); !strings.Contains(out, fragment) {
			t.Errorf("a fragment that is not a live meta CSP was altered: %s\nin: %s", fragment, out)
		}
	}
}

// TestProxiedArtifactRouteStripsMetaCSPAndLeadsWithTheBundle drives the same
// property through the route, so the wiring (not just the helper) is pinned.
func TestProxiedArtifactRouteStripsMetaCSPAndLeadsWithTheBundle(t *testing.T) {
	doc := `<!DOCTYPE html><HTML><HEAD><!-- </head> -->` +
		`<meta http-equiv="Content-Security-Policy" content="script-src 'nonce-abc'; default-src 'none'">` +
		`<title>Strict</title></HEAD><body><h1 id="hero">hi</h1></body></HTML>`
	f := &countingFetcher{body: []byte(doc)}
	h := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), f)

	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("proxied artifact: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	at := strings.Index(body, `<script src="`+h.assetPath+`"`)
	if at < 0 {
		t.Fatalf("the public bundle is missing from the proxied document:\n%s", body)
	}
	if !strings.HasSuffix(body[:at], "<HEAD>") {
		t.Fatalf("the bundle must be the first thing in the head, got prefix %q", body[:at])
	}
	if strings.Contains(body, "nonce-abc") || strings.Contains(strings.ToLower(body), `http-equiv="content-security-policy"`) {
		t.Fatalf("the upstream meta CSP survived on the public plane:\n%s", body)
	}
	if !strings.Contains(body, `id="hero"`) {
		t.Fatalf("the upstream document was damaged:\n%s", body)
	}
	if got := w.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length %s does not match the %d bytes served", got, len(body))
	}
}

// --- #1 and the uncapped paths: rate-limit identity ---------------------------

func doFrom(h http.Handler, remoteAddr, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPreTokenRoutesAreRateLimitedPerClient(t *testing.T) {
	h := newTestHandler(nil)
	h.preAuthLimiter = publish.NewRateLimiter(1, 3, nil)

	// Three different token-less routes spend the one client's burst of 3.
	routes := []struct {
		target string
		want   int
	}{
		{"/nope", http.StatusNotFound},
		{"/__devtool_metrics", http.StatusForbidden},
		{sharePrefix + "not-a-token", http.StatusNotFound},
	}
	for _, r := range routes {
		if w := doFrom(h, "198.51.100.7:1000", http.MethodGet, r.target, nil); w.Code != r.want {
			t.Fatalf("premise broken: %s got %d, want %d", r.target, w.Code, r.want)
		}
	}
	// Past the burst every route — unknown, forbidden, the bundle asset, token
	// guessing, and even a VALID token — is refused before it is looked at.
	for _, target := range []string{"/nope", "/__devtool_metrics", h.assetPath, sharePrefix + "guess", sharePrefix + validToken} {
		w := doFrom(h, "198.51.100.7:2000", http.MethodGet, target, nil)
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("%s past the per-client pre-token cap: got %d, want 429", target, w.Code)
		}
		if strings.TrimSpace(w.Body.String()) != "rate limited" {
			t.Fatalf("the pre-token 429 must be the constant message, got %q", w.Body.String())
		}
	}
	// Near miss: another client is untouched, and its valid token still works.
	if w := doFrom(h, "198.51.100.8:1000", http.MethodGet, sharePrefix+validToken, nil); w.Code != http.StatusOK {
		t.Fatalf("a different client: got %d, want 200", w.Code)
	}
	if w := doFrom(h, "198.51.100.8:1000", http.MethodGet, h.assetPath, nil); w.Code != http.StatusOK {
		t.Fatalf("a different client fetching the bundle: got %d, want 200", w.Code)
	}
}

func TestArtifactLimiterKeysIPv6ByThe64(t *testing.T) {
	h := newTestHandler(nil)
	h.WithRateLimits(publish.NewRateLimiter(1, 2, nil), nil)

	admitted := 0
	for i := 0; i < 12; i++ {
		// A fresh source address inside one /64 on every request.
		addr := fmt.Sprintf("[2001:db8:aa:bb::%x]:443", i+1)
		if doFrom(h, addr, http.MethodGet, sharePrefix+validToken, nil).Code == http.StatusOK {
			admitted++
		}
	}
	if admitted != 2 {
		t.Fatalf("rotating through one /64 was served %d times, want exactly the burst of 2", admitted)
	}
	// Near miss: the neighbouring /64 has its own bucket.
	if w := doFrom(h, "[2001:db8:aa:bc::1]:443", http.MethodGet, sharePrefix+validToken, nil); w.Code != http.StatusOK {
		t.Fatalf("a client in another /64: got %d, want 200", w.Code)
	}
}

func TestPerShareAggregateArtifactBucket(t *testing.T) {
	h := newTestHandler(nil)
	h.WithRateLimits(publish.NewRateLimiter(1, 2, nil), nil).
		WithShareAggregateLimiter(publish.NewRateLimiter(1, 5, nil))
	get := func(addr string) int {
		return doFrom(h, addr, http.MethodGet, sharePrefix+validToken, nil).Code
	}

	// One client spends its own burst, then floods.
	if get("203.0.113.1:1") != http.StatusOK || get("203.0.113.1:1") != http.StatusOK {
		t.Fatal("premise broken: the first client's burst of 2 must be served")
	}
	for i := 0; i < 50; i++ {
		if code := get("203.0.113.1:1"); code != http.StatusTooManyRequests {
			t.Fatalf("flood request %d from a spent client: got %d, want 429", i, code)
		}
	}
	// The flood drew nothing more from the share: a second viewer is served.
	if code := get("203.0.113.2:1"); code != http.StatusOK {
		t.Fatalf("one flooding client starved the share for a second viewer: got %d", code)
	}

	// Distinct clients are bounded by the aggregate (5): three are already spent.
	served := 3
	refused := false
	for i := 3; i < 12 && !refused; i++ {
		addr := "203.0.113." + strconv.Itoa(i) + ":1"
		switch get(addr) {
		case http.StatusOK:
			served++
		case http.StatusTooManyRequests:
			refused = true
		default:
			t.Fatalf("unexpected status from %s", addr)
		}
	}
	if !refused || served != 5 {
		t.Fatalf("the share served %d requests across distinct clients, want exactly the aggregate burst of 5 (refused=%v)", served, refused)
	}
}

func TestWithClientAddrDecidesTheRateLimitIdentity(t *testing.T) {
	sink := &preAdmitSink{}
	h := newTestHandler(sink)
	h.WithRateLimits(publish.NewRateLimiter(1, 1, nil), nil)
	// A resolver that trusts a header only from a loopback peer.
	h.WithClientAddr(func(r *http.Request) string {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return ""
		}
		return r.Header.Get("X-Test-Client")
	})
	get := func(remote, client string) int {
		return doFrom(h, remote, http.MethodGet, sharePrefix+validToken, map[string]string{"X-Test-Client": client}).Code
	}

	// Two viewers behind the same loopback peer are two clients.
	if get("127.0.0.1:5000", "203.0.113.1") != http.StatusOK {
		t.Fatal("viewer 1 must be served")
	}
	if get("127.0.0.1:5001", "203.0.113.2") != http.StatusOK {
		t.Fatal("viewer 2 shares a TCP peer with viewer 1 but is a different client and must have its own bucket")
	}
	if get("127.0.0.1:5002", "203.0.113.1") != http.StatusTooManyRequests {
		t.Fatal("viewer 1's second request must hit viewer 1's bucket")
	}
	// From a non-loopback peer the header is not trusted: rotating it buys nothing.
	if get("198.51.100.9:1", "203.0.113.50") != http.StatusOK {
		t.Fatal("premise broken: the direct client's first request must be served")
	}
	if get("198.51.100.9:2", "203.0.113.51") != http.StatusTooManyRequests {
		t.Fatal("a direct client rotated the header and got a fresh bucket — the header was trusted from a non-loopback peer")
	}

	// The same resolved address reaches the feedback sink.
	r := httptest.NewRequest(http.MethodPost, sharePrefix+validToken+"/feedback", strings.NewReader(`{"message":"hi"}`))
	r.RemoteAddr = "127.0.0.1:6000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Test-Client", "203.0.113.77")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted || sink.admitAddr != "203.0.113.77" {
		t.Fatalf("feedback: code %d, sink saw address %q — want 202 and the resolved client address", w.Code, sink.admitAddr)
	}
}

// preAdmitSink is a two-step sink that records the order of its calls.
type preAdmitSink struct {
	admitErr  error
	admitAddr string
	events    []string
	body      []byte
}

func (s *preAdmitSink) Accept(string, publish.RevisionDigest, string, []byte) error {
	s.events = append(s.events, "accept")
	return errors.New("the two-step sink must be driven through Admit/AcceptAdmitted")
}

func (s *preAdmitSink) Admit(_, remoteAddr string) error {
	s.events = append(s.events, "admit")
	s.admitAddr = remoteAddr
	return s.admitErr
}

func (s *preAdmitSink) AcceptAdmitted(_ string, _ publish.RevisionDigest, body []byte) error {
	s.events = append(s.events, "store")
	s.body = body
	return nil
}

// trackedBody records whether the handler read the request body at all.
type trackedBody struct {
	r     io.Reader
	reads int
}

func (b *trackedBody) Read(p []byte) (int, error) { b.reads++; return b.r.Read(p) }
func (b *trackedBody) Close() error               { return nil }

func postFeedback(h http.Handler, body *trackedBody, contentLength int64) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, sharePrefix+validToken+"/feedback", nil)
	r.Body = body
	r.ContentLength = contentLength
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestFeedbackIsBoundedBeforeTheBodyIsRead(t *testing.T) {
	const msg = `{"message":"hi"}`

	t.Run("rate limited", func(t *testing.T) {
		sink := &preAdmitSink{admitErr: publish.ErrFeedbackRateLimited}
		body := &trackedBody{r: strings.NewReader(msg)}
		w := postFeedback(newTestHandler(sink), body, int64(len(msg)))
		if w.Code != http.StatusTooManyRequests {
			t.Fatalf("got %d, want 429", w.Code)
		}
		if body.reads != 0 {
			t.Fatalf("a rate-limited submission had its body read (%d reads)", body.reads)
		}
		if strings.Join(sink.events, ",") != "admit" {
			t.Fatalf("sink calls = %v, want only admit", sink.events)
		}
	})

	t.Run("declared length over the cap", func(t *testing.T) {
		sink := &preAdmitSink{}
		body := &trackedBody{r: strings.NewReader(msg)}
		w := postFeedback(newTestHandler(sink), body, defaultMaxFeedbackBody+1)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("got %d, want 413", w.Code)
		}
		if body.reads != 0 || len(sink.events) != 0 {
			t.Fatalf("an over-length submission was read (%d reads) or reached the sink (%v)", body.reads, sink.events)
		}
	})

	t.Run("no sink", func(t *testing.T) {
		body := &trackedBody{r: strings.NewReader(msg)}
		w := postFeedback(newTestHandler(nil), body, int64(len(msg)))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("got %d, want 503 — the route must not acknowledge feedback it cannot store", w.Code)
		}
		if body.reads != 0 {
			t.Fatalf("the body was read (%d reads) with no sink to hand it to", body.reads)
		}
	})

	t.Run("near miss: admitted", func(t *testing.T) {
		sink := &preAdmitSink{}
		body := &trackedBody{r: strings.NewReader(msg)}
		w := postFeedback(newTestHandler(sink), body, int64(len(msg)))
		if w.Code != http.StatusAccepted {
			t.Fatalf("got %d, want 202", w.Code)
		}
		if strings.Join(sink.events, ",") != "admit,store" || string(sink.body) != msg {
			t.Fatalf("sink calls = %v body = %q, want admit then store with the body", sink.events, sink.body)
		}
	})

	t.Run("near miss: chunked body over the cap is still refused", func(t *testing.T) {
		sink := &preAdmitSink{}
		body := &trackedBody{r: strings.NewReader(strings.Repeat("x", defaultMaxFeedbackBody+10))}
		w := postFeedback(newTestHandler(sink), body, -1)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("got %d, want 413", w.Code)
		}
		if strings.Join(sink.events, ",") != "admit" {
			t.Fatalf("sink calls = %v, want admit only (nothing stored)", sink.events)
		}
	})
}

// --- #13: address fallback keeps the pin --------------------------------------

func TestGuardedFetchFallsBackAcrossValidatedAddressesOnly(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, upstreamDoc)
	}))
	t.Cleanup(srv.Close)
	tlsCfg := srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()

	dead := netip.MustParseAddr("2606:2800:220:1:248:1893:25c8:1946") // unreachable family on this host
	var dialed []string
	f := &guardedUpstreamFetcher{
		resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{dead, publicAddr}, nil
		},
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed = append(dialed, addr)
			switch addr {
			case net.JoinHostPort(dead.String(), "443"):
				return nil, errors.New("network is unreachable")
			case net.JoinHostPort(publicAddr.String(), "443"):
				return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
			}
			return nil, errors.New("dial address is not one the guard validated: " + addr)
		},
		tlsConfig: tlsCfg,
	}

	body, _, err := f.fetchDocument(context.Background(), "https://example.com/app")
	if err != nil {
		t.Fatalf("the fetch must succeed on the second validated address: %v", err)
	}
	if !strings.Contains(string(body), `id="hero"`) {
		t.Fatalf("unexpected body: %s", body)
	}
	want := []string{net.JoinHostPort(dead.String(), "443"), net.JoinHostPort(publicAddr.String(), "443")}
	if strings.Join(dialed, " ") != strings.Join(want, " ") {
		t.Fatalf("dialed %v, want %v — each attempt must dial exactly a guard-validated address, in order", dialed, want)
	}

	// Fallback is not a way past the guard: one denied answer refuses the host
	// before ANY address is dialed, including the public one beside it.
	dialed = nil
	f.resolve = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{publicAddr, netip.MustParseAddr("10.0.0.5")}, nil
	}
	_, _, err = f.fetchDocument(context.Background(), "https://example.com/app")
	if err == nil || !strings.Contains(err.Error(), "upstream refused") {
		t.Fatalf("a host with a private answer must be refused by the guard, got %v", err)
	}
	if len(dialed) != 0 {
		t.Fatalf("the guard refused the host but %v was dialed", dialed)
	}

	// The attempt cap bounds the dials of one hop.
	dialed = nil
	many := []netip.Addr{
		netip.MustParseAddr("93.184.216.1"), netip.MustParseAddr("93.184.216.2"),
		netip.MustParseAddr("93.184.216.3"), netip.MustParseAddr("93.184.216.4"),
		netip.MustParseAddr("93.184.216.5"),
	}
	f.resolve = func(context.Context, string) ([]netip.Addr, error) { return many, nil }
	f.dial = func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		return nil, errors.New("connection refused")
	}
	if _, _, err := f.fetchDocument(context.Background(), "https://example.com/app"); err == nil {
		t.Fatal("every address failed; the fetch must fail")
	}
	if len(dialed) != 3 {
		t.Fatalf("one hop dialed %d addresses, want the cap of 3: %v", len(dialed), dialed)
	}
}
