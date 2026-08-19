//go:build chromee2e

package proxy

// publish_browser_e2e_test.go is the P10 gate's Tier B: real-Chrome coverage of
// the served public artifact. Tier A proves the bytes on the wire; this proves
// the served RolePublic bundle actually LOADS and RUNS in a real browser on the
// real page delivered by the real listener — and, crucially, that the dev
// control surface (window.__devtool) is ABSENT in the public page context.
//
// It is env-gated via skipIfNoBrowser (matching variant_engine_e2e_test.go /
// walkthrough_player_e2e_test.go): it runs under the normal `go test` when a
// Chrome/Chromium binary is present and SKIPS LOUDLY (t.Skip with a clear
// message) when it is not — never a silent pass. The `make e2e-publish-browser`
// target runs exactly this test with the browser present.
//
// Unlike the engine/player e2e fixtures (which serve the bundle from a bespoke
// httptest page), these tests navigate to the REAL /s/{token} artifact the public
// plane emits, so the bundle loads under the REAL wholesale CSP (script-src is
// HASH-ONLY — the bundle hash plus one sha256 per authored script body in the
// served revision, deliberately no 'self'; connect-src 'self'), and the page
// fetches its OWN artifact JSON same-origin the way a deployed viewer would.
//
// Two tests live here:
//
//	TestE2E_PublicPlane_RealBrowser — the bundle loads, the dev surface is absent,
//	  variant/player/feedback all work on the real page.
//	TestE2E_PublicPlane_RealBrowser_AuthoredScriptExecutionCSP — the authored-hash
//	  EXECUTION path: a pinned publisher script runs, an unpinned one does not.

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	cdp "github.com/chromedp/chromedp"
	"github.com/standardbeagle/agnt/internal/publish"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPublicBrowser(t *testing.T) context.Context {
	t.Helper()
	allocCtx, allocCancel := cdp.NewExecAllocator(context.Background(),
		append(cdp.DefaultExecAllocatorOptions[:],
			cdp.Flag("headless", true),
			cdp.Flag("no-sandbox", true),
			cdp.Flag("disable-gpu", true),
		)...)
	t.Cleanup(allocCancel)
	ctx, cancel := cdp.NewContext(allocCtx)
	t.Cleanup(cancel)
	ctx, tcancel := context.WithTimeout(ctx, 180*time.Second)
	t.Cleanup(tcancel)
	return ctx
}

