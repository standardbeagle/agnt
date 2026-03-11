package chromedp

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	cdp "github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startIframeTestServer creates an HTTP server serving pages with iframes.
func startIframeTestServer() *httptest.Server {
	mux := http.NewServeMux()

	// Simple colored page for use as iframe content
	mux.HandleFunc("/red", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body style="margin:0;background:red;width:100%;height:100%"><div style="color:white;font-size:24px;padding:20px">Red Frame</div></body></html>`)
	})

	mux.HandleFunc("/blue", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body style="margin:0;background:blue;width:100%;height:100%"><div style="color:white;font-size:24px;padding:20px">Blue Frame</div></body></html>`)
	})

	mux.HandleFunc("/green", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body style="margin:0;background:#00ff00;width:100%;height:100%"><div style="color:black;font-size:24px;padding:20px">Green Frame</div></body></html>`)
	})

	// Page with a single iframe
	mux.HandleFunc("/single-iframe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body style="margin:0;background:white">
			<h1 style="padding:10px">Page with iframe</h1>
			<iframe src="/red" style="width:400px;height:300px;border:none"></iframe>
		</body></html>`)
	})

	// Page with multiple iframes
	mux.HandleFunc("/multi-iframe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body style="margin:0;background:white">
			<h1 style="padding:10px">Multiple iframes</h1>
			<div style="display:flex;gap:10px;padding:10px">
				<iframe src="/red" style="width:300px;height:200px;border:none"></iframe>
				<iframe src="/blue" style="width:300px;height:200px;border:none"></iframe>
				<iframe src="/green" style="width:300px;height:200px;border:none"></iframe>
			</div>
		</body></html>`)
	})

	// Page with nested iframes
	mux.HandleFunc("/nested-iframe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body style="margin:0;background:white">
			<h1 style="padding:10px">Nested iframe</h1>
			<iframe src="/inner-iframe" style="width:500px;height:400px;border:2px solid black"></iframe>
		</body></html>`)
	})

	mux.HandleFunc("/inner-iframe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body style="margin:0;background:#eeeeee;padding:10px">
			<h2>Inner frame</h2>
			<iframe src="/red" style="width:300px;height:200px;border:none"></iframe>
		</body></html>`)
	})

	// Tall page with iframe below the fold
	mux.HandleFunc("/scrollable-iframe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body style="margin:0;background:white">
			<div style="height:1500px;background:linear-gradient(white,#ccc);padding:10px">
				<h1>Tall page — iframe below the fold</h1>
			</div>
			<iframe src="/blue" style="width:400px;height:300px;border:none"></iframe>
		</body></html>`)
	})

	return httptest.NewServer(mux)
}

// helperStartSession creates and starts a chromedp session for testing.
func helperStartSession(t *testing.T, ctx context.Context, manager *SessionManager, id, url string) *AutomationSession {
	t.Helper()
	config := SessionConfig{
		ID:       id,
		URL:      url,
		Headless: true,
	}
	session, err := manager.Start(ctx, id, config)
	require.NoError(t, err, "Failed to start session")
	require.Equal(t, StateRunning, session.State())
	return session
}

// hasColor checks whether the image contains any pixel matching the given color
// within the specified tolerance per channel.
func hasColor(img image.Image, target color.RGBA, tolerance uint8) bool {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			// RGBA returns 16-bit values, scale down
			pr, pg, pb := uint8(r>>8), uint8(g>>8), uint8(b>>8)
			if colorClose(pr, target.R, tolerance) &&
				colorClose(pg, target.G, tolerance) &&
				colorClose(pb, target.B, tolerance) {
				return true
			}
		}
	}
	return false
}

func colorClose(a, b, tolerance uint8) bool {
	if a > b {
		return a-b <= tolerance
	}
	return b-a <= tolerance
}

// loadPNG reads a PNG file and returns the decoded image.
func loadPNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	img, err := png.Decode(f)
	require.NoError(t, err)
	return img
}

