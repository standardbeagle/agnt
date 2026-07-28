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

func startVECSPFixture(t *testing.T) *httptest.Server {
	t.Helper()
	engine, err := os.ReadFile("scripts/variant-engine.js")
	require.NoError(t, err)
	design, err := os.ReadFile("scripts/design.js")
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("/variant-engine.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write(engine)
	})
	mux.HandleFunc("/design.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		_, _ = w.Write(design)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><body><h1 id="title">Original</h1><script src="/variant-engine.js"></script><script src="/design.js"></script></body></html>`)
	})
	return httptest.NewServer(mux)
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
		assert.Equal(t, 5, refusedCount, "every malicious op must be refused client-side")
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

	// 8. the RETIRED §5b allowlist / §5 forbidden-token scan: an applyStyle that
	//    the old client-side rule refused must now APPLY. internal/publish/style.go
	//    dropped both guards with INV-6 (2026-07-27), so the server accepts this
	//    op at publish; a JS mirror that still refused it produced an op that was
	//    neither rejected loudly nor honored. This is the flipped half of case 7.
	t.Run("retired_style_guards_now_apply", func(t *testing.T) {
		loadFixture(t, ctx, url)
		retiredOps := `[{ id: 'r', ops: [
			{ op: 'applyStyle', selector: '#title', props: { background: 'url(https://cdn.example.com/x.png)' } },
			{ op: 'applyStyle', selector: '.msg', props: { '-webkit-mask-image': 'linear-gradient(black, transparent)' } }
		] }]`
		var refusedCount, writeCount int
		var bg, mask string
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkEngine(`+retiredOps+`); window.__eng.apply('r'); window.__eng.refusedOps().length`, &refusedCount),
			cdp.Evaluate(`window.__eng.stats().writeCount`, &writeCount),
			cdp.Evaluate(`document.getElementById('title').style.background`, &bg),
			cdp.Evaluate(`document.querySelector('.msg').style.getPropertyValue('-webkit-mask-image')`, &mask),
		))
		assert.Equal(t, 0, refusedCount, "the retired property allowlist / token scan must refuse nothing")
		assert.Equal(t, 2, writeCount, "both ops must reach the DOM")
		assert.Contains(t, bg, "url(", "a url() background is publisher-authored CSS, contained by CSP not by a substring scan")
		assert.Contains(t, mask, "linear-gradient", "an off-allowlist property must apply")
	})

	// 9. the AGGREGATE budgets (internal/publish/validate.go:94 and :139), which
	//    the engine previously did not mirror at all: splitting one oversize
	//    payload across several per-op-legal ops must not dodge the bound.
	t.Run("aggregate_budgets_enforced", func(t *testing.T) {
		loadFixture(t, ctx, url)
		var styleRefused, scriptRefused int
		require.NoError(t, cdp.Run(ctx,
			// Per-VARIANT style budget: applyStyle and addStyle share one 4096B
			// budget, so 3000B + 3000B overflows even though neither op alone does.
			cdp.Evaluate(`(function(){
				var big = new Array(3001).join('x');
				window.mkEngine([{ id: 's', ops: [
					{ op: 'applyStyle', selector: '#title', props: { color: big } },
					{ op: 'addStyle', css: '/*' + big + '*/' }
				] }]);
				return window.__eng.refusedOps().length;
			})()`, &styleRefused),
			// Per-REVISION script budget: summed across every variant in the set,
			// so two 10000B bodies in two different variants overflow 16384B.
			cdp.Evaluate(`(function(){
				var body = new Array(10001).join('y');
				window.mkEngine([
					{ id: 'p', ops: [{ op: 'addScript', code: body }] },
					{ id: 'q', ops: [{ op: 'addScript', code: body }] }
				]);
				return window.__eng.refusedOps().length;
			})()`, &scriptRefused),
		))
		assert.Equal(t, 1, styleRefused, "the op that overflows the per-variant style budget must be refused")
		assert.Equal(t, 1, scriptRefused, "the op that overflows the per-revision script budget must be refused")
	})

	// 10. the §5 size limits are denominated in UTF-8 BYTES, not UTF-16 code
	//     units. Every Go limit is a `len(...)` over a string; JS `.length`
	//     counts code units, so a CJK payload measures 1/3 of its real size and
	//     an emoji payload 1/4. Each probe below is comfortably UNDER its limit
	//     in code units and comfortably OVER it in bytes — under a `.length`
	//     comparison every one of them is wrongly ACCEPTED here while the Go
	//     validator rejects it, which is exactly the mirror disagreement this
	//     engine exists to prevent. Each site is paired with an under-both
	//     control so the assertion cannot be satisfied by refusing everything.
	t.Run("size_caps_are_utf8_byte_denominated", func(t *testing.T) {
		loadFixture(t, ctx, url)

		// 900 CJK chars: 900 code units (< 2048), 2700 UTF-8 bytes (> 2048).
		// mirrors internal/publish/op.go:93 len(o.Value) > MaxAttrValueLength.
		var attrOverReason, attrUnderOK string
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`(function(){
				var v = new Array(901).join('漢');
				var r = window.__variantEngine.validateOp({ op: 'setAttribute', selector: '#title', name: 'title', value: v });
				return (v.length < 2048 && r.ok === false) ? r.reason : 'WRONGLY ACCEPTED: units=' + v.length;
			})()`, &attrOverReason),
			cdp.Evaluate(`(function(){
				var v = new Array(601).join('漢'); // 600 units, 1800 bytes — under both
				var r = window.__variantEngine.validateOp({ op: 'setAttribute', selector: '#title', name: 'title', value: v });
				return r.ok === true ? 'ok' : 'WRONGLY REFUSED: ' + r.reason;
			})()`, &attrUnderOK),
		))
		assert.Contains(t, attrOverReason, "setAttribute: value exceeds max 2048",
			"a 2700-byte / 900-code-unit setAttribute value must be refused (op.go:93 counts bytes)")
		assert.Equal(t, "ok", attrUnderOK, "an under-both-measures value must still be accepted")

		// 800 CJK chars in the query: ~840 code units (< 2048), ~2440 bytes
		// (> 2048). mirrors internal/publish/url.go:15 len(raw) > MaxURLLength.
		var urlOverReason, urlUnderOK string
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`(function(){
				var u = 'https://cdn.example.com/x.png?q=' + new Array(801).join('漢');
				var r = window.__variantEngine.validateOp({ op: 'setImageSrc', selector: '#title', url: u });
				return (u.length < 2048 && r.ok === false) ? r.reason : 'WRONGLY ACCEPTED: units=' + u.length;
			})()`, &urlOverReason),
			cdp.Evaluate(`(function(){
				var u = 'https://cdn.example.com/x.png?q=' + new Array(601).join('漢');
				var r = window.__variantEngine.validateOp({ op: 'setImageSrc', selector: '#title', url: u });
				return r.ok === true ? 'ok' : 'WRONGLY REFUSED: ' + r.reason;
			})()`, &urlUnderOK),
		))
		assert.Contains(t, urlOverReason, "exceeds max 2048",
			"a ~2440-byte / ~840-code-unit URL must be refused (url.go:15 counts bytes)")
		assert.Contains(t, urlOverReason, "url: length",
			"the refusal must come from the LENGTH cap, not some other guard")
		assert.Equal(t, "ok", urlUnderOK, "an under-both-measures https URL must still be accepted")

		// 120 CJK chars: 120 code units (< 256), 360 bytes (> 256). The selector
		// grammar rejects non-ASCII idents too, so the assertion pins that the
		// LENGTH cap is what fires — under `.length` the string sails past the
		// cap and is refused later by the grammar, a different (and wrong) reason.
		// mirrors internal/publish/selector.go:37 len(sel) > MaxSelectorLength.
		var selOverReason, selUnderOK string
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`(function(){
				var s = new Array(121).join('漢');
				var r = window.__variantEngine.validateSelector(s);
				return (s.length < 256 && r.ok === false) ? r.reason : 'WRONGLY ACCEPTED: units=' + s.length;
			})()`, &selOverReason),
			cdp.Evaluate(`(function(){
				var r = window.__variantEngine.validateSelector('#title');
				return r.ok === true ? 'ok' : 'WRONGLY REFUSED: ' + r.reason;
			})()`, &selUnderOK),
		))
		assert.Contains(t, selOverReason, "selector: length 360 exceeds max 256",
			"a 360-byte / 120-code-unit selector must be refused BY THE LENGTH CAP (selector.go:37 counts bytes), not incidentally by the grammar")
		assert.Equal(t, "ok", selUnderOK, "an ordinary in-grammar selector must still be accepted")
	})
}

func TestE2E_VariantEngine_SelfCSPAndDesignRoundTrip(t *testing.T) {
	skipIfNoBrowser(t)
	srv := startVECSPFixture(t)
	defer srv.Close()
	ctx := newVEBrowser(t)

	var ready bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate(srv.URL),
		cdp.WaitVisible(`#title`, cdp.ByID),
		cdp.Evaluate(`!!(window.__variantEngine && window.__devtool_design && window.__devtool_design.importVariantSet)`, &ready),
	))
	require.True(t, ready, "self-hosted scripts must load under script-src 'self'")

	var changed, restored, exported string
	require.NoError(t, cdp.Run(ctx,
		cdp.Evaluate(`window.__devtool_design.importVariantSet({version:'v1',id:'design',variants:[{id:'a',ops:[{op:'setText',selector:'#title',value:'Changed'}]}]}, {when:'any'}); window.__devtool_design.applyVariant('a'); document.getElementById('title').textContent`, &changed),
		cdp.Evaluate(`JSON.stringify(window.__devtool_design.exportVariantSet())`, &exported),
		cdp.Evaluate(`window.__devtool_design.revertVariant(); document.getElementById('title').textContent`, &restored),
	))
	assert.Equal(t, "Changed", changed)
	assert.JSONEq(t, `{"version":"v1","id":"design","variants":[{"id":"a","ops":[{"op":"setText","selector":"#title","value":"Changed"}]}]}`, exported)
	assert.Equal(t, "Original", restored)
}