// TestE2E_PublicPlane_RealBrowser loads the REAL served artifact in headless
// Chrome and proves: the RolePublic bundle loads + exposes its primitives, the
// dev control surface is absent, the variant applies (and re-applies after an SPA
// remount), the walkthrough player runs (advances a step), and feedback submits
// end-to-end through the real feedback client → real listener → real store.
func TestE2E_PublicPlane_RealBrowser(t *testing.T) {
	skipIfNoBrowser(t)

	p := newE2EPlane(t)
	id, token, err := p.store.Create(e2eWalkthrough(), e2eProjectA)
	require.NoError(t, err)

	ctx := newPublicBrowser(t)
	artifactURL := p.srv.URL + "/s/" + token

	// Navigate to the REAL artifact and wait for the RolePublic bundle to load
	// and register all three allowlisted primitives.
	var ready bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate(artifactURL),
		cdp.Poll(
			`!!(window.__variantEngine && window.__walkthroughViewer && window.__feedbackClient)`,
			&ready, cdp.WithPollingTimeout(10*time.Second),
		),
	))
	require.True(t, ready, "the served RolePublic bundle must load and register its primitives")

	// This subtest MUST run first: it asserts what the visitor gets with NO
	// scripted help, and the later subtests deliberately overwrite document.body
	// and build their own players. It tears its player down at the end so those
	// subtests still start from a clean page.
	t.Run("artifact_auto_boots_the_player_for_a_bare_visitor", func(t *testing.T) {
		// Nothing below calls create(): navigating to /s/{token} is the entire
		// visitor journey. public-boot reads the token from window.location,
		// fetches the artifact same-origin under the real CSP, and starts the
		// viewer. Before public-boot existed the served page stayed blank.
		var card, title bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Poll(`!!document.getElementById('__wt_player_card')`, &card,
				cdp.WithPollingTimeout(10*time.Second)),
			cdp.Evaluate(`document.getElementById('__wt_player_card').textContent.indexOf('Intro') !== -1`, &title),
		))
		assert.True(t, card, "a bare visit to /s/{token} must render the step card with no scripted create()")
		assert.True(t, title, "the auto-booted player must render step 0 of the published walkthrough")

		var idx int
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.__agntPublicBoot.player().activeIndex()`, &idx)))
		assert.Equal(t, 0, idx, "auto-boot must leave the player on step 0")

		// Hand the page back clean for the subtests that follow.
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.__agntPublicBoot.player().destroy(); true`, nil)))
	})

	t.Run("dev_control_surface_absent_in_public_page", func(t *testing.T) {
		var devtoolAbsent, execAbsent bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`typeof window.__devtool === 'undefined'`, &devtoolAbsent),
			// No dev exec/WS bridge object leaked into the public context either.
			cdp.Evaluate(`typeof window.__devtoolExec === 'undefined' && !document.querySelector('[data-devtool]')`, &execAbsent),
		))
		assert.True(t, devtoolAbsent, "window.__devtool must be ABSENT in the public page")
		assert.True(t, execAbsent, "no dev exec/WS control object may leak into the public page")
	})

	awaitPromise := func(pp *runtime.EvaluateParams) *runtime.EvaluateParams { return pp.WithAwaitPromise(true) }

	t.Run("variant_applies_and_reapplies_after_remount", func(t *testing.T) {
		// The page fetches its OWN variant set same-origin (proves connect-src
		// 'self' allows it), injects an app subtree, and applies the variant.
		var applied string
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`(async () => {
			document.body.innerHTML = '<div id="app"><h1 id="title">Original</h1><p class="msg">hi</p></div>';
			const vs = await (await fetch('/s/`+token+`/variants.json')).json();
			window.__eng = window.__variantEngine.create(vs, { when: 'any' });
			window.__eng.apply('a');
			return document.getElementById('title').textContent;
		})()`, &applied, awaitPromise)))
		assert.Equal(t, "Changed", applied, "the served variant must apply to the real page")

		// SPA remount: replace the subtree with fresh original nodes; the engine's
		// observer must re-apply to the remounted nodes without a write storm.
		var afterRemount string
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`(function(){var app=document.getElementById('app');var h=document.createElement('h1');h.id='title';h.textContent='Original';var pp=document.createElement('p');pp.className='msg';pp.textContent='hi';app.replaceChildren(h,pp);return true;})()`, nil),
			cdp.Sleep(300*time.Millisecond),
			cdp.Evaluate(`document.getElementById('title').textContent`, &afterRemount),
		))
		assert.Equal(t, "Changed", afterRemount, "variant must re-apply to remounted nodes")
	})

	t.Run("walkthrough_player_runs_and_advances", func(t *testing.T) {
		// Fetch the real walkthrough, build the player over the real bundle, start
		// it, and advance a step.
		var i0 int
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`(async () => {
			const wt = await (await fetch('/s/`+token+`/walkthrough.json')).json();
			if (window.__pl) { try { window.__pl.destroy(); } catch (e) {} }
			window.__pl = window.__walkthroughViewer.create(wt, {});
			window.__pl.start();
			return window.__pl.activeIndex();
		})()`, &i0, awaitPromise)))
		require.Equal(t, 0, i0, "player must start at step 0")

		var cardShown bool
		var i1 int
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`!!document.getElementById('__wt_player_card')`, &cardShown),
			cdp.Evaluate(`window.__pl.next(); window.__pl.activeIndex()`, &i1),
		))
		assert.True(t, cardShown, "the step card must render at step 0")
		assert.Equal(t, 1, i1, "next() must advance the player to step 1")
	})

	t.Run("feedback_submits_through_real_client_and_listener", func(t *testing.T) {
		// Use the REAL feedback client to POST same-origin to the REAL /feedback
		// endpoint on the REAL listener.
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`(function(){
			var fc = window.__feedbackClient.init({ endpoint: '/s/`+token+`/feedback' });
			fc.submit({ message: 'from real chrome', rating: 5 });
			return true;
		})()`, nil)))

		// The durable store must receive exactly the browser-submitted row.
		require.Eventually(t, func() bool {
			return p.feedback.Count(id) == 1
		}, 5*time.Second, 50*time.Millisecond, "feedback POSTed from Chrome must reach the durable store")

		rows, ok := ownerScopedFeedbackRead(p.store, p.feedback, e2eProjectA, id)
		require.True(t, ok)
		require.Len(t, rows, 1)
		assert.Contains(t, rows[0].Body, "from real chrome")
	})
}

// authoredScriptE2ECode is the publisher-authored script body carried by the
// revision under test. It is deliberately NON-ASCII: the CSP hash is computed in
// Go over []byte(op.Code) while the browser hashes the UTF-8 encoding of the
// script element's textContent (variant-engine.js assigns op.code to
// textContent). Any byte/encoding divergence between those two paths — the exact
// risk a header-string test cannot see — makes the browser refuse this script and
// fails the positive assertion below.
const authoredScriptE2ECode = "window.__authoredRan = 'ran ✓';\n" +
	"document.getElementById('app').setAttribute('data-authored', 'ran ✓');\n"

// authoredScriptE2ENearMiss differs from the authored body by ONE character
// (✗ instead of ✓). It proves the pin is a byte-exact hash rather than
// any prefix/shape match.
const authoredScriptE2ENearMiss = "window.__authoredRan = 'ran ✗';\n" +
	"document.getElementById('app').setAttribute('data-authored', 'ran ✗');\n"

