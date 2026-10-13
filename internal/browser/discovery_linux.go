//go:build linux

package browser

import (
	"os"
	"path/filepath"
)

// platformDiscoverer implements Discoverer for Linux.
type platformDiscoverer struct{}

// Find returns the path to Chrome/Chromium on Linux.
func (d *platformDiscoverer) Find() string {
	// Common Linux paths for Chrome/Chromium
	paths := []string{
		"/usr/bin/google-chrome-stable",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium-browser",
		"/usr/bin/chromium",
		"/snap/bin/chromium",
		"/opt/google/chrome/chrome",
	}

	if found := findFirst(paths); found != "" {
		return found
	}

	// Try PATH lookup
	if path := findInPath("google-chrome-stable"); path != "" {
		return path
	}
	if path := findInPath("google-chrome"); path != "" {
		return path
	}
	if path := findInPath("chromium"); path != "" {
		return path
	}
	if path := findInPath("chromium-browser"); path != "" {
		return path
	}

	// Try Playwright-managed Chromium (kept current via npm install)
	if path := findPlaywrightChromium(); path != "" {
		return path
	}

	return ""
}

// findPlaywrightChromium finds the latest Playwright-managed Chromium binary.
func findPlaywrightChromium() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return newestPlaywrightChromium(
		filepath.Join(home, ".cache", "ms-playwright", "chromium-*", "chrome-linux*", "chrome"),
	)
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
