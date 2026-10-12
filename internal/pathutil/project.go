package pathutil

import (
	"path/filepath"
	"runtime"
	"strings"
)

// NormalizeProjectPath returns the canonical form of a project directory, the
// form every project-keyed lookup compares: absolute, cleaned, and lowercased
// on Windows, whose filesystem is case-insensitive. An empty path or "."
// stays ".".
//
// The daemon (session, process and proxy registries) and the MCP tools (the
// project filter they send) must agree on this form exactly, or a project
// silently stops matching its own resources. Both call this one function
// rather than each carrying a copy.
func NormalizeProjectPath(path string) string {
	if path == "" || path == "." {
		return "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs
}