// TestE2E_PublicPlane_RealBrowser_AuthoredScriptExecutionCSP is the EXECUTION
// half of the authored-script story that S3 (inline <script> emission) and S4
// (per-revision sha256 widening of script-src) only proved at the header-string
// level. A green header assertion is compatible with a policy that blocks
// everything, so this drives a real Chrome against the real listener and asserts
// the two halves that actually matter:
//
//	POSITIVE — the publisher-authored body pinned by this revision RUNS (observable
//	           side effects: a global and a DOM attribute).
//	NEGATIVE — a byte-different foreign inline script (arbitrary, and a one-character
//	           near-miss of the authored body) is REFUSED by CSP and never runs.
//
// The name embeds TestE2E_PublicPlane_RealBrowser on purpose so `make
// e2e-publish-browser` (-run 'TestE2E_PublicPlane_RealBrowser') covers it too.
func TestE2E_PublicPlane_RealBrowser_AuthoredScriptExecutionCSP(t *testing.T) {
	skipIfNoBrowser(t)

	p := newE2EPlane(t)
	wt := e2eWalkthrough()
	wt.ID = "wt-authored-script"
	wt.VariantSet.ID = "set-authored-script"
	wt.VariantSet.Variants[0].Ops = []publish.Op{
		{Op: publish.OpSetText, Selector: "#title", Value: "Changed"},
		{Op: publish.OpAddScript, Code: authoredScriptE2ECode},
	}
	_, token, err := p.store.Create(wt, e2eProjectA)
	require.NoError(t, err, "a revision carrying an authored addScript{code} op must publish")

	// Test premise (named, per the mutation-verify discipline): the served CSP
	// must actually carry an authored hash source DISTINCT from the bundle hash.
	// Without it the execution assertion below would be testing nothing.
	authoredHash := cspSHA256([]byte(authoredScriptE2ECode))
	bundleHash := PublicInstrumentationAssetCSPHash()
	require.NotEqual(t, bundleHash, authoredHash, "premise broken: authored and bundle hashes collide")
	code, _, hdr := p.get(t, "/s/"+token)
	require.Equal(t, http.StatusOK, code)
	csp := hdr.Get("Content-Security-Policy")
	require.Contains(t, csp, authoredHash, "premise broken: served CSP does not pin the authored script hash")
	require.NotContains(t, csp, "unsafe-inline", "authored-script widening must stay hash-only")
	require.NotContains(t, csp, "script-src 'self'", "authored-script widening must not dilute the pin with 'self'")
	nearMissHash := cspSHA256([]byte(authoredScriptE2ENearMiss))
	require.NotContains(t, csp, nearMissHash, "premise broken: the near-miss body must NOT be pinned")

	ctx := newPublicBrowser(t)
	var ready bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate(p.srv.URL+"/s/"+token),
		cdp.Poll(`!!(window.__variantEngine && window.__walkthroughViewer)`, &ready,
			cdp.WithPollingTimeout(10*time.Second)),
	))
	require.True(t, ready, "the served RolePublic bundle must load")

	awaitPromise := func(pp *runtime.EvaluateParams) *runtime.EvaluateParams { return pp.WithAwaitPromise(true) }

	t.Run("authored_script_executes_under_pinned_hash", func(t *testing.T) {
		// Build the app subtree, fetch the OWN variant set same-origin, and apply
		// the variant. The engine emits the authored body as an inline <script>;
		// whether it runs is decided ENTIRELY by the served CSP.
		var applied string
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`(async () => {
			document.body.innerHTML = '<div id="app"><h1 id="title">Original</h1><p class="msg">hi</p></div>';
			const vs = await (await fetch('/s/`+token+`/variants.json')).json();
			window.__eng = window.__variantEngine.create(vs, { when: 'any' });
			window.__eng.apply('a');
			return document.getElementById('title').textContent;
		})()`, &applied, awaitPromise)))
		require.Equal(t, "Changed", applied, "the non-script ops must apply (the variant really ran)")

		var ranFlag, ranAttr string
		require.NoError(t, cdp.Run(ctx,
			cdp.Sleep(200*time.Millisecond),
			cdp.Evaluate(`String(window.__authoredRan)`, &ranFlag),
			cdp.Evaluate(`String(document.getElementById('app').getAttribute('data-authored'))`, &ranAttr),
		))
		assert.Equal(t, "ran ✓", ranFlag,
			"the pinned authored script must EXECUTE in the real browser (global side effect)")
		assert.Equal(t, "ran ✓", ranAttr,
			"the pinned authored script must EXECUTE in the real browser (DOM side effect)")
	})

	t.Run("foreign_and_near_miss_inline_scripts_refused_by_csp", func(t *testing.T) {
		// Inline scripts inserted into the DOM are subject to script-src; only a
		// body whose sha256 is pinned may run. Neither of these is pinned.
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`(function(){
			function inject(code){ var s=document.createElement('script'); s.textContent=code; document.body.appendChild(s); }
			inject("window.__foreignRan = true; document.getElementById('app').setAttribute('data-foreign','yes');");
			inject(`+jsonString(authoredScriptE2ENearMiss)+`);
			return true;
		})()`, nil)))

		var foreign, nearMiss, attr string
		require.NoError(t, cdp.Run(ctx,
			cdp.Sleep(200*time.Millisecond),
			cdp.Evaluate(`String(typeof window.__foreignRan)`, &foreign),
			// The near-miss body would OVERWRITE __authoredRan with the ✗ marker
			// if it ran, so the authored value surviving is the refusal evidence.
			cdp.Evaluate(`String(window.__authoredRan)`, &nearMiss),
			cdp.Evaluate(`String(document.getElementById('app').getAttribute('data-foreign'))`, &attr),
		))
		assert.Equal(t, "undefined", foreign, "an unpinned foreign inline script must be REFUSED by CSP")
		assert.Equal(t, "null", attr, "a refused script must leave no DOM side effect")
		assert.NotEqual(t, "ran ✗", nearMiss,
			"a ONE-CHARACTER-different near-miss of the authored body must be refused (the pin is byte-exact)")
	})
}

