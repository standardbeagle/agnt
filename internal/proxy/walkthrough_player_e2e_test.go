package proxy

// Real-browser coverage for the P5 public walkthrough PLAYER
// (internal/proxy/scripts/walkthrough-viewer.js). The player's value is DOM
// behavior a string test cannot exercise: applying the bound variant BEFORE
// step 0, manual + auto/click/wait advancement driven purely in-page,
// re-anchoring after a remount, teardown that leaves zero timer/listener/node
// residue, keyboard operation with a focus trap + restore, reduced-motion
// honoring, and the guarantee that an invalid/forbidden target neither executes
// code nor navigates. These run against a real headless Chrome, env-gated by
// skipIfNoBrowser (SKIP LOUDLY — never a silent pass).
//
// The player is loaded standalone alongside the variant engine (its P3
// dependency) via an httptest server so window.location is real for the
// variant route binding.

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

// wpFixturePage hosts a small app plus a nav link (to prove the player never
// navigates on its own) and a bootstrap that builds a player over a published
// walkthrough. __ENGINE__/__PLAYER__ are replaced at serve time. mkPlayer builds
// a player; the test then drives it with further Evaluate calls.
const wpFixturePage = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>WP</title></head>
<body>
<div id="app">
  <h1 id="title">Original</h1>
  <p class="msg">hi</p>
  <button id="cta">Do it</button>
  <a id="leavelink" href="/gone">leave</a>
</div>
<button id="outside">outside</button>
<script>__ENGINE__</script>
<script>__PLAYER__</script>
<script>
window.__wp_ready = !!(window.__walkthroughViewer && typeof window.__walkthroughViewer.create === 'function')
  && !!(window.__variantEngine && typeof window.__variantEngine.create === 'function');
window.mkPlayer = function (wt, opts) {
  if (window.__pl) { try { window.__pl.destroy(); } catch (e) {} }
  window.__pl = window.__walkthroughViewer.create(wt, opts || {});
  return true;
};
window.__navCount = 0;
window.addEventListener('beforeunload', function () { window.__navCount++; });
</script>
</body></html>`

func startWPFixture(t *testing.T) *httptest.Server {
	t.Helper()
	engine, err := os.ReadFile("scripts/variant-engine.js")
	require.NoError(t, err, "read variant-engine.js")
	player, err := os.ReadFile("scripts/walkthrough-viewer.js")
	require.NoError(t, err, "read walkthrough-viewer.js")
	page := strings.Replace(wpFixturePage, "__ENGINE__", string(engine), 1)
	page = strings.Replace(page, "__PLAYER__", string(player), 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newWPBrowser(t *testing.T) context.Context {
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

func loadWPFixture(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	var ready bool
	require.NoError(t, cdp.Run(ctx,
		cdp.Navigate(url),
		cdp.WaitVisible(`#title`, cdp.ByID),
		cdp.Evaluate(`window.__wp_ready`, &ready),
	))
	require.True(t, ready, "both __walkthroughViewer and __variantEngine must be registered")
}

// A published walkthrough whose bound variant rewrites #title. The player must
// apply the variant BEFORE showing step 0.
const wpWalkthroughVariant = `{
  version: 'v1', id: 'wt1', title: 'demo',
  steps: [
    { id: 's1', title: 'One', body: 'first', target: '#title', advance: { type: 'manual' } },
    { id: 's2', title: 'Two', body: 'second', target: '.msg', advance: { type: 'manual' } }
  ],
  variantSet: {
    version: 'v1', id: 'vs1', stepId: 's1',
    variants: [ { id: 'v', ops: [ { op: 'setText', selector: '#title', value: 'Variant!' } ] } ]
  }
}`

