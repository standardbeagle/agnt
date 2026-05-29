package browser

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// MinMajorVersion is the minimum recommended Chrome major version.
// Chrome 120+ is required for modern DevTools protocol features.
const MinMajorVersion = 120

// VersionInfo holds Chrome version details.
type VersionInfo struct {
	Path         string // Binary path
	FullVersion  string // e.g. "146.0.7680.164"
	MajorVersion int    // e.g. 146
	IsOutdated   bool   // Below MinMajorVersion
}

// CheckVersion finds Chrome and checks its version.
// Returns nil VersionInfo if Chrome is not found.
func CheckVersion() *VersionInfo {
	d := DefaultDiscoverer()
	path := d.Find()
	if path == "" {
		return nil
	}
	return CheckVersionAt(path)
}

// CheckVersionAt checks the version of a specific Chrome binary.
func CheckVersionAt(path string) *VersionInfo {
	info := &VersionInfo{Path: path}

	// On Windows, `chrome.exe --version` opens a browser window.
	// Use --headless=new --disable-gpu to prevent GUI, or try registry.
	out, err := exec.Command(path, "--headless=new", "--disable-gpu", "--version").Output()
	if err != nil {
		// Fallback: try reading version from file properties on Windows
		out, err = exec.Command(path, "--product-version").Output()
		if err != nil {
			return info
		}
	}

	info = parseVersionOutput(string(out))
	info.Path = path
	return info
}

// parseVersionOutput parses the raw output of `chrome --version` into a
// VersionInfo. It accepts "Google Chrome 146.0.7680.164", "Chromium
// 120.0.6099.0", or a bare "146.0.7680.164": the version number is taken from
// the last whitespace-separated field, the major version is the integer before
// the first dot, and IsOutdated is set when the major is below MinMajorVersion.
// Empty or garbage input yields a zero-value VersionInfo (no panic).
func parseVersionOutput(raw string) *VersionInfo {
	info := &VersionInfo{}

	// Parse "Google Chrome 146.0.7680.164" or "Chromium 120.0.6099.0" or just "146.0.7680.164"
	version := strings.TrimSpace(raw)
	parts := strings.Fields(version)
	if len(parts) == 0 {
		return info
	}

	// Version number is the last field
	verStr := parts[len(parts)-1]
	info.FullVersion = verStr

	// Extract major version
	if dot := strings.Index(verStr, "."); dot > 0 {
		if major, err := strconv.Atoi(verStr[:dot]); err == nil {
			info.MajorVersion = major
			info.IsOutdated = major < MinMajorVersion
		}
	}

	return info
}

// UpdateInstructions returns platform-specific instructions for updating Chrome.
func UpdateInstructions() string {
	return `Update Chrome:
  Linux:   sudo apt update && sudo apt install -y google-chrome-stable
  macOS:   brew install --cask google-chrome  (or update via Chrome menu)
  Windows: winget upgrade Google.Chrome  (or update via Chrome settings)

Or install Playwright Chromium (auto-managed):
  cd e2e && npm install  (installs latest Chromium via postinstall hook)`
}

// FormatWarning returns a warning message if Chrome is outdated.
func FormatWarning(info *VersionInfo) string {
	if info == nil {
		return fmt.Sprintf("Chrome not found. Install Chrome %d+ for browser diagnostics.\n\n%s", MinMajorVersion, UpdateInstructions())
	}
	if !info.IsOutdated {
		return ""
	}
	return fmt.Sprintf("Chrome %s (v%d) is outdated. Minimum recommended: v%d.\n\n%s",
		info.FullVersion, info.MajorVersion, MinMajorVersion, UpdateInstructions())
}