// proxiedE2EPlane is an e2ePlane whose PublicHandler is wired to a fake TLS
// upstream through the exported UpstreamSeam, so the /s/{token} DOCUMENT route
// fetches that origin through the real INV-13 guard and serves it with the
// RolePublic bundle injected — the proxied path that self-contained fixtures
// never exercise.
type proxiedE2EPlane struct {
	*e2ePlane
	upstreamMarker string    // an id present only in the upstream document
	dialed         *[]string // addresses the guarded dialer was handed (provenance)
}

// newProxiedE2EPlane mirrors newE2EPlane but chains WithUpstreamSeam onto the
// real PublicHandler. The seam OBSERVES the guard (publish-security-review-lessons
// §7/§8): the resolver answers with a genuinely public address (publicAddr) and
// the dialer asserts it was handed exactly that address before redirecting to the
// loopback TLS listener. The https-only deny-list, the resolve-pinned dial, and
// the per-hop re-check all run their real logic; nothing is stubbed. The upstream
// document already carries the #title/.msg subtree the variant ops target, so a
// successful variant apply mutates nodes DELIVERED BY the upstream — proving the
// proxied origin was served AND the variant was applied in one assertion.
func newProxiedE2EPlane(t *testing.T) *proxiedE2EPlane {
	t.Helper()

	const upstreamMarker = "live-hero"
	upstreamDoc := `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Live App</title></head>` +
		`<body><div id="app"><h1 id="title">Original</h1><p class="msg">hi</p></div>` +
		`<div id="` + upstreamMarker + `">proxied upstream page</div></body></html>`

	upSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A hostile upstream also seeds headers the public plane must NOT propagate;
		// the wholesale header policy is covered at the serve level, this fixture just
		// proves the proxied DOCUMENT reaches a real browser.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "script-src 'unsafe-inline' *")
		io.WriteString(w, upstreamDoc)
	}))
	t.Cleanup(upSrv.Close)

	dialed := new([]string)
	seam := UpstreamSeam{
		Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			if host != "example.com" {
				return nil, errors.New("unexpected resolve of " + host)
			}
			return []netip.Addr{publicAddr}, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			*dialed = append(*dialed, addr)
			// The pin under test: the dialer must be handed the guard-validated
			// PUBLIC address, never a re-resolution of the hostname.
			if addr != publicAddr.String()+":443" {
				return nil, errors.New("dial address is not the guard-validated one: " + addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, upSrv.Listener.Addr().String())
		},
		// example.com is the name httptest's cert is issued for, so TLS verifies for
		// real rather than being skipped.
		TLSConfig: upSrv.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
	}

	storeDir := t.TempDir()
	fbDir := t.TempDir()
	store, err := publish.New(storeDir, nil)
	require.NoError(t, err, "open publish store")
	feedback, err := publish.NewFeedbackStore(fbDir, e2eLimits(), nil)
	require.NoError(t, err, "open feedback store")
	h := NewPublicHandler(store, feedback, int(e2eLimits().MaxBodyBytes)).
		WithRateLimits(
			publish.NewRateLimiter(1_000_000, 1_000_000, nil),
			publish.NewRateLimiter(1_000_000, 1_000_000, nil),
		).
		WithUpstreamSeam(seam)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return &proxiedE2EPlane{
		e2ePlane:       &e2ePlane{srv: srv, store: store, feedback: feedback, storeDir: storeDir, fbDir: fbDir},
		upstreamMarker: upstreamMarker,
		dialed:         dialed,
	}
}

