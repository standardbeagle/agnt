//go:build chromee2e

package proxy

// Real-browser coverage for internal/proxy/scripts/variant-engine.js — the
// deterministic variant overlay engine (P3 of the walkthrough-publish epic).
//
// The engine's whole value is DOM behavior a string test cannot exercise:
// idempotent scoped apply/revert, re-apply to fresh nodes after an SPA remount
// WITHOUT an observer loop, route-mismatch teardown, a visible missing-target
// marker, and client-side refusal of malicious ops. So these run against a real
// headless Chrome. Env-gated like the other e2e tests (skipIfNoBrowser).
//
// The engine is loaded standalone (no proxy wrap needed) via an httptest server
// so window.location.pathname is real for the route-binding cases.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	cdp "github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// veFixturePage is the fixture app the engine overlays. __ENGINE__ is replaced
// with the engine source at serve time. A tiny bootstrap exposes mkEngine so a
// test can build + bind a VariantSet with one Evaluate call.
const veFixturePage = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>VE</title></head>
<body>
<div id="app"><h1 id="title">Original</h1><p class="msg">hi</p></div>
<script>__ENGINE__</script>
<script>
window.__ve_ready = !!(window.__variantEngine && typeof window.__variantEngine.create === 'function');
window.mkEngine = function (variants, binding) {
  if (window.__eng) { try { window.__eng.destroy(); } catch (e) {} }
  window.__eng = window.__variantEngine.create({ version: 'v1', id: 's', variants: variants }, binding || { when: 'any' });
  return true;
};
</script>
</body></html>`

// veVariantA is the canonical happy-path variant: four ops, each matching one
// element, exercising setText, addClass, applyStyle, and setAttribute.
const veVariantA = `[{ id: 'a', ops: [
  { op: 'setText', selector: '#title', value: 'Changed' },
  { op: 'addClass', selector: '.msg', value: 'hl' },
  { op: 'applyStyle', selector: '#title', props: { color: 'rgb(1, 2, 3)' } },
  { op: 'setAttribute', selector: '#title', name: 'title', value: 'tt' }
] }]`

func startVEFixture(t *testing.T) *httptest.Server {
	t.Helper()
	engine, err := os.ReadFile("scripts/variant-engine.js")
	require.NoError(t, err, "read variant-engine.js")
	page := strings.Replace(veFixturePage, "__ENGINE__", string(engine), 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newVEBrowser(t *testing.T) context.Context {
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

// loadFixture navigates to a fresh copy of the fixture (resetting DOM + JS) and
// confirms the engine registered.
func loadFixture(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	var ready bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate(url),
		cdp.WaitVisible(`#title`, cdp.ByID),
		cdp.Evaluate(`window.__ve_ready`, &ready),
	))
	require.True(t, ready, "window.__variantEngine must be registered")
}

