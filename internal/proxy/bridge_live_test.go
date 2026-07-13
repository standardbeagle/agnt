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

// TestContentToShellBridge_Live is an opt-in end-to-end check that content-frame
// fetch/XHR calls and JS/console errors are forwarded up to the chrome shell, so
// the indicator (which runs in the shell window) sees them. Gated on
// AGNT_LIVE_BRIDGE because it launches a real Chrome.
//
//	AGNT_LIVE_BRIDGE=1 go test -tags=chromee2e ./internal/proxy -run TestContentToShellBridge_Live -v
func TestContentToShellBridge_Live(t *testing.T) {
	if os.Getenv("AGNT_LIVE_BRIDGE") == "" {
		t.Skip("set AGNT_LIVE_BRIDGE=1 to run the live browser bridge check")
	}

	// Backend app: a full HTML document (so the proxy applies the always-wrap
	// shell model) that on load issues a fetch, logs a console error, and throws
	// an uncaught error.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/data" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>app</title></head><body>
<h1>app</h1>
<script>
  fetch('/api/data').then(function(r){return r.json();}).catch(function(){});
  console.error('CERR_MARKER');
  setTimeout(function(){ throw new Error('JSERR_MARKER'); }, 50);
</script>
</body></html>`))
	}))
	defer backend.Close()

	ps, err := NewProxyServer(ProxyConfig{
		ID:         "live-bridge",
		TargetURL:  backend.URL,
		ListenPort: 0, // auto-assign
	})
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

	// Wait for the proxy to serve.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, e := http.Get(proxyURL)
		if e == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Launch Chrome.
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

	// Evaluate in the TOP (shell) frame: read the shell's own buffers, which are
	// only non-empty if the content frame forwarded its captures up.
	var raw string
	expr := `(function(){
	  if(!window.__devtool_api||!window.__devtool_errors){return 'NO_API role='+(window.__devtool_frame_role||'?');}
	  var calls=window.__devtool_api.getCalls();
	  return JSON.stringify({
	    role: window.__devtool_frame_role||'?',
	    urls: calls.map(function(c){return c.url;}),
	    stats: window.__devtool_errors.getStats()
	  });
	})()`

	if err := chromedp.Run(runCtx,
		chromedp.Navigate(proxyURL),
		chromedp.Sleep(3*time.Second),
		chromedp.Evaluate(expr, &raw),
	); err != nil {
		t.Fatalf("chromedp run: %v", err)
	}

	t.Logf("shell-frame state: %s", raw)

	if strings.HasPrefix(raw, "NO_API") {
		t.Fatalf("shell frame had no devtool API: %s", raw)
	}

	var got struct {
		Role  string   `json:"role"`
		URLs  []string `json:"urls"`
		Stats struct {
			JSErrorCount      int `json:"jsErrorCount"`
			ConsoleErrorCount int `json:"consoleErrorCount"`
			TotalCount        int `json:"totalCount"`
		} `json:"stats"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}

	if got.Role != "chrome" {
		t.Errorf("top frame role = %q, want chrome (proxy not applying shell wrap?)", got.Role)
	}

	var sawAPI bool
	for _, u := range got.URLs {
		if strings.Contains(u, "/api/data") {
			sawAPI = true
		}
	}
	if !sawAPI {
		t.Errorf("shell Network buffer missing /api/data; urls=%v", got.URLs)
	}
	if got.Stats.JSErrorCount < 1 {
		t.Errorf("shell Errors buffer missing JS error; stats=%+v", got.Stats)
	}
	if got.Stats.ConsoleErrorCount < 1 {
		t.Errorf("shell Errors buffer missing console error; stats=%+v", got.Stats)
	}
}
