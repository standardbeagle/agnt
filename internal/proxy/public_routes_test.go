package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/publish"
)

// fakeVerifier is a constant-time-agnostic stand-in for *publishstore.Store on
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

// capturingSink records the last feedback body handed off, to prove the P7
// route-level guards ran before hand-off.
type capturingSink struct {
	shareID string
	body    []byte
	calls   int
}

func (s *capturingSink) Accept(shareID string, body []byte) error {
	s.calls++
	s.shareID = shareID
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
	return NewPublicHandler(v, sink)
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
		"/__devtool_metrics",             // metrics WebSocket upgrade
		"/__devtool_axe",                 // axe-core audit asset
		"/__devtool/ws",                  // dev WS channel
		"/__devtool/exec",                // proxy exec bridge
		"/__devtool/inject.js",           // dev bundle (non-public path)
		"/__devtool/inject.deadbeef.js",  // forged/unknown hash → must NOT serve dev bundle
		"/__devtool/",                    // control subtree root
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
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, h.cspHash) {
		t.Fatalf("served CSP is not agnt's self+hash policy: %q", csp)
	}
	if hostile.Get("Content-Security-Policy-Report-Only") != "" {
		t.Fatalf("INV-12 violated: Content-Security-Policy-Report-Only not deleted wholesale")
	}
	if hostile.Get("Set-Cookie") != "" {
		t.Fatalf("Set-Cookie not stripped on public plane")
	}

	// The live artifact response must carry the same CSP posture end-to-end.
	liveCSP := w.Header().Get("Content-Security-Policy")
	if strings.Contains(liveCSP, "unsafe-inline") || !strings.Contains(liveCSP, "script-src 'self'") {
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
	big := strings.Repeat("x", maxFeedbackBody+100)
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
