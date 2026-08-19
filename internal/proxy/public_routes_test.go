package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/publish"
)

// fakeVerifier is a constant-time-agnostic stand-in for *publish.Store on
// the public plane. It maps a single known token to a revision; every other
// token (including a revoked one) verifies false.
type fakeVerifier struct {
	token string
	rev   *publish.PublishedWalkthrough
	id    string
}

func (f *fakeVerifier) VerifyToken(token string) (*publish.PublishedWalkthrough, string, bool) {
	if f.token != "" && token == f.token {
		return f.rev, f.id, true
	}
	return nil, "", false
}

// capturingSink records the last feedback hand-off, to prove the P7 route-level
// guards ran and the handler threaded the revision id + real remote address.
type capturingSink struct {
	shareID    string
	revisionID string
	remoteAddr string
	body       []byte
	calls      int
}

func (s *capturingSink) Accept(shareID string, revisionDigest publish.RevisionDigest, remoteAddr string, body []byte) error {
	s.calls++
	s.shareID = shareID
	s.revisionID = string(revisionDigest)
	s.remoteAddr = remoteAddr
	s.body = body
	return nil
}

func sampleWalkthrough() *publish.PublishedWalkthrough {
	return &publish.PublishedWalkthrough{
		Version: publish.SchemaV1,
		ID:      "wt-1",
		Title:   "Sample Walk",
		Steps: []publish.Step{
			{ID: "s1", Title: "One", Body: "first", Advance: publish.Advance{Type: "auto", MS: 1000}},
		},
	}
}

const validToken = "valid-token-abc"

func newTestHandler(sink FeedbackSink) *PublicHandler {
	v := &fakeVerifier{token: validToken, rev: sampleWalkthrough(), id: "share-1"}
	return NewPublicHandler(v, sink, 0)
}

func do(h http.Handler, method, target string, body string, headers map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	for k, val := range headers {
		r.Header.Set(k, val)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestPublicRouteMatrix asserts allow/deny for each public route × method per
// the §2b endpoint matrix.
func TestPublicRouteMatrix(t *testing.T) {
	h := newTestHandler(&capturingSink{})
	base := sharePrefix + validToken
	jsonCT := map[string]string{"Content-Type": "application/json"}

	cases := []struct {
		name   string
		method string
		target string
		hdr    map[string]string
		want   int
	}{
		{"artifact GET", http.MethodGet, base, nil, http.StatusOK},
		{"artifact HEAD", http.MethodHead, base, nil, http.StatusOK},
		{"artifact POST denied", http.MethodPost, base, nil, http.StatusMethodNotAllowed},
		{"variants GET", http.MethodGet, base + "/variants.json", nil, http.StatusOK},
		{"variants POST denied", http.MethodPost, base + "/variants.json", nil, http.StatusMethodNotAllowed},
		{"walkthrough GET", http.MethodGet, base + "/walkthrough.json", nil, http.StatusOK},
		{"walkthrough DELETE denied", http.MethodDelete, base + "/walkthrough.json", nil, http.StatusMethodNotAllowed},
		{"feedback POST", http.MethodPost, base + "/feedback", jsonCT, http.StatusAccepted},
		{"feedback GET denied", http.MethodGet, base + "/feedback", nil, http.StatusMethodNotAllowed},
		{"unknown sub-route", http.MethodGet, base + "/secrets.json", nil, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := do(h, c.method, c.target, "", c.hdr)
			if w.Code != c.want {
				t.Fatalf("%s %s: got %d, want %d", c.method, c.target, w.Code, c.want)
			}
		})
	}
}

// TestForbiddenRouteScan proves the negative: every dev-control surface is
// unreachable on the public plane. This is the core security gate.
func TestForbiddenRouteScan(t *testing.T) {
	h := newTestHandler(nil)

	// Known dev-control paths → 403 (known-but-forbidden). None may 2xx.
	forbidden403 := []string{
		"/__devtool_metrics",            // metrics WebSocket upgrade
		"/__devtool_axe",                // axe-core audit asset
		"/__devtool_html2canvas",        // html2canvas capture asset (dev-only)
		"/__devtool/ws",                 // dev WS channel
		"/__devtool/exec",               // proxy exec bridge
		"/__devtool/inject.js",          // dev bundle (non-public path)
		"/__devtool/inject.deadbeef.js", // forged/unknown hash → must NOT serve dev bundle
		"/__devtool/",                   // control subtree root
	}
	for _, p := range forbidden403 {
		w := do(h, http.MethodGet, p, "", nil)
		if w.Code == http.StatusOK {
			t.Fatalf("forbidden control path %q returned 200 — dev surface reachable", p)
		}
		if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
			t.Fatalf("forbidden control path %q: got %d, want 403/404", p, w.Code)
		}
		// Whatever the body, it must never be the dev instrumentation bundle.
		if strings.Contains(w.Body.String(), "__devtool_proxy_id") || strings.Contains(w.Body.String(), "window.__devtool") {
			t.Fatalf("forbidden path %q leaked dev-bundle content", p)
		}
	}

	// Truly-unknown / traversal paths → 404, no oracle.
	unknown404 := []string{
		"/",
		"/index.html",
		"/s/../__devtool_metrics",
		"/s/" + validToken + "/../../etc/passwd",
		"/s/%2e%2e/secret",
		"/../server.go",
		"/s//double",
	}
	for _, p := range unknown404 {
		w := do(h, http.MethodGet, p, "", nil)
		if w.Code == http.StatusOK {
			t.Fatalf("unexpected 200 for %q", p)
		}
	}
}

// TestPublicAssetServesOnlyPublicBundle asserts the public asset route serves
// the RolePublic bundle at its exact content-addressed path and 404s an unknown
// hash — never the dev-bundle fallback (closes the P4 advisory).
func TestPublicAssetServesOnlyPublicBundle(t *testing.T) {
	h := newTestHandler(nil)

	// The exact public asset path serves 200 with the public bundle.
	w := do(h, http.MethodGet, h.assetPath, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("public asset path %q: got %d, want 200", h.assetPath, w.Code)
	}
	publicBody := w.Body.String()
	if strings.Contains(publicBody, "window.__devtool_proxy_id") {
		t.Fatalf("public bundle unexpectedly carries dev proxy-id bootstrap")
	}

	// An unknown hash under /__devtool/inject.<x>.js must NOT return the dev
	// (full) bundle. Compare against the actual dev bundle bytes.
	devBundle := string(instrumentationScriptBytes())
	w2 := do(h, http.MethodGet, "/__devtool/inject.0000000000000000.js", "", nil)
	if w2.Code == http.StatusOK && w2.Body.String() == devBundle {
		t.Fatalf("unknown hash on public plane served the FULL DEV bundle — P4 fallback not closed")
	}
	if w2.Code == http.StatusOK {
		t.Fatalf("unknown hash on public plane returned 200 (want 403/404)")
	}
}

