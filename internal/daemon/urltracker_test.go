package daemon

import (
	"testing"
)

func TestParseDevServerURLs(t *testing.T) {
	t.Parallel()
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
			expected: []string{"http://127.0.0.1:8080"},
		},
		{
			name:     "localhost only from multiple URLs",
			input:    "  Local:   http://localhost:5173/\n  Network: http://192.168.1.10:5173/\n",
			expected: []string{"http://localhost:5173"},
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
			expected: []string{"http://localhost:5173"},
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

func TestParseDevServerURLs_ANSIStripping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "URL wrapped in ANSI color codes",
			input:    "  \x1b[32m➜\x1b[0m  \x1b[1mLocal:\x1b[0m   \x1b[36mhttp://localhost:5173/\x1b[0m",
			expected: []string{"http://localhost:5173"},
		},
		{
			name:     "vite output with full ANSI formatting",
			input:    "\x1b[32m➜\x1b[0m  \x1b[1mLocal:\x1b[0m   \x1b[36mhttp://localhost:5173/\x1b[0m\n\x1b[32m➜\x1b[0m  \x1b[1mNetwork:\x1b[0m use --host to expose",
			expected: []string{"http://localhost:5173"},
		},
		{
			name:     "URL with bold and underline ANSI codes",
			input:    "Server: \x1b[1;4mhttp://localhost:3000\x1b[0m",
			expected: []string{"http://localhost:3000"},
		},
		{
			name:     "mixed clean and ANSI lines",
			input:    "Starting...\n\x1b[36mhttp://localhost:8080\x1b[0m ready",
			expected: []string{"http://localhost:8080"},
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

func TestParseDevServerURLsWithMatchers_ANSIStripping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    []byte
		matchers []string
		expected []string
	}{
		{
			name:     "ANSI-wrapped vite output with Local matcher",
			input:    []byte("\x1b[32m➜\x1b[0m  \x1b[1mLocal:\x1b[0m   \x1b[36mhttp://localhost:5173/\x1b[0m"),
			matchers: []string{"Local:\\s+{url}"},
			expected: []string{"http://localhost:5173"},
		},
		{
			name:     "ANSI-wrapped output with or-pattern matcher",
			input:    []byte("\x1b[1mLocal:\x1b[0m   \x1b[36mhttp://localhost:5173/\x1b[0m\n\x1b[1mNetwork:\x1b[0m http://192.168.1.10:5173/"),
			matchers: []string{"(Local|Network):\\s*{url}"},
			expected: []string{"http://localhost:5173"},
		},
		{
			name:     "ANSI output with non-matching matcher",
			input:    []byte("\x1b[1mNetwork:\x1b[0m \x1b[36mhttp://localhost:5173/\x1b[0m"),
			matchers: []string{"Local:\\s*{url}"},
			expected: nil,
		},
		{
			name:     "clean output still works with matchers",
			input:    []byte("Local: http://localhost:5173/"),
			matchers: []string{"Local:\\s*{url}"},
			expected: []string{"http://localhost:5173"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, _ := compileURLMatchers(tt.matchers)
			got := parseDevServerURLsWithMatchers(tt.input, compiled)
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

func TestShouldIgnoreURL(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	config := DefaultURLTrackerConfig()
	tracker := NewURLTracker(nil, config)

	// Manually set some data
	tracker.mu.Lock()
	tracker.urls["proc-1"] = []string{"http://localhost:3000"}
	tracker.seenURLs["proc-1"] = map[string]bool{"http://localhost:3000": true}
	tracker.scannedStdout["proc-1"] = 1000
	tracker.scannedStderr["proc-1"] = 500
	tracker.mu.Unlock()

	// Clear the process
	tracker.ClearProcess("proc-1")

	// Verify all data was cleared
	tracker.mu.RLock()
	_, urlsExist := tracker.urls["proc-1"]
	_, seenExist := tracker.seenURLs["proc-1"]
	_, stdoutExist := tracker.scannedStdout["proc-1"]
	_, stderrExist := tracker.scannedStderr["proc-1"]
	scannedExist := stdoutExist || stderrExist
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
	t.Parallel()
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
	t.Parallel()
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
			expected: []string{"http://localhost:5173"},
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
			expected: []string{"http://localhost:5173"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			compiled, _ := compileURLMatchers(tt.matchers)
			got := parseDevServerURLsWithMatchers(tt.input, compiled)
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

// TestURLTracker_ClearProcess_EnablesRescanning verifies that ClearProcess
// resets the per-stream scanned offsets, allowing a restarted process with the
// same ID to have its output re-scanned for URLs.
// This is a regression test for URL re-detection on process restart.
func TestURLTracker_ClearProcess_EnablesRescanning(t *testing.T) {
	t.Parallel()
	config := DefaultURLTrackerConfig()
	tracker := NewURLTracker(nil, config)

	// Simulate a process that has been fully scanned (maxScanBytes reached)
	tracker.mu.Lock()
	tracker.scannedStdout["proc-1"] = maxScanBytes // 8KB - scan limit reached
	tracker.scannedStderr["proc-1"] = maxScanBytes
	tracker.urls["proc-1"] = []string{"http://localhost:3000"}
	tracker.seenURLs["proc-1"] = map[string]bool{"http://localhost:3000": true}
	tracker.mu.Unlock()

	// Verify the process appears fully scanned
	tracker.mu.RLock()
	scannedBefore := tracker.scannedStdout["proc-1"]
	tracker.mu.RUnlock()
	if scannedBefore != maxScanBytes {
		t.Fatalf("Expected scannedStdout to be %d, got %d", maxScanBytes, scannedBefore)
	}

	// Clear the process (simulates what happens during restart)
	tracker.ClearProcess("proc-1")

	// Verify scanned offsets are cleared
	tracker.mu.RLock()
	stdoutAfter, stdoutExists := tracker.scannedStdout["proc-1"]
	_, stderrExists := tracker.scannedStderr["proc-1"]
	tracker.mu.RUnlock()

	if stdoutExists || stderrExists {
		t.Errorf("scanned offsets should not exist after ClearProcess, but got stdout=%d", stdoutAfter)
	}

	// The key behavior: with scanned offsets cleared, a new process with the
	// same ID will be scanned from byte 0 again, detecting new URLs
}

// TestScanStreamForURLs_StdoutURLSurvivesStderrAdvance is the regression test
// for the stdout/stderr offset-coupling bug. A URL printed to stdout must be
// detected even when stderr produced output in an earlier scan. This is the
// exact bifrost / Vite 8 scenario: the "Local:" URL line is on stdout while
// deprecation warnings stream to stderr. The previous single-offset-over-
// CombinedOutput model advanced its cursor past the stderr bytes and then
// skipped the stdout URL (which lived at a lower combined offset), so no
// proxy was ever created.
func TestScanStreamForURLs_StdoutURLSurvivesStderrAdvance(t *testing.T) {
	t.Parallel()
	matchers, _ := compileURLMatchers([]string{"Local:\\s+{url}"})
	// 626-ish bytes of Vite 8 deprecation noise on stderr, no URL.
	stderrNoise := []byte("6:34:41 AM [vite] warning: `optimizeDeps.esbuildOptions` option was specified " +
		"by \"vite:react-swc\" plugin. This option is deprecated, please use " +
		"`optimizeDeps.rolldownOptions` instead.\n`esbuild` option is set to false.\n")
	stdoutURL := []byte("\n  VITE v8.0.16  ready in 3422 ms\n\n  Local:   http://localhost:5173/\n")

	// Scan 1: only stderr has flushed; the stdout URL line is not present yet.
	outURLs, outOff := scanStreamForURLs(nil, 0, matchers)
	errURLs, errOff := scanStreamForURLs(stderrNoise, 0, matchers)
	if len(outURLs) != 0 || len(errURLs) != 0 {
		t.Fatalf("scan 1 should find no URLs, got stdout=%v stderr=%v", outURLs, errURLs)
	}
	if outOff != 0 {
		t.Fatalf("stdout offset should stay 0 with no stdout, got %d", outOff)
	}
	if errOff != len(stderrNoise) {
		t.Fatalf("stderr offset should advance to %d, got %d", len(stderrNoise), errOff)
	}

	// Scan 2: stdout now carries the URL; stderr is unchanged. With per-stream
	// offsets the stdout URL is found despite stderr having advanced. A single
	// combined cursor (== errOff) would have skipped it.
	outURLs2, outOff2 := scanStreamForURLs(stdoutURL, outOff, matchers)
	_, _ = scanStreamForURLs(stderrNoise, errOff, matchers)

	found := false
	for _, u := range outURLs2 {
		if u == "http://localhost:5173" {
			found = true
		}
	}
	if !found {
		t.Fatalf("stdout URL not detected after stderr advanced; got %v", outURLs2)
	}
	if outOff2 != len(stdoutURL) {
		t.Fatalf("stdout offset should advance to %d, got %d", len(stdoutURL), outOff2)
	}
}
