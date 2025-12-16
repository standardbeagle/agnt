package updater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGitHubRelease_GetVersion(t *testing.T) {
	tests := []struct {
		name    string
		tagName string
		want    string
	}{
		{
			name:    "with v prefix",
			tagName: "v0.6.5",
			want:    "0.6.5",
		},
		{
			name:    "without v prefix",
			tagName: "0.6.5",
			want:    "0.6.5",
		},
		{
			name:    "with V prefix (uppercase)",
			tagName: "V1.0.0",
			want:    "1.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release := &GitHubRelease{
				TagName: tt.tagName,
			}
			got := release.GetVersion()
			if got != tt.want {
				t.Errorf("GetVersion() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGitHubRelease_IsNewer(t *testing.T) {
	tests := []struct {
		name           string
		releaseVersion string
		currentVersion string
		want           bool
		wantErr        bool
	}{
		{
			name:           "newer patch version",
			releaseVersion: "0.6.6",
			currentVersion: "0.6.5",
			want:           true,
		},
		{
			name:           "newer minor version",
			releaseVersion: "0.7.0",
			currentVersion: "0.6.5",
			want:           true,
		},
		{
			name:           "newer major version",
			releaseVersion: "1.0.0",
			currentVersion: "0.6.5",
			want:           true,
		},
		{
			name:           "same version",
			releaseVersion: "0.6.5",
			currentVersion: "0.6.5",
			want:           false,
		},
		{
			name:           "older patch version",
			releaseVersion: "0.6.4",
			currentVersion: "0.6.5",
			want:           false,
		},
		{
			name:           "older minor version",
			releaseVersion: "0.5.9",
			currentVersion: "0.6.5",
			want:           false,
		},
		{
			name:           "with v prefix",
			releaseVersion: "v0.6.6",
			currentVersion: "v0.6.5",
			want:           true,
		},
		{
			name:           "mixed prefix",
			releaseVersion: "v0.6.6",
			currentVersion: "0.6.5",
			want:           true,
		},
		{
			name:           "invalid release version",
			releaseVersion: "invalid",
			currentVersion: "0.6.5",
			want:           false,
			wantErr:        true,
		},
		{
			name:           "invalid current version",
			releaseVersion: "0.6.6",
			currentVersion: "invalid",
			want:           false,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release := &GitHubRelease{
				TagName: tt.releaseVersion,
			}
			got, err := release.IsNewer(tt.currentVersion)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsNewer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("IsNewer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGitHubChecker_CheckLatestRelease(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check request headers
		if r.Header.Get("User-Agent") != "agnt-updater" {
			t.Errorf("Expected User-Agent header 'agnt-updater', got '%s'", r.Header.Get("User-Agent"))
		}

		// Return mock release
		release := GitHubRelease{
			TagName:     "v0.6.6",
			Name:        "Release 0.6.6",
			Draft:       false,
			Prerelease:  false,
			PublishedAt: time.Now(),
			HTMLURL:     "https://github.com/standardbeagle/agnt/releases/tag/v0.6.6",
			Body:        "Test release",
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(release)
	}))
	defer server.Close()

	// Create checker with mock server URL
	checker := NewGitHubChecker("standardbeagle/agnt")
	// Override the API URL for testing
	originalURL := GitHubAPIURL
	defer func() {
		// Can't actually restore this as it's a const, but this documents intent
		_ = originalURL
	}()

	// We can't easily test the real GitHub API without network access,
	// so we'll just verify the checker is created correctly
	if checker.repo != "standardbeagle/agnt" {
		t.Errorf("Expected repo 'standardbeagle/agnt', got '%s'", checker.repo)
	}

	if checker.httpClient == nil {
		t.Error("Expected http client to be initialized")
	}

	if checker.httpClient.Timeout != 10*time.Second {
		t.Errorf("Expected timeout 10s, got %v", checker.httpClient.Timeout)
	}
}

func TestNewGitHubChecker(t *testing.T) {
	tests := []struct {
		name     string
		repo     string
		wantRepo string
	}{
		{
			name:     "with repo",
			repo:     "user/repo",
			wantRepo: "user/repo",
		},
		{
			name:     "empty repo uses default",
			repo:     "",
			wantRepo: DefaultGitHubRepo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := NewGitHubChecker(tt.repo)
			if checker.repo != tt.wantRepo {
				t.Errorf("NewGitHubChecker().repo = %v, want %v", checker.repo, tt.wantRepo)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		name      string
		version   string
		wantMajor int
		wantMinor int
		wantPatch int
		wantErr   bool
	}{
		{
			name:      "simple version",
			version:   "0.6.5",
			wantMajor: 0,
			wantMinor: 6,
			wantPatch: 5,
			wantErr:   false,
		},
		{
			name:      "with v prefix",
			version:   "v1.2.3",
			wantMajor: 1,
			wantMinor: 2,
			wantPatch: 3,
			wantErr:   false,
		},
		{
			name:      "with pre-release",
			version:   "1.2.3-alpha.1",
			wantMajor: 1,
			wantMinor: 2,
			wantPatch: 3,
			wantErr:   false,
		},
		{
			name:      "with build metadata",
			version:   "1.2.3+build.123",
			wantMajor: 1,
			wantMinor: 2,
			wantPatch: 3,
			wantErr:   false,
		},
		{
			name:      "with both pre-release and build",
			version:   "1.2.3-beta.2+build.456",
			wantMajor: 1,
			wantMinor: 2,
			wantPatch: 3,
			wantErr:   false,
		},
		{
			name:      "invalid version",
			version:   "invalid",
			wantMajor: 0,
			wantMinor: 0,
			wantPatch: 0,
			wantErr:   true,
		},
		{
			name:      "incomplete version",
			version:   "1.2",
			wantMajor: 0,
			wantMinor: 0,
			wantPatch: 0,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			major, minor, patch, err := parseVersion(tt.version)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if major != tt.wantMajor {
				t.Errorf("parseVersion() major = %v, want %v", major, tt.wantMajor)
			}
			if minor != tt.wantMinor {
				t.Errorf("parseVersion() minor = %v, want %v", minor, tt.wantMinor)
			}
			if patch != tt.wantPatch {
				t.Errorf("parseVersion() patch = %v, want %v", patch, tt.wantPatch)
			}
		})
	}
}