// TestTokenGatingAndINV12 asserts a valid token serves the artifact with the
// public bundle injected and the agnt CSP wholesale-replaces any upstream CSP,
// and a revoked/unknown token 404s.
func TestTokenGatingAndINV12(t *testing.T) {
	h := newTestHandler(nil)

	// Valid token → 200 + public bundle tag injected.
	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("valid token artifact: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), h.assetPath) {
		t.Fatalf("artifact shell does not inject the public bundle path %q", h.assetPath)
	}

	// INV-12: seed a hostile upstream CSP into the header set, then apply the
	// public header policy, and assert the served CSP is agnt's — no unsafe-inline
	// survives, and Content-Security-Policy-Report-Only is deleted.
	hostile := http.Header{}
	hostile.Set("Content-Security-Policy", "script-src 'unsafe-inline' 'unsafe-eval' https://evil.example")
	hostile.Set("Content-Security-Policy-Report-Only", "default-src *")
	hostile.Set("Set-Cookie", "session=leak")
	h.writeHeaders(hostile, kindArtifact, "n0nce")
	csp := hostile.Get("Content-Security-Policy")
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("INV-12 violated: served CSP still contains unsafe-* : %q", csp)
	}
	// script-src must pin the bundle by hash ALONE — 'self' would render the
	// hash inert (finding 4). default-src keeps 'self'; only script-src drops it.
	if !strings.Contains(csp, "script-src '"+h.cspHash+"'") {
		t.Fatalf("served CSP script-src is not the hash-only pin: %q", csp)
	}
	if strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("served CSP script-src must not contain 'self' (inert hash): %q", csp)
	}
	if hostile.Get("Content-Security-Policy-Report-Only") != "" {
		t.Fatalf("INV-12 violated: Content-Security-Policy-Report-Only not deleted wholesale")
	}
	if hostile.Get("Set-Cookie") != "" {
		t.Fatalf("Set-Cookie not stripped on public plane")
	}

	// The live artifact response must carry the same CSP posture end-to-end.
	liveCSP := w.Header().Get("Content-Security-Policy")
	if strings.Contains(liveCSP, "unsafe-inline") || !strings.Contains(liveCSP, "script-src '"+h.cspHash+"'") {
		t.Fatalf("artifact response CSP wrong: %q", liveCSP)
	}

	// Revoked/unknown token → 404 (fakeVerifier returns false for any other token).
	for _, tok := range []string{"revoked-token", "unknown", ""} {
		w := do(h, http.MethodGet, sharePrefix+tok, "", nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("token %q: got %d, want 404", tok, w.Code)
		}
	}
}

// TestPublicHeaderAssertions checks Referrer-Policy, nosniff, cache, no cookies
// on artifact and feedback responses.
func TestPublicHeaderAssertions(t *testing.T) {
	h := newTestHandler(&capturingSink{})

	art := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if got := art.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("artifact Referrer-Policy: got %q", got)
	}
	if got := art.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("artifact nosniff: got %q", got)
	}
	if got := art.Header().Get("Cache-Control"); !strings.Contains(got, "must-revalidate") {
		t.Fatalf("artifact Cache-Control: got %q, want must-revalidate", got)
	}
	if art.Header().Get("Set-Cookie") != "" {
		t.Fatalf("artifact must not set cookies")
	}
	if art.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("artifact must not set ACAO")
	}

	fb := do(h, http.MethodPost, sharePrefix+validToken+"/feedback", `{"c":"hi"}`, map[string]string{"Content-Type": "application/json"})
	if got := fb.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("feedback Cache-Control: got %q, want no-store", got)
	}
}

// TestPublicBundleHashPinIsReal asserts finding 4: the public CSP pins the
// bundle by hash ALONE (no 'self', which would make the hash source inert), and
// the external <script src> carries a matching SRI integrity attribute — which
// is what makes a CSP hash-source authorise an external same-origin script, so
// dropping 'self' does not break the bundle load.
func TestPublicBundleHashPinIsReal(t *testing.T) {
	h := newTestHandler(nil)

	art := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if art.Code != http.StatusOK {
		t.Fatalf("artifact: got %d, want 200", art.Code)
	}

	csp := art.Header().Get("Content-Security-Policy")
	scriptSrc := ""
	for _, d := range strings.Split(csp, ";") {
		if strings.HasPrefix(strings.TrimSpace(d), "script-src") {
			scriptSrc = strings.TrimSpace(d)
		}
	}
	if scriptSrc == "" {
		t.Fatalf("no script-src directive in CSP: %q", csp)
	}
	if strings.Contains(scriptSrc, "'self'") {
		t.Fatalf("script-src must not contain 'self' (inert hash pin): %q", scriptSrc)
	}
	if !strings.Contains(scriptSrc, "'"+h.cspHash+"'") {
		t.Fatalf("script-src must pin the bundle hash %q: %q", h.cspHash, scriptSrc)
	}

	// The external <script src> must carry integrity matching the CSP hash so
	// the hash-source actually authorises it.
	body := art.Body.String()
	wantTag := "<script src=\"" + h.assetPath + "\" integrity=\"" + h.cspHash + "\" crossorigin=\"anonymous\"></script>"
	if !strings.Contains(body, wantTag) {
		t.Fatalf("artifact shell missing SRI-pinned bundle tag.\nwant: %s\nbody: %s", wantTag, body)
	}
}

// TestFeedbackRouteLimits asserts the P7 feedback guards: oversize body,
// wrong method, wrong content-type all rejected; a valid post reaches the sink.
func TestFeedbackRouteLimits(t *testing.T) {
	sink := &capturingSink{}
	h := newTestHandler(sink)
	fbPath := sharePrefix + validToken + "/feedback"
	jsonCT := map[string]string{"Content-Type": "application/json"}

	// Valid POST → 202, reaches the sink with the right share id.
	w := do(h, http.MethodPost, fbPath, `{"comment":"nice"}`, jsonCT)
	if w.Code != http.StatusAccepted {
		t.Fatalf("valid feedback: got %d, want 202", w.Code)
	}
	if sink.calls != 1 || sink.shareID != "share-1" {
		t.Fatalf("sink not invoked correctly: calls=%d id=%q", sink.calls, sink.shareID)
	}

	// Wrong method → 405.
	if w := do(h, http.MethodGet, fbPath, "", nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET feedback: got %d, want 405", w.Code)
	}

	// Wrong content-type → 415.
	if w := do(h, http.MethodPost, fbPath, "hi", map[string]string{"Content-Type": "text/plain"}); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain feedback: got %d, want 415", w.Code)
	}

	// Oversize body → 413.
	big := strings.Repeat("x", defaultMaxFeedbackBody+100)
	if w := do(h, http.MethodPost, fbPath, `{"c":"`+big+`"}`, jsonCT); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize feedback: got %d, want 413", w.Code)
	}
}

// TestArtifactJSONShape confirms variants/walkthrough JSON are well-formed and
// carry the immutable published data.
func TestArtifactJSONShape(t *testing.T) {
	h := newTestHandler(nil)

	w := do(h, http.MethodGet, sharePrefix+validToken+"/walkthrough.json", "", nil)
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("walkthrough.json content-type: %q", ct)
	}
	var pw publish.PublishedWalkthrough
	if err := json.Unmarshal(w.Body.Bytes(), &pw); err != nil {
		t.Fatalf("walkthrough.json not valid JSON: %v", err)
	}
	if pw.Title != "Sample Walk" {
		t.Fatalf("walkthrough.json title: %q", pw.Title)
	}

	wv := do(h, http.MethodGet, sharePrefix+validToken+"/variants.json", "", nil)
	if wv.Code != http.StatusOK {
		t.Fatalf("variants.json: got %d", wv.Code)
	}
	var vs publish.VariantSet
	if err := json.Unmarshal(wv.Body.Bytes(), &vs); err != nil {
		t.Fatalf("variants.json not valid JSON: %v", err)
	}
}

