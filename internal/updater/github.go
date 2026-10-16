package updater

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultGitHubRepo is the default repository for agnt
	DefaultGitHubRepo = "standardbeagle/agnt"

	// GitHubAPIURL is the base URL for GitHub API
	GitHubAPIURL = "https://api.github.com"
)

// GitHubRelease represents a GitHub release
type GitHubRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
}

// GitHubChecker checks for updates from GitHub releases
type GitHubChecker struct {
	repo       string
	baseURL    string
	httpClient *http.Client
}

// NewGitHubChecker creates a new GitHub release checker
func NewGitHubChecker(repo string) *GitHubChecker {
	if repo == "" {
		repo = DefaultGitHubRepo
	}

	return &GitHubChecker{
		repo:    repo,
		baseURL: GitHubAPIURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// CheckLatestRelease fetches the latest release from GitHub
func (g *GitHubChecker) CheckLatestRelease() (*GitHubRelease, error) {
	base := g.baseURL
	if base == "" {
		base = GitHubAPIURL
	}
	url := fmt.Sprintf("%s/repos/%s/releases/latest", base, g.repo)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set User-Agent header (GitHub API requires it)
	req.Header.Set("User-Agent", "agnt-updater")
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil, fmt.Errorf("github API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if len(body) > maxReleaseBytes {
		return nil, fmt.Errorf("release response is larger than %d bytes", maxReleaseBytes)
	}
	var release GitHubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &release, nil
}

// Response bounds. The release (its notes included) is cached by the daemon
// and handed to every client in INFO, and the error body is quoted in the
// check error, so neither may be as large as the server cares to send. A
// releases/latest document is a few tens of KiB.
const (
	maxReleaseBytes   = 1 << 20
	maxErrorBodyBytes = 4 << 10
)

// GetVersion extracts version from release tag
// Handles tags like "v0.6.5", "V0.6.5", or "0.6.5"
func (g *GitHubRelease) GetVersion() string {
	version := g.TagName
	if strings.HasPrefix(strings.ToLower(version), "v") {
		return version[1:]
	}
	return version
}

// IsNewer checks if this release is newer than the given version
func (g *GitHubRelease) IsNewer(currentVersion string) (bool, error) {
	return IsNewerVersion(g.GetVersion(), currentVersion)
}

// IsNewerVersion reports whether releaseVersion ranks strictly above
// currentVersion in SemVer order, prereleases included. The daemon's update
// notice and `agnt upgrade` both decide through it, so they cannot disagree.
func IsNewerVersion(releaseVersion, currentVersion string) (bool, error) {
	releaseVersion = strings.TrimPrefix(strings.TrimPrefix(releaseVersion, "V"), "v")
	currentVersion = strings.TrimPrefix(currentVersion, "v")

	// Parse versions
	relMajor, relMinor, relPatch, err := parseVersion(releaseVersion)
	if err != nil {
		return false, fmt.Errorf("failed to parse release version %s: %w", releaseVersion, err)
	}

	curMajor, curMinor, curPatch, err := parseVersion(currentVersion)
	if err != nil {
		return false, fmt.Errorf("failed to parse current version %s: %w", currentVersion, err)
	}

	// Compare versions
	if relMajor > curMajor {
		return true, nil
	}
	if relMajor < curMajor {
		return false, nil
	}

	if relMinor > curMinor {
		return true, nil
	}
	if relMinor < curMinor {
		return false, nil
	}

	if relPatch > curPatch {
		return true, nil
	}
	if relPatch < curPatch {
		return false, nil
	}

	// Same MAJOR.MINOR.PATCH: a version ranks above its own prereleases
	// (SemVer §11), so 1.0.0 is an update for someone on 1.0.0-rc.1.
	return comparePrerelease(prerelease(releaseVersion), prerelease(currentVersion)) > 0, nil
}

// prerelease returns the prerelease part of a version ("rc.1" for
// "v1.0.0-rc.1+build"), or "" for a release.
func prerelease(version string) string {
	version = strings.TrimPrefix(version, "v")
	if i := strings.IndexByte(version, '+'); i >= 0 {
		version = version[:i]
	}
	if i := strings.IndexByte(version, '-'); i > 0 {
		return version[i+1:]
	}
	return ""
}

// comparePrerelease orders two prerelease strings by SemVer §11: no
// prerelease ranks highest; dot-separated identifiers compare numerically
// when both are numeric, a numeric one ranks below an alphanumeric one,
// others compare in ASCII order; with an equal prefix the longer list ranks
// higher.
func comparePrerelease(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aErr := strconv.Atoi(as[i])
		bn, bErr := strconv.Atoi(bs[i])
		switch {
		case aErr == nil && bErr == nil:
			if an != bn {
				return cmp.Compare(an, bn)
			}
		case aErr == nil:
			return -1
		case bErr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return cmp.Compare(len(as), len(bs))
}

// ParseVersion parses a semantic version string into its major/minor/patch
// components. Exported so other packages needing version comparisons (e.g.
// internal/sshclient's compatibility-window check) reuse this parser rather
// than duplicating semver parsing.
func ParseVersion(version string) (major, minor, patch int, err error) {
	return parseVersion(version)
}

// parseVersion parses a semantic version string
func parseVersion(version string) (major, minor, patch int, err error) {
	version = strings.TrimPrefix(version, "v")

	// Remove pre-release and build metadata
	if idx := strings.IndexAny(version, "-+"); idx > 0 {
		version = version[:idx]
	}

	n, err := fmt.Sscanf(version, "%d.%d.%d", &major, &minor, &patch)
	if err != nil || n != 3 {
		return 0, 0, 0, fmt.Errorf("invalid version format: %s", version)
	}

	return major, minor, patch, nil
}