func TestE2E_VariantEngine_RealBrowser(t *testing.T) {
	skipIfNoBrowser(t)
	srv := startVEFixture(t)
	url := srv.URL + "/"
	ctx := newVEBrowser(t)

	// 1. apply then revert restores the DOM EXACTLY (idempotent bookkeeping).
	t.Run("apply_then_revert_restores_exactly", func(t *testing.T) {
		loadFixture(t, ctx, url)
		var before, after, changed string
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`document.getElementById('app').outerHTML`, &before),
			cdp.Evaluate(`window.mkEngine(`+veVariantA+`); window.__eng.apply('a'); document.getElementById('title').textContent`, &changed),
			cdp.Evaluate(`window.__eng.revert(); document.getElementById('app').outerHTML`, &after),
		))
		assert.Equal(t, "Changed", changed, "apply mutates the matched target")
		assert.Equal(t, before, after, "revert must restore the subtree byte-for-byte")
	})

	// 2. apply twice = a single mutation (no stacking, no double-write).
	t.Run("apply_twice_is_single_mutation", func(t *testing.T) {
		loadFixture(t, ctx, url)
		var w1, w2 int
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkEngine(`+veVariantA+`); window.__eng.apply('a'); window.__eng.stats().writeCount`, &w1),
			cdp.Evaluate(`window.__eng.apply('a'); window.__eng.stats().writeCount`, &w2),
		))
		assert.Equal(t, 4, w1, "four ops, four writes on first apply")
		assert.Equal(t, w1, w2, "re-applying the same variant must write nothing (idempotent)")
	})

	// 3. after an SPA remount (subtree replaced) the variant re-applies to the
	//    fresh nodes.
	t.Run("reapplies_after_remount", func(t *testing.T) {
		loadFixture(t, ctx, url)
		var afterRemount string
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkEngine(`+veVariantA+`); window.__eng.apply('a'); true`, nil),
			// Simulate a framework re-render: replace the #app subtree with fresh
			// original nodes (this is the app mutating, not the engine).
			cdp.Evaluate(`(function(){var app=document.getElementById('app');var h=document.createElement('h1');h.id='title';h.textContent='Original';var p=document.createElement('p');p.className='msg';p.textContent='hi';app.replaceChildren(h,p);return true;})()`, nil),
			cdp.Sleep(300*time.Millisecond), // let the observer + rAF debounce fire
			cdp.Evaluate(`document.getElementById('title').textContent`, &afterRemount),
		))
		assert.Equal(t, "Changed", afterRemount, "engine must re-apply to remounted nodes")
	})

	// 4. the MutationObserver does NOT loop: an unrelated page mutation triggers
	//    at most one idempotent re-sync, never a write storm.
	t.Run("observer_does_not_loop", func(t *testing.T) {
		loadFixture(t, ctx, url)
		var w1, w2 int
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkEngine(`+veVariantA+`); window.__eng.apply('a'); window.__eng.stats().writeCount`, &w1),
			// Poke the DOM with something unrelated, then let many frames pass.
			cdp.Evaluate(`document.body.appendChild(document.createElement('span')); true`, nil),
			cdp.Sleep(700*time.Millisecond),
			cdp.Evaluate(`window.__eng.stats().writeCount`, &w2),
		))
		assert.Equal(t, w1, w2, "no self-triggered write storm — writeCount must be stable after settle")
	})

	// 5. route mismatch removes the overlay; returning to the bound route
	//    re-applies it.
	t.Run("route_mismatch_removes_overlay", func(t *testing.T) {
		loadFixture(t, ctx, url)
		var atRoute, offRoute, backOnRoute string
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkEngine(`+veVariantA+`, { when: 'path-equals', value: '/' }); window.__eng.apply('a'); document.getElementById('title').textContent`, &atRoute),
			cdp.Evaluate(`history.pushState({}, '', '/other'); document.getElementById('title').textContent`, &offRoute),
			cdp.Sleep(150*time.Millisecond),
			cdp.Evaluate(`document.getElementById('title').textContent`, &offRoute),
			cdp.Evaluate(`history.pushState({}, '', '/'); document.getElementById('title').textContent`, &backOnRoute),
		))
		assert.Equal(t, "Changed", atRoute, "overlay present on the bound route")
		assert.Equal(t, "Original", offRoute, "overlay removed when the route no longer matches")
		assert.Equal(t, "Changed", backOnRoute, "overlay re-applied on returning to the bound route")
	})

	// 6. a missing-target selector surfaces a VISIBLE marker (not silent).
	t.Run("missing_target_is_visible", func(t *testing.T) {
		loadFixture(t, ctx, url)
		var markerText string
		var markerExists bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkEngine([{ id: 'm', ops: [{ op: 'setText', selector: '#does-not-exist', value: 'x' }] }]); window.__eng.apply('m'); !!document.getElementById('__variant-engine-missing')`, &markerExists),
			cdp.Evaluate(`(document.getElementById('__variant-engine-missing')||{}).textContent || ''`, &markerText),
		))
		assert.True(t, markerExists, "a missing selector must produce a visible marker element")
		assert.Contains(t, markerText, "#does-not-exist", "the marker names the unmatched selector")
	})

	// 7. a malicious variant is refused CLIENT-SIDE (mirrors P2 server rejection).
	t.Run("malicious_variant_refused", func(t *testing.T) {
		loadFixture(t, ctx, url)
		maliciousOps := `[{ id: 'x', ops: [
			{ op: 'setAttribute', selector: '#title', name: 'onclick', value: 'alert(1)' },
			{ op: 'setAttribute', selector: '#title', name: 'href', value: 'javascript:alert(1)' },
			{ op: 'applyStyle', selector: '#title', props: { background: 'url(https://evil.example/x.png)' } },
			{ op: 'setText', selector: '#title:has(script)', value: 'x' },
			{ op: 'setImageSrc', selector: '#title', url: 'http://evil.example/x.png' },
			{ op: 'setHTML', selector: '#title', value: '<img src=x onerror=alert(1)>' }
		] }]`
		var refusedCount, writeCount int
		var titleText string
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkEngine(`+maliciousOps+`); window.__eng.apply('x'); window.__eng.refusedOps().length`, &refusedCount),
			cdp.Evaluate(`window.__eng.stats().writeCount`, &writeCount),
			cdp.Evaluate(`document.getElementById('title').textContent`, &titleText),
		))
		assert.Equal(t, 6, refusedCount, "every malicious op must be refused client-side")
		assert.Equal(t, 0, writeCount, "no malicious op may reach the DOM")
		assert.Equal(t, "Original", titleText, "the target is untouched by refused ops")
		// Belt-and-suspenders: the forbidden handler/attr never landed.
		var hasOnclick, hasHref bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`document.getElementById('title').hasAttribute('onclick')`, &hasOnclick),
			cdp.Evaluate(`document.getElementById('title').hasAttribute('href')`, &hasHref),
		))
		assert.False(t, hasOnclick, "onclick handler must never be set")
		assert.False(t, hasHref, "href must never be set via setAttribute")
	})
}