func TestE2E_WalkthroughPlayer_RealBrowser(t *testing.T) {
	skipIfNoBrowser(t)
	srv := startWPFixture(t)
	url := srv.URL + "/"
	ctx := newWPBrowser(t)

	// 1. The bound variant is applied BEFORE step 0 renders.
	t.Run("variant_applied_before_step_0", func(t *testing.T) {
		loadWPFixture(t, ctx, url)
		var titleAtStep0, active string
		var cardShown bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkPlayer(`+wpWalkthroughVariant+`); window.__pl.start();
				(function(){ return document.getElementById('title').textContent; })()`, &titleAtStep0),
			cdp.Evaluate(`String(window.__pl.activeVariant())`, &active),
			cdp.Evaluate(`!!document.getElementById('__wt_player_card')`, &cardShown),
		))
		assert.Equal(t, "Variant!", titleAtStep0, "variant must be applied before the first step is shown")
		assert.Equal(t, "v", active, "player should report the applied variant id")
		assert.True(t, cardShown, "the step card must be present at step 0")
	})

	// 2. Manual advancement: next/prev move the index and re-anchor the highlight.
	t.Run("manual_advance_next_prev", func(t *testing.T) {
		loadWPFixture(t, ctx, url)
		var i0, i1, i2 int
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkPlayer(`+wpWalkthroughVariant+`); window.__pl.start(); window.__pl.activeIndex()`, &i0),
			cdp.Evaluate(`window.__pl.next(); window.__pl.activeIndex()`, &i1),
			cdp.Evaluate(`window.__pl.prev(); window.__pl.activeIndex()`, &i2),
		))
		assert.Equal(t, 0, i0)
		assert.Equal(t, 1, i1)
		assert.Equal(t, 0, i2)
	})

	// 3. Auto advancement: an auto step fires its in-page timer and advances with
	//    no transport at all.
	t.Run("auto_advance_via_timer", func(t *testing.T) {
		loadWPFixture(t, ctx, url)
		const autoWT = `{ version:'v1', id:'a', title:'t', steps:[
			{ id:'s1', title:'One', body:'b', advance:{ type:'auto', ms:80 } },
			{ id:'s2', title:'Two', body:'b', advance:{ type:'manual' } } ] }`
		var start int
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkPlayer(`+autoWT+`); window.__pl.start(); window.__pl.activeIndex()`, &start),
		))
		require.Equal(t, 0, start)
		var advanced bool
		require.NoError(t, cdp.Run(ctx, cdp.Poll(`window.__pl.activeIndex() === 1`, &advanced,
			cdp.WithPollingTimeout(3*time.Second))))
	})

	// 4. click-target advancement: clicking the highlighted target advances,
	//    driven purely by an in-page DOM listener.
	t.Run("click_target_advance", func(t *testing.T) {
		loadWPFixture(t, ctx, url)
		const clickWT = `{ version:'v1', id:'c', title:'t', steps:[
			{ id:'s1', title:'One', body:'b', target:'#cta', advance:{ type:'click-target' } },
			{ id:'s2', title:'Two', body:'b', advance:{ type:'manual' } } ] }`
		var start int
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkPlayer(`+clickWT+`); window.__pl.start(); window.__pl.activeIndex()`, &start),
		))
		require.Equal(t, 0, start)
		require.NoError(t, cdp.Run(ctx, cdp.Click(`#cta`, cdp.ByID)))
		var advanced bool
		require.NoError(t, cdp.Run(ctx, cdp.Poll(`window.__pl.activeIndex() === 1`, &advanced,
			cdp.WithPollingTimeout(3*time.Second))))
	})

	// 5. wait advancement: an element-present condition polled in-page advances
	//    once the awaited element appears.
	t.Run("wait_condition_advance", func(t *testing.T) {
		loadWPFixture(t, ctx, url)
		const waitWT = `{ version:'v1', id:'w', title:'t', steps:[
			{ id:'s1', title:'One', body:'b', advance:{ type:'wait', when:'element-present', value:'#late' } },
			{ id:'s2', title:'Two', body:'b', advance:{ type:'manual' } } ] }`
		var start int
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkPlayer(`+waitWT+`); window.__pl.start(); window.__pl.activeIndex()`, &start),
		))
		require.Equal(t, 0, start)
		// The awaited element does not exist yet: the player must still be on s1.
		var stillZero int
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`window.__pl.activeIndex()`, &stillZero)))
		assert.Equal(t, 0, stillZero, "wait step must not advance until its condition holds")
		// Inject the element; the poll should pick it up and advance.
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(
			`(function(){ var d=document.createElement('div'); d.id='late'; document.body.appendChild(d); return true; })()`, nil)))
		var advanced bool
		require.NoError(t, cdp.Run(ctx, cdp.Poll(`window.__pl.activeIndex() === 1`, &advanced,
			cdp.WithPollingTimeout(3*time.Second))))
	})

	// 6. Remount re-anchor: replacing the target node keeps the highlight glued
	//    to the fresh element rather than crashing or going stale.
	t.Run("remount_reanchors_highlight", func(t *testing.T) {
		loadWPFixture(t, ctx, url)
		var missingBefore bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkPlayer(`+wpWalkthroughVariant+`); window.__pl.start(); window.__pl.missingTarget()`, &missingBefore),
		))
		assert.False(t, missingBefore, "target #title exists at step 0")
		// Remove and recreate #title (SPA remount).
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(
			`(function(){ var app=document.getElementById('app'); var old=document.getElementById('title'); old.remove();
			   var h=document.createElement('h1'); h.id='title'; h.textContent='Fresh'; app.insertBefore(h, app.firstChild); return true; })()`, nil)))
		var reanchored bool
		require.NoError(t, cdp.Run(ctx, cdp.Poll(
			`(function(){ var hl=document.getElementById('__wt_player_highlight'); return !!hl && !window.__pl.missingTarget(); })()`,
			&reanchored, cdp.WithPollingTimeout(3*time.Second))))
	})

	// 7. Teardown: destroy removes the card, highlight, and EVERY tracked timer +
	//    listener — no residue.
	t.Run("teardown_leaves_no_residue", func(t *testing.T) {
		loadWPFixture(t, ctx, url)
		const autoWT = `{ version:'v1', id:'a', title:'t', steps:[
			{ id:'s1', title:'One', body:'b', target:'#cta', advance:{ type:'auto', ms:100000 } },
			{ id:'s2', title:'Two', body:'b', advance:{ type:'manual' } } ] }`
		var timersBefore, listenersBefore int
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkPlayer(`+autoWT+`); window.__pl.start(); window.__pl.trackedTimerCount()`, &timersBefore),
			cdp.Evaluate(`window.__pl.trackedListenerCount()`, &listenersBefore),
		))
		assert.GreaterOrEqual(t, timersBefore, 1, "an auto step must arm at least one live timer")
		assert.GreaterOrEqual(t, listenersBefore, 1, "the card must have live listeners while open")
		var timersAfter, listenersAfter int
		var cardGone, hlGone bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.__pl.destroy(); window.__pl.trackedTimerCount()`, &timersAfter),
			cdp.Evaluate(`window.__pl.trackedListenerCount()`, &listenersAfter),
			cdp.Evaluate(`!document.getElementById('__wt_player_card')`, &cardGone),
			cdp.Evaluate(`!document.getElementById('__wt_player_highlight')`, &hlGone),
		))
		assert.Equal(t, 0, timersAfter, "destroy must clear every timer/interval")
		assert.Equal(t, 0, listenersAfter, "destroy must remove every tracked listener")
		assert.True(t, cardGone, "destroy must remove the card")
		assert.True(t, hlGone, "destroy must remove the highlight")
	})

	// 8. Keyboard operation + focus restore: focus lands on the card on open,
	//    Escape closes, and focus returns to where it was.
	t.Run("keyboard_focus_and_restore", func(t *testing.T) {
		loadWPFixture(t, ctx, url)
		var focusedCardOnOpen bool
		var restoredAfterClose bool
		require.NoError(t, cdp.Run(ctx,
			// Focus a page element first so we can prove restore.
			cdp.Evaluate(`document.getElementById('outside').focus(); true`, nil),
			cdp.Evaluate(`window.mkPlayer(`+wpWalkthroughVariant+`); window.__pl.start();
				document.activeElement === document.getElementById('__wt_player_card')`, &focusedCardOnOpen),
		))
		assert.True(t, focusedCardOnOpen, "focus must move to the card on show")
		// Escape via a real key event on the card.
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`(function(){ var c=document.getElementById('__wt_player_card');
				c.dispatchEvent(new KeyboardEvent('keydown', { key:'Escape', bubbles:true })); return true; })()`, nil),
			cdp.Evaluate(`!document.getElementById('__wt_player_card') && document.activeElement === document.getElementById('outside')`, &restoredAfterClose),
		))
		assert.True(t, restoredAfterClose, "Escape must close the player and restore focus to the prior element")
	})

	// 9. Keyboard arrow advancement.
	t.Run("keyboard_arrow_advances", func(t *testing.T) {
		loadWPFixture(t, ctx, url)
		var i0, i1 int
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkPlayer(`+wpWalkthroughVariant+`); window.__pl.start(); window.__pl.activeIndex()`, &i0),
			cdp.Evaluate(`(function(){ var c=document.getElementById('__wt_player_card');
				c.dispatchEvent(new KeyboardEvent('keydown', { key:'ArrowRight', bubbles:true })); return window.__pl.activeIndex(); })()`, &i1),
		))
		assert.Equal(t, 0, i0)
		assert.Equal(t, 1, i1, "ArrowRight must advance to the next step")
	})

	// 10. Reduced motion honored: when opts.reducedMotion is set, the highlight
	//     carries no CSS transition.
	t.Run("reduced_motion_no_transition", func(t *testing.T) {
		loadWPFixture(t, ctx, url)
		var transition string
		var reduced bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.mkPlayer(`+wpWalkthroughVariant+`, { reducedMotion:true }); window.__pl.start();
				window.__pl.reducedMotion()`, &reduced),
			cdp.Evaluate(`(function(){ var hl=document.getElementById('__wt_player_highlight');
				return hl ? (hl.style.transition || '') : 'nohl'; })()`, &transition),
		))
		assert.True(t, reduced, "reducedMotion option must be respected")
		assert.Equal(t, "", transition, "reduced motion must suppress the highlight transition")
	})

	// 11. Invalid/forbidden target must NOT navigate or execute. A step whose
	//     target is an anchor with an exotic selector is rejected by the grammar,
	//     so no highlight, no querySelector on it, and crucially no navigation.
	t.Run("invalid_target_does_not_navigate_or_execute", func(t *testing.T) {
		loadWPFixture(t, ctx, url)
		const badWT = `{ version:'v1', id:'x', title:'t', steps:[
			{ id:'s1', title:'One', body:'b', target:'a[href^="javascript:"]', advance:{ type:'click-target' } },
			{ id:'s2', title:'Two', body:'b', advance:{ type:'manual' } } ] }`
		var navCount, idx int
		var missing bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.__navCount = 0; window.mkPlayer(`+badWT+`); window.__pl.start(); window.__navCount`, &navCount),
			cdp.Evaluate(`window.__pl.activeIndex()`, &idx),
			cdp.Evaluate(`window.__pl.missingTarget()`, &missing),
		))
		assert.Equal(t, 0, navCount, "an invalid target must not cause navigation")
		assert.Equal(t, 0, idx, "the player must remain on the step (click-target with rejected target degrades to manual)")
		// The rejected target means no highlight was drawn (treated as no-target).
		var hasHighlight bool
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`!!document.getElementById('__wt_player_highlight')`, &hasHighlight)))
		assert.False(t, hasHighlight, "a rejected selector must not be resolved or highlighted")
	})

	// 12. The player must not follow a real anchor's href on its own: even a
	//     valid link target only gets a highlight, never a synthetic activation.
	t.Run("valid_link_target_is_not_followed", func(t *testing.T) {
		loadWPFixture(t, ctx, url)
		const linkWT = `{ version:'v1', id:'l', title:'t', steps:[
			{ id:'s1', title:'One', body:'b', target:'#leavelink', advance:{ type:'manual' } } ] }`
		var navCount int
		var hasHighlight bool
		require.NoError(t, cdp.Run(ctx,
			cdp.Evaluate(`window.__navCount = 0; window.mkPlayer(`+linkWT+`); window.__pl.start(); window.__navCount`, &navCount),
			cdp.Evaluate(`!!document.getElementById('__wt_player_highlight')`, &hasHighlight),
		))
		assert.Equal(t, 0, navCount, "highlighting a link target must not navigate")
		assert.True(t, hasHighlight, "a valid link target should still be highlighted")
	})
}