// authoredScriptRevision is a revision whose variant set carries publisher
// script (§6a addScript), the input INV-12's per-revision hash pin is computed
// from.
func authoredScriptRevision(codes ...string) *publish.PublishedWalkthrough {
	rev := sampleWalkthrough()
	ops := make([]publish.Op, 0, len(codes))
	for _, c := range codes {
		ops = append(ops, publish.Op{Op: publish.OpAddScript, Code: c})
	}
	rev.VariantSet = &publish.VariantSet{
		Version:  publish.SchemaV1,
		ID:       "vs-1",
		Variants: []publish.Variant{{ID: "v1", Ops: ops}},
	}
	return rev
}

func artifactScriptSrc(t *testing.T, h *PublicHandler) string {
	t.Helper()
	art := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if art.Code != http.StatusOK {
		t.Fatalf("artifact: got %d, want 200", art.Code)
	}
	for _, d := range strings.Split(art.Header().Get("Content-Security-Policy"), ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src") {
			return d
		}
	}
	t.Fatalf("no script-src directive in CSP %q", art.Header().Get("Content-Security-Policy"))
	return ""
}

// TestAuthoredScriptHashPinnedInScriptSrc is the INV-12 widening: the artifact
// CSP gains exactly one 'sha256-…' per authored script body in the served
// revision, so publisher script executes — while a byte-different (foreign)
// inline script, whose hash is absent, stays refused. Hashes are over the exact
// bytes the renderer assigns to script.textContent (variant-engine.js
// addScript), so the browser's inline-hash check matches.
func TestAuthoredScriptHashPinnedInScriptSrc(t *testing.T) {
	const authored = "window.__demo_variant=1;"
	const foreign = "window.__evil=1;"

	h := NewPublicHandler(&fakeVerifier{token: validToken, rev: authoredScriptRevision(authored), id: "share-1"}, nil, 0)
	scriptSrc := artifactScriptSrc(t, h)

	authoredHash := cspSHA256([]byte(authored))
	if !strings.Contains(scriptSrc, "'"+authoredHash+"'") {
		t.Fatalf("authored script hash %q missing from script-src: %q", authoredHash, scriptSrc)
	}
	if foreignHash := cspSHA256([]byte(foreign)); strings.Contains(scriptSrc, foreignHash) {
		t.Fatalf("foreign script hash must not be pinned: %q", scriptSrc)
	}
	// The bundle pin survives the widening, and no shortcut source is admitted.
	if !strings.Contains(scriptSrc, "'"+h.cspHash+"'") {
		t.Fatalf("bundle hash dropped from script-src: %q", scriptSrc)
	}
	for _, forbidden := range []string{"'unsafe-inline'", "'unsafe-eval'", "'self'", "http", "*"} {
		if strings.Contains(scriptSrc, forbidden) {
			t.Fatalf("script-src admitted forbidden source %q: %q", forbidden, scriptSrc)
		}
	}

	// A revision with NO authored script pins the bundle hash alone: the
	// widening is per-revision, not a blanket relaxation.
	plain := artifactScriptSrc(t, newTestHandler(nil))
	if plain != "script-src '"+h.cspHash+"'" {
		t.Fatalf("script-less revision widened script-src: %q", plain)
	}

	// Two authored bodies → two hashes; a repeated body is pinned once.
	multi := NewPublicHandler(&fakeVerifier{token: validToken, rev: authoredScriptRevision(authored, foreign, authored), id: "share-1"}, nil, 0)
	multiSrc := artifactScriptSrc(t, multi)
	if got := strings.Count(multiSrc, "sha256-"); got != 3 {
		t.Fatalf("want bundle + 2 deduped authored hashes (3), got %d: %q", got, multiSrc)
	}
}

// shellStyleReset is the signature of the SELF-CONTAINED artifact shell (the
// only response that carries an inline nonce'd style). Its presence in a
// response proves the self-contained path served it.
const shellStyleReset = "html,body{margin:0;height:100%}"

// upstreamRevision is a published revision that names a live upstream origin —
// the input that must take the proxied path rather than the self-contained one.
func upstreamRevision(rawURL string) *publish.PublishedWalkthrough {
	rev := sampleWalkthrough()
	rev.Upstream = &publish.UpstreamConfig{URL: rawURL}
	return rev
}

