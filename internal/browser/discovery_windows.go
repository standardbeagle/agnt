//go:build windows

package browser

import (
	"os"
	"path/filepath"
)

// platformDiscoverer implements Discoverer for Windows.
type platformDiscoverer struct{}

// Find returns the path to Chrome on Windows.
func (d *platformDiscoverer) Find() string {
	// Try PROGRAMFILES paths
	programFiles := os.Getenv("PROGRAMFILES")
	if programFiles != "" {
		path := filepath.Join(programFiles, "Google", "Chrome", "Application", "chrome.exe")
		if fileExists(path) {
			return path
		}
	}

	// Try PROGRAMFILES(X86) for 32-bit Chrome on 64-bit Windows
	programFilesX86 := os.Getenv("PROGRAMFILES(X86)")
	if programFilesX86 != "" {
		path := filepath.Join(programFilesX86, "Google", "Chrome", "Application", "chrome.exe")
		if fileExists(path) {
			return path
		}
	}

	// Try LOCALAPPDATA (user-specific install)
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData != "" {
		path := filepath.Join(localAppData, "Google", "Chrome", "Application", "chrome.exe")
		if fileExists(path) {
			return path
		}
	}

	// Try common default paths
	paths := []string{
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	}
	if found := findFirst(paths); found != "" {
		return found
	}

	// Try PATH lookup
	if path := findInPath("chrome.exe"); path != "" {
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
