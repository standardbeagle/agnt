package chromedp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	Viewport *ViewportPreset // Viewport preset to use (applies emulation)
	Quality  int             // JPEG quality (0-100, 0 = PNG)
	Scale    float64         // Scale factor for capture (default 1.0)
}

// GetScreenshotDir returns the screenshots directory path within .agnt/audit/.
// Creates the directory if it doesn't exist.
func GetScreenshotDir() (string, error) {
	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get working directory: %w", err)
	}

	// Create .agnt/audit/screenshots directory path
	screenshotDir := filepath.Join(cwd, ".agnt", "audit", ScreenshotDirName)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(screenshotDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create screenshots directory: %w", err)
	}

	return screenshotDir, nil
}

// generateScreenshotPath creates a unique filename for a screenshot.
func generateScreenshotPath(prefix, label, viewport string) (string, error) {
	dir, err := GetScreenshotDir()
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

	filename := strings.Join(parts, "-") + ".png"
	return filepath.Join(dir, filename), nil
}

// sanitizeFilename replaces characters that are not safe for filenames.
func sanitizeFilename(name string) string {
	return pathutil.SafePathComponent(name)
}

// CaptureViewport captures a screenshot of the current viewport.
// If opts.Viewport is set, it first applies the viewport emulation.
func CaptureViewport(session *AutomationSession, opts ScreenshotOptions) (*ScreenshotResult, error) {
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if session.State() != StateRunning {
		return nil, fmt.Errorf("session not running (state: %s)", session.State())
	}

	var viewportName string
	var width, height int64

	// Apply viewport emulation if specified
	if opts.Viewport != nil {
		viewportName = opts.Viewport.Name
		width = opts.Viewport.Width
		height = opts.Viewport.Height

		if err := session.Run(opts.Viewport.ToEmulateActions()...); err != nil {
			return nil, fmt.Errorf("failed to set viewport: %w", err)
		}
	}

	// Generate file path
	filePath, err := generateScreenshotPath("viewport", opts.Label, viewportName)
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
func CaptureFullPage(session *AutomationSession, opts ScreenshotOptions) (*ScreenshotResult, error) {
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if session.State() != StateRunning {
		return nil, fmt.Errorf("session not running (state: %s)", session.State())
	}

	var viewportName string
	var width, height int64

	// Apply viewport emulation if specified
	if opts.Viewport != nil {
		viewportName = opts.Viewport.Name
		width = opts.Viewport.Width
		height = opts.Viewport.Height

		if err := session.Run(opts.Viewport.ToEmulateActions()...); err != nil {
			return nil, fmt.Errorf("failed to set viewport: %w", err)
		}
	}

	// Generate file path
	filePath, err := generateScreenshotPath("fullpage", opts.Label, viewportName)
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
func CaptureElement(session *AutomationSession, selector string, opts ScreenshotOptions) (*ScreenshotResult, error) {
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

	// Apply viewport emulation if specified
	if opts.Viewport != nil {
		viewportName = opts.Viewport.Name

		if err := session.Run(opts.Viewport.ToEmulateActions()...); err != nil {
			return nil, fmt.Errorf("failed to set viewport: %w", err)
		}
	}

	// Generate file path - include sanitized selector in label
	label := opts.Label
	if label == "" {
		label = sanitizeFilename(selector)
	}
	filePath, err := generateScreenshotPath("element", label, viewportName)
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
func CaptureWithClip(session *AutomationSession, x, y, width, height float64, opts ScreenshotOptions) (*ScreenshotResult, error) {
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if session.State() != StateRunning {
		return nil, fmt.Errorf("session not running (state: %s)", session.State())
	}

	var viewportName string

	// Apply viewport emulation if specified
	if opts.Viewport != nil {
		viewportName = opts.Viewport.Name

		if err := session.Run(opts.Viewport.ToEmulateActions()...); err != nil {
			return nil, fmt.Errorf("failed to set viewport: %w", err)
		}
	}

	// Generate file path
	filePath, err := generateScreenshotPath("clip", opts.Label, viewportName)
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
