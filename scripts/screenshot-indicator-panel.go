//go:build ignore

// Script to screenshot the indicator panel with tabs
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/standardbeagle/agnt/internal/chromedp"
	"github.com/standardbeagle/agnt/internal/proxy"

	cdp "github.com/chromedp/chromedp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Start a simple test server
	testHTML := `<!DOCTYPE html>
<html>
<head>
    <title>Tab Test</title>
    <style>
        body { font-family: system-ui; padding: 40px; background: #f5f5f5; }
        h1 { color: #333; }
    </style>
</head>
<body>
    <h1>Testing Indicator Panel Tabs</h1>
    <p>Click the indicator to see the panel with tabs.</p>
</body>
</html>`

	server := &http.Server{
		Addr: ":18081",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(testHTML))
		}),
	}
	go server.ListenAndServe()
	defer server.Shutdown(ctx)
	time.Sleep(300 * time.Millisecond)

	// Start proxy
	proxyMgr := proxy.NewProxyManager()
	proxyServer, err := proxyMgr.Create(ctx, proxy.ProxyConfig{
		ID:         "tab-test",
		TargetURL:  "http://localhost:18081",
		ListenPort: 0,
		MaxLogSize: 1000,
		Path:       ".",
	})
	if err != nil {
		log.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxyMgr.Stop(ctx, "tab-test")

	proxyURL := "http://" + proxyServer.ListenAddr
	log.Printf("Proxy at: %s", proxyURL)

	// Start automation session
	sessionMgr := chromedp.NewSessionManager()
	session, err := sessionMgr.Start(ctx, "tab-capture", chromedp.SessionConfig{
		ID:       "tab-capture",
		URL:      proxyURL,
		Headless: true,
	})
	if err != nil {
		log.Fatalf("Failed to start session: %v", err)
	}
	defer sessionMgr.Stop(ctx, "tab-capture")

	// Wait for page and indicator to load
	time.Sleep(2 * time.Second)

	// Open the panel using the __devtool API
	log.Println("Opening indicator panel via __devtool API...")
	var openResult string
	if err := session.Run(cdp.Evaluate(`
		if (window.__devtool && window.__devtool.indicator && window.__devtool.indicator.togglePanel) {
			window.__devtool.indicator.togglePanel(true);
			'opened';
		} else {
			'__devtool API not available';
		}
	`, &openResult)); err != nil {
		log.Printf("Open panel error: %v", err)
	}
	log.Printf("Panel open result: %s", openResult)

	// Wait for panel animation to complete
	time.Sleep(800 * time.Millisecond)

	// Take screenshot showing the panel
	result, err := chromedp.CaptureViewport(session, chromedp.ScreenshotOptions{
		Label: "indicator-panel-tabs",
	})
	if err != nil {
		log.Fatalf("Screenshot error: %v", err)
	}
	log.Printf("Screenshot saved: %s", result.Path)

	// Also get the indicator HTML to analyze
	var indicatorHTML string
	if err := session.Run(cdp.Evaluate(`
		var panel = document.getElementById('__devtool-panel');
		panel ? panel.outerHTML.substring(0, 2000) : 'panel not found';
	`, &indicatorHTML)); err != nil {
		log.Printf("Get HTML error: %v", err)
	}
	log.Printf("Panel HTML preview:\n%s", indicatorHTML)

	// Take another screenshot with a different tab (errors) to show tab switching
	log.Println("Switching to Errors tab...")
	if err := session.Run(cdp.Evaluate(`
		if (window.__devtool && window.__devtool.indicator) {
			// Click on the errors tab
			var errTab = document.getElementById('__devtool-tab-errors');
			if (errTab) errTab.click();
			'switched';
		} else {
			'API not available';
		}
	`, nil)); err != nil {
		log.Printf("Tab switch error: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	result, err = chromedp.CaptureViewport(session, chromedp.ScreenshotOptions{
		Label: "indicator-panel-errors-tab",
	})
	if err != nil {
		log.Printf("Screenshot error: %v", err)
	} else {
		log.Printf("Errors tab screenshot saved: %s", result.Path)
	}

	// Test container queries by resizing panel to narrow width
	log.Println("Testing narrow panel (container query)...")
	if err := session.Run(cdp.Evaluate(`
		var panel = document.getElementById('__devtool-panel');
		if (panel) {
			panel.style.width = '350px';
			'resized to 350px';
		} else {
			'panel not found';
		}
	`, nil)); err != nil {
		log.Printf("Resize error: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	result, err = chromedp.CaptureViewport(session, chromedp.ScreenshotOptions{
		Label: "indicator-panel-narrow",
	})
	if err != nil {
		log.Printf("Screenshot error: %v", err)
	} else {
		log.Printf("Narrow panel screenshot saved: %s", result.Path)
	}

	// Test very narrow panel
	log.Println("Testing very narrow panel...")
	if err := session.Run(cdp.Evaluate(`
		var panel = document.getElementById('__devtool-panel');
		if (panel) {
			panel.style.width = '280px';
			'resized to 280px';
		} else {
			'panel not found';
		}
	`, nil)); err != nil {
		log.Printf("Resize error: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	result, err = chromedp.CaptureViewport(session, chromedp.ScreenshotOptions{
		Label: "indicator-panel-very-narrow",
	})
	if err != nil {
		log.Printf("Screenshot error: %v", err)
	} else {
		log.Printf("Very narrow panel screenshot saved: %s", result.Path)
	}
}