// TestUpstreamShareNeverFallsBackToSelfContainedShell is the no-silent-fallback
// rule for S6, and the one assertion that holds on EVERY failure path: a share
// that names a live upstream is a demo OF that upstream, so when the upstream
// cannot be served the plane must refuse loudly. Quietly substituting the
// self-contained shell would present a different page as the published demo —
// a Silent Failure Prohibition violation dressed as graceful degradation.
//
// The upstream here is an RFC1918 literal, so the INV-13 guard refuses it before
// any socket is opened: this test never touches the network.
func TestUpstreamShareNeverFallsBackToSelfContainedShell(t *testing.T) {
	h := NewPublicHandler(&fakeVerifier{token: validToken, rev: upstreamRevision("https://10.0.0.7/app"), id: "share-1"}, nil, 0)

	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code == http.StatusOK {
		t.Fatalf("refused upstream served 200 — the plane fell back to something")
	}
	if w.Code != http.StatusBadGateway {
		t.Fatalf("refused upstream: got %d, want 502", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, shellStyleReset) || strings.Contains(body, h.assetPath) {
		t.Fatalf("refused upstream silently served the self-contained shell: %s", body)
	}
}

// TestHostileUpstreamCSPCannotSurviveWidening re-asserts INV-11/INV-12 with the
// authored-hash widening in place: an upstream CSP is DELETED wholesale (both
// enforcing and report-only) and agnt's is SET — never merged — so an upstream
// 'unsafe-inline'/host source cannot ride along beside the authored hashes.
func TestHostileUpstreamCSPCannotSurviveWidening(t *testing.T) {
	const authored = "window.__demo_variant=1;"
	h := NewPublicHandler(&fakeVerifier{token: validToken, rev: authoredScriptRevision(authored), id: "share-1"}, nil, 0)

	hostile := http.Header{}
	hostile.Set("Content-Security-Policy", "script-src 'unsafe-inline' 'unsafe-eval' https://evil.example; default-src *")
	hostile.Set("Content-Security-Policy-Report-Only", "default-src *")
	h.writeHeaders(hostile, kindArtifact, "n0nce", cspSHA256([]byte(authored)))

	csp := hostile.Get("Content-Security-Policy")
	if len(hostile.Values("Content-Security-Policy")) != 1 {
		t.Fatalf("CSP must be a single wholesale-set header, got %v", hostile.Values("Content-Security-Policy"))
	}
	for _, leaked := range []string{"unsafe-inline", "unsafe-eval", "evil.example", "default-src *"} {
		if strings.Contains(csp, leaked) {
			t.Fatalf("INV-12 violated: upstream directive %q survived: %q", leaked, csp)
		}
	}
	if hostile.Get("Content-Security-Policy-Report-Only") != "" {
		t.Fatalf("report-only CSP not deleted wholesale")
	}
	if !strings.Contains(csp, "'"+cspSHA256([]byte(authored))+"'") {
		t.Fatalf("authored hash missing after wholesale replace: %q", csp)
	}
}

// --- Live-upstream proxied artifact (S6) ---

// countingFetcher stands in for the guarded fetcher on the route-level tests. It
// records whether the plane attempted a fetch at all, which is how the INV-4
// revoke assertion is made: a revoked share must 404 BEFORE any outbound work.
type countingFetcher struct {
	calls  int
	gotURL string
	body   []byte
	err    error
}

func (f *countingFetcher) fetchDocument(_ context.Context, rawURL string) ([]byte, error) {
	f.calls++
	f.gotURL = rawURL
	return f.body, f.err
}

// fetchSubresource keeps countingFetcher a complete upstreamDocFetcher. It
// counts into the same call tally, so the "a revoked share does no outbound
// work" assertions cover the subresource route too.
func (f *countingFetcher) fetchSubresource(_ context.Context, rawURL string) ([]byte, string, error) {
	f.calls++
	f.gotURL = rawURL
	return f.body, "text/css", f.err
}

const upstreamDoc = `<!DOCTYPE html><html><head><title>Live App</title></head>` +
	`<body><h1 id="hero">Real upstream page</h1></body></html>`

func upstreamHandler(t *testing.T, rev *publish.PublishedWalkthrough, f upstreamDocFetcher) *PublicHandler {
	t.Helper()
	h := NewPublicHandler(&fakeVerifier{token: validToken, rev: rev, id: "share-1"}, nil, 0)
	h.upstream = f
	return h
}

// TestUpstreamShareServesProxiedDocument is the positive S6 criterion: an
// upstream-bearing share serves the UPSTREAM's document with the RolePublic
// bundle injected — not the self-contained shell — under the wholesale public
// header policy.
func TestUpstreamShareServesProxiedDocument(t *testing.T) {
	f := &countingFetcher{body: []byte(upstreamDoc)}
	h := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), f)

	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("proxied artifact: got %d, want 200", w.Code)
	}
	if f.calls != 1 || f.gotURL != "https://demo.example.com/app" {
		t.Fatalf("fetcher calls=%d url=%q — the published upstream URL must be the fetch target", f.calls, f.gotURL)
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="hero"`) {
		t.Fatalf("proxied response does not carry the upstream document: %s", body)
	}
	if strings.Contains(body, shellStyleReset) {
		t.Fatalf("proxied response carries the self-contained shell's style reset: %s", body)
	}
	// RolePublic bundle, SRI-pinned exactly as on the self-contained path.
	wantTag := `<script src="` + h.assetPath + `" integrity="` + h.cspHash + `" crossorigin="anonymous"></script>`
	if !strings.Contains(body, wantTag) {
		t.Fatalf("proxied response missing SRI-pinned RolePublic bundle.\nwant: %s\ngot: %s", wantTag, body)
	}
	// The bundle must run before the upstream's own body content.
	if strings.Index(body, wantTag) > strings.Index(body, `id="hero"`) {
		t.Fatalf("bundle injected after upstream body content: %s", body)
	}
	// No dev control surface rode along.
	for _, forbidden := range []string{"__devtool_proxy_id", "window.__devtool"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("proxied response leaked dev control symbol %q (INV-1)", forbidden)
		}
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("proxied content-type: %q", ct)
	}
	if got, want := w.Header().Get("Content-Length"), strconv.Itoa(len(body)); got != want {
		t.Fatalf("Content-Length %q does not match body length %q", got, want)
	}

	// HEAD serves the same headers and no body.
	hd := do(h, http.MethodHead, sharePrefix+validToken, "", nil)
	if hd.Code != http.StatusOK || hd.Body.Len() != 0 {
		t.Fatalf("HEAD proxied artifact: code=%d bodyLen=%d", hd.Code, hd.Body.Len())
	}
}

// TestProxiedArtifactAppliesWholesaleCSP is INV-11/INV-12 on the PROXIED path,
// which is the path that actually has a hostile upstream. The served CSP is
// agnt's alone, script-src stays hash-only and gains the authored-revision
// hashes, and none of the upstream's directives, cookies, or CORS grants survive.
func TestProxiedArtifactAppliesWholesaleCSP(t *testing.T) {
	const authored = "window.__demo_variant=1;"
	rev := authoredScriptRevision(authored)
	rev.Upstream = &publish.UpstreamConfig{URL: "https://demo.example.com/app"}

	// An upstream document that itself tries to smuggle a CSP in via <meta> and
	// carries inline script: neither may become authorised.
	hostileDoc := `<!DOCTYPE html><html><head>` +
		`<meta http-equiv="Content-Security-Policy" content="script-src 'unsafe-inline' *">` +
		`</head><body><script>window.__evil=1</script></body></html>`
	h := upstreamHandler(t, rev, &countingFetcher{body: []byte(hostileDoc)})

	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("proxied artifact: got %d, want 200", w.Code)
	}
	if n := len(w.Header().Values("Content-Security-Policy")); n != 1 {
		t.Fatalf("CSP must be a single wholesale-set header, got %d", n)
	}
	csp := w.Header().Get("Content-Security-Policy")
	// INV-12 is a claim about script-src specifically, so it is asserted against
	// that directive's own sources. The proxied response's style-src does carry
	// 'unsafe-inline' by design (INV-18) — a whole-header substring scan would
	// conflate the two and report the deliberate style widening as a script
	// capability it is not.
	scriptSrcSources := strings.Fields(mustCSPDirective(t, csp, "script-src"))
	for _, src := range scriptSrcSources {
		if !strings.HasPrefix(src, "'sha256-") {
			t.Fatalf("INV-12 violated on the proxied path: script-src carries the non-hash source %q in %q", src, csp)
		}
	}
	if strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "unsafe-hashes") {
		t.Fatalf("public CSP admitted an unsafe-eval/unsafe-hashes source: %q", csp)
	}
	if !strings.Contains(csp, "'"+h.cspHash+"'") || !strings.Contains(csp, "'"+cspSHA256([]byte(authored))+"'") {
		t.Fatalf("proxied CSP must pin bundle + authored hashes: %q", csp)
	}
	if w.Header().Get("Content-Security-Policy-Report-Only") != "" {
		t.Fatalf("report-only CSP present on proxied response")
	}
	for _, mustNotSet := range []string{"Set-Cookie", "Access-Control-Allow-Origin", "Access-Control-Allow-Credentials"} {
		if w.Header().Get(mustNotSet) != "" {
			t.Fatalf("proxied response set %s", mustNotSet)
		}
	}
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("proxied Referrer-Policy: %q", got)
	}
	if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("proxied X-Frame-Options: %q", got)
	}
	// The upstream's inline script survives as BYTES (we pass the document
	// through) but is not authorised: its hash is absent from script-src, which
	// is the whole containment story now INV-6 is retired.
	if h := cspSHA256([]byte("window.__evil=1")); strings.Contains(csp, h) {
		t.Fatalf("upstream inline script hash was pinned: %q", csp)
	}
}

// TestUpstreamlessShareStaysSelfContained pins the other half of the branch: a
// share that names no upstream keeps the pre-S6 behaviour exactly, and performs
// NO outbound fetch. A regression here would turn every existing published
// walkthrough into a network call.
func TestUpstreamlessShareStaysSelfContained(t *testing.T) {
	f := &countingFetcher{body: []byte(upstreamDoc)}
	h := upstreamHandler(t, sampleWalkthrough(), f)

	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("self-contained artifact: got %d, want 200", w.Code)
	}
	if f.calls != 0 {
		t.Fatalf("upstream-less share performed %d outbound fetches, want 0", f.calls)
	}
	body := w.Body.String()
	if !strings.Contains(body, shellStyleReset) || strings.Contains(body, `id="hero"`) {
		t.Fatalf("upstream-less share no longer serves the self-contained shell: %s", body)
	}

	// An empty upstream URL is not an upstream: it must not take the proxied
	// branch and 502 an otherwise-serviceable share.
	empty := upstreamHandler(t, upstreamRevision(""), f)
	if w := do(empty, http.MethodGet, sharePrefix+validToken, "", nil); w.Code != http.StatusOK || f.calls != 0 {
		t.Fatalf("empty upstream URL: code=%d fetches=%d, want 200/0", w.Code, f.calls)
	}
}

// TestRevokedUpstreamShareNeverFetches is INV-4 on the proxied route: revoke
// kills it atomically, and "atomically" has to mean the outbound fetch never
// happens either — a revoked share that still pulls its upstream is both a
// leaked signal to the origin and work done for a dead share.
func TestRevokedUpstreamShareNeverFetches(t *testing.T) {
	f := &countingFetcher{body: []byte(upstreamDoc)}
	h := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), f)

	for _, tok := range []string{"revoked-token", "unknown", ""} {
		w := do(h, http.MethodGet, sharePrefix+tok, "", nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("token %q on proxied share: got %d, want 404", tok, w.Code)
		}
	}
	// Every sub-route of a revoked share dies with it.
	for _, sub := range []string{"/variants.json", "/walkthrough.json", "/feedback"} {
		if w := do(h, http.MethodGet, sharePrefix+"revoked-token"+sub, "", nil); w.Code != http.StatusNotFound {
			t.Fatalf("revoked %s: got %d, want 404", sub, w.Code)
		}
	}
	if f.calls != 0 {
		t.Fatalf("revoked share triggered %d upstream fetches, want 0 (INV-4)", f.calls)
	}
}

// TestUpstreamFetchFailureIsLoudAndLeaksNothing covers every refusal branch of
// the fetcher as seen from the route, plus INV-9: neither the response nor its
// headers may echo the share token, and a nil fetcher fails closed instead of
// fetching unguarded.
func TestUpstreamFetchFailureIsLoudAndLeaksNothing(t *testing.T) {
	h := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"),
		&countingFetcher{err: errors.New("upstream refused: origin \"10.0.0.7\" resolves to denied address")})

	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("failed upstream: got %d, want 502", w.Code)
	}
	// The refusal detail is daemon-network topology; the viewer gets a constant.
	if body := w.Body.String(); strings.Contains(body, "10.0.0.7") || strings.Contains(body, "denied address") {
		t.Fatalf("refusal leaked guard detail to the viewer: %q", body)
	}
	// INV-9: the token must not appear anywhere in the response we emit.
	if strings.Contains(w.Body.String(), validToken) {
		t.Fatalf("refusal body echoed the share token")
	}
	for k, vals := range w.Header() {
		for _, v := range vals {
			if strings.Contains(v, validToken) {
				t.Fatalf("header %s echoed the share token: %q", k, v)
			}
		}
	}
	// The public header policy still applies to the error response.
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "'"+h.cspHash+"'") {
		t.Fatalf("refusal response missing the public CSP: %q", csp)
	}

	// Fail closed with no fetcher at all.
	nilFetch := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), nil)
	nilFetch.upstream = nil
	if w := do(nilFetch, http.MethodGet, sharePrefix+validToken, "", nil); w.Code != http.StatusBadGateway {
		t.Fatalf("nil fetcher: got %d, want 502", w.Code)
	}

	// And the correlation handle a log IS allowed to carry is the hash prefix,
	// never the token (INV-9).
	scrubbed := publish.ScrubSharePath(sharePrefix + validToken)
	if strings.Contains(scrubbed, validToken) || scrubbed != sharePrefix+publish.HashPrefix(validToken) {
		t.Fatalf("share path scrub is not hash[:8]: %q", scrubbed)
	}
}

// --- The guarded fetcher itself (INV-13 obligations the pure guard cannot keep) ---

// publicAddr is a routable address outside every §4a deny-list prefix. Tests
// resolve their upstream hostname to it so the guard genuinely PASSES, and then
// assert the dial was made to exactly it — the pin is what is under test, not
// bypassed.
var publicAddr = netip.MustParseAddr("93.184.216.34")

// testUpstream stands up a TLS listener and returns a guardedUpstreamFetcher
// wired to reach it only through the guard: the resolver answers with publicAddr,
// and the dialer asserts it was handed that exact address before redirecting the
// connection to the local listener. A local listener is on loopback, which the
// deny-list refuses by design, so this indirection is the only way to test the
// real guarded path end to end without weakening the guard.
//
// The upstream hostname is example.com because that is the name httptest's
// certificate is issued for, so TLS verification is real rather than skipped.
func testUpstream(t *testing.T, handler http.Handler) (*httptest.Server, *guardedUpstreamFetcher, *[]string) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	tlsCfg := srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	dialed := new([]string)
	f := &guardedUpstreamFetcher{
		resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			if host != "example.com" {
				return nil, errors.New("unexpected resolve of " + host)
			}
			return []netip.Addr{publicAddr}, nil
		},
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			*dialed = append(*dialed, addr)
			if addr != publicAddr.String()+":443" {
				return nil, errors.New("dial address is not the guard-validated one: " + addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
		tlsConfig: tlsCfg,
	}
	return srv, f, dialed
}

// TestGuardedFetchDialsTheValidatedAddress is the INV-13 pin: the connection goes
// to the address CheckUpstreamOrigin approved, never to a re-resolution of the
// hostname. Without the pin a rebinding resolver answers publicly for the check
// and privately for the dial, and the whole deny-list is decorative.
func TestGuardedFetchDialsTheValidatedAddress(t *testing.T) {
	var gotReq *http.Request
	_, f, dialed := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r.Clone(context.Background())
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// A hostile upstream also tries to seed headers the public plane must not
		// propagate.
		w.Header().Set("Content-Security-Policy", "script-src 'unsafe-inline' *")
		w.Header().Set("Set-Cookie", "upstream=1")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		io.WriteString(w, upstreamDoc)
	}))

	body, err := f.fetchDocument(context.Background(), "https://example.com/app?x=1")
	if err != nil {
		t.Fatalf("guarded fetch of a public origin failed: %v", err)
	}
	if !strings.Contains(string(body), `id="hero"`) {
		t.Fatalf("fetched body is not the upstream document: %q", body)
	}
	if len(*dialed) != 1 || (*dialed)[0] != publicAddr.String()+":443" {
		t.Fatalf("dialed %v, want exactly [%s:443] — the pin is broken", *dialed, publicAddr)
	}
	if gotReq == nil {
		t.Fatal("upstream never received a request")
	}
	if gotReq.URL.RequestURI() != "/app?x=1" {
		t.Fatalf("upstream path/query not preserved: %q", gotReq.URL.RequestURI())
	}
	if gotReq.Host != "example.com" {
		t.Fatalf("Host header must remain the published hostname (TLS/vhost), got %q", gotReq.Host)
	}
}

// TestGuardedFetchForwardsNoViewerContext is INV-1 + INV-9 on the outbound leg.
// The upstream is a third party: it must learn nothing about the viewer and,
// above all, must never receive the share token — which a forwarded Referer would
// hand it in the path.
func TestGuardedFetchForwardsNoViewerContext(t *testing.T) {
	var gotReq *http.Request
	_, f, _ := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r.Clone(context.Background())
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, upstreamDoc)
	}))
	h := upstreamHandler(t, upstreamRevision("https://example.com/app"), f)

	// A viewer request loaded with everything that must NOT travel onward.
	w := do(h, http.MethodGet, sharePrefix+validToken, "", map[string]string{
		"Cookie":          "session=viewer-secret",
		"Authorization":   "Bearer viewer-token",
		"Referer":         "https://public.example" + sharePrefix + validToken,
		"X-Forwarded-For": "203.0.113.9",
		"User-Agent":      "ViewerBrowser/1.0",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("proxied artifact over the real guarded fetcher: got %d", w.Code)
	}
	if gotReq == nil {
		t.Fatal("upstream never received a request")
	}
	for _, header := range []string{"Cookie", "Authorization", "Referer", "X-Forwarded-For"} {
		if v := gotReq.Header.Get(header); v != "" {
			t.Fatalf("viewer %s forwarded to the upstream: %q", header, v)
		}
	}
	// Belt and braces: the token must not appear in ANY outbound header or in the
	// request line (INV-9).
	for k, vals := range gotReq.Header {
		for _, v := range vals {
			if strings.Contains(v, validToken) {
				t.Fatalf("outbound header %s carried the share token: %q", k, v)
			}
		}
	}
	if strings.Contains(gotReq.URL.String(), validToken) {
		t.Fatalf("outbound request line carried the share token: %q", gotReq.URL)
	}
}

// TestGuardedFetchRefusesPrivateOriginsWithoutDialing is the SSRF core: a share
// pointing into private/link-local/metadata space is refused BEFORE a socket is
// opened. Asserting "no dial happened" is the point — an error alone would also
// be produced by a connection that was attempted and failed, which is a very
// different security property.
func TestGuardedFetchRefusesPrivateOriginsWithoutDialing(t *testing.T) {
	refused := []struct{ name, rawURL string }{
		{"loopback", "https://127.0.0.1/app"},
		{"loopback name form", "https://127.1/app"},
		{"loopback decimal", "https://2130706433/app"},
		{"loopback octal", "https://0177.0.0.1/app"},
		{"loopback hex", "https://0x7f000001/app"},
		{"rfc1918 10", "https://10.0.0.7/app"},
		{"rfc1918 172.16", "https://172.16.4.4/app"},
		{"rfc1918 192.168", "https://192.168.1.1/app"},
		{"link-local", "https://169.254.1.1/app"},
		{"cloud metadata", "https://169.254.169.254/latest/meta-data/"},
		{"alibaba metadata", "https://100.100.100.200/"},
		{"cgnat", "https://100.64.0.1/"},
		{"ipv6 loopback", "https://[::1]/app"},
		{"ipv6 unique-local", "https://[fd00:ec2::254]/"},
		{"ipv6 link-local zone", "https://[fe80::1%25eth0]/"},
		{"ipv4-mapped private", "https://[::ffff:10.0.0.1]/"},
		{"nat64 metadata", "https://[64:ff9b::a9fe:a9fe]/"},
		{"6to4 loopback", "https://[2002:7f00:1::1]/"},
		{"plaintext http", "http://93.184.216.34/app"},
		{"file scheme", "file:///etc/passwd"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			var dials int
			f := &guardedUpstreamFetcher{
				// A resolver that must never be reached for a literal, and a dialer
				// that must never be reached at all.
				resolve: func(context.Context, string) ([]netip.Addr, error) {
					return []netip.Addr{publicAddr}, nil
				},
				dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
					dials++
					return nil, errors.New("must not dial")
				},
			}
			if _, err := f.fetchDocument(context.Background(), c.rawURL); err == nil {
				t.Fatalf("%s was NOT refused", c.rawURL)
			}
			if dials != 0 {
				t.Fatalf("%s opened %d connections — refusal must precede the dial", c.rawURL, dials)
			}
		})
	}

	// A name that resolves to a mix of public and private answers is refused
	// wholesale: one public answer among private ones is exactly the rebinding
	// shape, not a safe host.
	var dials int
	mixed := &guardedUpstreamFetcher{
		resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{publicAddr, netip.MustParseAddr("10.1.2.3")}, nil
		},
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials++
			return nil, errors.New("must not dial")
		},
	}
	if _, err := mixed.fetchDocument(context.Background(), "https://example.com/app"); err == nil {
		t.Fatal("host with one private answer was not refused")
	}
	if dials != 0 {
		t.Fatalf("mixed-answer host opened %d connections", dials)
	}
}

// TestPrivateUpstreamShareIsRefusedAtTheRoute is the same property observed where
// it matters: through the real handler, with the production fetcher, a share
// naming a private origin 502s and serves none of it.
func TestPrivateUpstreamShareIsRefusedAtTheRoute(t *testing.T) {
	for _, raw := range []string{"https://169.254.169.254/latest/meta-data/", "https://127.0.0.1:8080/admin", "https://10.0.0.7/"} {
		h := NewPublicHandler(&fakeVerifier{token: validToken, rev: upstreamRevision(raw), id: "share-1"}, nil, 0)
		if _, ok := h.upstream.(*guardedUpstreamFetcher); !ok {
			t.Fatalf("NewPublicHandler must install the guarded fetcher, got %T", h.upstream)
		}
		w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
		if w.Code != http.StatusBadGateway {
			t.Fatalf("%s: got %d, want 502", raw, w.Code)
		}
		if strings.Contains(w.Body.String(), shellStyleReset) {
			t.Fatalf("%s: fell back to the self-contained shell", raw)
		}
	}
}

// TestGuardedFetchRechecksEveryRedirectHop covers §4a's per-hop obligation and
// its fail-closed depth cap. A public origin that redirects into metadata space
// is the classic bypass: guarding only the first URL lets the origin choose the
// final address.
func TestGuardedFetchRechecksEveryRedirectHop(t *testing.T) {
	t.Run("hop into private space is refused", func(t *testing.T) {
		_, f, dialed := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://169.254.169.254/latest/meta-data/", http.StatusFound)
		}))
		_, err := f.fetchDocument(context.Background(), "https://example.com/app")
		if err == nil {
			t.Fatal("redirect into cloud-metadata space was followed")
		}
		// The refusal must come from the GUARD, not incidentally from a dial that
		// happened to fail. Without a per-hop re-check this test would still see an
		// error (the pinned-dial assertion inside the harness would produce one),
		// so the reason is the assertion that has teeth here.
		if !strings.Contains(err.Error(), "upstream refused") {
			t.Fatalf("hop was not refused by the origin guard: %v", err)
		}
		if len(*dialed) != 1 {
			t.Fatalf("dialed %v — the metadata hop must never reach the dialer", *dialed)
		}
	})

	t.Run("relative hop is re-guarded and followed", func(t *testing.T) {
		_, f, dialed := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/final" {
				http.Redirect(w, r, "/final", http.StatusMovedPermanently)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, upstreamDoc)
		}))
		body, err := f.fetchDocument(context.Background(), "https://example.com/app")
		if err != nil {
			t.Fatalf("legitimate redirect not followed: %v", err)
		}
		if !strings.Contains(string(body), `id="hero"`) {
			t.Fatalf("redirected body wrong: %q", body)
		}
		// Two hops, each pinned to the validated address.
		if len(*dialed) != 2 {
			t.Fatalf("dialed %v, want one dial per hop", *dialed)
		}
	})

	t.Run("chain longer than the cap fails closed", func(t *testing.T) {
		var hops int
		_, f, _ := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hops++
			http.Redirect(w, r, "/next"+strconv.Itoa(hops), http.StatusFound)
		}))
		if _, err := f.fetchDocument(context.Background(), "https://example.com/app"); err == nil {
			t.Fatal("unbounded redirect chain was not refused")
		}
		if hops > maxUpstreamRedirects+1 {
			t.Fatalf("followed %d hops, cap is %d", hops, maxUpstreamRedirects)
		}
	})

	t.Run("redirect with no Location is refused", func(t *testing.T) {
		_, f, _ := testUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusFound)
		}))
		if _, err := f.fetchDocument(context.Background(), "https://example.com/app"); err == nil {
			t.Fatal("3xx with no Location was accepted")
		}
	})
}

// TestGuardedFetchRefusesNonDocumentResponses keeps the artifact route a document
// route. Without these checks an anonymous URL becomes a general-purpose relay
// for whatever bytes the publisher's origin decides to return, and an oversize
// body becomes a remote memory-amplification lever.
func TestGuardedFetchRefusesNonDocumentResponses(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"non-HTML content-type", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"not":"a document"}`)
		}},
		{"missing content-type", func(w http.ResponseWriter, r *http.Request) {
			w.Header()["Content-Type"] = nil
			w.WriteHeader(http.StatusOK)
			w.Write([]byte{0x00, 0x01})
		}},
		{"upstream error status", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			http.Error(w, "<html>upstream 500</html>", http.StatusInternalServerError)
		}},
		{"oversize document", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, "<html><head></head><body>")
			chunk := strings.Repeat("x", 64<<10)
			for written := 0; written <= maxPublicUpstreamBytes; written += len(chunk) {
				if _, err := io.WriteString(w, chunk); err != nil {
					return
				}
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, f, _ := testUpstream(t, c.handler)
			body, err := f.fetchDocument(context.Background(), "https://example.com/app")
			if err == nil {
				t.Fatalf("accepted a non-document upstream response (%d bytes)", len(body))
			}
			if body != nil {
				t.Fatalf("refusal returned %d bytes alongside the error — a partial document must never be served", len(body))
			}
		})
	}
}

