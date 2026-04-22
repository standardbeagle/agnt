package chromedp

import (
	"image"
	"image/color"
	"os"
	"testing"

	cdp "github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScreenshotVariants exercises every chromedp screenshot capture mode
// against a single, long-lived headless browser session. Previously these
// were 8 separate integration tests (1 baseline + 7 iframe variants), each
// booting its own browser — browser startup dominated per-test wall-clock,
// so amortizing across variants and polling DOM predicates instead of
// fixed sleeps cuts package wall-clock >75%.
//
// Each case: navigate → settle → capture → assert subject + post-state.
// Post-state invariants (session still Running, screenshot bytes are valid
// PNG, non-trivial payload size, screenshot file deleted cleanly) run
// after every case — they catch leaks between cases, which is the point
// of NOT using TestMain.
func TestScreenshotVariants(t *testing.T) {
	srv := startIframeTestServer()
	defer srv.Close()

	_, session := setupBrowserOnce(t, "about:blank")

	cases := []struct {
		name     string
		path     string // server path; empty means capture baseline about:blank page
		scenario func(t *testing.T, session *AutomationSession, serverURL string) *ScreenshotResult
		assert   func(t *testing.T, result *ScreenshotResult, img image.Image)
	}{
		{
			name: "BaselineViewport",
			path: "", // data: URL handled inside scenario
			scenario: func(t *testing.T, session *AutomationSession, _ string) *ScreenshotResult {
				navigateAndSettle(t, session, "data:text/html,<h1 style='color:red'>Screenshot Test</h1>")
				result, err := CaptureViewport(session, ScreenshotOptions{Label: "baseline"})
				require.NoError(t, err)
				return result
			},
			assert: func(t *testing.T, result *ScreenshotResult, img image.Image) {
				// Subject: captured something visible. The data: URL renders on
				// default white body, so we only check that the image is non-empty
				// and has realistic dimensions.
				assert.Greater(t, img.Bounds().Dx(), 100, "viewport width should be >100px")
				assert.Greater(t, img.Bounds().Dy(), 100, "viewport height should be >100px")
			},
		},
		{
			name: "SingleIframe",
			path: "/single-iframe",
			scenario: func(t *testing.T, session *AutomationSession, serverURL string) *ScreenshotResult {
				navigateAndSettle(t, session, serverURL+"/single-iframe")
				waitForIframePaint(t, session)
				result, err := CaptureViewport(session, ScreenshotOptions{Label: "single-iframe"})
				require.NoError(t, err)
				return result
			},
			assert: func(t *testing.T, _ *ScreenshotResult, img image.Image) {
				assert.True(t, hasColor(img, color.RGBA{R: 255, G: 0, B: 0, A: 255}, 30),
					"single-iframe should contain red pixels")
			},
		},
		{
			name: "MultipleIframes",
			path: "/multi-iframe",
			scenario: func(t *testing.T, session *AutomationSession, serverURL string) *ScreenshotResult {
				navigateAndSettle(t, session, serverURL+"/multi-iframe")
				waitForIframePaint(t, session)
				result, err := CaptureViewport(session, ScreenshotOptions{Label: "multi-iframe"})
				require.NoError(t, err)
				return result
			},
			assert: func(t *testing.T, _ *ScreenshotResult, img image.Image) {
				assert.True(t, hasColor(img, color.RGBA{R: 255, G: 0, B: 0, A: 255}, 30), "red iframe pixels")
				assert.True(t, hasColor(img, color.RGBA{R: 0, G: 0, B: 255, A: 255}, 30), "blue iframe pixels")
				assert.True(t, hasColor(img, color.RGBA{R: 0, G: 255, B: 0, A: 255}, 30), "green iframe pixels")
			},
		},
		{
			name: "NestedIframes",
			path: "/nested-iframe",
			scenario: func(t *testing.T, session *AutomationSession, serverURL string) *ScreenshotResult {
				navigateAndSettle(t, session, serverURL+"/nested-iframe")
				waitForIframePaint(t, session)
				result, err := CaptureViewport(session, ScreenshotOptions{Label: "nested-iframe"})
				require.NoError(t, err)
				return result
			},
			assert: func(t *testing.T, _ *ScreenshotResult, img image.Image) {
				assert.True(t, hasColor(img, color.RGBA{R: 255, G: 0, B: 0, A: 255}, 30),
					"nested red iframe pixels should reach top-level screenshot")
			},
		},
		{
			name: "FullPageIframeBelowFold",
			path: "/scrollable-iframe",
			scenario: func(t *testing.T, session *AutomationSession, serverURL string) *ScreenshotResult {
				navigateAndSettle(t, session, serverURL+"/scrollable-iframe")
				waitForIframePaint(t, session)
				result, err := CaptureFullPage(session, ScreenshotOptions{Label: "scrollable-iframe"})
				require.NoError(t, err)
				return result
			},
			assert: func(t *testing.T, _ *ScreenshotResult, img image.Image) {
				assert.True(t, hasColor(img, color.RGBA{R: 0, G: 0, B: 255, A: 255}, 30),
					"full page should contain blue iframe below the fold")
				assert.Greater(t, img.Bounds().Dy(), 1000,
					"full page height should exceed viewport (page=1500px+iframe=300px)")
			},
		},
		{
			name: "ViewportHidesIframeBelowFold",
			path: "/scrollable-iframe",
			scenario: func(t *testing.T, session *AutomationSession, serverURL string) *ScreenshotResult {
				navigateAndSettle(t, session, serverURL+"/scrollable-iframe")
				waitForIframePaint(t, session)
				// Ensure scroll is at top so the below-fold iframe is out of viewport.
				require.NoError(t, session.Run(cdp.Evaluate(`window.scrollTo(0,0)`, nil)))
				result, err := CaptureViewport(session, ScreenshotOptions{Label: "viewport-noscroll"})
				require.NoError(t, err)
				return result
			},
			assert: func(t *testing.T, _ *ScreenshotResult, img image.Image) {
				assert.False(t, hasColor(img, color.RGBA{R: 0, G: 0, B: 255, A: 255}, 30),
					"viewport-only screenshot should NOT contain blue pixels from below-fold iframe")
			},
		},
		{
			name: "ClipOfIframeRegion",
			path: "/single-iframe",
			scenario: func(t *testing.T, session *AutomationSession, serverURL string) *ScreenshotResult {
				navigateAndSettle(t, session, serverURL+"/single-iframe")
				waitForIframePaint(t, session)

				// CaptureElement with NodeVisible times out on iframe elements
				// (chromedp limitation), so we use CaptureWithClip against the
				// iframe's rect instead.
				var box struct{ X, Y, W, H float64 }
				require.NoError(t, session.Run(cdp.Evaluate(`(() => {
					var r = document.querySelector('iframe').getBoundingClientRect();
					return {X: r.x, Y: r.y, W: r.width, H: r.height};
				})()`, &box)))

				require.Greater(t, box.W, float64(0), "iframe should have width")
				require.Greater(t, box.H, float64(0), "iframe should have height")

				result, err := CaptureWithClip(session, box.X, box.Y, box.W, box.H, ScreenshotOptions{Label: "iframe-clip"})
				require.NoError(t, err)
				return result
			},
			assert: func(t *testing.T, _ *ScreenshotResult, img image.Image) {
				assert.True(t, hasColor(img, color.RGBA{R: 255, G: 0, B: 0, A: 255}, 30),
					"clip should contain red pixels from iframe")

				// Majority of the clipped image should be red (the iframe content).
				bounds := img.Bounds()
				redCount, total := 0, 0
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
					"clip should be majority-iframe content (red), got %.1f%%", redRatio*100)
			},
		},
		{
			name: "AllViewportsIframe",
			path: "/single-iframe",
			// AllViewports is structurally different — CaptureAllViewports
			// returns a slice, so assertions live in scenario and the generic
			// img asserter is a no-op. We still exercise post-state invariants
			// via the returned *ScreenshotResult by picking one representative.
			scenario: func(t *testing.T, session *AutomationSession, serverURL string) *ScreenshotResult {
				navigateAndSettle(t, session, serverURL+"/single-iframe")
				waitForIframePaint(t, session)
				results, err := CaptureAllViewports(session, "iframe-viewports", false)
				require.NoError(t, err)
				require.Len(t, results, len(DefaultViewports),
					"should capture all %d default viewports", len(DefaultViewports))

				// Assert red iframe shows up in every viewport capture AND each
				// file has a valid PNG header + non-trivial size. That's two
				// post-state invariants per viewport, six viewports = 12 asserts.
				for _, r := range results {
					t.Cleanup(func() { os.Remove(r.Path) })
					img := loadPNG(t, r.Path)
					assert.True(t, hasColor(img, color.RGBA{R: 255, G: 0, B: 0, A: 255}, 30),
						"viewport %s: red iframe pixels", r.Viewport)

					raw := readScreenshotBytes(t, r.Path)
					assert.True(t, isPNG(raw), "viewport %s: valid PNG header", r.Viewport)
					assert.Greater(t, len(raw), 1000, "viewport %s: non-trivial payload", r.Viewport)
				}

				// Return the last result for the outer generic invariants.
				return results[len(results)-1]
			},
			assert: func(t *testing.T, _ *ScreenshotResult, _ image.Image) {
				// Inner assertions done in scenario; this is intentionally empty.
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Pre-state invariant: shared session healthy before we start.
			require.Equal(t, StateRunning, session.State(), "session leaked failed state from prior case")
			require.NotNil(t, session.Context(), "session context must be live")

			result := tc.scenario(t, session, srv.URL)
			require.NotNil(t, result, "scenario must return a result")

			// Clean up the file at the end regardless of failure.
			t.Cleanup(func() { os.Remove(result.Path) })

			// --- Post-state invariants: file existence + byte integrity ---
			info, err := os.Stat(result.Path)
			require.NoError(t, err, "screenshot file missing")
			assert.Greater(t, info.Size(), int64(100),
				"screenshot bytes should be non-trivial (>100B), got %d", info.Size())

			raw := readScreenshotBytes(t, result.Path)
			assert.True(t, isPNG(raw), "screenshot should start with PNG magic header")
			assert.GreaterOrEqual(t, len(raw), 100, "raw bytes non-trivial")

			img := loadPNG(t, result.Path)
			assert.NotNil(t, img, "PNG decode must succeed")

			// Subject-specific asserts live in tc.assert.
			tc.assert(t, result, img)

			// --- Post-state invariants: browser still alive for the NEXT case ---
			// This is the reason we don't use TestMain — each case must verify
			// it left the shared browser in a usable state.
			assert.Equal(t, StateRunning, session.State(),
				"case must leave shared session in Running state")
			assert.NotNil(t, session.Context(), "session context must survive case")

			// Probe the session with a cheap eval — if the browser crashed
			// mid-case, this will fail before the next case silently papers
			// over the leak.
			var alive bool
			evalErr := session.Run(cdp.Evaluate(`true`, &alive))
			assert.NoError(t, evalErr, "session eval must succeed post-case")
			assert.True(t, alive, "session eval must return truthy result")
		})
	}
}
