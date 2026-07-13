//go:build chromee2e

package proxy

// Tier 4 — full e2e happy path. Drives a REAL headless Chrome through the
// entire currentpage pipeline (injected JS → /__devtool_metrics WebSocket →
// PageTracker) against a live proxy, then asserts the exact session shape that
// manual validation established as expected. Runs in the chromee2e tier and
// skips when SKIP_BROWSER_TESTS is set or no Chrome binary is present.
//
//	SKIP_BROWSER_TESTS=1 go test -tags=chromee2e ./internal/proxy   # skip
//	go test -tags=chromee2e -run TestE2E_CurrentPage ./internal/proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	cdp "github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e2eInteractiveBackend serves the interactive validation page plus its css/js.
// The catch-all "/" handler returns the HTML page for EVERY otherwise-unmatched
// path — i.e. it mimics an SPA dev server's index.html fallback, so Chrome's
// automatic /favicon.ico fetch comes back as text/html. This is exactly the
// case the tightened isDocumentRequest must classify as a resource (not a
// navigation); the test asserts no spurious favicon session appears.
func e2eInteractiveBackend(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, "body{font-family:sans-serif}")
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, e2eAppJS)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// SPA fallback: serve the shell HTML for any unmatched path, favicon included.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, e2eInteractivePage)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const e2eInteractivePage = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>CurrentPage Validation</title>
<link rel="stylesheet" href="/style.css"></head>
<body>
<h1 id="title">CurrentPage Test</h1>
<button id="btn-mutate">Add Item</button>
<button id="btn-error">Throw Error</button>
<button id="btn-vuewarn">Vue Warn</button>
<button id="btn-keywarn">React Key Warn</button>
<form id="myform" onsubmit="event.preventDefault()">
  <input id="name" name="name" type="text" placeholder="name">
  <button id="btn-submit" type="submit">Submit</button>
