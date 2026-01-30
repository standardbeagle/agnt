//go:build darwin

package browser

import "os"

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
