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
	"net/http"
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