// TestE2E_PublicPlane_RealBrowser_ProxiedUpstream is the real-Chrome coverage S6
// could not carry: its fileScope excluded this file, the only place this tier
// lives, so the criterion "an upstream-bearing share, served over the public
// plane, loads the RolePublic bundle and applies variant ops on the PROXIED
// origin's own DOM" was unsatisfiable inside that slice (a planning defect, not
// an execution one — publish-security-review-lessons §9). This closes it.
//
// The name embeds TestE2E_PublicPlane_RealBrowser on purpose so `make
// e2e-publish-browser` (-run 'TestE2E_PublicPlane_RealBrowser') covers it too.
//
// It asserts, on the REAL served proxied document in a REAL headless Chrome:
//
//	(a) the RolePublic bundle LOADS and registers its allowlisted primitives —
//	    proof the injected bundle ran under the real wholesale CSP;
//	(b) the served DOM is the UPSTREAM document — the upstream-only #live-hero
//	    element is present (a DOM signal, polled directly, not location.href);
//	(c) the guard OBSERVED the fetch — the dialer got exactly the validated
//	    public address, once (§8 provenance: a bypassed pin dials elsewhere);
//	(d) the variant ops APPLY to the upstream-delivered nodes — #title becomes
//	    "Changed" and .msg gains the "hl" class, both mutating elements that
//	    came from the proxied origin, not from a test-built subtree.
func TestE2E_PublicPlane_RealBrowser_ProxiedUpstream(t *testing.T) {
	skipIfNoBrowser(t)

	p := newProxiedE2EPlane(t)

	// An upstream-bearing share: the same rich walkthrough (steps + variant set),
	// now naming a live origin so the DOCUMENT route proxies it.
	wt := e2eWalkthrough()
	wt.Upstream = &publish.UpstreamConfig{URL: "https://example.com/app"}
	_, token, err := p.store.Create(wt, e2eProjectA)
	require.NoError(t, err, "an upstream-bearing share must publish")

	ctx := newPublicBrowser(t)
	artifactURL := p.srv.URL + "/s/" + token

	// (a) The served proxied document loads the RolePublic bundle and registers its
	// primitives. Poll the actual JS signal being asserted on, never location.href.
	var ready bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate(artifactURL),
		cdp.Poll(
			`!!(window.__variantEngine && window.__walkthroughViewer && window.__feedbackClient)`,
			&ready, cdp.WithPollingTimeout(10*time.Second),
		),
	))
	require.True(t, ready, "the RolePublic bundle injected into the proxied upstream document must load and register its primitives")

	// (b) The served DOM IS the upstream document: an element that exists ONLY in
	// the proxied origin's HTML is present. Poll the element + its non-empty
	// textContent (the asserted DOM state), not a navigation proxy signal.
	var upstreamServed bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Poll(
			`(function(){var e=document.getElementById('`+p.upstreamMarker+`');return !!(e && e.textContent.indexOf('proxied upstream page')!==-1);})()`,
			&upstreamServed, cdp.WithPollingTimeout(10*time.Second),
		),
	))
	require.True(t, upstreamServed, "the served document must be the PROXIED upstream page (its unique element must be in the DOM)")

	// (c) Provenance (§8): the guard's pin actually ran — the dialer received
	// exactly the guard-validated public address, once. A bypassed pin would dial
	// a re-resolved address and never reach the upstream (a and b would fail too).
	got := *p.dialed
	require.Len(t, got, 1, "the guarded upstream fetch must dial exactly once")
	assert.Equal(t, publicAddr.String()+":443", got[0], "the dialer must receive the guard-validated public address, not a re-resolution")

	// (d) Variant ops apply to the UPSTREAM-DELIVERED nodes. The #title/.msg subtree
	// came from the proxied origin (not a test-built body), so a successful apply
	// proves the proxied-origin-served and variant-applied properties together.
	awaitPromise := func(pp *runtime.EvaluateParams) *runtime.EvaluateParams { return pp.WithAwaitPromise(true) }
	var applied string
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`(async () => {
		const vs = await (await fetch('/s/`+token+`/variants.json')).json();
		window.__eng = window.__variantEngine.create(vs, { when: 'any' });
		window.__eng.apply('a');
		return document.getElementById('title').textContent;
	})()`, &applied, awaitPromise)))
	assert.Equal(t, "Changed", applied, "the variant setText op must apply to the upstream-delivered #title node")

	var msgHasClass bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Evaluate(`document.querySelector('.msg').classList.contains('hl')`, &msgHasClass)))
	assert.True(t, msgHasClass, "the variant addClass op must apply to the upstream-delivered .msg node")
}

// --- Subresource route: real-Chrome RENDERING coverage -----------------------
//
// The ~25 Go-level tests around the subresource route prove the guard, the MAC
// binding, the media-type allowlist, the caps, and the CSP composition on the
// wire. None of them can prove the property the route exists for: that a real
// browser FETCHES a rewritten stylesheet through /s/{token}/sub and APPLIES it
// to the upstream-delivered document. That is what this fixture and test carry.

