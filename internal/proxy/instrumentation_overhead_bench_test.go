//go:build chromee2e

package proxy

// BenchmarkBrowser_InstrumentationOverhead measures what the injected runtime
// costs a real page in real Chrome: the same document loaded directly from the
// upstream and through the proxy (which injects the instrumentation). Go-side
// benchmarks cannot see this: the cost is script parse/compile/execute and the
// extra bytes on the wire, paid by the browser on every load.
//
// Reported as medians across b.N loads: dcl-ms (DOMContentLoaded) and
// load-ms (load event), both from the navigation timing entry. The difference
// between the two sub-benchmarks is the instrumentation overhead.
//
// chromee2e tier: real-Chrome timings are only meaningful on an unloaded
// machine, so run it alone:
//
//	go test -tags=chromee2e -run '^$' -bench BrowserInstrumentationOverhead -benchtime 30x ./internal/proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	cdp "github.com/chromedp/chromedp"
)

func benchSkipIfNoBrowser(b *testing.B) {
	b.Helper()
	if strings.TrimSpace(os.Getenv("SKIP_BROWSER_TESTS")) != "" {
		b.Skip("SKIP_BROWSER_TESTS is set")
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "chrome.exe"} {
		if _, err := exec.LookPath(name); err == nil {
			return
		}
	}
	b.Skip("no Chrome/Chromium binary found")
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[len(s)/2]
}

func BenchmarkBrowser_InstrumentationOverhead(b *testing.B) {
	benchSkipIfNoBrowser(b)

	page := benchHTML(64 << 10)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	}))
	b.Cleanup(backend.Close)
	ps := startBenchProxy(b, backend)

	allocCtx, allocCancel := cdp.NewExecAllocator(context.Background(),
		append(cdp.DefaultExecAllocatorOptions[:],
			cdp.Flag("headless", true),
			cdp.Flag("no-sandbox", true),
			cdp.Flag("disable-gpu", true),
		)...)
	b.Cleanup(allocCancel)
	browserCtx, browserCancel := cdp.NewContext(allocCtx)
	b.Cleanup(browserCancel)
	// A liveness ceiling for a hung browser, not a latency budget.
	browserCtx, timeoutCancel := context.WithTimeout(browserCtx, 10*time.Minute)
	b.Cleanup(timeoutCancel)

	const timing = `(() => {
		const n = performance.getEntriesByType('navigation')[0];
		return {dcl: n.domContentLoadedEventEnd, load: n.loadEventEnd,
			instrumented: typeof window.__devtool !== 'undefined'};
	})()`

	run := func(b *testing.B, target string, wantInstrumented bool) {
		var dcl, load []float64
		for i := 0; i < b.N+1; i++ { // +1: the first load warms Chrome and is discarded
			var res struct {
				DCL          float64 `json:"dcl"`
				Load         float64 `json:"load"`
				Instrumented bool    `json:"instrumented"`
			}
			if err := cdp.Run(browserCtx,
				cdp.Navigate("about:blank"),
				cdp.Navigate(target),
				cdp.WaitReady("body", cdp.ByQuery),
				cdp.Poll(`document.readyState === 'complete'`, nil, cdp.WithPollingTimeout(30*time.Second)),
				cdp.Evaluate(timing, &res),
			); err != nil {
				b.Fatalf("load %s: %v", target, err)
			}
			if res.Instrumented != wantInstrumented {
				b.Fatalf("%s: instrumented=%v, want %v", target, res.Instrumented, wantInstrumented)
			}
			if i == 0 {
				continue
			}
			dcl = append(dcl, res.DCL)
			load = append(load, res.Load)
		}
		b.ReportMetric(median(dcl), "dcl-ms")
		b.ReportMetric(median(load), "load-ms")
	}

	b.Run("direct", func(b *testing.B) { run(b, backend.URL+"/", false) })
	b.Run("proxied", func(b *testing.B) {
		run(b, "http://"+ps.ListenAddr+"/?"+frameMarkerParam+"=f1", true)
	})
}
