//go:build chromee2e

package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	chromedp "github.com/chromedp/chromedp"
)

// TestResponsiveOverlay_Live is an opt-in end-to-end check that responsive-mode
// shift overlays (1) scroll with the page and (2) toggle on/off. Gated on
// AGNT_LIVE_RESP because it launches a real Chrome.
//
//	AGNT_LIVE_RESP=1 go test -tags=chromee2e ./internal/proxy -run TestResponsiveOverlay_Live -v
func TestResponsiveOverlay_Live(t *testing.T) {
	if os.Getenv("AGNT_LIVE_RESP") == "" {
		t.Skip("set AGNT_LIVE_RESP=1 to run the live responsive-overlay check")
	}

	// A page with a 2000px-wide element placed below a tall spacer, so at width
	// 375 it forces horizontal overflow (a critical finding → an overlay box) and
	// sits far enough down that vertical scroll visibly moves its top.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>resp</title></head><body style="margin:0">
<div style="height:1500px"></div>
<div id="wide" style="width:2000px;height:80px;background:#ccc">wide</div>
<div style="height:1500px"></div>
</body></html>`))
	}))
	defer backend.Close()

	ps, err := NewProxyServer(ProxyConfig{ID: "live-resp", TargetURL: backend.URL, ListenPort: 0})
	if err != nil {
		t.Fatalf("NewProxyServer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ps.Start(ctx); err != nil {
		t.Fatalf("proxy Start: %v", err)
	}
	defer ps.Stop(context.Background())

	proxyURL := "http://" + ps.ListenAddr + "/"
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp, e := http.Get(proxyURL); e == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	execPath := os.Getenv("AGNT_CHROME")
	if execPath == "" {
		execPath = "google-chrome"
	}
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(execPath),
		chromedp.Headless,
		chromedp.DisableGPU,
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer allocCancel()
	bctx, bcancel := chromedp.NewContext(allocCtx)
	defer bcancel()
	runCtx, runCancel := context.WithTimeout(bctx, 40*time.Second)
	defer runCancel()

	// Reads the overlay box tops out of the shell mount root. A box is an
	// absolute-positioned div with a 2px border (the shift outline).
	// A responsive shift box is an absolute-positioned div with a 2px border whose
	// only child (the label tag) reads the finding type ('overflow' / 'layout').
	// Filtering on that text isolates it from other modules' overlay divs.
	boxFn := `function(){
	  var root = (typeof window.__devtoolGetMountRoot==='function') ? window.__devtoolGetMountRoot() : document.body;
	  var all = root.querySelectorAll('div'); var tops=[];
	  for (var i=0;i<all.length;i++){ var d=all[i]; var s=d.style;
	    if (s.position==='absolute' && s.border && s.border.indexOf('2px')>=0) {
	      var t = d.textContent || '';
	      if (t.indexOf('overflow')>=0 || t.indexOf('layout')>=0) { tops.push(parseFloat(s.top)); }
	    }
	  }
	  return tops;
	}`

	var openRaw, afterScrollRaw, toggleOffRaw, toggleOnRaw string

	openExpr := `(function(){
	  var ns = window.__devtool_responsive;
	  if (!ns || !ns.open) { return JSON.stringify({err:'no responsive ns role='+(window.__devtool_frame_role||'?')}); }
	  ns.open(); ns.setWidth(375);
	  return 'ok';
	})()`

	stateExpr := func() string {
		return `(function(){
		  var ns = window.__devtool_responsive;
		  var st = ns.getState();
		  var sels = st.shifts.map(function(s){return s.selector;});
		  return JSON.stringify({open:st.open, overlaysVisible:st.overlaysVisible, hasWide: sels.indexOf('#wide')>=0, tops:(` + boxFn + `)()});
		})()`
	}

	scrollExpr := `(function(){
	  var f = document.getElementById('__devtool_content_frame');
	  f.contentWindow.scrollTo(0, 500);
	  return 'scrolled';
	})()`

	if err := chromedp.Run(runCtx,
		chromedp.Navigate(proxyURL),
		chromedp.Sleep(2500*time.Millisecond),
		chromedp.Evaluate(openExpr, &openRaw),
		chromedp.Sleep(800*time.Millisecond),            // let capture debounce settle + overlays render
		chromedp.Evaluate(stateExpr(), &afterScrollRaw), // pre-scroll snapshot (reuse var name below)
	); err != nil {
		t.Fatalf("chromedp open run: %v", err)
	}
	t.Logf("open: %s", openRaw)
	if openRaw != "ok" {
		t.Fatalf("responsive open failed: %s", openRaw)
	}

	var preScroll struct {
		Open            bool      `json:"open"`
		OverlaysVisible bool      `json:"overlaysVisible"`
		HasWide         bool      `json:"hasWide"`
		Tops            []float64 `json:"tops"`
	}
	if err := json.Unmarshal([]byte(afterScrollRaw), &preScroll); err != nil {
		t.Fatalf("unmarshal preScroll %q: %v", afterScrollRaw, err)
	}
	t.Logf("pre-scroll: %+v", preScroll)
	if !preScroll.Open || !preScroll.HasWide {
		t.Fatalf("responsive mode not open with #wide finding: %+v", preScroll)
	}
	if !preScroll.OverlaysVisible || len(preScroll.Tops) == 0 {
		t.Fatalf("no overlay box rendered: %+v", preScroll)
	}
	top1 := preScroll.Tops[0]

	// Scroll the content frame, then re-read the box top — it must move.
	var scrolled string
	if err := chromedp.Run(runCtx,
		chromedp.Evaluate(scrollExpr, &scrolled),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(stateExpr(), &afterScrollRaw),
	); err != nil {
		t.Fatalf("chromedp scroll run: %v", err)
	}
	var postScroll struct {
		Tops []float64 `json:"tops"`
	}
	if err := json.Unmarshal([]byte(afterScrollRaw), &postScroll); err != nil {
		t.Fatalf("unmarshal postScroll %q: %v", afterScrollRaw, err)
	}
	t.Logf("post-scroll tops: %v (was %v)", postScroll.Tops, top1)
	if len(postScroll.Tops) == 0 {
		t.Fatalf("overlay box gone after scroll")
	}
	top2 := postScroll.Tops[0]
	if diff := top1 - top2; diff < 400 || diff > 600 {
		t.Errorf("overlay did not scroll with page: top %v -> %v (expected ~500px drop)", top1, top2)
	}

	// Toggle overlays OFF → no boxes; ON → boxes return.
	if err := chromedp.Run(runCtx,
		chromedp.Evaluate(`window.__devtool_responsive.toggleOverlays(false)`, nil),
		chromedp.Sleep(100*time.Millisecond),
		chromedp.Evaluate(stateExpr(), &toggleOffRaw),
		chromedp.Evaluate(`window.__devtool_responsive.toggleOverlays(true)`, nil),
		chromedp.Sleep(100*time.Millisecond),
		chromedp.Evaluate(stateExpr(), &toggleOnRaw),
	); err != nil {
		t.Fatalf("chromedp toggle run: %v", err)
	}

	var off, on struct {
		OverlaysVisible bool      `json:"overlaysVisible"`
		Tops            []float64 `json:"tops"`
	}
	_ = json.Unmarshal([]byte(toggleOffRaw), &off)
	_ = json.Unmarshal([]byte(toggleOnRaw), &on)
	t.Logf("toggle off: %+v  on: %+v", off, on)
	if off.OverlaysVisible || len(off.Tops) != 0 {
		t.Errorf("toggle off did not hide overlays: %+v", off)
	}
	if !on.OverlaysVisible || len(on.Tops) == 0 {
		t.Errorf("toggle on did not restore overlays: %+v", on)
	}
}