const (
	subE2EMarker = "live-hero"
	// The color app.css paints onto the upstream-delivered #title. Written in the
	// exact serialization getComputedStyle returns so the assertion is an equality,
	// not a fuzzy match.
	subE2EStyledColor = "rgb(0, 128, 64)"
	// late.css is NOT referenced by the upstream document. It exists only so the
	// forged-signature case has a genuine counterpart: the same URL under a valid
	// MAC must style #live-hero, which is what makes the forged refusal non-vacuous.
	subE2ELateColor = "rgb(200, 0, 50)"
)

// subresourceUpstream records what the DAEMON asked the fake origin for. It is
// the origin-side half of the route's provenance: a stylesheet that reached the
// browser without a recorded upstream fetch would not have come through the route.
type subresourceUpstream struct {
	mu        sync.Mutex
	paths     []string
	sawCookie bool
	sawRefer  bool
}

func (u *subresourceUpstream) record(r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.paths = append(u.paths, r.URL.Path)
	u.sawCookie = u.sawCookie || r.Header.Get("Cookie") != ""
	u.sawRefer = u.sawRefer || r.Header.Get("Referer") != ""
}

func (u *subresourceUpstream) snapshot() ([]string, bool, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.paths...), u.sawCookie, u.sawRefer
}

// dialRecorder counts guarded dials under concurrency: a browser fetches the
// document and its subresources on separate connections, so the bare slice the
// document-only fixture uses would be a data race here.
type dialRecorder struct {
	mu    sync.Mutex
	addrs []string
}

func (d *dialRecorder) add(addr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addrs = append(d.addrs, addr)
}

func (d *dialRecorder) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addrs...)
}

// subresourceE2EPlane is newProxiedE2EPlane's sibling: the same observed-seam
// wiring (publish-security-review-lessons §7 — the resolver answers with a
// genuinely public address and the dialer asserts it was handed exactly that one,
// so the real guard runs and nothing is stubbed), over an upstream that serves a
// DOCUMENT REFERENCING AN EXTERNAL STYLESHEET plus a second, unreferenced sheet.
//
// It keeps the *PublicHandler because the forged-signature case needs the
// daemon's own signer to mint the genuine counterpart of the forged reference.
type subresourceE2EPlane struct {
	*e2ePlane
	h        *PublicHandler
	dialed   *dialRecorder
	upstream *subresourceUpstream
}

func newSubresourceE2EPlane(t *testing.T) *subresourceE2EPlane {
	t.Helper()

	// The document references its stylesheet RELATIVELY. That matters: the
	// rewriter resolves references against the fetched document's own URL, so a
	// relative href is the real path a live app takes into the route.
	upstreamDoc := `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Live App</title>` +
		`<link rel="stylesheet" href="app.css"></head>` +
		`<body><div id="app"><h1 id="title">Original</h1><p class="msg">hi</p></div>` +
		`<div id="` + subE2EMarker + `">proxied upstream page</div></body></html>`

	up := &subresourceUpstream{}
	upSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.record(r)
		switch r.URL.Path {
		case "/app":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "script-src 'unsafe-inline' *")
			io.WriteString(w, upstreamDoc)
		case "/app.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			io.WriteString(w, "#title { color: "+subE2EStyledColor+"; }\n")
		case "/late.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			io.WriteString(w, "#"+subE2EMarker+" { color: "+subE2ELateColor+"; }\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upSrv.Close)

	dialed := &dialRecorder{}
	seam := UpstreamSeam{
		Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			if host != "example.com" {
				return nil, errors.New("unexpected resolve of " + host)
			}
			return []netip.Addr{publicAddr}, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed.add(addr)
			if addr != publicAddr.String()+":443" {
				return nil, errors.New("dial address is not the guard-validated one: " + addr)
			}
			return (&net.Dialer{}).DialContext(ctx, network, upSrv.Listener.Addr().String())
		},
		TLSConfig: upSrv.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
	}

	storeDir := t.TempDir()
	fbDir := t.TempDir()
	store, err := publish.New(storeDir, nil)
	require.NoError(t, err, "open publish store")
	feedback, err := publish.NewFeedbackStore(fbDir, e2eLimits(), nil)
	require.NoError(t, err, "open feedback store")
	h := NewPublicHandler(store, feedback, int(e2eLimits().MaxBodyBytes)).
		WithRateLimits(
			publish.NewRateLimiter(1_000_000, 1_000_000, nil),
			publish.NewRateLimiter(1_000_000, 1_000_000, nil),
		).
		WithUpstreamSeam(seam)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return &subresourceE2EPlane{
		e2ePlane: &e2ePlane{srv: srv, store: store, feedback: feedback, storeDir: storeDir, fbDir: fbDir},
		h:        h,
		dialed:   dialed,
		upstream: up,
	}
}

