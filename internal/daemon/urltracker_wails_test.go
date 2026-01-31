package daemon

import (
	"testing"
)

func TestWailsURLMatching(t *testing.T) {
	// Simulated Wails dev output with both Vite and Wails URLs
	output := []byte(`
  VITE v5.2.0  ready in 300 ms

  ➜  Local:   http://localhost:5173/
  ➜  Network: http://192.168.1.100:5173/

Wails 3.0.0-alpha5
Using DevServer URL: http://localhost:34115
App ready.
`)

	// Test with no matchers - should capture all URLs
	urls := parseDevServerURLsWithMatchers(output, nil)
	t.Logf("No matchers - captured %d URLs: %v", len(urls), urls)

	// Test with Wails-specific matcher
	matchers := []string{`DevServer URL:\s*{url}`}
	urls = parseDevServerURLsWithMatchers(output, matchers)
	t.Logf("With Wails matcher - captured %d URLs: %v", len(urls), urls)

	if len(urls) != 1 {
		t.Errorf("Expected 1 URL with Wails matcher, got %d: %v", len(urls), urls)
	}

	if len(urls) > 0 && urls[0] != "http://localhost:34115" {
		t.Errorf("Expected http://localhost:34115, got %s", urls[0])
	}
}

func TestMatchesURLPatternWails(t *testing.T) {
	tests := []struct {
		line    string
		pattern string
		want    bool
	}{
		{"  Local:   http://localhost:5173/", `DevServer URL:\s*{url}`, false},
		{"Using DevServer URL: http://localhost:34115", `DevServer URL:\s*{url}`, true},
		{"  ➜  Network: http://192.168.1.100:5173/", `DevServer URL:\s*{url}`, false},
		{"App ready.", `DevServer URL:\s*{url}`, false},
	}

	for _, tt := range tests {
		got := matchesURLPattern(tt.line, tt.pattern)
		if got != tt.want {
			t.Errorf("matchesURLPattern(%q, %q) = %v, want %v", tt.line, tt.pattern, got, tt.want)
		}
	}
}