// --- Always-on demo indicator (S7, spec §9c / INV-14) ---

// cspDirectives parses a served Content-Security-Policy into directive name ->
// source list, so a test can assert the EXACT shape of the policy rather than
// substring-matching it. That precision is the point: the interesting regression
// is a directive or source silently GAINED to make some new module work.
func cspDirectives(t *testing.T, csp string) map[string][]string {
	t.Helper()
	if csp == "" {
		t.Fatal("response carried no Content-Security-Policy")
	}
	out := map[string][]string{}
	for _, d := range strings.Split(csp, ";") {
		fields := strings.Fields(strings.TrimSpace(d))
		if len(fields) == 0 {
			continue
		}
		if _, dup := out[fields[0]]; dup {
			t.Fatalf("duplicate directive %q in CSP %q", fields[0], csp)
		}
		out[fields[0]] = fields[1:]
	}
	return out
}

// assertPublicCSPUnchanged pins the served public policy directive-by-directive
// against the S6 baseline: exactly these nine directives, and for each exactly
// these sources. The demo indicator had to be styled without paying for a single
// one of them (CSSOM-only, closed shadow root), so this is the assertion that
// catches the tempting shortcut — an inline style needs style-src widened, an
// external asset needs img-src/font-src widened, and either would show up here.
// style-src's nonce source is the one value that legitimately varies: the
// self-contained shell carries one for its own reset, the proxied path passes an
// empty nonce and so must carry none.
// styleExtra names the ONE source a response shape may carry in style-src
// beyond 'self': the self-contained shell's per-response nonce, or the proxied
// artifact's 'unsafe-inline' (INV-18). Any other value on any shape fails.
type styleExtra string

