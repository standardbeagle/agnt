package urltracker

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
)

// TestWailsConfigURLMatchersLoading tests that URL matchers from config are parsed correctly
func TestWailsConfigURLMatchersLoading(t *testing.T) {
	// This is the exact config from beagle-term/.agnt.kdl
	kdlConfig := `
project {
    type "wails"
    name "beagle-term"
}

scripts {
    frontend-dev {
        run "npm run dev"
        cwd "frontend"
    }
    wails-dev {
        run "wails dev"
        autostart true
        // Pattern must match "Using DevServer URL:" but NOT "Using Frontend DevServer URL:"
        url-matchers "Using DevServer URL:\\s*{url}"
    }
    build {
        run "wails build"
    }
}

proxies {
    wails-dev {
        script "wails-dev"
        fallback-port 34115
        websocket true
    }
}
`
	cfg, err := config.ParseAgntConfig(kdlConfig)
	if err != nil {
		t.Fatalf("Failed to parse config: %v", err)
	}

	script, ok := cfg.Scripts["wails-dev"]
	if !ok {
		t.Fatal("Script 'wails-dev' not found in config")
	}

	if len(script.URLMatchers) == 0 {
		t.Fatal("URLMatchers is empty - config parsing failed")
	}

	expectedMatcher := `Using DevServer URL:\s*{url}`
	if script.URLMatchers[0] != expectedMatcher {
		t.Errorf("URLMatchers[0] = %q, want %q", script.URLMatchers[0], expectedMatcher)
	}

	t.Logf("URLMatchers correctly parsed: %v", script.URLMatchers)

	// Now test that the matcher correctly filters Wails output
	wailsOutput := []byte(`
  VITE v5.2.0  ready in 300 ms

  ➜  Local:   http://localhost:5174/
  ➜  Network: http://192.168.1.100:5174/

Wails 3.0.0-alpha5
Using DevServer URL: http://localhost:34115
Using Frontend DevServer URL: http://localhost:5174/
App ready.
`)

	urls := ParseDevServerURLsWithMatchers(wailsOutput, script.URLMatchers)
	t.Logf("URLs detected with config matchers: %v", urls)

	if len(urls) != 1 {
		t.Errorf("Expected 1 URL, got %d: %v", len(urls), urls)
	}

	if len(urls) > 0 && urls[0] != "http://localhost:34115" {
		t.Errorf("Expected http://localhost:34115, got %s", urls[0])
	}
}

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
	urls := ParseDevServerURLsWithMatchers(output, nil)
	t.Logf("No matchers - captured %d URLs: %v", len(urls), urls)

	// Test with Wails-specific matcher (must include "Using" to avoid matching "Frontend DevServer URL")
	matchers := []string{`Using DevServer URL:\s*{url}`}
	urls = ParseDevServerURLsWithMatchers(output, matchers)
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
		{"  Local:   http://localhost:5173/", `Using DevServer URL:\s*{url}`, false},
		{"Using DevServer URL: http://localhost:34115", `Using DevServer URL:\s*{url}`, true},
		{"Using Frontend DevServer URL: http://localhost:5174/", `Using DevServer URL:\s*{url}`, false}, // Must NOT match frontend
		{"  ➜  Network: http://192.168.1.100:5173/", `Using DevServer URL:\s*{url}`, false},
		{"App ready.", `Using DevServer URL:\s*{url}`, false},
	}

	for _, tt := range tests {
		got := MatchesURLPattern(tt.line, tt.pattern)
		if got != tt.want {
			t.Errorf("MatchesURLPattern(%q, %q) = %v, want %v", tt.line, tt.pattern, got, tt.want)
		}
	}
}
