//go:build chromee2e

package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	chromedp "github.com/chromedp/chromedp"
)

// TestTabsContentPull_Live verifies the data source the indicator tabs rely on:
// capture (fetch, errors, mutations) lives in the CONTENT frame, while the
// shell frame's own __devtool_* globals stay empty. The indicator (shell) fix
// reads via targetWindow() -> the content frame window, which is exactly the
// window this test reaches through document.getElementById('__devtool_content_frame').
// Gated on AGNT_LIVE_TABS because it launches a real Chrome.
//
//	AGNT_LIVE_TABS=1 go test -tags=chromee2e ./internal/proxy -run TestTabsContentPull_Live -v
func TestTabsContentPull_Live(t *testing.T) {
	if os.Getenv("AGNT_LIVE_TABS") == "" {
		t.Skip("set AGNT_LIVE_TABS=1 to run the live tabs content-pull check")
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/data" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>app</title></head><body>
<h1>app</h1>
<div id="sink"></div>
<script>
  fetch('/api/data').then(function(r){return r.json();}).catch(function(){});
  console.error('CERR_MARKER');
  setTimeout(function(){ throw new Error('JSERR_MARKER'); }, 50);
  // Continuous DOM mutations so the mutation observer records a non-zero rate.
  setInterval(function(){
    var d = document.createElement('div'); d.textContent = 't';
    document.getElementById('sink').appendChild(d);
  }, 30);
</script>
</body></html>`))
	}))
	defer backend.Close()

	ps, err := NewProxyServer(ProxyConfig{ID: "live-tabs", TargetURL: backend.URL, ListenPort: 0})
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
	runCtx, runCancel := context.WithTimeout(bctx, 30*time.Second)
	defer runCancel()

	// Evaluated in the TOP (shell) frame. Compares the shell's own globals (which
	// the buggy code read) against the content frame's globals (which the fix
	// reads via targetWindow()).
	expr := `(function(){
	  var cw = null;
	  try { cw = document.getElementById('__devtool_content_frame').contentWindow; } catch(e){}
	  function calls(w){ try { return w.__devtool_api ? w.__devtool_api.getCalls().map(function(c){return c.url;}) : null; } catch(e){ return null; } }
	  function errStats(w){ try { return w.__devtool_errors ? w.__devtool_errors.getStats() : null; } catch(e){ return null; } }
	  function mutRate(w){ try {
	    if (!w.__devtool_mutations) return null;
	    var rs = w.__devtool_mutations.getRateStats([5000]) || {};
	    return (rs.windows && rs.windows['5s'] != null) ? rs.windows['5s'] : 0;
	  } catch(e){ return null; } }
	  return JSON.stringify({
	    role: window.__devtool_frame_role || '?',
	    haveContent: !!cw,
	    shell: { urls: calls(window), err: errStats(window), mut: mutRate(window) },
	    content: cw ? { urls: calls(cw), err: errStats(cw), mut: mutRate(cw) } : null
	  });
	})()`

	var raw string
	if err := chromedp.Run(runCtx,
		chromedp.Navigate(proxyURL),
		chromedp.Sleep(3*time.Second), // let fetch/errors fire + mutations accumulate
		chromedp.Evaluate(expr, &raw),
	); err != nil {
		t.Fatalf("chromedp run: %v", err)
	}
	t.Logf("state: %s", raw)

	type side struct {
		URLs []string `json:"urls"`
		Err  *struct {
			JSErrorCount      int `json:"jsErrorCount"`
			ConsoleErrorCount int `json:"consoleErrorCount"`
		} `json:"err"`
		Mut *float64 `json:"mut"`
	}
	var got struct {
		Role        string `json:"role"`
		HaveContent bool   `json:"haveContent"`
		Shell       side   `json:"shell"`
		Content     *side  `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}

	if got.Role != "chrome" {
		t.Fatalf("top frame role = %q, want chrome (no shell wrap?)", got.Role)
	}
	if !got.HaveContent || got.Content == nil {
		t.Fatalf("content frame not reachable from shell: %s", raw)
	}

	// Shell-local globals must be EMPTY — this is the bug the fix routes around.
	if len(got.Shell.URLs) != 0 {
		t.Errorf("shell saw API calls it should not: %v", got.Shell.URLs)
	}
	if got.Shell.Err != nil && (got.Shell.Err.JSErrorCount > 0 || got.Shell.Err.ConsoleErrorCount > 0) {
		t.Errorf("shell saw errors it should not: %+v", got.Shell.Err)
	}

	// Content frame (what targetWindow() resolves to) must HAVE the data.
	var sawAPI bool
	for _, u := range got.Content.URLs {
		if strings.Contains(u, "/api/data") {
			sawAPI = true
		}
	}
	if !sawAPI {
		t.Errorf("content frame missing /api/data: %v", got.Content.URLs)
	}
	if got.Content.Err == nil || got.Content.Err.JSErrorCount < 1 || got.Content.Err.ConsoleErrorCount < 1 {
		t.Errorf("content frame missing errors: %+v", got.Content.Err)
	}
	if got.Content.Mut == nil || *got.Content.Mut <= 0 {
		t.Errorf("content frame missing mutation rate: %v", got.Content.Mut)
	}
}
