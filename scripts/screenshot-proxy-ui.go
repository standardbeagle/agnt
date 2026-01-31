//go:build ignore
// +build ignore

// Script to screenshot the proxy UI for design analysis.
// Run with: go run scripts/screenshot-proxy-ui.go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/standardbeagle/agnt/internal/chromedp"
	"github.com/standardbeagle/agnt/internal/proxy"

	cdp "github.com/chromedp/chromedp"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create output directory
	outputDir := filepath.Join(".", ".agnt", "audit", "proxy-ui-screenshots")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}
	log.Printf("Output directory: %s", outputDir)

	// Start a simple test server
	testHTML := `<!DOCTYPE html>
<html>
<head>
    <title>Proxy UI Test Page</title>
    <style>
        body {
            font-family: system-ui, sans-serif;
            max-width: 800px;
            margin: 0 auto;
            padding: 40px 20px;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            color: white;
        }
        h1 { margin-bottom: 20px; }
        .card {
            background: rgba(255,255,255,0.1);
            border-radius: 12px;
            padding: 24px;
            margin: 20px 0;
            backdrop-filter: blur(10px);
        }
        button {
            background: #4CAF50;
            color: white;
            border: none;
            padding: 12px 24px;
            border-radius: 8px;
            cursor: pointer;
            font-size: 16px;
            margin: 5px;
        }
        button:hover { background: #45a049; }
        input {
            padding: 12px;
            border: none;
            border-radius: 8px;
            font-size: 16px;
            width: 200px;
        }
    </style>
</head>
<body>
    <h1>Proxy UI Test Page</h1>
    <div class="card">
        <h2>Test Content</h2>
        <p>This page demonstrates the agnt proxy devtool integration.</p>
        <p>The floating indicator should appear in the bottom-right corner.</p>
    </div>
    <div class="card">
        <h2>Interactive Elements</h2>
        <button onclick="alert('Button clicked!')">Test Button</button>
        <button onclick="console.log('Console test')">Log to Console</button>
        <input type="text" placeholder="Test input field">
    </div>
    <div class="card">
        <h2>Error Trigger</h2>
        <button onclick="throw new Error('Test error from button')">Trigger Error</button>
        <button onclick="fetch('/nonexistent-api')">Failed API Call</button>
    </div>
</body>
</html>`

	// Start test server
	server := &http.Server{
		Addr: ":18080",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(testHTML))
		}),
	}
	go server.ListenAndServe()
	defer server.Shutdown(ctx)

	log.Println("Test server started on :18080")
	time.Sleep(500 * time.Millisecond)

	// Start a proxy
	proxyMgr := proxy.NewProxyManager()
	proxyConfig := proxy.ProxyConfig{
		ID:         "ui-test",
		TargetURL:  "http://localhost:18080",
		ListenPort: 0, // Auto-assign
		MaxLogSize: 1000,
		Path:       ".",
	}

	proxyServer, err := proxyMgr.Create(ctx, proxyConfig)
	if err != nil {
		log.Fatalf("Failed to create proxy: %v", err)
	}
	defer proxyMgr.Stop(ctx, "ui-test")

	proxyURL := "http://" + proxyServer.ListenAddr
	log.Printf("Proxy started at: %s", proxyURL)

	// Create automation session manager
	sessionMgr := chromedp.NewSessionManager()

	// Start automation session
	sessionConfig := chromedp.SessionConfig{
		ID:       "proxy-ui-capture",
		URL:      proxyURL,
		Headless: true,
	}

	session, err := sessionMgr.Start(ctx, "proxy-ui-capture", sessionConfig)
	if err != nil {
		log.Fatalf("Failed to start automation session: %v", err)
	}
	defer sessionMgr.Stop(ctx, "proxy-ui-capture")

	log.Println("Automation session started")

	// Wait for page to load and indicator to appear
	time.Sleep(2 * time.Second)

	// Take screenshots at different states

	// 1. Default view with indicator
	log.Println("Taking screenshot: default view")
	result, err := chromedp.CaptureViewport(session, chromedp.ScreenshotOptions{
		Label: "proxy-ui-default",
	})
	if err != nil {
		log.Printf("Screenshot error: %v", err)
	} else {
		log.Printf("Saved: %s", result.Path)
	}

	// 2. Click the indicator to show panel
	log.Println("Clicking indicator to show panel")
	if err := session.Run(
		cdp.Click(`#devtool-indicator`, cdp.ByQuery),
	); err != nil {
		log.Printf("Click indicator error: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	result, err = chromedp.CaptureViewport(session, chromedp.ScreenshotOptions{
		Label: "proxy-ui-panel",
	})
	if err != nil {
		log.Printf("Screenshot error: %v", err)
	} else {
		log.Printf("Saved: %s", result.Path)
	}

	// 3. Show sketch mode if available
	log.Println("Attempting to activate sketch mode")
	if err := session.Run(
		cdp.Evaluate(`window.__devtool?.sketch?.open()`, nil),
	); err != nil {
		log.Printf("Sketch mode error: %v", err)
	}
	time.Sleep(1 * time.Second)

	result, err = chromedp.CaptureViewport(session, chromedp.ScreenshotOptions{
		Label: "proxy-ui-sketch",
	})
	if err != nil {
		log.Printf("Screenshot error: %v", err)
	} else {
		log.Printf("Saved: %s", result.Path)
	}

	// 4. Full page screenshot
	log.Println("Taking full page screenshot")
	result, err = chromedp.CaptureFullPage(session, chromedp.ScreenshotOptions{
		Label: "proxy-ui-fullpage",
	})
	if err != nil {
		log.Printf("Full page screenshot error: %v", err)
	} else {
		log.Printf("Saved: %s", result.Path)
	}

	// 5. Mobile viewport
	log.Println("Taking mobile viewport screenshot")
	mobileVP := chromedp.GetViewport("mobile")
	result, err = chromedp.CaptureViewport(session, chromedp.ScreenshotOptions{
		Label:    "proxy-ui-mobile",
		Viewport: mobileVP,
	})
	if err != nil {
		log.Printf("Mobile screenshot error: %v", err)
	} else {
		log.Printf("Saved: %s", result.Path)
	}

	log.Println("Screenshots complete!")
	fmt.Println("\nScreenshots saved to:", outputDir)
}