func TestScreenshotWithSingleIframe(t *testing.T) {
	if os.Getenv("SKIP_BROWSER_TESTS") != "" {
		t.Skip("SKIP_BROWSER_TESTS is set")
	}

	srv := startIframeTestServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manager := NewSessionManager()
	session := helperStartSession(t, ctx, manager, "iframe-single", srv.URL+"/single-iframe")
	defer manager.Stop(ctx, "iframe-single")

	// Wait for iframe to load
	require.NoError(t, session.Run(cdp.Sleep(500*time.Millisecond)))

	result, err := CaptureViewport(session, ScreenshotOptions{Label: "single-iframe"})
	require.NoError(t, err)
	defer os.Remove(result.Path)

	// Verify file exists and has content
	info, err := os.Stat(result.Path)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(1000), "Screenshot should have substantial content")

	// Verify the red iframe content is visible in the screenshot
	img := loadPNG(t, result.Path)
	assert.True(t, hasColor(img, color.RGBA{R: 255, G: 0, B: 0, A: 255}, 30),
		"Screenshot should contain red pixels from the iframe")
}

func TestScreenshotWithMultipleIframes(t *testing.T) {
	if os.Getenv("SKIP_BROWSER_TESTS") != "" {
		t.Skip("SKIP_BROWSER_TESTS is set")
	}

	srv := startIframeTestServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manager := NewSessionManager()
	session := helperStartSession(t, ctx, manager, "iframe-multi", srv.URL+"/multi-iframe")
	defer manager.Stop(ctx, "iframe-multi")

	require.NoError(t, session.Run(cdp.Sleep(500*time.Millisecond)))

	result, err := CaptureViewport(session, ScreenshotOptions{Label: "multi-iframe"})
	require.NoError(t, err)
	defer os.Remove(result.Path)

	img := loadPNG(t, result.Path)

	// All three iframe colors should be present
	assert.True(t, hasColor(img, color.RGBA{R: 255, G: 0, B: 0, A: 255}, 30),
		"Screenshot should contain red pixels from first iframe")
	assert.True(t, hasColor(img, color.RGBA{R: 0, G: 0, B: 255, A: 255}, 30),
		"Screenshot should contain blue pixels from second iframe")
	assert.True(t, hasColor(img, color.RGBA{R: 0, G: 255, B: 0, A: 255}, 30),
		"Screenshot should contain green pixels from third iframe")
}

func TestScreenshotWithNestedIframes(t *testing.T) {
	if os.Getenv("SKIP_BROWSER_TESTS") != "" {
		t.Skip("SKIP_BROWSER_TESTS is set")
	}

	srv := startIframeTestServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manager := NewSessionManager()
	session := helperStartSession(t, ctx, manager, "iframe-nested", srv.URL+"/nested-iframe")
	defer manager.Stop(ctx, "iframe-nested")

	// Nested iframes may need slightly more time
	require.NoError(t, session.Run(cdp.Sleep(800*time.Millisecond)))

	result, err := CaptureViewport(session, ScreenshotOptions{Label: "nested-iframe"})
	require.NoError(t, err)
	defer os.Remove(result.Path)

	img := loadPNG(t, result.Path)

	// The deeply nested red iframe content should still be captured
	assert.True(t, hasColor(img, color.RGBA{R: 255, G: 0, B: 0, A: 255}, 30),
		"Screenshot should contain red pixels from nested iframe")
}

func TestFullPageScreenshotWithIframeBelowFold(t *testing.T) {
	if os.Getenv("SKIP_BROWSER_TESTS") != "" {
		t.Skip("SKIP_BROWSER_TESTS is set")
	}

	srv := startIframeTestServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manager := NewSessionManager()
	session := helperStartSession(t, ctx, manager, "iframe-scroll", srv.URL+"/scrollable-iframe")
	defer manager.Stop(ctx, "iframe-scroll")

	require.NoError(t, session.Run(cdp.Sleep(500*time.Millisecond)))

	result, err := CaptureFullPage(session, ScreenshotOptions{Label: "scrollable-iframe"})
	require.NoError(t, err)
	defer os.Remove(result.Path)

	img := loadPNG(t, result.Path)

	// Full page screenshot should include the iframe below the fold
	assert.True(t, hasColor(img, color.RGBA{R: 0, G: 0, B: 255, A: 255}, 30),
		"Full page screenshot should contain blue pixels from iframe below the fold")

	// Image height should be larger than a normal viewport (page is 1500px + 300px iframe)
	assert.Greater(t, img.Bounds().Dy(), 1000,
		"Full page screenshot should be taller than a single viewport")
}

