package scripts

import (
	"regexp"
	"strings"
	"testing"
)

// Pure-logic (no-browser) gates for public-boot.js — the glue that turns the
// three inert public members on for a real visitor. The DOM behavior (a served
// /s/{token} actually rendering step 0) is covered by the chromee2e test in
// internal/proxy/publish_browser_e2e_test.go; these pin what can be proven
// without a browser: the double gate, the same-origin token-in-path fetch, the
// absence of code/markup sinks, and loud failure.

// TestPublicBootDoubleGate pins the two independent gates that keep the ONLY
// self-starting public module inert everywhere but a served share. Both must be
// checked BEFORE any listener is registered: a dev-proxied page must carry
// neither the boot nor the listener.
func TestPublicBootDoubleGate(t *testing.T) {
	js := publicBootJS
	if !strings.Contains(js, "__agnt_public_version") {
		t.Error("gate 1 missing: boot must require the RolePublic version marker")
	}
	if !strings.Contains(js, "if (isPublicPlane()) {") {
		t.Error("gate 1 must wrap the listener registration, not merely early-return inside the callback")
	}
	if !strings.Contains(js, "SHARE_PATH_RE") || !strings.Contains(js, "function shareToken") {
		t.Error("gate 2 missing: boot must derive the token from the /s/{token} pathname")
	}
	// The listener must be registered inside the gate. Everything before the
	// gate's opening line must be listener-free.
	gate := strings.Index(js, "if (isPublicPlane()) {")
	if gate < 0 {
		t.Fatal("public-plane gate not found")
	}
	if strings.Contains(js[:gate], "addEventListener('DOMContentLoaded'") {
		t.Error("DOMContentLoaded listener registered before the public-plane gate — the module would not be inert in dev bundles")
	}
}

// TestPublicBootShareTokenGrammar pins the share-route pattern against the token
// shape create() actually mints (43-char base64url) and the paths that must NOT
// boot. The Go-side regexp mirrors the JS literal so the table is meaningful.
func TestPublicBootShareTokenGrammar(t *testing.T) {
	const want = `/^\/s\/([A-Za-z0-9_-]{16,128})\/?$/`
	if !strings.Contains(publicBootJS, want) {
		t.Fatalf("share-path pattern drifted; expected %s in public-boot.js", want)
	}
	re := regexp.MustCompile(`^/s/([A-Za-z0-9_-]{16,128})/?$`)
	token43 := strings.Repeat("a", 43)
	boots := []string{"/s/" + token43, "/s/" + token43 + "/", "/s/" + strings.Repeat("A9_-", 4)}
	for _, p := range boots {
		if !re.MatchString(p) {
			t.Errorf("path %q must boot the player", p)
		}
	}
	// Non-share paths, traversal attempts, and sub-routes must not boot: the
	// JSON endpoints and the feedback POST are not player pages.
	inert := []string{
		"/", "/s/", "/s/short", "/index.html",
		"/s/" + token43 + "/walkthrough.json",
		"/s/" + token43 + "/feedback",
		"/s/" + token43 + "/../admin",
		"/s/" + strings.Repeat("a", 129),
	}
	for _, p := range inert {
		if re.MatchString(p) {
			t.Errorf("path %q must NOT boot the player", p)
		}
	}
}

// TestPublicBootFetchContract pins how the artifact is retrieved: same-origin,
// token in the PATH (never a query string — those leak via Referer and proxy
// logs), no credentials (the public plane is anonymous and sets no cookies).
func TestPublicBootFetchContract(t *testing.T) {
	js := publicBootJS
	if !strings.Contains(js, "'/s/' + token + '/walkthrough.json'") {
		t.Error("boot must fetch the artifact from the token's own same-origin path")
	}
	if !strings.Contains(js, "credentials: 'omit'") {
		t.Error("the artifact fetch must omit credentials (anonymous public plane)")
	}
	if strings.Contains(js, "?token=") || strings.Contains(js, "&token=") {
		t.Error("the token must never travel in a query string")
	}
	// A cross-origin sink would defeat the token-confidentiality story even
	// though connect-src 'self' also blocks it.
	for _, sink := range []string{"http://", "https://", "//' +", "XMLHttpRequest", "sendBeacon"} {
		if strings.Contains(js, sink) {
			t.Errorf("boot must have no cross-origin/alternate transport sink, found %q", sink)
		}
	}
}

// TestPublicBootNoCodeOrMarkupSinks asserts the boot introduces none of the
// sinks the public bundle is proven free of. The forbidden-symbol scan in
// role_public_test.go covers the assembled bundle; this pins the module itself
// so a regression names this file.
func TestPublicBootNoCodeOrMarkupSinks(t *testing.T) {
	js := publicBootJS
	for _, sink := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "javascript:"} {
		if strings.Contains(js, sink) {
			t.Errorf("public-boot must not contain the %q sink", sink)
		}
	}
	if !strings.Contains(js, "el.textContent = text") {
		t.Error("the failure notice must be built with textContent")
	}
	// The response is handed to the viewer, which re-validates every step; boot
	// itself must never treat a response value as a selector or as code.
	if strings.Contains(js, "querySelector") {
		t.Error("boot must not resolve selectors itself — the viewer owns selector validation")
	}
}

// TestPublicBootFailsLoud pins the Silent Failure Prohibition on the public
// plane: every failure mode tells the visitor something instead of leaving a
// blank page, and a revoked/unknown token must not become an existence oracle.
func TestPublicBootFailsLoud(t *testing.T) {
	js := publicBootJS
	for _, branch := range []string{"res.status === 404", "!res.ok", "['catch']", "no steps"} {
		if !strings.Contains(js, branch) {
			t.Errorf("boot missing the %q failure branch", branch)
		}
	}
	if !strings.Contains(js, "function notice") {
		t.Error("boot must render a visitor-facing notice on failure")
	}
	// 404 (unknown), revoked, and rotated must be indistinguishable to a viewer.
	if strings.Contains(js, "revoked'") || strings.Contains(js, "not found'") {
		t.Error("the failure notice must not distinguish revoked from unknown — that is an existence oracle")
	}
}

// TestPublicBootIsolatedIIFE asserts the module keeps the leading space after
// `function` so wrapModule leaves its IIFE intact rather than merging it into
// the shared bundle scope (same convention as the other three public members).
func TestPublicBootIsolatedIIFE(t *testing.T) {
	body := headerless(publicBootJS)
	if !strings.HasPrefix(body, "(function () {") {
		t.Errorf("public-boot must open with an isolated IIFE `(function () {`, got %.24q", body)
	}
	if wrapped := wrapModule(publicBootJS); !strings.Contains(wrapped, "(function () {") {
		t.Error("wrapModule must leave the public-boot IIFE intact")
	}
}