const (
	styleExtraNonce          styleExtra = "nonce"
	styleExtraUpstreamInline styleExtra = "unsafe-inline"
)

func assertPublicCSPUnchanged(t *testing.T, h *PublicHandler, csp string, extraAllowed styleExtra) {
	t.Helper()
	got := cspDirectives(t, csp)

	want := map[string][]string{
		"default-src":     {"'self'"},
		"script-src":      {"'" + h.cspHash + "'"},
		"style-src":       {"'self'"},
		"img-src":         {"'self'", "data:"},
		"connect-src":     {"'self'"},
		"frame-ancestors": {"'none'"},
		"base-uri":        {"'none'"},
		"form-action":     {"'self'"},
		"object-src":      {"'none'"},
	}
	if len(got) != len(want) {
		t.Fatalf("public CSP gained/lost a directive: got %v, want exactly %v", got, want)
	}
	for name, wantSrc := range want {
		gotSrc, ok := got[name]
		if !ok {
			t.Errorf("public CSP missing directive %q", name)
			continue
		}
		if name == "style-src" {
			// 'self' plus exactly ONE further source, and only the one this shape
			// is entitled to: the shell's nonce, or the proxied document's
			// 'unsafe-inline' (INV-18). Never both, never anything else.
			if len(gotSrc) == 0 || gotSrc[0] != "'self'" {
				t.Errorf("style-src must start with 'self': %v", gotSrc)
			}
			extra := gotSrc[1:]
			if len(extra) != 1 {
				t.Errorf("style-src must carry exactly one source beyond 'self', got %v", extra)
				continue
			}
			switch extraAllowed {
			case styleExtraNonce:
				if !strings.HasPrefix(extra[0], "'nonce-") {
					t.Errorf("the self-contained shell's style-src must carry the shell nonce, got %v", extra)
				}
			case styleExtraUpstreamInline:
				if extra[0] != "'unsafe-inline'" {
					t.Errorf("the proxied document's style-src must carry 'unsafe-inline' for the upstream's own inline styles, got %v", extra)
				}
			default:
				t.Errorf("unexpected style-src extra %v for an unentitled shape", extra)
			}
			continue
		}
		if len(gotSrc) != len(wantSrc) {
			t.Errorf("directive %q gained/lost a source: got %v, want %v", name, gotSrc, wantSrc)
			continue
		}
		for i := range wantSrc {
			if gotSrc[i] != wantSrc[i] {
				t.Errorf("directive %q source %d = %q, want %q", name, i, gotSrc[i], wantSrc[i])
			}
		}
	}
	// Belt and braces on the sources that would make the whole hash pin moot.
	// 'unsafe-inline' is checked PER DIRECTIVE rather than over the whole header:
	// it is admissible in style-src on exactly one shape (INV-18) and admissible
	// nowhere in script-src on any shape (INV-12), and a whole-header substring
	// scan cannot tell those apart.
	for name, sources := range got {
		for _, src := range sources {
			if name == "style-src" && src == "'unsafe-inline'" {
				continue // adjudicated above, against this shape's entitlement
			}
			if strings.Contains(src, "unsafe-") {
				t.Errorf("directive %q admitted %q: %q", name, src, csp)
			}
		}
	}
	for _, src := range got["script-src"] {
		if !strings.HasPrefix(src, "'sha256-") {
			t.Errorf("script-src admitted the non-hash source %q: %q", src, csp)
		}
	}
}

