package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	httpClient *http.Client
}

// NewGitHubChecker creates a new GitHub release checker
func NewGitHubChecker(repo string) *GitHubChecker {
	if repo == "" {
		repo = DefaultGitHubRepo
	}

	return &GitHubChecker{
		repo: repo,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// CheckLatestRelease fetches the latest release from GitHub
func (g *GitHubChecker) CheckLatestRelease() (*GitHubRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", GitHubAPIURL, g.repo)

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
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github API returned status %d: %s", resp.StatusCode, string(body))
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &release, nil
}

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
	releaseVersion := g.GetVersion()
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

	return false, nil
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
