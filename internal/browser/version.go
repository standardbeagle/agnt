package browser

import (
	"fmt"
	"regexp"
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

// CheckVersionAt checks the version of a specific Chrome binary. How the
// version is read is platform-specific (readVersion): asking the binary is
// only safe where `--version` prints and exits.
func CheckVersionAt(path string) *VersionInfo {
	out, err := readVersion(path)
	if err != nil {
		return &VersionInfo{Path: path}
	}
	info := parseVersionOutput(out)
	info.Path = path
	return info
}

// versionNumber matches a dotted version standing as its own
// whitespace-delimited field: at least "major.minor", usually the four-part
// "146.0.7680.164". Requiring a whole field keeps a number embedded in some
// other token ("vX.0.1.2") from being read as a version.
var versionNumber = regexp.MustCompile(`(?:^|\s)(\d+(?:\.\d+)+)(?:\s|$)`)

// parseVersionOutput parses the raw output of `chrome --version` into a
// VersionInfo. The version is the first dotted-number field in the output,
// wherever it sits: "Google Chrome 146.0.7680.164", "Chromium 120.0.6099.0", a bare
// "146.0.7680.164", and the channel- or packaging-suffixed forms "Google
// Chrome 147.0.7700.3 beta" and "Chromium 120.0.6099.0 snap" all parse.
// (Taking the last whitespace-separated field instead read "beta" or "snap" as
// the version, so an outdated beta or snap build was never reported.) The
// major version is the integer before the first dot, and IsOutdated is set
// when it is below MinMajorVersion. Output with no dotted number keeps its
// last field as FullVersion with a zero major; empty input yields a zero-value
// VersionInfo. Never panics.
func parseVersionOutput(raw string) *VersionInfo {
	info := &VersionInfo{}

	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return info
	}

	match := versionNumber.FindStringSubmatch(raw)
	if match == nil {
		info.FullVersion = parts[len(parts)-1]
		return info
	}
	verStr := match[1]
	info.FullVersion = verStr

	if major, err := strconv.Atoi(verStr[:strings.Index(verStr, ".")]); err == nil {
		info.MajorVersion = major
		info.IsOutdated = major < MinMajorVersion
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