// TestDemoIndicatorShipsOnBothPublicArtifactPaths is the INV-14 route-level
// criterion: the served bundle carries the disclosure badge with non-empty text,
// and BOTH artifact shapes — self-contained shell and proxied upstream document —
// load exactly that bundle under its SRI pin. The two paths are asserted
// separately because they are different code paths in serveArtifact and only one
// of them existed before S6.
func TestDemoIndicatorShipsOnBothPublicArtifactPaths(t *testing.T) {
	// The bundle the public asset route actually serves must carry the module and
	// its disclosure text.
	h := newTestHandler(nil)
	asset := do(h, http.MethodGet, h.assetPath, "", nil)
	if asset.Code != http.StatusOK {
		t.Fatalf("public asset: got %d, want 200", asset.Code)
	}
	bundle := asset.Body.String()
	for _, want := range []string{
		"// demo-indicator module\n",
		"Demo walkthrough",
		"not the live site",
		"attachShadow({ mode: 'closed' })",
	} {
		if !strings.Contains(bundle, want) {
			t.Errorf("served public bundle missing %q", want)
		}
	}
	// And the badge must not be reachable-by-removal from the served bytes: no
	// disable surface travelled with it.
	for _, banned := range []string{"__agntDemoIndicator.remove", "demo-indicator-disabled"} {
		if strings.Contains(bundle, banned) {
			t.Errorf("served bundle carries a disable surface %q", banned)
		}
	}

	tag := func(h *PublicHandler) string {
		return `<script src="` + h.assetPath + `" integrity="` + h.cspHash + `" crossorigin="anonymous"></script>`
	}

	// Path 1: self-contained shell.
	selfContained := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if selfContained.Code != http.StatusOK {
		t.Fatalf("self-contained artifact: got %d, want 200", selfContained.Code)
	}
	if !strings.Contains(selfContained.Body.String(), tag(h)) {
		t.Errorf("self-contained artifact does not load the bundle carrying the indicator")
	}

	// Path 2: proxied upstream document.
	up := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), &countingFetcher{body: []byte(upstreamDoc)})
	proxied := do(up, http.MethodGet, sharePrefix+validToken, "", nil)
	if proxied.Code != http.StatusOK {
		t.Fatalf("proxied artifact: got %d, want 200", proxied.Code)
	}
	body := proxied.Body.String()
	if !strings.Contains(body, tag(up)) {
		t.Errorf("proxied artifact does not load the bundle carrying the indicator")
	}
	// The disclosure must be able to render before the upstream's own content
	// paints, or a viewer sees the lookalike first.
	if strings.Index(body, tag(up)) > strings.Index(body, `id="hero"`) {
		t.Errorf("indicator bundle injected after upstream body content")
	}
	// Same bundle bytes on both paths: one hash, one pin, no per-path flavour that
	// could omit the badge.
	if h.assetPath != up.assetPath || h.cspHash != up.cspHash {
		t.Errorf("public artifact paths serve different bundles (%q/%q vs %q/%q)", h.assetPath, h.cspHash, up.assetPath, up.cspHash)
	}
}

