//go:build chromee2e

package proxy

// Real-browser coverage for the shell toolbar under the always-wrap model.
// Every "Capture & tools" button lives in the chrome shell, but the page it
// works on is the content iframe. A button that runs its module in the shell
// window draws over an opaque iframe and does nothing the user can see, so each
// case asserts the effect landed in the content frame (or, for the shell-owned
// overlays, in the shell) after a click on the real button.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cdp "github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const toolbarPage = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Toolbar</title>
<style>
  body { margin: 0; padding: 40px; font-family: sans-serif; color: rgb(17, 24, 39); background: rgb(255, 255, 255); }
  .card { width: 320px; padding: 16px; border-radius: 8px; background: rgb(243, 244, 246); }
  .btn { background: rgb(37, 99, 235); color: rgb(255, 255, 255); padding: 8px; border-radius: 6px; }
</style></head>
<body>
  <div class="card" id="target"><h3>Plan</h3><a class="btn" href="#buy">Buy</a></div>
  <div class="card"><h3>Other</h3></div>
</body></html>`

// shellJS is evaluated in the shell (top) window. content() is the content
// iframe's window; clickTool clicks the toolbar button by its label.
const shellJS = `
window.__t = {
  content: function () {
    var f = document.querySelector('iframe');
    return f && f.contentWindow;
  },
  clickTool: function (label) {
    // Open the panel on the compose tab, as a user does, so the toolbar exists.
    window.__devtool_indicator.togglePanel(true);
    window.__devtool_indicator.switchTab('compose');
    var host = document.getElementById('__devtool-root');
    var root = (host && host.shadowRoot) || document;
    var btns = root.querySelectorAll('.__devtool-tool-btn');
    for (var i = 0; i < btns.length; i++) {
      if (btns[i].textContent.trim() === label) { btns[i].click(); return true; }
    }
    return false;
  }
};`

func evalJSON(ctx context.Context, t *testing.T, expr string, out any) {
	t.Helper()
	var raw string
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`JSON.stringify((function(){`+expr+`})())`, &raw)))
	require.NoError(t, json.Unmarshal([]byte(raw), out), "eval returned %s", raw)
}

func TestE2E_ShellToolbar_EveryButtonReachesTheContentFrame(t *testing.T) {
	skipIfNoBrowser(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, toolbarPage)
	}))
	t.Cleanup(backend.Close)
	ps := startWrapProxy(t, backend)

	allocCtx, allocCancel := cdp.NewExecAllocator(context.Background(),
		append(cdp.DefaultExecAllocatorOptions[:],
			cdp.Flag("headless", true),
			cdp.Flag("no-sandbox", true),
			cdp.Flag("disable-gpu", true),
			cdp.WindowSize(1280, 800),
		)...)
	t.Cleanup(allocCancel)
	ctx, cancel := cdp.NewContext(allocCtx)
	t.Cleanup(cancel)
	ctx, tcancel := context.WithTimeout(ctx, 90*time.Second)
	t.Cleanup(tcancel)

	// Top-level navigation: the proxy serves the shell with the page in an iframe.
	var ignored any
	err := cdp.Run(ctx,
		cdp.Navigate("http://"+ps.ListenAddr+"/"),
		cdp.Poll(`(function(){
		  var f=document.querySelector('iframe'); var w=f&&f.contentWindow;
		  var host=document.getElementById('__devtool-root');
		  return !!(w && w.__devtool_design && w.document.getElementById('target') &&
		    host && host.shadowRoot && window.__devtool_indicator && window.__devtool_indicator.state.panel);
		})()`, nil, cdp.WithPollingTimeout(30*time.Second)),
	)
	if err != nil {
		var diag string
		_ = cdp.Run(ctx, cdp.Evaluate(`JSON.stringify((function(){
		  var f=document.querySelector('iframe'); var w=f&&f.contentWindow; var host=document.getElementById('__devtool-root');
		  return {iframe:!!f, src:f&&f.src, design:!!(w&&w.__devtool_design), target:!!(w&&w.document.getElementById('target')),
		    host:!!host, shadow:!!(host&&host.shadowRoot), btns:host&&host.shadowRoot?host.shadowRoot.querySelectorAll('.__devtool-tool-btn').length:-1,
		    anyBtn: document.querySelectorAll('.__devtool-tool-btn').length};
		})())`, &diag))
		require.NoError(t, err, "shell not ready: %s", diag)
	}
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(shellJS, &ignored)))

	var role string
	require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`window.__devtool_frame_role||window.__devtool_role||''`, &role)))
	require.Equal(t, "chrome", role, "top frame is the shell")

	t.Run("Design swaps an option into the page in place", func(t *testing.T) {
		var ok bool
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`__t.clickTool('Design')`, &ok)))
		require.True(t, ok, "Design button present")

		var st struct {
			ContentActive bool `json:"contentActive"`
			ShellActive   bool `json:"shellActive"`
			X, Y          float64
		}
		evalJSON(ctx, t, `var w=__t.content(); var r=w.document.getElementById('target').getBoundingClientRect();
		  return {contentActive: w.__devtool_design.getState().isActive,
		    shellActive: !!(window.__devtool_design && window.__devtool_design.getState().isActive),
		    X: r.left + 6, Y: r.top + 6};`, &st) // the card padding, not its h3
		require.True(t, st.ContentActive, "design mode runs in the content frame")
		assert.False(t, st.ShellActive, "design mode must not run in the shell")

		// A real mouse click: the content frame fills the shell, so the
		// coordinates are the same in both.
		require.NoError(t, cdp.Run(ctx,
			cdp.MouseEvent("mouseMoved", st.X-5, st.Y-5),
			cdp.MouseEvent("mouseMoved", st.X, st.Y),
			cdp.Sleep(100*time.Millisecond), // one animation frame for the hover to land
			cdp.MouseClickXY(st.X, st.Y)))
		var diag string
		_ = cdp.Run(ctx, cdp.Evaluate(`JSON.stringify({st: __t.content().__devtool_design.getState(),
		  topAt: (function(){var e=document.elementFromPoint(`+fmt.Sprint(st.X)+`,`+fmt.Sprint(st.Y)+`); return e && (e.id||e.tagName);})(),
		  innerAt: (function(){var e=__t.content().document.elementFromPoint(`+fmt.Sprint(st.X)+`,`+fmt.Sprint(st.Y)+`); return e && (e.id||e.tagName);})()})`, &diag))

		var sw struct {
			Swapped      bool    `json:"swapped"`
			Class        string  `json:"cls"`
			TargetHidden bool    `json:"targetHidden"`
			CloneHeight  float64 `json:"cloneHeight"`
			Index        int     `json:"index"`
			Count        int     `json:"count"`
			Hit          string  `json:"hit"`
		}
		require.NoError(t, cdp.Run(ctx, cdp.Poll(`!!__t.content().document.querySelector('[data-devtool-design-swap]')`, nil,
			cdp.WithPollingTimeout(5*time.Second))), "no swap after click: %s", diag)
		evalJSON(ctx, t, `var w=__t.content(), d=w.document, c=d.querySelector('[data-devtool-design-swap]'),
		    tg=d.getElementById('target'), s=w.__devtool_design.getState(), r=c.getBoundingClientRect(),
		    hit=d.elementFromPoint(r.left+r.width/2, r.top+4);
		  return {swapped: !!c, cls: c.className, targetHidden: w.getComputedStyle(tg).display==='none',
		    cloneHeight: r.height, index: s.currentIndex, count: s.alternativesCount,
		    hit: hit ? (hit.closest('[data-devtool-design-swap]') ? 'clone' : hit.tagName) : ''};`, &sw)
		assert.Equal(t, "card", sw.Class, "clone keeps the target's classes")
		assert.True(t, sw.TargetHidden, "the original is hidden while the option shows")
		assert.Positive(t, sw.CloneHeight, "the option takes the element's place")
		assert.Equal(t, "clone", sw.Hit, "the option is what the user sees at that spot")
		assert.Equal(t, 1, sw.Index, "first instant option shows at once")
		assert.GreaterOrEqual(t, sw.Count, 3, "original plus at least two instant options")

		// An agent alternative arriving via proxy exec lands in the same frame.
		var added struct {
			Added int `json:"added"`
		}
		evalJSON(ctx, t, `return __t.content().__devtool_design.addAlternatives([
		    {html:'<h3>Agent one</h3>', note:'first-note'}, {html:'<h3>Agent two</h3>', note:'second-note'}]);`, &added)
		assert.Equal(t, 2, added.Added)

		// What the user sees: the swapped option in the page and the panel badge.
		type view struct {
			Inner string `json:"inner"`
			Panel string `json:"panel"`
			Count int    `json:"count"`
		}
		look := func() view {
			var v view
			evalJSON(ctx, t, `var w=__t.content(), d=w.document, sr=d.getElementById('__devtool-root').shadowRoot,
			    c=d.querySelectorAll('[data-devtool-design-swap]');
			  var ph=sr.getElementById('__devtool-design-panel'), pr=ph && (ph.shadowRoot || ph);
			  return {inner: c.length ? c[0].innerHTML : '', panel: pr ? pr.textContent : '', count: c.length};`, &v)
			return v
		}
		v := look()
		assert.Equal(t, 1, v.Count, "one option in the page at a time")
		assert.Equal(t, "<h3>Agent two</h3>", v.Inner, "the newest agent option is swapped in")
		assert.Contains(t, v.Panel, "second-note", "the panel names the option on screen")

		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`__t.content().__devtool_design.previous()`, &ignored)))
		v = look()
		assert.Equal(t, "<h3>Agent one</h3>", v.Inner, "previous swaps the page to the other option")
		assert.Contains(t, v.Panel, "first-note")

		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`__t.content().__devtool_design.next()`, &ignored)))
		v = look()
		assert.Equal(t, "<h3>Agent two</h3>", v.Inner, "next swaps it back")
		assert.Equal(t, 1, v.Count)

		var after struct {
			Swapped bool   `json:"swapped"`
			Display string `json:"display"`
		}
		evalJSON(ctx, t, `var w=__t.content(); w.__devtool_design.stop(); var d=w.document;
		  return {swapped: !!d.querySelector('[data-devtool-design-swap]'), display: w.getComputedStyle(d.getElementById('target')).display};`, &after)
		assert.False(t, after.Swapped, "stop removes the option")
		assert.Equal(t, "block", after.Display, "stop shows the original again")
	})

	t.Run("Sketch opens over the page", func(t *testing.T) {
		var ok bool
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`__t.clickTool('Sketch')`, &ok)))
		require.True(t, ok)
		var st struct {
			Content bool `json:"content"`
			Shell   bool `json:"shell"`
		}
		evalJSON(ctx, t, `return {content: __t.content().__devtool_sketch.state.isActive,
		  shell: !!(window.__devtool_sketch && window.__devtool_sketch.state.isActive)};`, &st)
		assert.True(t, st.Content, "sketch runs in the content frame")
		assert.False(t, st.Shell)
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`__t.content().__devtool_sketch.close()`, &ignored)))
	})

	t.Run("Inspect opens the style editor on the page", func(t *testing.T) {
		var ok bool
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`__t.clickTool('Inspect')`, &ok)))
		require.True(t, ok)
		var open bool
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`__t.content().__devtool_style_editor.isOpen()`, &open)))
		assert.True(t, open, "style editor runs in the content frame")
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`__t.content().__devtool_style_editor.close()`, &ignored)))
	})

	t.Run("Element picker hit-tests page elements", func(t *testing.T) {
		var ok bool
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`__t.clickTool('Element')`, &ok)))
		require.True(t, ok)
		var st struct {
			X, Y float64
		}
		evalJSON(ctx, t, `var r=__t.content().document.getElementById('target').getBoundingClientRect();
		  return {X: r.left + 20, Y: r.top + 4};`, &st)
		require.NoError(t, cdp.Run(ctx, cdp.MouseEvent("mouseMoved", st.X, st.Y)))
		var label string
		require.NoError(t, cdp.Run(ctx, cdp.Poll(`(function(){
		  var host=document.getElementById('__devtool-root'); var all=[document];
		  if (host && host.shadowRoot) all.push(host.shadowRoot);
		  for (var i=0;i<all.length;i++){ var t=all[i].textContent||''; if (t.indexOf('#target')>=0||t.indexOf('div.card')>=0) return t.slice(0,200); }
		  var txt=document.body.innerText; return txt.indexOf('card')>=0 ? txt.slice(0,200) : false;
		})()`, &label, cdp.WithPollingTimeout(5*time.Second))))
		assert.NotContains(t, label, "iframe", "picker names the page element, not the iframe")
		require.NoError(t, cdp.Run(ctx, cdp.KeyEvent("\x1b")))
	})

	t.Run("Responsive opens in the shell", func(t *testing.T) {
		var ok bool
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`__t.clickTool('Responsive')`, &ok)))
		require.True(t, ok)
		var open bool
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`window.__devtool_responsive.getState().open`, &open)))
		assert.True(t, open)
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`window.__devtool_responsive.toggle()`, &ignored)))
	})

	t.Run("Refresh reloads the page without dropping the shell", func(t *testing.T) {
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`__t.content().__marker = 1; window.__shellMarker = 1`, &ignored)))
		var ok bool
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`__t.clickTool('Refresh')`, &ok)))
		require.True(t, ok)
		require.NoError(t, cdp.Run(ctx, cdp.Poll(`(function(){var w=__t.content(); return !!(w && !w.__marker && w.__devtool_design && w.document.getElementById('target'));})()`,
			nil, cdp.WithPollingTimeout(15*time.Second))))
		var shell int
		require.NoError(t, cdp.Run(ctx, cdp.Evaluate(`window.__shellMarker`, &shell)))
		assert.Equal(t, 1, shell, "the shell stays loaded")
	})
}
