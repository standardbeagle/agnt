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
	"github.com/stretchr/testify/require"
)

// skipIfBrowserDisabled skips the test if SKIP_BROWSER_TESTS is set.
// Shared by all chromedp tests that need a real Chromium binary.
func skipIfBrowserDisabled(tb testing.TB) {
	tb.Helper()
	if os.Getenv("SKIP_BROWSER_TESTS") != "" {
		tb.Skip("SKIP_BROWSER_TESTS is set")
	}
}

// startIframeTestServer creates an HTTP server serving pages with iframes.
// Routes:
//
//	/red, /blue, /green       — solid-color leaf pages for use as iframe content
//	/single-iframe            — one red iframe
//	/multi-iframe             — three iframes (red, blue, green) side by side
//	/nested-iframe            — iframe containing iframe (red grandchild)
//	/scrollable-iframe        — tall page with blue iframe below the fold
func startIframeTestServer() *httptest.Server {
	mux := http.NewServeMux()

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

	mux.HandleFunc("/single-iframe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body style="margin:0;background:white">
			<h1 style="padding:10px">Page with iframe</h1>
			<iframe src="/red" style="width:400px;height:300px;border:none"></iframe>
		</body></html>`)
	})

	mux.HandleFunc("/multi-iframe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body style="margin:0;background:white">
			<h1 style="padding:10px">Multiple iframes</h1>
			<div style="display:flex;gap:10px;padding:10px">
				<iframe src="/red" style="width:300px;height:200px;border:none"></iframe>
				<iframe src="/blue" style="width:300px;height:200px;border:none"></iframe>
				<iframe src="/green" style="width:300px;height:200px;border:none"></iframe>
			</div>
		</body></html>`)
	})

	mux.HandleFunc("/nested-iframe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body style="margin:0;background:white">
			<h1 style="padding:10px">Nested iframe</h1>
			<iframe src="/inner-iframe" style="width:500px;height:400px;border:2px solid black"></iframe>
		</body></html>`)
	})

	mux.HandleFunc("/inner-iframe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body style="margin:0;background:#eeeeee;padding:10px">
			<h2>Inner frame</h2>
			<iframe src="/red" style="width:300px;height:200px;border:none"></iframe>
		</body></html>`)
	})

	mux.HandleFunc("/scrollable-iframe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body style="margin:0;background:white">
			<div style="height:1500px;background:linear-gradient(white,#ccc);padding:10px">
				<h1>Tall page — iframe below the fold</h1>
			</div>
			<iframe src="/blue" style="width:400px;height:300px;border:none"></iframe>
		</body></html>`)
	})

	return httptest.NewServer(mux)
}

// setupBrowserOnce boots a single headless chromedp session and registers
// teardown via t.Cleanup. The returned session is reused across subtests
// via cdp.Navigate — the one-shot browser boot is the dominant cost, so
// sharing amortizes it across every screenshot variant.
func setupBrowserOnce(tb testing.TB, startURL string) (*SessionManager, *AutomationSession) {
	tb.Helper()
	skipIfBrowserDisabled(tb)

	// This context is the browser's LIFETIME, not a boot deadline — it parents
	// the chromedp allocator, so when it expires Chrome is killed and the
	// session flips to Stopped mid-test. 60s looked generous but the full
	// suite under -race load pushed the shared-session variants past it
	// (observed: AllViewportsIframe failing at ~69s with "session not
	// running"). Keep a ceiling only as hang protection, far above any
	// legitimate run time.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	tb.Cleanup(cancel)

	manager := NewSessionManager()
	id := "shared-screenshot-variants"
	config := SessionConfig{
		ID:       id,
		URL:      startURL,
		Headless: true,
	}
	session, err := manager.Start(ctx, id, config)
	require.NoError(tb, err, "boot shared browser")
	require.Equal(tb, StateRunning, session.State())

	tb.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = manager.Stop(stopCtx, id)
	})

	return manager, session
}

// navigateAndSettle navigates to url and waits for the page body to paint.
// Uses a DOM-observable predicate (document.readyState === "complete")
// polled via require.Eventually rather than a fixed time.Sleep.
func navigateAndSettle(t *testing.T, session *AutomationSession, url string) {
	t.Helper()
	require.NoError(t, session.Run(cdp.Navigate(url)))

	require.Eventually(t, func() bool {
		var ready string
		if err := session.Run(cdp.Evaluate(`document.readyState`, &ready)); err != nil {
			return false
		}
		return ready == "complete"
	}, 10*time.Second, 50*time.Millisecond, "page did not reach readyState=complete for %s", url)
}

// waitForIframePaint polls until every same-origin iframe's body has non-zero
// offsetWidth — i.e. its content has rendered. Skips cross-origin frames.
// Returns immediately when there are no iframes on the page.
func waitForIframePaint(t *testing.T, session *AutomationSession) {
	t.Helper()
	require.Eventually(t, func() bool {
		var ok bool
		err := session.Run(cdp.Evaluate(`(() => {
			const frames = document.querySelectorAll('iframe');
			if (frames.length === 0) return true;
			for (const f of frames) {
				try {
					const d = f.contentDocument;
					if (!d || !d.body) return false;
					if (d.body.offsetWidth === 0) return false;
				} catch (e) {
					// Cross-origin frame — can't inspect, trust browser.
				}
			}
			return true;
		})()`, &ok))
		return err == nil && ok
	}, 10*time.Second, 50*time.Millisecond, "iframes did not paint")
}

// hasColor checks whether the image contains any pixel matching the given color
// within the specified tolerance per channel.
func hasColor(img image.Image, target color.RGBA, tolerance uint8) bool {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
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

// isPNG checks whether the byte stream starts with a valid PNG magic header.
func isPNG(buf []byte) bool {
	if len(buf) < 8 {
		return false
	}
	// PNG signature: 89 50 4E 47 0D 0A 1A 0A
	return buf[0] == 0x89 && buf[1] == 0x50 && buf[2] == 0x4E && buf[3] == 0x47 &&
		buf[4] == 0x0D && buf[5] == 0x0A && buf[6] == 0x1A && buf[7] == 0x0A
}

// readScreenshotBytes reads the raw PNG file for post-state validation.
func readScreenshotBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}
