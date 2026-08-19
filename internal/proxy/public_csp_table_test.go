package proxy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/publish"
)

// public_csp_table_test.go is INV-18's header table: 'unsafe-inline' in
// style-src appears on EXACTLY one response shape — the proxied live-upstream
// artifact document — and the fetch-governing directives widen nowhere.
//
// A single-shape assertion would not carry that claim; the point is the sweep.

// cspDirective pulls one directive's value out of a CSP header.
func cspDirective(csp, name string) (string, bool) {
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		if d == name {
			return "", true
		}
		if strings.HasPrefix(d, name+" ") {
			return strings.TrimSpace(strings.TrimPrefix(d, name+" ")), true
		}
	}
	return "", false
}

// publicResponseShapes drives one handler through every response shape the
// public plane can produce and returns each one's CSP.
func publicResponseShapes(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}

	// 1. Self-contained artifact (no upstream): the shell, with its nonce.
	selfContained := NewPublicHandler(&fakeVerifier{token: validToken, rev: sampleWalkthrough(), id: subShareID}, nil, 0)
	out["self-contained artifact"] = do(selfContained, http.MethodGet, sharePrefix+validToken, "", nil).Header().Get("Content-Security-Policy")
	out["variants.json"] = do(selfContained, http.MethodGet, sharePrefix+validToken+"/variants.json", "", nil).Header().Get("Content-Security-Policy")
	out["walkthrough.json"] = do(selfContained, http.MethodGet, sharePrefix+validToken+"/walkthrough.json", "", nil).Header().Get("Content-Security-Policy")
	out["feedback"] = do(selfContained, http.MethodPost, sharePrefix+validToken+"/feedback", `{"note":"hi"}`,
		map[string]string{"Content-Type": "application/json"}).Header().Get("Content-Security-Policy")

	// 2. Proxied artifact, and its refusal shape.
	proxied := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"),
		&countingFetcher{body: []byte(upstreamDoc)})
	out["proxied artifact"] = do(proxied, http.MethodGet, sharePrefix+validToken, "", nil).Header().Get("Content-Security-Policy")

	refusing := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), refusingFetcher{})
	out["upstream refusal"] = do(refusing, http.MethodGet, sharePrefix+validToken, "", nil).Header().Get("Content-Security-Policy")

	// 3. Subresource: served, and refused.
	f, _ := upstreamServing(t, map[string]struct{ ct, body string }{
		"/site.css":  {"text/css", "body{}"},
		"/evil.html": {"text/html", "<html></html>"},
	})
	sub := upstreamHandler(t, upstreamRevision("https://example.com/app"), f)
	out["subresource"] = do(sub, http.MethodGet, signedSubPath(t, sub, "https://example.com/site.css", 1), "", nil).Header().Get("Content-Security-Policy")
	out["subresource refusal"] = do(sub, http.MethodGet, signedSubPath(t, sub, "https://example.com/evil.html", 1), "", nil).Header().Get("Content-Security-Policy")

	for name, csp := range out {
		if csp == "" {
			t.Fatalf("%s produced no CSP header at all", name)
		}
	}
	return out
}

// TestUnsafeInlineStyleIsConfinedToTheProxiedArtifact is INV-18. Removing the
// widening must fail ONLY the proxied-artifact row; adding it anywhere else must
// fail that row.
//
// The expected value is the LITERAL directive, not the proxiedUpstreamStyleSrc
// constant: asserting a constant against itself passes no matter what the
// constant says, so the mutation "narrow the constant back to 'self'" would slip
// straight through (publish-security-review-lessons §8).
func TestUnsafeInlineStyleIsConfinedToTheProxiedArtifact(t *testing.T) {
	const wantProxiedStyleSrc = "'self' 'unsafe-inline'"

	shapes := publicResponseShapes(t)

	for name, csp := range shapes {
		styleSrc, ok := cspDirective(csp, "style-src")
		if !ok {
			t.Fatalf("%s: no style-src directive: %s", name, csp)
		}
		hasUnsafe := strings.Contains(styleSrc, "unsafe-inline")
		if name == "proxied artifact" {
			if styleSrc != wantProxiedStyleSrc {
				t.Fatalf("proxied artifact style-src = %q, want %q — without it the upstream's own inline styles are refused and the page renders broken", styleSrc, wantProxiedStyleSrc)
			}
			continue
		}
		if hasUnsafe {
			t.Fatalf("%s carries 'unsafe-inline' in style-src (%q); the widening belongs to the proxied artifact alone", name, styleSrc)
		}
	}

	// The self-contained shell keeps its nonce, unchanged.
	if styleSrc, _ := cspDirective(shapes["self-contained artifact"], "style-src"); !strings.HasPrefix(styleSrc, "'self' 'nonce-") {
		t.Fatalf("self-contained shell style-src = %q, want 'self' plus a nonce", styleSrc)
	}
}