// subresourceRefURL builds the same-origin reference the route accepts. Callers
// pass the signature so both the genuine and the forged case go through one
// builder and differ in exactly the byte under test.
func subresourceRefURL(token, absURL string, depth int, sig string) string {
	q := url.Values{}
	q.Set("u", absURL)
	q.Set("d", strconv.Itoa(depth))
	q.Set("sig", sig)
	return sharePrefix + token + subresourceSubPath + "?" + q.Encode()
}

// TestE2E_PublicPlane_RealBrowser_ProxiedSubresourceRendering closes the
// declared scope reduction of the subresource-route slice: the Go-level tests
// prove the bytes, the guard, the MAC, and the caps, but RENDERING — a real
// browser fetching a rewritten stylesheet through /s/{token}/sub and that
// stylesheet actually taking effect on the proxied document — is only observable
// in a real browser.
//
// The name embeds TestE2E_PublicPlane_RealBrowser on purpose so `make
// e2e-publish-browser` (-run 'TestE2E_PublicPlane_RealBrowser') covers it too.
//
// It asserts, on the REAL served proxied document in a REAL headless Chrome:
//
//	(a) the stylesheet is fetched THROUGH THE SUBRESOURCE ROUTE, not from the
//	    upstream origin: the served <link href> is the signed same-origin path,
//	    the loaded CSSOM sheet resolves to this listener's origin and its rules
//	    are readable (a cross-origin sheet's cssRules throw), the daemon-side
//	    fetch of /app.css is recorded at the fake origin, and the guarded dialer
//	    was handed the validated public address once per fetch (§8 provenance);
//	(b) the sheet APPLIES to an UPSTREAM-DELIVERED element — #title's computed
//	    color, polled as the real DOM signal being asserted on, never
//	    location.href (lessons-ssh-transport §2);
//	(c) a FORGED signature is refused with an observable in-browser consequence:
//	    the link fires `error`, the target element keeps its unstyled color, and
//	    NO new dial happens (the refusal precedes the socket). Its genuine
//	    counterpart — same URL, valid MAC — then styles that element, which is
//	    what makes the refusal assertion non-vacuous rather than a URL that never
//	    could have worked.
func TestE2E_PublicPlane_RealBrowser_ProxiedSubresourceRendering(t *testing.T) {
	skipIfNoBrowser(t)

	p := newSubresourceE2EPlane(t)

	wt := e2eWalkthrough()
	wt.Upstream = &publish.UpstreamConfig{URL: "https://example.com/app"}
	_, token, err := p.store.Create(wt, e2eProjectA)
	require.NoError(t, err, "an upstream-bearing share must publish")

	_, shareID, ok := p.store.VerifyToken(token)
	require.True(t, ok, "the freshly minted token must verify")
	require.NotNil(t, p.h.signer, "premise broken: the daemon has no subresource signer, so the route serves nothing")

	ctx := newPublicBrowser(t)

	var ready bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate(p.srv.URL+"/s/"+token),
		cdp.Poll(`!!(window.__variantEngine && window.__walkthroughViewer)`, &ready,
			cdp.WithPollingTimeout(10*time.Second)),
	))
	require.True(t, ready, "the RolePublic bundle injected into the proxied upstream document must load")

	// The served DOM is the upstream document (the element exists only there).
	var upstreamServed bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Poll(`(function(){var e=document.getElementById('`+subE2EMarker+`');return !!(e && e.textContent.indexOf('proxied upstream page')!==-1);})()`,
			&upstreamServed, cdp.WithPollingTimeout(10*time.Second)),
	))
	require.True(t, upstreamServed, "the served document must be the PROXIED upstream page")

	// (b) The stylesheet APPLIED. Poll the computed style of the upstream-delivered
	// #title — the actual DOM signal being asserted on.
	var styled bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Poll(`getComputedStyle(document.getElementById('title')).color === '`+subE2EStyledColor+`'`,
			&styled, cdp.WithPollingTimeout(15*time.Second)),
	))
	require.True(t, styled,
		"the upstream stylesheet, served through the subresource route, must APPLY to the upstream-delivered #title node")

	// (a) …and it got there through the route, not from the upstream origin.
	var linkHref, sheetState string
	require.NoError(t, cdp.Run(ctx,
		cdp.Evaluate(`(function(){var l=document.querySelector('link[rel="stylesheet"]');return l?l.getAttribute('href'):'';})()`, &linkHref),
		cdp.Evaluate(`(function(){
			var l = document.querySelector('link[rel="stylesheet"]');
			if (!l) return 'no-link';
			var want = new URL(l.getAttribute('href'), location.href).href;
			for (var i = 0; i < document.styleSheets.length; i++) {
				var s = document.styleSheets[i];
				if (s.href !== want) continue;
				try { return s.cssRules.length > 0 ? 'readable' : 'empty'; }
				catch (e) { return 'opaque'; }
			}
			return 'missing';
		})()`, &sheetState),
	))
	assert.True(t, strings.HasPrefix(linkHref, sharePrefix+token+subresourceSubPath+"?"),
		"the served <link href> must be the signed same-origin subresource path, got %q", linkHref)
	assert.Contains(t, linkHref, url.QueryEscape("https://example.com/app.css"),
		"the rewritten reference must carry the absolute upstream URL it was minted for")
	assert.Contains(t, linkHref, "sig=", "the rewritten reference must be MAC-bound")
	assert.Equal(t, "readable", sheetState,
		"the applied sheet must be the same-origin route response (a cross-origin sheet's cssRules are opaque)")

	paths, sawCookie, sawRefer := p.upstream.snapshot()
	assert.Contains(t, paths, "/app", "the daemon must have fetched the upstream document")
	assert.Contains(t, paths, "/app.css", "the daemon must have fetched the stylesheet itself — the browser never reaches the origin")
	assert.False(t, sawCookie, "no viewer Cookie may be forwarded upstream")
	assert.False(t, sawRefer, "no Referer may be forwarded upstream (it would carry the share token, INV-9)")

	dials := p.dialed.snapshot()
	require.Len(t, dials, 2, "exactly two guarded fetches must have happened: the document and the stylesheet")
	for i, addr := range dials {
		assert.Equal(t, publicAddr.String()+":443", addr,
			"dial %d must receive the guard-validated public address, not a re-resolution", i)
	}

	// (c) Forged signature, then its genuine counterpart.
	const lateURL = "https://example.com/late.css"
	genuineSig := p.h.signer.Sign(shareID, lateURL, 1)
	require.NotEmpty(t, genuineSig, "premise broken: the signer minted no signature")
	forgedSig := flipFirstRune(genuineSig)
	require.NotEqual(t, genuineSig, forgedSig, "premise broken: the forged signature equals the genuine one")

	var baseColor string
	require.NoError(t, cdp.Run(ctx,
		cdp.Evaluate(`getComputedStyle(document.getElementById('`+subE2EMarker+`')).color`, &baseColor)))
	require.NotEqual(t, subE2ELateColor, baseColor,
		"premise broken: the target element already carries the late-stylesheet color before it is loaded")

	t.Run("forged_signature_is_refused_and_the_element_stays_unstyled", func(t *testing.T) {
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`(function(){
			window.__forgedResult = 'pending';
			var l = document.createElement('link');
			l.rel = 'stylesheet';
			l.id = '__forged';
			l.onload = function(){ window.__forgedResult = 'load'; };
			l.onerror = function(){ window.__forgedResult = 'error'; };
			l.href = `+jsonString(subresourceRefURL(token, lateURL, 1, forgedSig))+`;
			document.head.appendChild(l);
			return true;
		})()`, nil)))

		var settled bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Poll(`window.__forgedResult !== 'pending'`, &settled, cdp.WithPollingTimeout(10*time.Second))))
		require.True(t, settled, "the forged stylesheet request must settle")

		var result, color string
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.__forgedResult`, &result),
			cdp.Evaluate(`getComputedStyle(document.getElementById('`+subE2EMarker+`')).color`, &color),
		))
		assert.Equal(t, "error", result, "a forged-MAC reference must be REFUSED by the route")
		assert.Equal(t, baseColor, color, "a refused stylesheet must leave the element unstyled")

		// The refusal precedes the socket: no fetch was made for it (§8 — assert the
		// dangerous side effect was never reached, not merely that something failed).
		assert.Len(t, p.dialed.snapshot(), 2, "a forged reference must be refused BEFORE any upstream dial")
	})

	t.Run("genuine_signature_for_the_same_url_does_style_it", func(t *testing.T) {
		// This is the forged case's premise: the URL, the depth, the media type and
		// the stylesheet itself are all fine — only the MAC was wrong above.
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`(function(){
			var l = document.createElement('link');
			l.rel = 'stylesheet';
			l.id = '__genuine';
			l.href = `+jsonString(subresourceRefURL(token, lateURL, 1, genuineSig))+`;
			document.head.appendChild(l);
			return true;
		})()`, nil)))

		var lateStyled bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Poll(`getComputedStyle(document.getElementById('`+subE2EMarker+`')).color === '`+subE2ELateColor+`'`,
				&lateStyled, cdp.WithPollingTimeout(15*time.Second))))
		require.True(t, lateStyled,
			"a genuinely signed reference to the same URL must be served and applied — otherwise the forged refusal above proves nothing")

		paths, _, _ := p.upstream.snapshot()
		assert.Contains(t, paths, "/late.css", "the genuine reference must have provoked exactly the upstream fetch the forged one did not")
		assert.Len(t, p.dialed.snapshot(), 3, "the genuine reference must add exactly one guarded dial")
	})
}

// flipFirstRune returns s with its first byte changed, so a forged signature is
// the same length and alphabet as the genuine one and differs in exactly the
// field under test.
func flipFirstRune(s string) string {
	if s == "" {
		return "x"
	}
	first := "A"
	if s[0] == 'A' {
		first = "B"
	}
	return first + s[1:]
}
