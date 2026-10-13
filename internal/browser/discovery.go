// Package browser provides Chrome browser management for automation.
package browser

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
)

// Discoverer finds Chrome/Chromium browser binaries on the system.
type Discoverer interface {
	// Find returns the path to a Chrome/Chromium binary, or empty string if not found.
	Find() string
}

// DefaultDiscoverer returns the platform-specific discoverer.
func DefaultDiscoverer() Discoverer {
	return &platformDiscoverer{}
}

// findInPath looks for a binary in PATH.
func findInPath(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

// playwrightRevision matches the revision in a Playwright browser directory
// name such as "chromium-1208".
var playwrightRevision = regexp.MustCompile(`chromium-(\d+)`)

// newestPlaywrightChromium returns the binary from the highest-numbered
// Playwright Chromium revision matched by any of the glob patterns, or "".
//
// Revisions are compared as numbers. Sorting the paths as strings — what this
// used to do — ranks "chromium-999" above "chromium-1208", so the oldest
// install won as soon as revisions crossed a digit boundary.
//
// Each pattern globs the platform directory ("chrome-win*", "chrome-mac*",
// "chrome-linux*") rather than naming it: Playwright has renamed those
// directories between releases (chrome-win → chrome-win64, chrome-linux →
// chrome-linux64, chrome-mac → chrome-mac-arm64 / chrome-mac-x64), and a
// hard-coded name silently stops matching after an upgrade.
func newestPlaywrightChromium(patterns ...string) string {
	best, bestRev := "", -1
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if !fileExists(m) {
				continue
			}
			rev := 0
			if sub := playwrightRevision.FindStringSubmatch(filepath.ToSlash(m)); sub != nil {
				rev, _ = strconv.Atoi(sub[1])
			}
			if rev > bestRev || (rev == bestRev && m > best) {
				best, bestRev = m, rev
			}
		}
	}
	return best
}

// findFirst returns the first existing binary from the paths list.
func findFirst(paths []string) string {
	for _, path := range paths {
		if fileExists(path) {
			return path
		}
	}
	return ""
}
