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
			name:     "multiple dev server URLs",
			input:    "  Local:   http://localhost:5173/\n  Network: http://192.168.1.10:5173/\n",
			expected: []string{"http://localhost:5173/", "http://192.168.1.10:5173/"},
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
			name:     "vite dev server output",
			input:    "  VITE v5.0.0  ready in 500 ms\n\n  ➜  Local:   http://localhost:5173/\n  ➜  Network: http://192.168.1.100:5173/\n",
			expected: []string{"http://localhost:5173/", "http://192.168.1.100:5173/"},
		},
		{
			name:     "next.js dev server output",
			input:    "ready - started server on 0.0.0.0:3000, url: http://localhost:3000",
			expected: []string{"http://localhost:3000"},
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
