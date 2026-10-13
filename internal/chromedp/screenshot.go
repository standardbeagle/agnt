package chromedp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/pathutil"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// ScreenshotDirName is the subdirectory for screenshots within the audit directory.
const ScreenshotDirName = "screenshots"

// ScreenshotResult contains information about a captured screenshot.
type ScreenshotResult struct {
	Path      string `json:"path"`      // Full path to the screenshot file
	Filename  string `json:"filename"`  // Just the filename
	Width     int64  `json:"width"`     // Viewport/capture width
	Height    int64  `json:"height"`    // Viewport/capture height
	Viewport  string `json:"viewport"`  // Viewport preset name (if used)
	Timestamp string `json:"timestamp"` // ISO timestamp of capture
}

// ScreenshotOptions configures screenshot capture behavior.
type ScreenshotOptions struct {
	Label    string          // Optional label for the filename
	Viewport *ViewportPreset // Viewport preset to use (emulation applied for this capture only)
	Scale    float64         // Scale factor for CaptureWithClip (default 1.0); other captures ignore it
}

// GetScreenshotDir returns the screenshots directory path within
// projectRoot/.agnt/audit/, creating it if it does not exist.
//
// projectRoot is a required parameter rather than something resolved here from
// os.Getwd(): the daemon that hosts automation sessions is long-lived and shared
// across projects, so its working directory is unrelated to the project a
// session belongs to. Resolving the destination from ambient process state put
// one project's screenshots in another project's tree (and, under test, inside
// the source tree). An empty projectRoot is an error, never a cwd fallback.
func GetScreenshotDir(projectRoot string) (string, error) {
	if projectRoot == "" {
		return "", fmt.Errorf("project root is required to resolve the screenshots directory")
	}

	// Create <projectRoot>/.agnt/audit/screenshots directory path
	screenshotDir := filepath.Join(projectRoot, ".agnt", "audit", ScreenshotDirName)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(screenshotDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create screenshots directory: %w", err)
	}

	return screenshotDir, nil
}

// generateScreenshotPath creates a unique filename for a screenshot beneath the
// project root the session was started for.
func generateScreenshotPath(session *AutomationSession, prefix, label, viewport string) (string, error) {
	projectRoot := session.Path()
	if projectRoot == "" {
		return "", fmt.Errorf("session %q has no project path; cannot resolve screenshot directory", session.ID())
	}

	dir, err := GetScreenshotDir(projectRoot)
	if err != nil {
		return "", err
	}

	timestamp := time.Now().Format("20060102-150405")
	parts := []string{prefix}

	if label != "" {
		parts = append(parts, sanitizeFilename(label))
	}
	if viewport != "" {
		parts = append(parts, viewport)
	}
	parts = append(parts, timestamp)

	return reserveScreenshotPath(dir, strings.Join(parts, "-")), nil
}

// issuedPaths remembers the screenshot paths handed out during the current
// clock second. The timestamp in a filename has one-second resolution, so two
// captures with the same prefix, label and viewport inside one second used to
// get the same path and the second silently overwrote the first. Only the
// current second needs remembering: an older stem can never be generated
// again.
var issuedPaths = struct {
	sync.Mutex
	second string
	paths  map[string]struct{}
}{paths: map[string]struct{}{}}

// reserveScreenshotPath returns dir/stem.png, or dir/stem-2.png, -3, ... when
// that name was already issued this second or exists on disk. The reservation
// is in-process (the daemon is the only writer of this directory); the disk
// check covers a restart inside the same second.
func reserveScreenshotPath(dir, stem string) string {
	issuedPaths.Lock()
	defer issuedPaths.Unlock()

	second := time.Now().Format("20060102-150405")
	if issuedPaths.second != second {
		issuedPaths.second = second
		issuedPaths.paths = map[string]struct{}{}
	}

	for n := 1; ; n++ {
		name := stem
		if n > 1 {
			name = fmt.Sprintf("%s-%d", stem, n)
		}
		path := filepath.Join(dir, name+".png")
		if _, taken := issuedPaths.paths[path]; taken {
			continue
		}
		if _, err := os.Lstat(path); err == nil {
			continue
		}
		issuedPaths.paths[path] = struct{}{}
		return path
	}
}