// TestDemoIndicatorDisclosureTextPerPath follows the disclosure from each artifact
// document to the exact bytes that document pins, and asserts the wording is true
// of THAT path. It is a per-path assertion even though both paths serve one bundle:
// the self-contained path has no upstream at all, so wording claiming a proxied
// site would be a false statement served under a share URL. The proxied path is
// where the deception risk is highest, so the same check runs there too.
func TestDemoIndicatorDisclosureTextPerPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    *PublicHandler
	}{
		{"self-contained", newTestHandler(nil)},
		{"proxied", upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), &countingFetcher{body: []byte(upstreamDoc)})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := do(tc.h, http.MethodGet, sharePrefix+validToken, "", nil)
			if doc.Code != http.StatusOK {
				t.Fatalf("artifact: got %d, want 200", doc.Code)
			}
			// The document must pin the bundle whose bytes we are about to read;
			// otherwise this test would assert about a bundle nobody serves.
			tag := `<script src="` + tc.h.assetPath + `" integrity="` + tc.h.cspHash + `" crossorigin="anonymous"></script>`
			if !strings.Contains(doc.Body.String(), tag) {
				t.Fatalf("artifact does not load the pinned public bundle")
			}
			asset := do(tc.h, http.MethodGet, tc.h.assetPath, "", nil)
			if asset.Code != http.StatusOK {
				t.Fatalf("public asset: got %d, want 200", asset.Code)
			}
			served := asset.Body.String()

			if !strings.Contains(served, "Demo walkthrough — not the live site.") {
				t.Errorf("%s path does not serve the path-neutral disclosure text", tc.name)
			}
			// The defect this pins: wording that asserts a relationship to a live
			// site which, on the self-contained path, does not exist.
			if strings.Contains(served, "Demo walkthrough of a proxied site") {
				t.Errorf("%s path serves a disclosure asserting proxying; false on the self-contained path", tc.name)
			}
		})
	}
}

// TestDemoIndicatorDoesNotWidenPublicCSP is the assertion most likely to catch a
// regression, so it is exact rather than substring-based: adding the mandatory
// badge must not have bought a single directive or source on EITHER artifact path.
// A future "make the badge visible" fix that reaches for an inline style, a web
// font, or an image would have to widen style-src/font-src/img-src, and that is
// what fails here.
func TestDemoIndicatorDoesNotWidenPublicCSP(t *testing.T) {
	h := newTestHandler(nil)
	selfContained := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	if selfContained.Code != http.StatusOK {
		t.Fatalf("self-contained artifact: got %d, want 200", selfContained.Code)
	}
	if n := len(selfContained.Header().Values("Content-Security-Policy")); n != 1 {
		t.Fatalf("CSP must be a single wholesale-set header, got %d", n)
	}
	assertPublicCSPUnchanged(t, h, selfContained.Header().Get("Content-Security-Policy"), styleExtraNonce)

	up := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), &countingFetcher{body: []byte(upstreamDoc)})
	proxied := do(up, http.MethodGet, sharePrefix+validToken, "", nil)
	if proxied.Code != http.StatusOK {
		t.Fatalf("proxied artifact: got %d, want 200", proxied.Code)
	}
	if n := len(proxied.Header().Values("Content-Security-Policy")); n != 1 {
		t.Fatalf("proxied CSP must be a single wholesale-set header, got %d", n)
	}
	assertPublicCSPUnchanged(t, up, proxied.Header().Get("Content-Security-Policy"), styleExtraUpstreamInline)
}
