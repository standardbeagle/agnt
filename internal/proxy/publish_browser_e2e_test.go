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
// httptest page), this test navigates to the REAL /s/{token} artifact the public
// plane emits, so the bundle loads under the REAL wholesale CSP (script-src
// 'self' + bundle hash, connect-src 'self'), and the page fetches its OWN
// artifact JSON same-origin the way a deployed viewer would.

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/runtime"
	cdp "github.com/chromedp/chromedp"
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