// applyViewport applies vp's emulation for one capture and returns the
// function that removes it again. A nil vp applies nothing and restores
// nothing. Device metrics and touch emulation are browser state that outlives
// the command that set them, so a capture that did not put them back left the
// whole session — every later navigate, evaluate and screenshot — emulating
// the last preset used.
func applyViewport(session *AutomationSession, vp *ViewportPreset) (restore func() error, err error) {
	if vp == nil {
		return func() error { return nil }, nil
	}
	restore = func() error {
		if err := session.Run(ResetEmulationActions()...); err != nil {
			return fmt.Errorf("failed to restore viewport after capture: %w", err)
		}
		return nil
	}
	if err := session.Run(vp.ToEmulateActions()...); err != nil {
		// A partial apply (metrics set, touch failed) must not linger either.
		_ = restore()
		return nil, fmt.Errorf("failed to set viewport: %w", err)
	}
	return restore, nil
}

// finishCapture runs restore and folds its failure into the capture's result:
// a screenshot that was taken but left the session emulated is reported as an
// error, because every later command on that session would be affected.
func finishCapture(restore func() error, result **ScreenshotResult, err *error) {
	if rerr := restore(); rerr != nil && *err == nil {
		*result = nil
		*err = rerr
	}
}

// sanitizeFilename replaces characters that are not safe for filenames.
func sanitizeFilename(name string) string {
	return pathutil.SafePathComponent(name)
}