func TestClipScreenshotOfIframeRegion(t *testing.T) {
	if os.Getenv("SKIP_BROWSER_TESTS") != "" {
		t.Skip("SKIP_BROWSER_TESTS is set")
	}

	srv := startIframeTestServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manager := NewSessionManager()
	session := helperStartSession(t, ctx, manager, "iframe-clip", srv.URL+"/single-iframe")
	defer manager.Stop(ctx, "iframe-clip")

	require.NoError(t, session.Run(cdp.Sleep(500*time.Millisecond)))

	// Get the iframe's bounding box via JavaScript, then clip to that region.
	// CaptureElement with NodeVisible times out on iframe elements (chromedp limitation),
	// so CaptureWithClip is the reliable alternative for targeting iframe regions.
	var box struct{ X, Y, W, H float64 }
	require.NoError(t, session.Run(cdp.Evaluate(`(() => {
		var r = document.querySelector('iframe').getBoundingClientRect();
		return {X: r.x, Y: r.y, W: r.width, H: r.height};
	})()`, &box)))

	require.Greater(t, box.W, float64(0), "iframe should have width")
	require.Greater(t, box.H, float64(0), "iframe should have height")

	result, err := CaptureWithClip(session, box.X, box.Y, box.W, box.H, ScreenshotOptions{Label: "iframe-clip"})
	require.NoError(t, err)
	defer os.Remove(result.Path)

	img := loadPNG(t, result.Path)

	// The clipped screenshot should capture iframe content (red background)
	assert.True(t, hasColor(img, color.RGBA{R: 255, G: 0, B: 0, A: 255}, 30),
		"Clip screenshot of iframe region should contain red pixels from iframe content")

	// Majority of the clipped image should be red (the iframe content)
	bounds := img.Bounds()
	redCount := 0
	total := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			pr, pg, pb := uint8(r>>8), uint8(g>>8), uint8(b>>8)
			if colorClose(pr, 255, 30) && colorClose(pg, 0, 30) && colorClose(pb, 0, 30) {
				redCount++
			}
			total++
		}
	}
	redRatio := float64(redCount) / float64(total)
	assert.Greater(t, redRatio, 0.3,
		"Clip screenshot should be mostly iframe content (red), got %.1f%%", redRatio*100)
}

func TestViewportScreenshotIframeNotBelowFold(t *testing.T) {
	if os.Getenv("SKIP_BROWSER_TESTS") != "" {
		t.Skip("SKIP_BROWSER_TESTS is set")
	}

	srv := startIframeTestServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manager := NewSessionManager()
	session := helperStartSession(t, ctx, manager, "iframe-noscroll", srv.URL+"/scrollable-iframe")
	defer manager.Stop(ctx, "iframe-noscroll")

	require.NoError(t, session.Run(cdp.Sleep(500*time.Millisecond)))

	// Viewport-only screenshot should NOT contain the iframe below the fold
	result, err := CaptureViewport(session, ScreenshotOptions{Label: "viewport-noscroll"})
	require.NoError(t, err)
	defer os.Remove(result.Path)

	img := loadPNG(t, result.Path)

	// The blue iframe is at 1500px down, viewport screenshot should not include it
	assert.False(t, hasColor(img, color.RGBA{R: 0, G: 0, B: 255, A: 255}, 30),
		"Viewport screenshot should NOT contain blue pixels from iframe below the fold")
}

func TestAllViewportsWithIframe(t *testing.T) {
	if os.Getenv("SKIP_BROWSER_TESTS") != "" {
		t.Skip("SKIP_BROWSER_TESTS is set")
	}

	srv := startIframeTestServer()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	manager := NewSessionManager()
	session := helperStartSession(t, ctx, manager, "iframe-viewports", srv.URL+"/single-iframe")
	defer manager.Stop(ctx, "iframe-viewports")

	require.NoError(t, session.Run(cdp.Sleep(500*time.Millisecond)))

	results, err := CaptureAllViewports(session, "iframe-viewports", false)
	require.NoError(t, err)
	require.Len(t, results, len(DefaultViewports), "Should capture all default viewports")

	for _, result := range results {
		defer os.Remove(result.Path)

		img := loadPNG(t, result.Path)
		assert.True(t, hasColor(img, color.RGBA{R: 255, G: 0, B: 0, A: 255}, 30),
			"Viewport %s screenshot should contain red pixels from iframe", result.Viewport)
	}
}
