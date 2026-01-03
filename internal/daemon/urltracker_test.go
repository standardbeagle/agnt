package daemon

import (
	"testing"
)

func TestParseDevServerURLs(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "localhost URL",
			input:    "Server started at http://localhost:3000",
			expected: []string{"http://localhost:3000"},
		},
		{
			name:     "127.0.0.1 URL",
			input:    "Listening on http://127.0.0.1:8080/",
			expected: []string{"http://127.0.0.1:8080/"},
		},
		{
			name:     "localhost only from multiple URLs",
			input:    "  Local:   http://localhost:5173/\n  Network: http://192.168.1.10:5173/\n",
			expected: []string{"http://localhost:5173/"},
		},
		{
			name:     "URL with trailing punctuation",
			input:    "Available at http://localhost:3000.",
			expected: []string{"http://localhost:3000"},
		},
		{
			name:     "duplicate URLs deduplicated",
			input:    "http://localhost:3000 http://localhost:3000",
			expected: []string{"http://localhost:3000"},
		},
		{
			name:     "no URLs",
			input:    "Starting server...\nCompiling...\nDone.",
			expected: nil,
		},
		{
			name:     "ignores external URLs",
			input:    "Visit https://github.com/user/repo for docs",
			expected: nil,
		},
		{
			name:     "ignores URLs with query strings",
			input:    "http://localhost:3000/app?debug=true",
			expected: nil,
		},
		{
			name:     "ignores API paths",
			input:    "API running at http://localhost:3000/api/v1",
			expected: nil,
		},
		{
			name:     "keeps simple paths",
			input:    "App: http://localhost:3000/app",
			expected: []string{"http://localhost:3000/app"},
		},
		{
			name:     "vite dev server output - localhost only",
			input:    "  VITE v5.0.0  ready in 500 ms\n\n  ➜  Local:   http://localhost:5173/\n  ➜  Network: http://192.168.1.100:5173/\n",
			expected: []string{"http://localhost:5173/"},
		},
		{
			name:     "next.js dev server output",
			input:    "ready - started server on 0.0.0.0:3000, url: http://localhost:3000",
			expected: []string{"http://localhost:3000"},
		},
		{
			name:     "ignores 192.168.x.x network IPs",
			input:    "Network: http://192.168.1.100:3000",
			expected: nil,
		},
		{
			name:     "ignores 10.x.x.x network IPs",
			input:    "Network: http://10.255.255.254:3737",
			expected: nil,
		},
		{
			name:     "allows 0.0.0.0 binding",
			input:    "Listening on http://0.0.0.0:3000",
			expected: []string{"http://0.0.0.0:3000"},
		},
		{
			name:     "allows IPv6 localhost",
			input:    "Listening on http://[::1]:3000",
			expected: []string{"http://[::1]:3000"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDevServerURLs([]byte(tt.input))

			if len(got) != len(tt.expected) {
				t.Errorf("parseDevServerURLs() got %d URLs, want %d", len(got), len(tt.expected))
				t.Errorf("got: %v", got)
				t.Errorf("want: %v", tt.expected)
				return
			}

			for i, url := range got {
				if url != tt.expected[i] {
					t.Errorf("parseDevServerURLs()[%d] = %q, want %q", i, url, tt.expected[i])
				}
			}
		})
	}
}

func TestShouldIgnoreURL(t *testing.T) {
	tests := []struct {
		url      string
		expected bool
	}{
		{"http://localhost:3000", false},
		{"http://localhost:3000/", false},
		{"http://localhost:3000/app", false},
		{"http://localhost:3000/api/users", true},
		{"http://localhost:3000?debug=true", true},
		{"http://localhost:3000/error", true},
		{"http://localhost:3000/static/main.js", true},
		{"http://localhost:3000/favicon.ico", true},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := shouldIgnoreURL(tt.url)
			if got != tt.expected {
				t.Errorf("shouldIgnoreURL(%q) = %v, want %v", tt.url, got, tt.expected)
			}
		})
	}
}

func TestMatchesURLPattern(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		pattern  string
		expected bool
	}{
		{
			name:     "empty pattern matches URL",
			line:     "Server: http://localhost:3000",
			pattern:  "{url}",
			expected: true,
		},
		{
			name:     "empty pattern no URL",
			line:     "Starting server...",
			pattern:  "{url}",
			expected: false,
		},
		{
			name:     "local prefix pattern matches",
			line:     "Local: http://localhost:5173/",
			pattern:  "Local:\\s*{url}",
			expected: true,
		},
		{
			name:     "local prefix pattern no match",
			line:     "Network: http://localhost:5173/",
			pattern:  "Local:\\s*{url}",
			expected: false,
		},
		{
			name:     "or pattern matches local",
			line:     "Local: http://localhost:5173/",
			pattern:  "(Local|Network):\\s*{url}",
			expected: true,
		},
		{
			name:     "or pattern matches network",
			line:     "Network: http://192.168.1.10:5173/",
			pattern:  "(Local|Network):\\s*{url}",
			expected: true,
		},
		{
			name:     "or pattern no match",
			line:     "External: http://example.com/",
			pattern:  "(Local|Network):\\s*{url}",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesURLPattern(tt.line, tt.pattern)
			if got != tt.expected {
				t.Errorf("matchesURLPattern(%q, %q) = %v, want %v", tt.line, tt.pattern, got, tt.expected)
			}
		})
	}
}

