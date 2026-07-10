package proxy

// Real-browser coverage for the OAuth popup round trip. The popup marker
// cannot be tested without a browser: it depends on window.open cloning the
// opener's sessionStorage, and on Chrome clearing window.name across the
// cross-site proxy→IdP→proxy navigation the popup actually makes. A string
// test would assert the code we wrote, not the behavior we depend on.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cdp "github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"
)

// startBreakoutProxy wraps startWrapProxy and installs breakout rules that
// match the fake IdP's host.
func startBreakoutProxy(t *testing.T, backend *httptest.Server, idpHost, mode string) *ProxyServer {
	t.Helper()
	ps := startWrapProxy(t, backend)
	ps.SetAuthBreakout(&AuthBreakout{Mode: mode, Patterns: []string{idpHost}})
	return ps
}

// newFakeIDP serves an /authorize endpoint that immediately 302s to the
// supplied callback URL with an auth code, standing in for a real provider.
// It also sends X-Frame-Options: DENY so a regression that leaves the flow in
// the iframe fails here the same way it fails in production.
//
// The returned URL is rewritten onto `localhost` while the proxy stays on
// `127.0.0.1`. That is load-bearing, not cosmetic: two ports on 127.0.0.1 are
// cross-*origin* but same-*site*, and Chrome only clears window.name across a
// cross-site navigation. A same-site fake IdP would let a window.name-only
// popup marker pass here while failing against every real provider.
func newFakeIDP(t *testing.T) (idpURL string, callback *string) {
	t.Helper()
	callback = new(string)
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		http.Redirect(w, r, *callback+"?code=abc123", http.StatusFound)
	})
	idp := httptest.NewServer(mux)
	t.Cleanup(idp.Close)
	return strings.Replace(idp.URL, "127.0.0.1", "localhost", 1), callback
}

// newAuthBackend serves an app whose root links to the IdP and whose /callback
// renders a marker the test can wait on.
func newAuthBackend(t *testing.T, authorizeURL func() string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Callback</title></head>`+
			`<body><div id="signed-in">code=%s</div></body></html>`, r.URL.Query().Get("code"))
	})
	mux.HandleFunc("/redirect-login", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, authorizeURL(), http.StatusFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>App</title></head>`+
			`<body><a id="login" href="%s">Sign in</a></body></html>`, authorizeURL())
	})
	backend := httptest.NewServer(mux)
	t.Cleanup(backend.Close)
	return backend
}

// newBrowser boots a headless Chrome. Popups need a real window target, so
// no --headless=old quirks: default chromedp exec allocator is fine.
func newBrowser(t *testing.T, timeout time.Duration) context.Context {
	t.Helper()
	allocCtx, allocCancel := cdp.NewExecAllocator(context.Background(),
		append(cdp.DefaultExecAllocatorOptions[:],
			cdp.Flag("headless", true),
			cdp.Flag("no-sandbox", true),
			cdp.Flag("disable-gpu", true),
			// Chrome's popup blocker would otherwise suppress the window.open
			// that the whole flow hinges on.
			cdp.Flag("disable-popup-blocking", true),
		)...)
	t.Cleanup(allocCancel)
	ctx, cancel := cdp.NewContext(allocCtx)
	t.Cleanup(cancel)
	ctx, tcancel := context.WithTimeout(ctx, timeout)
	t.Cleanup(tcancel)
	return ctx
}

// contentHref reads the live URL of the shell's content iframe (same origin).
const contentHrefJS = `(function(){var f=document.getElementById('__devtool_content_frame');` +
	`try{return f?f.contentWindow.location.href:'';}catch(e){return 'CROSS_ORIGIN';}})()`

// waitContentHref polls the content frame URL until it contains want.
func waitContentHref(ctx context.Context, t *testing.T, want string) string {
	t.Helper()
	var href string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := cdp.Run(ctx, cdp.Evaluate(contentHrefJS, &href)); err == nil && strings.Contains(href, want) {
			return href
		}
		if err := cdp.Run(ctx, cdp.Sleep(150*time.Millisecond)); err != nil {
			t.Fatalf("browser died while polling content frame: %v", err)
		}
	}
	t.Fatalf("content frame never reached %q (last: %q)", want, href)
	return ""
}

