// Package browser provides Chrome browser management for automation.
package browser

import (
	"os/exec"
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

// findFirst returns the first existing binary from the paths list.
func findFirst(paths []string) string {
	for _, path := range paths {
		if fileExists(path) {
			return path
		}
	}
	return ""
}