func TestParseURLsFromBytes(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected []string
	}{
		{
			name:     "simple URL",
			input:    []byte("http://localhost:3000"),
			expected: []string{"http://localhost:3000"},
		},
		{
			name:     "localhost only from multiple URLs",
			input:    []byte("Local: http://localhost:3000\nNetwork: http://192.168.1.10:3000"),
			expected: []string{"http://localhost:3000"},
		},
		{
			name:     "no URLs",
			input:    []byte("No URLs here"),
			expected: nil,
		},
		{
			name:     "empty input",
			input:    []byte{},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseURLsFromBytes(tt.input)
			if len(got) != len(tt.expected) {
				t.Errorf("parseURLsFromBytes() got %d URLs, want %d", len(got), len(tt.expected))
				return
			}
			for i, url := range got {
				if url != tt.expected[i] {
					t.Errorf("parseURLsFromBytes()[%d] = %q, want %q", i, url, tt.expected[i])
				}
			}
		})
	}
}

func TestURLTracker_SetURLMatchers(t *testing.T) {
	config := DefaultURLTrackerConfig()
	tracker := NewURLTracker(nil, config)

	// Set matchers for a process
	tracker.SetURLMatchers("proc-1", []string{"Local:\\s*{url}", "{url}"})

	// Verify matchers were set (internal state)
	tracker.mu.RLock()
	matchers := tracker.urlMatchers["proc-1"]
	tracker.mu.RUnlock()

	if len(matchers) != 2 {
		t.Errorf("Expected 2 matchers, got %d", len(matchers))
	}

	// Clear matchers by setting empty slice
	tracker.SetURLMatchers("proc-1", nil)

	tracker.mu.RLock()
	_, exists := tracker.urlMatchers["proc-1"]
	tracker.mu.RUnlock()

	if exists {
		t.Error("Expected matchers to be removed")
	}
}

func TestURLTracker_ClearProcess(t *testing.T) {
	config := DefaultURLTrackerConfig()
	tracker := NewURLTracker(nil, config)

	// Manually set some data
	tracker.mu.Lock()
	tracker.urls["proc-1"] = []string{"http://localhost:3000"}
	tracker.seenURLs["proc-1"] = map[string]bool{"http://localhost:3000": true}
	tracker.scannedBytes["proc-1"] = 1000
	tracker.mu.Unlock()

	// Clear the process
	tracker.ClearProcess("proc-1")

	// Verify all data was cleared
	tracker.mu.RLock()
	_, urlsExist := tracker.urls["proc-1"]
	_, seenExist := tracker.seenURLs["proc-1"]
	_, scannedExist := tracker.scannedBytes["proc-1"]
	tracker.mu.RUnlock()

	if urlsExist {
		t.Error("URLs should be cleared")
	}
	if seenExist {
		t.Error("Seen URLs should be cleared")
	}
	if scannedExist {
		t.Error("Scanned bytes should be cleared")
	}
}

func TestURLTracker_GetURLs(t *testing.T) {
	config := DefaultURLTrackerConfig()
	tracker := NewURLTracker(nil, config)

	// Initially no URLs
	urls := tracker.GetURLs("proc-1")
	if len(urls) != 0 {
		t.Errorf("Expected 0 URLs, got %d", len(urls))
	}

	// Add some URLs manually
	tracker.mu.Lock()
	tracker.urls["proc-1"] = []string{"http://localhost:3000", "http://localhost:4000"}
	tracker.mu.Unlock()

	// Get URLs should return a copy
	urls = tracker.GetURLs("proc-1")
	if len(urls) != 2 {
		t.Errorf("Expected 2 URLs, got %d", len(urls))
	}

	// Verify it's a copy (modifying shouldn't affect original)
	urls[0] = "modified"
	originalURLs := tracker.GetURLs("proc-1")
	if originalURLs[0] == "modified" {
		t.Error("GetURLs should return a copy, not the original slice")
	}
}

func TestParseDevServerURLsWithMatchers(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		matchers []string
		expected []string
	}{
		{
			name:     "with matching pattern",
			input:    []byte("Local: http://localhost:5173/"),
			matchers: []string{"Local:\\s*{url}"},
			expected: []string{"http://localhost:5173/"},
		},
		{
			name:     "with non-matching pattern",
			input:    []byte("Network: http://localhost:5173/"),
			matchers: []string{"Local:\\s*{url}"},
			expected: nil,
		},
		{
			name:     "with empty matchers falls back to parseDevServerURLs",
			input:    []byte("http://localhost:3000"),
			matchers: nil,
			expected: []string{"http://localhost:3000"},
		},
		{
			name:     "multiple matchers with or pattern - localhost only",
			input:    []byte("Local: http://localhost:5173/\nNetwork: http://192.168.1.10:5173/"),
			matchers: []string{"(Local|Network):\\s*{url}"},
			expected: []string{"http://localhost:5173/"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDevServerURLsWithMatchers(tt.input, tt.matchers)
			if len(got) != len(tt.expected) {
				t.Errorf("parseDevServerURLsWithMatchers() got %d URLs, want %d", len(got), len(tt.expected))
				t.Errorf("got: %v", got)
				t.Errorf("want: %v", tt.expected)
				return
			}
			for i, url := range got {
				if url != tt.expected[i] {
					t.Errorf("parseDevServerURLsWithMatchers()[%d] = %q, want %q", i, url, tt.expected[i])
				}
			}
		})
	}
}