// TestE2E_AuthBreakout_PopupRoundTrip drives the full flow: a click on a
// cross-origin auth link inside the content iframe is intercepted, run in a
// popup, and the IdP's callback lands back in the *same* iframe — proving the
// app keeps its sessionStorage-backed auth state (MSAL's nonce et al).
func TestE2E_AuthBreakout_PopupRoundTrip(t *testing.T) {
	skipIfNoBrowser(t)

	idpURL, callback := newFakeIDP(t)
	backend := newAuthBackend(t, func() string { return idpURL + "/authorize" })
	ps := startBreakoutProxy(t, backend, mustHost(t, idpURL), "popup")
	*callback = "http://" + ps.ListenAddr + "/callback"

	ctx := newBrowser(t, 90*time.Second)

	// Top-level nav: the proxy answers with the chrome shell + content iframe.
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate("http://"+ps.ListenAddr+"/"),
		cdp.WaitVisible(`#__devtool_content_frame`, cdp.ByID),
		cdp.Sleep(400*time.Millisecond), // instrumentation eval
	))

	// The shell must expose the breakout API before anything can use it.
	var hasAPI bool
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`!!(window.__devtool_auth && window.__devtool_auth.breakout)`, &hasAPI)))
	require.True(t, hasAPI, "chrome shell exposes window.__devtool_auth")

	// Sentinel on the shell's global object. A popup flow must leave the shell
	// realm untouched; a fallback top-level navigation would destroy it. The
	// shell's *URL* legitimately changes (frames.js syncs it via replaceState),
	// so the URL cannot distinguish the two — only the realm can.
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`window.__shell_sentinel=1`, nil)))

	// Click the cross-origin login link *inside* the iframe. The content-frame
	// interceptor should cancel the in-frame navigation and hand it to the shell.
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(
		`document.getElementById('__devtool_content_frame').contentWindow.document.getElementById('login').click()`, nil)))

	href := waitContentHref(ctx, t, "/callback")
	require.Contains(t, href, "code=abc123", "auth code survives the round trip into the iframe")

	// The iframe really rendered the app's callback page, not an error or the
	// IdP: the breakout replayed the URL through the proxy in content role.
	var signedIn string
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(
		`document.getElementById('__devtool_content_frame').contentWindow.document.getElementById('signed-in').textContent`, &signedIn)))
	require.Equal(t, "code=abc123", signedIn)

	// The shell realm survived — the flow ran in the popup, not by navigating
	// the top window — and no popup marker is left to hijack its next load.
	var alive, leftover bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Evaluate(`window.__shell_sentinel===1`, &alive),
		cdp.Evaluate(`sessionStorage.getItem('__devtool_auth_popup')!==null`, &leftover),
	))
	require.True(t, alive, "shell window was never navigated (popup mode, not top fallback)")
	require.False(t, leftover, "popup marker cleared from the opener")

	// The shell's displayed URL tracks the content frame, minus the internal
	// frame marker — the address bar shows the app's real callback URL.
	var shellHref string
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`window.location.pathname+window.location.search`, &shellHref)))
	require.Equal(t, "/callback?code=abc123", shellHref)
}

// TestE2E_AuthBreakout_PopupMarkerSurvivesNameLoss pins the sessionStorage
// half of the popup marker on its own. Chrome happens to preserve window.name
// across the proxy→IdP→proxy round trip, so the round-trip test above cannot
// tell the two signals apart — it passes on window.name alone. Engines that
// clear window.name on cross-origin navigation (Safari/ITP) would silently
// fall back to rendering the app inside the popup.
//
// The popup is opened under a name the relay will never match, so only the
// cloned sessionStorage marker can identify it. Deleting either half of
// markAuthPopup/hasAuthPopupMark fails here.
func TestE2E_AuthBreakout_PopupMarkerSurvivesNameLoss(t *testing.T) {
	skipIfNoBrowser(t)

	idpURL, callback := newFakeIDP(t)
	backend := newAuthBackend(t, func() string { return idpURL + "/authorize" })
	ps := startBreakoutProxy(t, backend, mustHost(t, idpURL), "popup")
	*callback = "http://" + ps.ListenAddr + "/callback"

	ctx := newBrowser(t, 90*time.Second)
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate("http://"+ps.ListenAddr+"/"),
		cdp.WaitVisible(`#__devtool_content_frame`, cdp.ByID),
		cdp.Sleep(400*time.Millisecond),
	))

	// Reproduce breakout()'s popup step by hand, but with a foreign window name.
	// The marker is written to the opener's sessionStorage before window.open so
	// the popup inherits the clone, exactly as breakout() does it.
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`(function(){
		sessionStorage.setItem('__devtool_auth_popup','1');
		window.open(`+"`"+idpURL+`/authorize`+"`"+`,'not-the-devtool-name','popup,width=600,height=760');
		sessionStorage.removeItem('__devtool_auth_popup');
	})()`, nil)))

	href := waitContentHref(ctx, t, "/callback")
	require.Contains(t, href, "code=abc123",
		"sessionStorage marker alone identified the popup and relayed the callback")
}

// TestE2E_AuthBreakout_ServerRedirectStub covers the other entry point: the
// backend answers a content-frame request with a 3xx straight to the IdP.
// modifyResponse swaps it for the stub, whose script calls back into the shell.
func TestE2E_AuthBreakout_ServerRedirectStub(t *testing.T) {
	skipIfNoBrowser(t)

	idpURL, callback := newFakeIDP(t)
	backend := newAuthBackend(t, func() string { return idpURL + "/authorize" })
	ps := startBreakoutProxy(t, backend, mustHost(t, idpURL), "popup")
	*callback = "http://" + ps.ListenAddr + "/callback"

	ctx := newBrowser(t, 90*time.Second)
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate("http://"+ps.ListenAddr+"/"),
		cdp.WaitVisible(`#__devtool_content_frame`, cdp.ByID),
		cdp.Sleep(400*time.Millisecond),
	))

	// Navigate the iframe to a path the backend 302s to the IdP. The redirect
	// never reaches the browser — the proxy replaces it with the stub.
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(
		`document.getElementById('__devtool_content_frame').contentWindow.location.href='/redirect-login'`, nil)))

	href := waitContentHref(ctx, t, "/callback")
	require.Contains(t, href, "code=abc123", "server-redirect breakout lands the callback in the iframe")
}

// mustHost extracts the host:port of a test server URL for use as a pattern.
func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	host := strings.TrimPrefix(rawURL, "http://")
	require.NotEqual(t, rawURL, host, "expected http:// test server URL")
	return host
}