// TestFetchDirectivesNeverWiden: style is presentation, and only style moved.
// Every fetch-governing directive must read identically on every shape.
func TestFetchDirectivesNeverWiden(t *testing.T) {
	want := map[string]string{
		"default-src":     "'self'",
		"img-src":         "'self' data:",
		"connect-src":     "'self'",
		"frame-ancestors": "'none'",
		"base-uri":        "'none'",
		"form-action":     "'self'",
		"object-src":      "'none'",
	}
	for name, csp := range publicResponseShapes(t) {
		for directive, expect := range want {
			got, ok := cspDirective(csp, directive)
			if !ok {
				t.Fatalf("%s: missing %s: %s", name, directive, csp)
			}
			if got != expect {
				t.Fatalf("%s: %s = %q, want %q — the fetch directives must not widen anywhere", name, directive, got, expect)
			}
		}
		// font-src is deliberately absent so fonts fall to default-src 'self'.
		if _, ok := cspDirective(csp, "font-src"); ok {
			t.Fatalf("%s grew a font-src directive: %s", name, csp)
		}
	}
}

// TestScriptSrcStaysHashOnlyEverywhere is INV-12 restated across the widened
// world: no public response's script-src may contain 'self', a host source, or
// any unsafe-* keyword. This is the assertion that makes the style-src widening
// containable — style moved, capability did not.
func TestScriptSrcStaysHashOnlyEverywhere(t *testing.T) {
	for name, csp := range publicResponseShapes(t) {
		scriptSrcValue, ok := cspDirective(csp, "script-src")
		if !ok {
			t.Fatalf("%s: no script-src directive: %s", name, csp)
		}
		for _, source := range strings.Fields(scriptSrcValue) {
			if !strings.HasPrefix(source, "'sha256-") || !strings.HasSuffix(source, "'") {
				t.Fatalf("%s: script-src carries the non-hash source %q (full: %q)", name, source, scriptSrcValue)
			}
		}
	}
}

// TestSelfContainedArtifactCSPUnchanged pins the self-contained path byte for
// byte (modulo the per-response nonce). The subresource work must not have
// touched the shape that performs no outbound fetch at all.
func TestSelfContainedArtifactCSPUnchanged(t *testing.T) {
	h := NewPublicHandler(&fakeVerifier{token: validToken, rev: sampleWalkthrough(), id: subShareID}, nil, 0)
	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	csp := w.Header().Get("Content-Security-Policy")

	styleSrc, _ := cspDirective(csp, "style-src")
	normalized := strings.Replace(csp, styleSrc, "<STYLE>", 1)
	want := "default-src 'self'; script-src '" + h.cspHash + "'; style-src <STYLE>; " +
		"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; " +
		"base-uri 'none'; form-action 'self'; object-src 'none'"
	if normalized != want {
		t.Fatalf("self-contained CSP changed.\n got: %s\nwant: %s", normalized, want)
	}
}

// TestProxiedArtifactCSPExact pins the proxied shape whole, so a future edit
// cannot widen a second directive under cover of this one.
func TestProxiedArtifactCSPExact(t *testing.T) {
	h := upstreamHandler(t, upstreamRevision("https://demo.example.com/app"), &countingFetcher{body: []byte(upstreamDoc)})
	w := do(h, http.MethodGet, sharePrefix+validToken, "", nil)
	// Literal, for the same reason as TestUnsafeInlineStyleIsConfined…: a pin
	// written in terms of the value it pins holds under any change to it.
	want := "default-src 'self'; script-src '" + h.cspHash + "'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; " +
		"base-uri 'none'; form-action 'self'; object-src 'none'"
	if got := w.Header().Get("Content-Security-Policy"); got != want {
		t.Fatalf("proxied artifact CSP.\n got: %s\nwant: %s", got, want)
	}
}

// TestAuthoredScriptHashesStillWidenOnlyScriptSrc: the authored-script hashes
// (INV-12) compose with the style widening without either leaking into the
// other.
func TestAuthoredScriptHashesStillWidenOnlyScriptSrc(t *testing.T) {
	rev := authoredScriptRevision("window.__demo_variant=1;")
	rev.Upstream = &publish.UpstreamConfig{URL: "https://demo.example.com/app"}
	h := upstreamHandler(t, rev, &countingFetcher{body: []byte(upstreamDoc)})
	csp := do(h, http.MethodGet, sharePrefix+validToken, "", nil).Header().Get("Content-Security-Policy")

	scriptSrcValue, _ := cspDirective(csp, "script-src")
	if len(strings.Fields(scriptSrcValue)) != 2 {
		t.Fatalf("script-src should carry the bundle hash plus one authored hash: %q", scriptSrcValue)
	}
	styleSrc, _ := cspDirective(csp, "style-src")
	if styleSrc != "'self' 'unsafe-inline'" {
		t.Fatalf("style-src = %q, want %q", styleSrc, "'self' 'unsafe-inline'")
	}
}

// mustCSPDirective returns one directive's sources or fails the test.
func mustCSPDirective(t *testing.T, csp, name string) string {
	t.Helper()
	v, ok := cspDirective(csp, name)
	if !ok {
		t.Fatalf("CSP has no %s directive: %s", name, csp)
	}
	return v
}
