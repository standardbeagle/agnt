//go:build linux

package browser

import "os"

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
	if path := findInPath("google-chrome"); path != "" {
		return path
	}
	if path := findInPath("chromium"); path != "" {
		return path
	}
	if path := findInPath("chromium-browser"); path != "" {
		return path
	}

	return ""
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