// CaptureViewport captures a screenshot of the current viewport.
// If opts.Viewport is set, it first applies the viewport emulation.
func CaptureViewport(session *AutomationSession, opts ScreenshotOptions) (result *ScreenshotResult, err error) {
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if session.State() != StateRunning {
		return nil, fmt.Errorf("session not running (state: %s)", session.State())
	}

	var viewportName string
	var width, height int64

	// Apply viewport emulation if specified, for this capture only.
	if opts.Viewport != nil {
		viewportName = opts.Viewport.Name
		width = opts.Viewport.Width
		height = opts.Viewport.Height
	}
	restore, err := applyViewport(session, opts.Viewport)
	if err != nil {
		return nil, err
	}
	defer finishCapture(restore, &result, &err)

	// Generate file path
	filePath, err := generateScreenshotPath(session, "viewport", opts.Label, viewportName)
	if err != nil {
		return nil, fmt.Errorf("failed to generate screenshot path: %w", err)
	}

	// Capture screenshot
	var buf []byte
	if err := session.Run(chromedp.CaptureScreenshot(&buf)); err != nil {
		return nil, fmt.Errorf("failed to capture screenshot: %w", err)
	}

	// Write to file
	if err := os.WriteFile(filePath, buf, 0644); err != nil {
		return nil, fmt.Errorf("failed to write screenshot: %w", err)
	}

	return &ScreenshotResult{
		Path:      filePath,
		Filename:  filepath.Base(filePath),
		Width:     width,
		Height:    height,
		Viewport:  viewportName,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// CaptureFullPage captures a full-page screenshot (scrolls to capture entire page).
// If opts.Viewport is set, it first applies the viewport emulation.
func CaptureFullPage(session *AutomationSession, opts ScreenshotOptions) (result *ScreenshotResult, err error) {
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if session.State() != StateRunning {
		return nil, fmt.Errorf("session not running (state: %s)", session.State())
	}

	var viewportName string
	var width, height int64

	// Apply viewport emulation if specified, for this capture only.
	if opts.Viewport != nil {
		viewportName = opts.Viewport.Name
		width = opts.Viewport.Width
		height = opts.Viewport.Height
	}
	restore, err := applyViewport(session, opts.Viewport)
	if err != nil {
		return nil, err
	}
	defer finishCapture(restore, &result, &err)

	// Generate file path
	filePath, err := generateScreenshotPath(session, "fullpage", opts.Label, viewportName)
	if err != nil {
		return nil, fmt.Errorf("failed to generate screenshot path: %w", err)
	}

	// Capture full page screenshot
	var buf []byte
	if err := session.Run(chromedp.FullScreenshot(&buf, 100)); err != nil {
		return nil, fmt.Errorf("failed to capture full page screenshot: %w", err)
	}

	// Write to file
	if err := os.WriteFile(filePath, buf, 0644); err != nil {
		return nil, fmt.Errorf("failed to write screenshot: %w", err)
	}

	return &ScreenshotResult{
		Path:      filePath,
		Filename:  filepath.Base(filePath),
		Width:     width,
		Height:    height,
		Viewport:  viewportName,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// CaptureElement captures a screenshot of a specific element by CSS selector.
func CaptureElement(session *AutomationSession, selector string, opts ScreenshotOptions) (result *ScreenshotResult, err error) {
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if session.State() != StateRunning {
		return nil, fmt.Errorf("session not running (state: %s)", session.State())
	}
	if selector == "" {
		return nil, fmt.Errorf("selector is required")
	}

	var viewportName string

	// Apply viewport emulation if specified, for this capture only.
	if opts.Viewport != nil {
		viewportName = opts.Viewport.Name
	}
	restore, err := applyViewport(session, opts.Viewport)
	if err != nil {
		return nil, err
	}
	defer finishCapture(restore, &result, &err)

	// Generate file path - include sanitized selector in label
	label := opts.Label
	if label == "" {
		label = sanitizeFilename(selector)
	}
	filePath, err := generateScreenshotPath(session, "element", label, viewportName)
	if err != nil {
		return nil, fmt.Errorf("failed to generate screenshot path: %w", err)
	}

	// Capture element screenshot
	var buf []byte
	if err := session.Run(chromedp.Screenshot(selector, &buf, chromedp.NodeVisible)); err != nil {
		return nil, fmt.Errorf("failed to capture element screenshot: %w", err)
	}

	// Write to file
	if err := os.WriteFile(filePath, buf, 0644); err != nil {
		return nil, fmt.Errorf("failed to write screenshot: %w", err)
	}

	return &ScreenshotResult{
		Path:      filePath,
		Filename:  filepath.Base(filePath),
		Viewport:  viewportName,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// CaptureWithClip captures a screenshot of a specific region defined by clip bounds.
func CaptureWithClip(session *AutomationSession, x, y, width, height float64, opts ScreenshotOptions) (result *ScreenshotResult, err error) {
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if session.State() != StateRunning {
		return nil, fmt.Errorf("session not running (state: %s)", session.State())
	}

	var viewportName string

	// Apply viewport emulation if specified, for this capture only.
	if opts.Viewport != nil {
		viewportName = opts.Viewport.Name
	}
	restore, err := applyViewport(session, opts.Viewport)
	if err != nil {
		return nil, err
	}
	defer finishCapture(restore, &result, &err)

	// Generate file path
	filePath, err := generateScreenshotPath(session, "clip", opts.Label, viewportName)
	if err != nil {
		return nil, fmt.Errorf("failed to generate screenshot path: %w", err)
	}

	// Capture clipped screenshot using page.CaptureScreenshot action
	scale := opts.Scale
	if scale <= 0 {
		scale = 1.0
	}

	var buf []byte
	captureAction := chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		buf, err = page.CaptureScreenshot().
			WithClip(&page.Viewport{
				X:      x,
				Y:      y,
				Width:  width,
				Height: height,
				Scale:  scale,
			}).
			Do(ctx)
		return err
	})

	if err := session.Run(captureAction); err != nil {
		return nil, fmt.Errorf("failed to capture clipped screenshot: %w", err)
	}

	// Write to file
	if err := os.WriteFile(filePath, buf, 0644); err != nil {
		return nil, fmt.Errorf("failed to write screenshot: %w", err)
	}

	return &ScreenshotResult{
		Path:      filePath,
		Filename:  filepath.Base(filePath),
		Width:     int64(width),
		Height:    int64(height),
		Viewport:  viewportName,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// CaptureAllViewports captures screenshots at all default viewport sizes.
// Returns a slice of results for each viewport.
func CaptureAllViewports(session *AutomationSession, label string, fullPage bool) ([]*ScreenshotResult, error) {
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if session.State() != StateRunning {
		return nil, fmt.Errorf("session not running (state: %s)", session.State())
	}

	results := make([]*ScreenshotResult, 0, len(DefaultViewports))

	for _, viewport := range DefaultViewports {
		opts := ScreenshotOptions{
			Label:    label,
			Viewport: &viewport,
		}

		var result *ScreenshotResult
		var err error

		if fullPage {
			result, err = CaptureFullPage(session, opts)
		} else {
			result, err = CaptureViewport(session, opts)
		}

		if err != nil {
			return results, fmt.Errorf("failed to capture %s: %w", viewport.Name, err)
		}

		results = append(results, result)
	}

	return results, nil
}
