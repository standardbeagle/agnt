package updater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	t.Run("happy path decodes release and sends headers", func(t *testing.T) {
		var gotPath, gotUA, gotAccept string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotUA = r.Header.Get("User-Agent")
			gotAccept = r.Header.Get("Accept")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(GitHubRelease{
				TagName: "v0.6.6",
				Name:    "Release 0.6.6",
				HTMLURL: "https://github.com/standardbeagle/agnt/releases/tag/v0.6.6",
				Body:    "Test release",
			})
		}))
		defer server.Close()

		checker := NewGitHubChecker("standardbeagle/agnt")
		checker.baseURL = server.URL

		rel, err := checker.CheckLatestRelease()
		if err != nil {
			t.Fatalf("CheckLatestRelease() error = %v", err)
		}
		if rel.TagName != "v0.6.6" {
			t.Errorf("TagName = %q, want v0.6.6", rel.TagName)
		}
		if rel.Body != "Test release" {
			t.Errorf("Body = %q, want 'Test release'", rel.Body)
		}
		if gotPath != "/repos/standardbeagle/agnt/releases/latest" {
			t.Errorf("request path = %q", gotPath)
		}
		if gotUA != "agnt-updater" {
			t.Errorf("User-Agent = %q, want agnt-updater", gotUA)
		}
		if gotAccept != "application/vnd.github+json" {
			t.Errorf("Accept = %q", gotAccept)
		}
	})

	t.Run("non-200 returns error with status and body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
		}))
		defer server.Close()

		checker := NewGitHubChecker("x/y")
		checker.baseURL = server.URL
		_, err := checker.CheckLatestRelease()
		if err == nil {
			t.Fatal("expected error on 404")
		}
		if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "not found") {
			t.Errorf("error should mention status and body: %v", err)
		}
	})

	t.Run("malformed JSON returns decode error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("{not json"))
		}))
		defer server.Close()

		checker := NewGitHubChecker("x/y")
		checker.baseURL = server.URL
		_, err := checker.CheckLatestRelease()
		if err == nil || !strings.Contains(err.Error(), "decode") {
			t.Fatalf("expected decode error, got %v", err)
		}
	})

	t.Run("unreachable server returns fetch error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := server.URL
		server.Close() // close immediately so the address refuses connections

		checker := NewGitHubChecker("x/y")
		checker.baseURL = url
		_, err := checker.CheckLatestRelease()
		if err == nil || !strings.Contains(err.Error(), "fetch") {
			t.Fatalf("expected fetch error, got %v", err)
		}
	})
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
