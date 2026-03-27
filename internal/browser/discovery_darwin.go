//go:build darwin

package browser

import (
	"os"
	"path/filepath"
	"sort"
)

// platformDiscoverer implements Discoverer for macOS.
type platformDiscoverer struct{}

// Find returns the path to Chrome/Chromium on macOS.
func (d *platformDiscoverer) Find() string {
	// Common macOS paths for Chrome/Chromium
	paths := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
	}

	if found := findFirst(paths); found != "" {
		return found
	}

	// Try user-specific Applications folder
	home := os.Getenv("HOME")
	if home != "" {
		userPaths := []string{
			home + "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			home + "/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
		if found := findFirst(userPaths); found != "" {
			return found
		}
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
	pattern := filepath.Join(home, "Library", "Caches", "ms-playwright", "chromium-*", "chrome-mac", "Chromium.app", "Contents", "MacOS", "Chromium")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