</form>
<ul id="list"></ul>
<div style="height:3000px">spacer</div>
<script src="/app.js"></script>
</body></html>`

const e2eAppJS = `
var n = 0;
document.getElementById('btn-mutate').addEventListener('click', function () {
  n++;
  var li = document.createElement('li');
  li.textContent = 'item ' + n;
  document.getElementById('list').appendChild(li);
});
document.getElementById('btn-error').addEventListener('click', function () {
  throw new ReferenceError('intentional currentpage test error ' + n);
});
document.getElementById('btn-vuewarn').addEventListener('click', function () {
  // Vue surfaces reactivity loss via console.warn — the signal the warn-forward
  // allowlist must carry to the server.
  console.warn('[Vue warn]: Set operation on key "count" failed: target is readonly.');
});
document.getElementById('btn-keywarn').addEventListener('click', function () {
  // React surfaces the key/identity warning via console.error.
  console.error('Warning: Each child in a list should have a unique "key" prop. Check the render method of List.');
});
`

func skipIfNoBrowser(t *testing.T) {
	t.Helper()
	if v := strings.TrimSpace(os.Getenv("SKIP_BROWSER_TESTS")); v != "" {
		t.Skip("SKIP_BROWSER_TESTS is set")
	}
	for _, b := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if _, err := exec.LookPath(b); err == nil {
			return
		}
	}
	t.Skip("no Chrome/Chromium binary found")
}

func TestE2E_CurrentPage_RealBrowserHappyPath(t *testing.T) {
	skipIfNoBrowser(t)

	backend := e2eInteractiveBackend(t)
	ps := startWrapProxy(t, backend)
	// Navigate straight to the marked content URL: served unwrapped with the
	// content-role runtime + interaction listeners at top level (rewrite.go).
	contentURL := "http://" + ps.ListenAddr + "/?" + frameMarkerParam + "=f1"

	allocCtx, allocCancel := cdp.NewExecAllocator(context.Background(),
		append(cdp.DefaultExecAllocatorOptions[:],
			cdp.Flag("headless", true),
			cdp.Flag("no-sandbox", true),
			cdp.Flag("disable-gpu", true),
		)...)
	t.Cleanup(allocCancel)
	ctx, cancel := cdp.NewContext(allocCtx)
	t.Cleanup(cancel)
	// This is a liveness bound on the whole real-Chrome DOM interaction
	// sequence, not a latency budget — it exists only to fail a genuinely
	// hung browser session fast. Observed a real "context deadline
	// exceeded" at 61.45s against a 60s budget when `go test ./...` runs
	// every package's browser e2e test concurrently, each spinning up its
	// own Chrome and competing for the host's CPU. Widened to 180s (3x)
	// rather than tightened, since nothing here is actually stuck.
	ctx, tcancel := context.WithTimeout(ctx, 180*time.Second)
	t.Cleanup(tcancel)

	// Happy path: load, mutate the DOM 3×, type, submit, scroll, throw an error.
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate(contentURL),
		cdp.WaitVisible(`#btn-mutate`, cdp.ByID),
		cdp.Click(`#btn-mutate`, cdp.ByID),
		cdp.Click(`#btn-mutate`, cdp.ByID),
		cdp.Click(`#btn-mutate`, cdp.ByID),
		cdp.SendKeys(`#name`, "hello world", cdp.ByID),
		cdp.Click(`#btn-submit`, cdp.ByID),
		cdp.Evaluate(`window.scrollTo(0, 1200)`, nil),
		cdp.Click(`#btn-error`, cdp.ByID),
		cdp.Sleep(2500*time.Millisecond), // let the 1s batch timers flush twice
	))

	// Pull the primary page session.
	var s *PageSession
	require.Eventually(t, func() bool {
		for _, c := range ps.PageTracker().GetActiveSessions() {
			if c.URL == "/" && c.InteractionCount > 0 && c.MutationCount > 0 {
				s = c
				return true
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "real browser telemetry must populate the session")

	// URL is marker-free; title promoted from the performance sample.
	assert.Equal(t, "/", s.URL, "session URL is the clean, marker-stripped page URL")
	assert.Equal(t, "CurrentPage Validation", s.PageTitle, "document.title promoted via performance sample")

	// Exactly three DOM appends → three mutations.
	assert.Equal(t, 3, s.MutationCount, "three mutate clicks produce exactly three mutations")

	// Exactly one uncaught error, and it is the ReferenceError we threw.
	require.Len(t, s.Errors, 1, "one uncaught error captured")
	assert.Contains(t, s.Errors[0].Message+s.Errors[0].Error, "ReferenceError", "the thrown error type is preserved")

	// Interactions captured with their event types and selectors.
	types := map[string]int{}
	for _, in := range s.Interactions {
		types[in.EventType]++
	}
	assert.Positive(t, types["click"], "clicks captured")
	assert.Positive(t, types["keydown"], "keystrokes captured")
	assert.GreaterOrEqual(t, s.InteractionCount, 5, "a realistic interaction count")

	// Resources attached to the session via Referer matching.
	res := map[string]bool{}
	for _, r := range s.Resources {
		res[r.URL] = true
	}
	assert.True(t, res["/style.css"], "stylesheet attached as a resource")
	assert.True(t, res["/app.js"], "script attached as a resource")

	// Performance sample recorded.
	require.NotNil(t, s.Performance, "performance metrics recorded")

	// The tightened classifier: Chrome's favicon fetch returned the SPA HTML
	// fallback, but a .ico path must not spawn its own page session.
	for _, c := range ps.PageTracker().GetActiveSessions() {
		assert.NotContains(t, c.URL, "favicon", "HTML-served favicon must not create a page session")
	}

	if testing.Verbose() {
		t.Logf("e2e session: url=%q title=%q interactions=%d (types=%v) mutations=%d errors=%d resources=%d",
			s.URL, s.PageTitle, s.InteractionCount, types, s.MutationCount, len(s.Errors), len(s.Resources))
	}
}

// TestE2E_CurrentPage_FrameworkWarningsForwarded proves the Part A capture gap is
// closed through a REAL browser: a Vue-style console.warn (reactivity loss) and a
// React-style console.error (key identity) both reach the server-side session, so
// the currentpage diagnostic classifier has the runtime signals it needs.
// Classification correctness itself is unit-tested in internal/tools — proxy
// cannot import tools (cycle), so this asserts the raw delivery only.
func TestE2E_CurrentPage_FrameworkWarningsForwarded(t *testing.T) {
	skipIfNoBrowser(t)

	backend := e2eInteractiveBackend(t)
	ps := startWrapProxy(t, backend)
	contentURL := "http://" + ps.ListenAddr + "/?" + frameMarkerParam + "=f1"

	allocCtx, allocCancel := cdp.NewExecAllocator(context.Background(),
		append(cdp.DefaultExecAllocatorOptions[:],
			cdp.Flag("headless", true),
			cdp.Flag("no-sandbox", true),
			cdp.Flag("disable-gpu", true),
		)...)
	t.Cleanup(allocCancel)
	ctx, cancel := cdp.NewContext(allocCtx)
	t.Cleanup(cancel)
	// This is a liveness bound on the whole real-Chrome DOM interaction
	// sequence, not a latency budget — it exists only to fail a genuinely
	// hung browser session fast. Observed a real "context deadline
	// exceeded" at 61.45s against a 60s budget when `go test ./...` runs
	// every package's browser e2e test concurrently, each spinning up its
	// own Chrome and competing for the host's CPU; widened to 180s (3x).
	// Still failed once at 186s under `stress -c 6` (task 01KX8R68VM9AY8CXSE2AKQKQHX).
	// CDP target-attached tracing of the sibling auth-breakout popup test
	// (same task) root-caused this class: under sufficiently extreme induced
	// CPU + memory pressure, a Chrome renderer's script execution/DOM
	// readiness can stall for 90+ seconds regardless of which step is
	// "next" — genuine host-level scheduling/paging starvation, not a
	// lifecycle bug in this test. No finite budget is provably sufficient
	// under unbounded host starvation; widened further (180s->300s) to
	// reduce the practical flake rate without pretending it's now
	// deterministic — nothing here is actually stuck.
	ctx, tcancel := context.WithTimeout(ctx, 300*time.Second)
	t.Cleanup(tcancel)

	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate(contentURL),
		cdp.WaitVisible(`#btn-vuewarn`, cdp.ByID),
		cdp.Click(`#btn-vuewarn`, cdp.ByID),
		cdp.Click(`#btn-keywarn`, cdp.ByID),
		cdp.Sleep(2500*time.Millisecond), // let the 1s batch timers flush twice
	))

	// Collect the page session's error messages.
	var msgs string
	require.Eventually(t, func() bool {
		for _, c := range ps.PageTracker().GetActiveSessions() {
			if c.URL != "/" {
				continue
			}
			msgs = ""
			for _, e := range c.Errors {
				msgs += e.Message + " " + e.Error + "\n"
			}
			return strings.Contains(msgs, "[Vue warn]") && strings.Contains(msgs, "unique")
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "both the forwarded console.warn and the console.error must reach the session")

	// The Vue reactivity warning arrived via the signature-gated warn-forward.
	assert.Contains(t, msgs, "[Vue warn]", "console.warn reactivity signal forwarded to server")
	assert.Contains(t, msgs, "target is readonly", "the actionable warning text survived the round trip")
	// The React key warning arrived via the existing console.error path.
	assert.Contains(t, msgs, `unique "key" prop`, "console.error key warning captured")
}
