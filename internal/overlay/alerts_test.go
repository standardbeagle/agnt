package overlay

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultAlertPatterns(t *testing.T) {
	patterns := DefaultAlertPatterns()
	assert.True(t, len(patterns) >= 21, "should have at least 21 default patterns, got %d", len(patterns))

	// Verify all patterns have required fields
	ids := map[string]bool{}
	for _, p := range patterns {
		assert.NotEmpty(t, p.ID, "pattern ID must not be empty")
		assert.NotNil(t, p.Pattern, "pattern regex must not be nil for %s", p.ID)
		assert.NotEmpty(t, p.Severity, "severity must not be empty for %s", p.ID)
		assert.NotEmpty(t, p.Category, "category must not be empty for %s", p.ID)
		assert.False(t, ids[p.ID], "duplicate pattern ID: %s", p.ID)
		ids[p.ID] = true
	}
}

func TestAlertPatternMatching(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		matchID  string // Expected matching pattern ID, empty if no match
		severity AlertSeverity
	}{
		// .NET patterns
		{"dotnet restart needed", "Restart is needed to apply the changes", "dotnet-restart", AlertSeverityError},
		{"dotnet ENC error", "error ENC1008: Something went wrong", "dotnet-enc-error", AlertSeverityError},
		{"dotnet ENC warning", "warning ENC0042: Minor issue", "dotnet-enc-warning", AlertSeverityWarning},
		{"dotnet build failed", "Build FAILED.", "dotnet-build-error", AlertSeverityError},
		{"dotnet-watch error emoji", "dotnet watch ❌  : error NU1301: Unable to load the service index", "dotnet-watch-error", AlertSeverityError},
		{"dotnet-watch warning emoji", "dotnet watch ⚠  : warning NU1903: Package 'Foo' has a known critical vulnerability", "dotnet-watch-warning", AlertSeverityWarning},
		{"dotnet-watch restore failed", "dotnet watch 🔨   Failed to restore /app/MyApp.csproj", "dotnet-watch-restore-failed", AlertSeverityError},
		{"dotnet-watch normal progress", "dotnet watch 🔨   1 of 3 projects are up-to-date for restore.", "", ""},

		// Webpack patterns
		{"webpack error", "ERROR in ./src/index.js", "webpack-error", AlertSeverityError},
		{"webpack compile fail", "Failed to compile.", "webpack-compile-fail", AlertSeverityError},

		// Vite
		{"vite hmr fail", "HMR update failed", "vite-hmr-fail", AlertSeverityError},

		// Go patterns
		{"dotnet build failed matches generic too", "build failed", "dotnet-build-error", AlertSeverityError},
		{"go panic", "panic: runtime error: index out of range", "go-panic", AlertSeverityError},
		{"go test fail", "FAIL	github.com/example/pkg", "go-test-fail", AlertSeverityError},

		// Python patterns
		{"python traceback", "Traceback (most recent call last)", "python-traceback", AlertSeverityError},
		{"python syntax", "SyntaxError: invalid syntax", "python-syntax", AlertSeverityError},

		// Generic patterns
		{"connection refused", "Error: connect ECONNREFUSED 127.0.0.1:3000", "connection-refused", AlertSeverityWarning},
		{"addr in use", "Error: listen EADDRINUSE: address already in use :::3000", "addr-in-use", AlertSeverityError},
		{"segfault", "Segmentation fault (core dumped)", "segfault", AlertSeverityError},
		{"unhandled exception", "Unhandled Exception: System.NullReferenceException", "unhandled-exception", AlertSeverityError},
		{"out of memory", "FATAL ERROR: JavaScript heap out of memory", "out-of-memory", AlertSeverityError},

		// Non-matching lines
		{"normal output", "Server listening on port 3000", "", ""},
		{"empty line", "", "", ""},
		{"info log", "INFO: Application started successfully", "", ""},
	}

	patterns := DefaultAlertPatterns()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var matched *AlertPattern
			for _, p := range patterns {
				if p.Pattern.MatchString(tt.line) {
					matched = p
					break
				}
			}

			if tt.matchID == "" {
				assert.Nil(t, matched, "expected no match for line: %s", tt.line)
			} else {
				require.NotNil(t, matched, "expected match for line: %s", tt.line)
				assert.Equal(t, tt.matchID, matched.ID)
				assert.Equal(t, tt.severity, matched.Severity)
			}
		})
	}
}

func TestAlertScannerProcessLine(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  50 * time.Millisecond,
		DedupeWindow: 60 * time.Second,
		OnAlert: func(batch *AlertBatch) {
			mu.Lock()
			received = append(received, batch)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	scanner.ProcessLine("panic: runtime error", "dev")
	scanner.ProcessLine("Build FAILED.", "dev")

	// Wait for batch window to flush
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received, 1, "should receive one batch")
	assert.Equal(t, "dev", received[0].ScriptID)
	assert.Len(t, received[0].Matches, 2)
}

func TestAlertScannerProcessLineCarriesLifetimeTokenThroughDeferredDelivery(t *testing.T) {
	token := &struct{ generation int }{generation: 7}
	delivered := make(chan *AlertMatch, 1)
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow: 5 * time.Millisecond,
		OnAlert: func(batch *AlertBatch) {
			delivered <- batch.Matches[0]
		},
	})
	t.Cleanup(scanner.Stop)

	scanner.ProcessLineWithLifetime("panic: delayed", "reused-process", token)
	select {
	case match := <-delivered:
		require.Same(t, token, match.LifetimeToken)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for deferred alert delivery")
	}
}

func TestAlertScannerDeduplication(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  50 * time.Millisecond,
		DedupeWindow: 2 * time.Second,
		OnAlert: func(batch *AlertBatch) {
			mu.Lock()
			received = append(received, batch)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	// Send same line twice
	scanner.ProcessLine("panic: runtime error", "dev")
	scanner.ProcessLine("panic: runtime error", "dev")

	// Wait for batch
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	require.Len(t, received, 1)
	assert.Len(t, received[0].Matches, 1, "duplicate should be suppressed")
	mu.Unlock()
}

// TestAlertScannerDedupeExpiry asserts the invariant "once DedupeWindow has
// elapsed since the last sighting of a fingerprint, the next occurrence is
// treated as new, not a duplicate." Wall-clock sleeps racing a short
// DedupeWindow against host scheduling jitter under load previously made
// this flaky (batch flush can be delayed arbitrarily under CPU saturation,
// so a fixed sleep is not a reliable proxy for "the dedupe window elapsed").
// A fake clock makes the dedupe-window boundary deterministic; polling via
// require.Eventually (instead of a fixed sleep) makes waiting for batch
// delivery robust to scheduler delay.
func TestAlertScannerDedupeExpiry(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex

	fake := newFakeClock(time.Now())

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  10 * time.Millisecond,
		DedupeWindow: 100 * time.Millisecond,
		OnAlert: func(batch *AlertBatch) {
			mu.Lock()
			received = append(received, batch)
			mu.Unlock()
		},
	})
	scanner.clockNow = fake.Now
	defer scanner.Stop()

	batchCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(received)
	}

	scanner.ProcessLine("panic: runtime error", "dev")
	require.Eventually(t, func() bool { return batchCount() == 1 }, 5*time.Second, 5*time.Millisecond,
		"first batch should be delivered")

	// Advance the fake clock past DedupeWindow so the dedupe check
	// (clockNow().Sub(lastSeen) < dedupeWindow) deterministically sees
	// expiry, independent of real elapsed wall time.
	fake.Advance(101 * time.Millisecond)

	scanner.ProcessLine("panic: runtime error", "dev")
	require.Eventually(t, func() bool { return batchCount() == 2 }, 5*time.Second, 5*time.Millisecond,
		"second batch should be delivered after dedupe window expiry")
}

func TestAlertScannerDisabledPattern(t *testing.T) {
	// Disabling go-panic alone no longer fully silences "panic: runtime error":
	// the line is error-looking, so the unparsed catch-all (G2) resurfaces it
	// rather than silently dropping a real error. To assert true silence we
	// must disable both the specific pattern AND the catch-all safety net.
	var received []*AlertBatch
	var mu sync.Mutex

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow: 50 * time.Millisecond,
		DisabledIDs: []string{"go-panic", unparsedPatternID},
		OnAlert: func(batch *AlertBatch) {
			mu.Lock()
			received = append(received, batch)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	scanner.ProcessLine("panic: runtime error", "dev")
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, received, 0, "disabling both the pattern and the catch-all should yield silence")
}

// TestAlertScannerDisabledPatternCatchAll proves the complement: disabling a
// specific pattern (but NOT the catch-all) still surfaces the error-looking
// line as unparsed, so disabling one pattern never silently drops a real error.
func TestAlertScannerDisabledPatternCatchAll(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow: 50 * time.Millisecond,
		DisabledIDs: []string{"go-panic"},
		OnAlert: func(batch *AlertBatch) {
			mu.Lock()
			received = append(received, batch)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	scanner.ProcessLine("panic: runtime error", "dev")
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	var catchAll *AlertMatch
	for _, b := range received {
		for _, m := range b.Matches {
			if m.Pattern != nil && m.Pattern.ID == unparsedPatternID {
				catchAll = m
			}
		}
	}
	require.NotNil(t, catchAll, "disabling only go-panic must leave the catch-all to resurface the error as unparsed")
	assert.Equal(t, "unparsed", catchAll.Pattern.Category)
	assert.Equal(t, AlertSeverityError, catchAll.Pattern.Severity)
}

func TestAlertScannerCustomPattern(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow: 50 * time.Millisecond,
		Patterns: []*AlertPattern{
			{
				ID:       "custom-error",
				Pattern:  regexp.MustCompile(`MY_APP_ERROR:`),
				Severity: AlertSeverityError,
				Category: "custom",
			},
		},
		OnAlert: func(batch *AlertBatch) {
			mu.Lock()
			received = append(received, batch)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	scanner.ProcessLine("MY_APP_ERROR: something broke", "api")
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received, 1)
	assert.Equal(t, "api", received[0].ScriptID)
	assert.Equal(t, "custom-error", received[0].Matches[0].Pattern.ID)
}

func TestAlertScannerDisableAtRuntime(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow: 50 * time.Millisecond,
		OnAlert: func(batch *AlertBatch) {
			mu.Lock()
			received = append(received, batch)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	scanner.SetEnabled(false)
	scanner.ProcessLine("panic: runtime error", "dev")
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	assert.Len(t, received, 0, "disabled scanner should not produce alerts")
	mu.Unlock()

	// Re-enable
	scanner.SetEnabled(true)
	scanner.ProcessLine("panic: runtime error", "dev")
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, received, 1, "re-enabled scanner should produce alerts")
}

func TestAlertScannerActivityDeferral(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex
	var active atomic.Bool
	active.Store(true)

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:   50 * time.Millisecond,
		RetryInterval: 50 * time.Millisecond,
		ActivityState: func() ActivityState {
			if active.Load() {
				return ActivityActive
			}
			return ActivityIdle
		},
		OnAlert: func(batch *AlertBatch) {
			mu.Lock()
			received = append(received, batch)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	scanner.ProcessLine("panic: runtime error", "dev")

	// First batch window passes, but AI is active - defers
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	assert.Len(t, received, 0, "should defer while active")
	mu.Unlock()

	// Transition to idle - flush should happen on next retry
	active.Store(false)
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, received, 1, "should flush after becoming idle")
}

func TestAlertScannerMultipleScripts(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow: 50 * time.Millisecond,
		OnAlert: func(batch *AlertBatch) {
			mu.Lock()
			received = append(received, batch)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	scanner.ProcessLine("panic: runtime error", "api")
	scanner.ProcessLine("Build FAILED.", "frontend")

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, received, 2, "should get separate batches per script")

	scriptIDs := map[string]bool{}
	for _, b := range received {
		scriptIDs[b.ScriptID] = true
	}
	assert.True(t, scriptIDs["api"])
	assert.True(t, scriptIDs["frontend"])
}

func TestAlertScannerStop(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow: 5 * time.Second, // Long batch window
		OnAlert: func(batch *AlertBatch) {
			mu.Lock()
			received = append(received, batch)
			mu.Unlock()
		},
	})

	scanner.ProcessLine("panic: runtime error", "dev")
	scanner.Stop()

	// Stop should flush pending
	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, received, 1, "Stop should flush pending alerts")
}

func TestAlertScannerIgnoresEmptyLines(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow: 50 * time.Millisecond,
		OnAlert: func(batch *AlertBatch) {
			mu.Lock()
			received = append(received, batch)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	scanner.ProcessLine("", "dev")
	scanner.ProcessLine("   ", "dev")
	scanner.ProcessLine("\t", "dev")

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, received, 0, "empty/whitespace lines should be ignored")
}

func TestAlertBatchFormat(t *testing.T) {
	batch := &AlertBatch{
		ScriptID: "dev",
		Matches: []*AlertMatch{
			{
				Pattern: &AlertPattern{
					ID:       "dotnet-enc-error",
					Severity: AlertSeverityError,
				},
				Line: "error ENC1008: Changing source file in a stale project has no effect",
			},
			{
				Pattern: &AlertPattern{
					ID:       "dotnet-restart",
					Severity: AlertSeverityError,
				},
				Line: "Restart is needed to apply the changes",
			},
			{
				Pattern: &AlertPattern{
					ID:       "connection-refused",
					Severity: AlertSeverityWarning,
				},
				Line: "DEPRECATION WARNING: Method Foo is deprecated",
			},
		},
	}

	formatted := batch.Format()
	assert.Contains(t, formatted, "[agnt process alert]")
	assert.Contains(t, formatted, `Script "dev"`)
	assert.Contains(t, formatted, "Errors (2):")
	assert.Contains(t, formatted, "Warnings (1):")
	assert.Contains(t, formatted, "error ENC1008")
	assert.Contains(t, formatted, "Restart is needed")
	assert.Contains(t, formatted, "Consider restarting")
}

func TestAlertBatchFormatLineTruncation(t *testing.T) {
	longLine := strings.Repeat("x", 200)
	batch := &AlertBatch{
		ScriptID: "dev",
		Matches: []*AlertMatch{
			{
				Pattern: &AlertPattern{
					ID:       "test",
					Severity: AlertSeverityError,
				},
				Line: longLine,
			},
		},
	}

	formatted := batch.Format()
	assert.Contains(t, formatted, "...")
	assert.Less(t, len(formatted), 300)
}

func TestAlertBatchMaxSeverity(t *testing.T) {
	tests := []struct {
		name       string
		severities []AlertSeverity
		expected   AlertSeverity
	}{
		{"all errors", []AlertSeverity{AlertSeverityError, AlertSeverityError}, AlertSeverityError},
		{"mixed with error", []AlertSeverity{AlertSeverityWarning, AlertSeverityError}, AlertSeverityError},
		{"all warnings", []AlertSeverity{AlertSeverityWarning, AlertSeverityWarning}, AlertSeverityWarning},
		{"warning and info", []AlertSeverity{AlertSeverityInfo, AlertSeverityWarning}, AlertSeverityWarning},
		{"all info", []AlertSeverity{AlertSeverityInfo}, AlertSeverityInfo},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch := &AlertBatch{ScriptID: "test"}
			for _, sev := range tt.severities {
				batch.Matches = append(batch.Matches, &AlertMatch{
					Pattern: &AlertPattern{Severity: sev},
				})
			}
			assert.Equal(t, tt.expected, batch.MaxSeverity())
		})
	}
}

func TestAlertBatchFormatEmpty(t *testing.T) {
	batch := &AlertBatch{ScriptID: "dev"}
	assert.Equal(t, "", batch.Format())
}

func TestAlertScannerAddAndDisablePattern(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow: 50 * time.Millisecond,
		OnAlert: func(batch *AlertBatch) {
			mu.Lock()
			received = append(received, batch)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	// Add a pattern at runtime
	scanner.AddPattern(&AlertPattern{
		ID:       "runtime-pattern",
		Pattern:  regexp.MustCompile(`CUSTOM_ERR`),
		Severity: AlertSeverityError,
		Category: "custom",
	})

	scanner.ProcessLine("CUSTOM_ERR: something", "dev")
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	require.Len(t, received, 1)
	mu.Unlock()

	// Disable it
	scanner.DisablePattern("runtime-pattern")
	scanner.ProcessLine("CUSTOM_ERR: another", "dev")
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, received, 1, "disabled pattern should not produce more alerts")
}

func TestAlertScanner_RecentMatches(t *testing.T) {
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  5 * time.Second, // Long window so batch doesn't fire
		DedupeWindow: 60 * time.Second,
	})
	defer scanner.Stop()

	scanner.ProcessLine("panic: runtime error", "api")
	scanner.ProcessLine("Build FAILED.", "frontend")

	matches := scanner.RecentMatches(time.Time{})
	require.Len(t, matches, 2, "should return both matches")
	assert.Equal(t, "api", matches[0].ScriptID)
	assert.Equal(t, "go-panic", matches[0].Pattern.ID)
	assert.Equal(t, "frontend", matches[1].ScriptID)
	assert.Equal(t, "dotnet-build-error", matches[1].Pattern.ID)
}

func TestAlertScanner_RecentMatches_SinceFilter(t *testing.T) {
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  5 * time.Second,
		DedupeWindow: 60 * time.Second,
	})
	defer scanner.Stop()

	scanner.ProcessLine("panic: runtime error", "api")
	time.Sleep(50 * time.Millisecond)

	cutoff := time.Now()
	time.Sleep(50 * time.Millisecond)

	scanner.ProcessLine("Build FAILED.", "frontend")

	matches := scanner.RecentMatches(cutoff)
	require.Len(t, matches, 1, "should return only the match after cutoff")
	assert.Equal(t, "frontend", matches[0].ScriptID)
	assert.Equal(t, "dotnet-build-error", matches[0].Pattern.ID)
}

func TestAlertScanner_RecentMatches_RingBufferOverflow(t *testing.T) {
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  5 * time.Second,
		DedupeWindow: 60 * time.Second,
	})
	defer scanner.Stop()

	// Write 250 matches (50 more than buffer capacity).
	// Use unique lines so dedup in addMatch doesn't matter (ring buffer is pre-dedup).
	for i := 0; i < 250; i++ {
		// Each line is unique thanks to the index.
		scanner.ProcessLine(fmt.Sprintf("panic: error number %d", i), "dev")
	}

	matches := scanner.RecentMatches(time.Time{})
	require.Len(t, matches, matchBufSize, "should retain exactly matchBufSize entries")

	// Most recent entry should be the last one written.
	last := matches[len(matches)-1]
	assert.Contains(t, last.Line, "error number 249")
	// Oldest entry should be #50 (first 50 were evicted).
	first := matches[0]
	assert.Contains(t, first.Line, "error number 50")
}

// matchLine scans DefaultAlertPatterns for the first match against line,
// returning the matched pattern or nil.
func matchLine(line string) *AlertPattern {
	for _, p := range DefaultAlertPatterns() {
		if p.Pattern.MatchString(line) {
			return p
		}
	}
	return nil
}

func TestAlertPatterns_DotnetExtended(t *testing.T) {
	tests := []struct {
		line    string
		wantID  string
		wantSev AlertSeverity
	}{
		// Already-covered patterns — non-regression
		{"dotnet watch ❌  : error NU1301: Unable to load the service index", "dotnet-watch-error", AlertSeverityError},
		{"dotnet watch ⚠  : warning NU1903: Package 'Foo' has a known critical vulnerability", "dotnet-watch-warning", AlertSeverityWarning},
		{"Build FAILED.", "dotnet-build-error", AlertSeverityError},
		// New: Unhandled exception (covered by generic unhandled-exception)
		{"Unhandled exception. System.InvalidOperationException: Sequence contains no elements", "unhandled-exception", AlertSeverityError},
		// New: Error(s) in
		{"Error(s) in /app/MyApp.csproj", "dotnet-errors-in", AlertSeverityError},
		// New: NETSDK error codes
		{"NETSDK1045: The current .NET SDK does not support targeting .NET 6.0.", "dotnet-netsdk", AlertSeverityError},
		// New: MSBuild error
		{"MSBuild error MSB3073: The command exited with code 1.", "dotnet-msbuild-error", AlertSeverityError},
	}

	for _, tt := range tests {
		t.Run(tt.line[:min(len(tt.line), 40)], func(t *testing.T) {
			p := matchLine(tt.line)
			require.NotNil(t, p, "expected match for: %s", tt.line)
			assert.Equal(t, tt.wantID, p.ID)
			assert.Equal(t, tt.wantSev, p.Severity)
		})
	}
}

func TestAlertPatterns_NodeJS(t *testing.T) {
	tests := []struct {
		line    string
		wantID  string
		wantSev AlertSeverity
	}{
		{"Error: ENOENT: no such file or directory, open '/app/config.json'", "node-error", AlertSeverityError},
		{"UnhandledPromiseRejectionWarning: TypeError: Cannot read property 'x' of undefined", "node-unhandled-promise", AlertSeverityError},
		{"Cannot find module './utils/helper'", "node-cannot-find-module", AlertSeverityError},
		// SyntaxError: is already covered by python-syntax; node output shares the same prefix.
		{"SyntaxError: Unexpected token '<'", "python-syntax", AlertSeverityError},
		{"npm ERR! code ELIFECYCLE", "npm-err", AlertSeverityError},
		{"yarn error Command failed with exit code 1.", "yarn-error", AlertSeverityError},
		{"[nodemon] app crashed - waiting for file changes before starting...", "nodemon-crash", AlertSeverityError},
	}

	for _, tt := range tests {
		t.Run(tt.wantID, func(t *testing.T) {
			p := matchLine(tt.line)
			require.NotNil(t, p, "expected match for: %s", tt.line)
			assert.Equal(t, tt.wantID, p.ID)
			assert.Equal(t, tt.wantSev, p.Severity)
		})
	}
}

func TestAlertPatterns_Python(t *testing.T) {
	tests := []struct {
		line    string
		wantID  string
		wantSev AlertSeverity
	}{
		// Already-covered — non-regression
		{"Traceback (most recent call last):", "python-traceback", AlertSeverityError},
		{"SyntaxError: invalid syntax", "python-syntax", AlertSeverityError},
		// New patterns
		{"ModuleNotFoundError: No module named 'requests'", "python-module-not-found", AlertSeverityError},
		{"ImportError: cannot import name 'something' from 'mymodule'", "python-import-error", AlertSeverityError},
		{"django.core.exceptions.ImproperlyConfigured: SECRET_KEY not set", "python-django-exception", AlertSeverityError},
		{"RuntimeError: CUDA error: device-side assert triggered", "python-runtime-error", AlertSeverityError},
		{"werkzeug.exceptions.NotFound: 404 Not Found", "python-werkzeug-error", AlertSeverityError},
	}

	for _, tt := range tests {
		t.Run(tt.wantID, func(t *testing.T) {
			p := matchLine(tt.line)
			require.NotNil(t, p, "expected match for: %s", tt.line)
			assert.Equal(t, tt.wantID, p.ID)
			assert.Equal(t, tt.wantSev, p.Severity)
		})
	}
}

func TestAlertPatterns_Go(t *testing.T) {
	tests := []struct {
		line    string
		wantID  string
		wantSev AlertSeverity
	}{
		// Already-covered — non-regression
		{"panic: runtime error: index out of range [0] with length 0", "go-panic", AlertSeverityError},
		// "build failed" matches dotnet-build-error ((?i)Build FAILED) which appears first;
		// the go-build-fail pattern is a duplicate that also matches but loses to dotnet-build-error.
		// New patterns
		{"fatal error: concurrent map read and map write", "go-fatal-error", AlertSeverityError},
		{"go: error loading module requirements", "go-module-error", AlertSeverityError},
	}

	for _, tt := range tests {
		t.Run(tt.wantID, func(t *testing.T) {
			p := matchLine(tt.line)
			require.NotNil(t, p, "expected match for: %s", tt.line)
			assert.Equal(t, tt.wantID, p.ID)
			assert.Equal(t, tt.wantSev, p.Severity)
		})
	}
}

func TestAlertPatterns_Rust(t *testing.T) {
	tests := []struct {
		line    string
		wantID  string
		wantSev AlertSeverity
	}{
		{"error[E0308]: mismatched types", "rust-error-code", AlertSeverityError},
		{"error: aborting due to previous error", "rust-aborting", AlertSeverityError},
		{"RUST_BACKTRACE=1 cargo run", "rust-backtrace-hint", AlertSeverityWarning},
		{"thread 'main' panicked at 'index out of bounds', src/main.rs:10:5", "rust-thread-panic", AlertSeverityError},
	}

	for _, tt := range tests {
		t.Run(tt.wantID, func(t *testing.T) {
			p := matchLine(tt.line)
			require.NotNil(t, p, "expected match for: %s", tt.line)
			assert.Equal(t, tt.wantID, p.ID)
			assert.Equal(t, tt.wantSev, p.Severity)
		})
	}
}

func TestAlertPatterns_Java(t *testing.T) {
	tests := []struct {
		line    string
		wantID  string
		wantSev AlertSeverity
	}{
		{"Exception in thread \"main\" java.lang.NullPointerException", "java-exception-in-thread", AlertSeverityError},
		{"BUILD FAILURE", "java-build-failure", AlertSeverityError},
		{"Caused by: java.io.FileNotFoundException: config.xml (No such file)", "java-caused-by", AlertSeverityError},
		{"SEVERE: Exception sending context destroyed event to listener", "java-severe", AlertSeverityError},
		{"java.lang.OutOfMemoryError: Java heap space", "java-lang-exception", AlertSeverityError},
	}

	for _, tt := range tests {
		t.Run(tt.wantID, func(t *testing.T) {
			p := matchLine(tt.line)
			require.NotNil(t, p, "expected match for: %s", tt.line)
			assert.Equal(t, tt.wantID, p.ID)
			assert.Equal(t, tt.wantSev, p.Severity)
		})
	}
}

func TestAlertPatterns_Ruby(t *testing.T) {
	tests := []struct {
		line    string
		wantID  string
		wantSev AlertSeverity
	}{
		// RuntimeError: matches python-runtime-error (^RuntimeError:) which appears first;
		// both Python and Ruby share this error format so the existing python pattern covers it.
		{"RuntimeError: some runtime error occurred", "python-runtime-error", AlertSeverityError},
		{"NoMethodError: undefined method 'foo' for nil:NilClass", "ruby-no-method-error", AlertSeverityError},
		{"ArgumentError: wrong number of arguments (given 2, expected 1)", "ruby-argument-error", AlertSeverityError},
		{"LoadError: cannot load such file -- nokogiri", "ruby-load-error", AlertSeverityError},
		{"Errno::ENOENT: No such file or directory", "ruby-errno", AlertSeverityError},
	}

	for _, tt := range tests {
		t.Run(tt.wantID, func(t *testing.T) {
			p := matchLine(tt.line)
			require.NotNil(t, p, "expected match for: %s", tt.line)
			assert.Equal(t, tt.wantID, p.ID)
			assert.Equal(t, tt.wantSev, p.Severity)
		})
	}
}

func TestAlertPatterns_DefaultCountUpdated(t *testing.T) {
	patterns := DefaultAlertPatterns()
	assert.True(t, len(patterns) >= 45, "should have at least 45 default patterns after extension, got %d", len(patterns))
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestAlertScanner_Inject_SharesPendingPipeline verifies that injected
// matches share the same dedup/batch/activity-defer queue as regex-matched
// matches — the canonical-queue invariant from the messaging-queue skill.
func TestAlertScanner_Inject_SharesPendingPipeline(t *testing.T) {
	var batches []*AlertBatch
	var mu sync.Mutex

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  30 * time.Millisecond,
		DedupeWindow: 5 * time.Second,
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			batches = append(batches, b)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	pat := &AlertPattern{
		ID:       "browser-js-error",
		Severity: AlertSeverityError,
		Category: "browser",
	}
	for i := 0; i < 5; i++ {
		scanner.Inject(&AlertMatch{
			Pattern:      pat,
			Line:         "TypeError: foo is undefined",
			Timestamp:    time.Now(),
			ScriptID:     "browser:dev",
			Source:       AlertSourceBrowser,
			RenderedText: "[browser-js error]\n  TypeError: foo is undefined\n",
		})
	}

	time.Sleep(120 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, batches, 1, "five identical injects should dedup to one batch")
	require.Len(t, batches[0].Matches, 1, "dedup should collapse to one match")
	assert.Equal(t, AlertSourceBrowser, batches[0].Matches[0].Source)
}

func TestAlertScanner_Inject_RespectsActivityDefer(t *testing.T) {
	var batches int32
	state := atomic.Int32{}
	state.Store(int32(ActivityActive))

	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:   20 * time.Millisecond,
		DedupeWindow:  5 * time.Second,
		RetryInterval: 30 * time.Millisecond,
		ActivityState: func() ActivityState {
			return ActivityState(state.Load())
		},
		OnAlert: func(*AlertBatch) {
			atomic.AddInt32(&batches, 1)
		},
	})
	defer scanner.Stop()

	scanner.Inject(&AlertMatch{
		Pattern:   &AlertPattern{ID: "browser-js-error", Severity: AlertSeverityError},
		Line:      "Uncaught ReferenceError",
		Timestamp: time.Now(),
		ScriptID:  "browser:dev",
		Source:    AlertSourceBrowser,
	})

	// Active → flush should defer; nothing delivered yet.
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, int32(0), atomic.LoadInt32(&batches), "active state must defer")

	// Go idle; deferred flush must deliver.
	state.Store(int32(ActivityIdle))
	time.Sleep(150 * time.Millisecond)
	assert.GreaterOrEqual(t, atomic.LoadInt32(&batches), int32(1), "idle must release deferred batch")
}

func TestAlertBatch_Format_RenderedTextOverridesDefault(t *testing.T) {
	batch := &AlertBatch{
		ScriptID: "browser:dev",
		Matches: []*AlertMatch{
			{
				Pattern:      &AlertPattern{ID: "browser-js-error", Severity: AlertSeverityError},
				Line:         "TypeError: x is undefined",
				Source:       AlertSourceBrowser,
				RenderedText: "[browser-js error]\n  TypeError: x is undefined\n",
			},
		},
	}
	out := batch.Format()
	// When RenderedText is set, the script-alert wrapper must NOT appear —
	// otherwise we leak the process-output framing into browser/http events.
	assert.NotContains(t, out, "Script ")
	assert.Contains(t, out, "[browser-js error]")
	assert.Contains(t, out, "TypeError: x is undefined")
}

// --- [G1] RED: real errors that match no AlertScanner pattern are invisible ---
//
// These tests are RED on purpose (get_errors epic, child G1). They prove the
// confirmed gap: the pattern bank has ZERO database/ORM patterns and the
// scanner is line-by-line with no unclassified catch-all, so a real Prisma
// auth failure is silently dropped — get_errors then reports a false "all
// clear" on the actual broken session. G2 (unparsed catch-all) + G3
// (structured DB/ORM parsers) turn these GREEN.

// prismaAuthBlock is a representative multi-line Prisma client error: the
// signal line ("Authentication failed…") is separated from the invocation
// anchor lines, exactly the multi-line shape the line-by-line scanner loses.
var prismaAuthBlock = []string{
	"prisma:error",
	"Invalid `prisma.user.findUnique()` invocation in",
	"/app/src/db.ts:42:18",
	"",
	"Authentication failed against database server, the provided database credentials for 'ai_user' are not valid.",
}

const prismaAuthSignal = "Authentication failed against database server, the provided database credentials for 'ai_user' are not valid."

// TestG1_DBAuthErrorSurfaced: a genuine Prisma DB-auth credential failure must
// SURFACE through the scanner so get_errors never reports a false "all clear".
//
// G2 calibration decision (option a, per the task handoff): this test asserts
// BEHAVIOR (the error is surfaced, not dropped), not MECHANISM (a specific
// pattern-bank anchor classifies it). The default pattern bank has no DB/ORM
// anchor — adding one is G3's structured-parser job. G2 guarantees the line is
// not silently lost via the unparsed catch-all, which is exactly the
// "get_errors surfaces it" acceptance wording. When G3 lands a Prisma anchor,
// the surfaced match's category upgrades from "unparsed" to a DB category, but
// the behavioral guarantee this test pins stays true.
func TestG1_DBAuthErrorSurfaced(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  50 * time.Millisecond,
		DedupeWindow: 60 * time.Second,
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	scanner.ProcessLine(prismaAuthSignal, "api")
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	var matched *AlertMatch
	for _, b := range received {
		for _, m := range b.Matches {
			if strings.Contains(m.Line, "Authentication failed against database server") {
				matched = m
			}
		}
	}
	require.NotNil(t, matched,
		"the Prisma DB-auth error %q must surface (it is a real credential failure) — get_errors must not report all-clear", prismaAuthSignal)
	require.NotNil(t, matched.Pattern, "surfaced match must carry a pattern for severity/dedup keying")
	assert.Equal(t, AlertSeverityError, matched.Pattern.Severity,
		"a DB-auth failure must surface at error severity")
}

// TestG1_RED_MultiLineDBErrorSurfaces: feeding the full multi-line Prisma block
// through the scanner must surface at least one alert that carries the auth
// signal. RED today — no line matches a pattern and there is no multi-line
// correlation, so the whole block is dropped.
func TestG1_RED_MultiLineDBErrorSurfaces(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  50 * time.Millisecond,
		DedupeWindow: 60 * time.Second,
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	for _, line := range prismaAuthBlock {
		scanner.ProcessLine(line, "api")
	}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	var sawSignal bool
	for _, b := range received {
		for _, m := range b.Matches {
			if strings.Contains(m.Line, "Authentication failed against database server") {
				sawSignal = true
			}
		}
	}
	require.True(t, sawSignal,
		"RED: the multi-line Prisma block produced no alert carrying the auth signal — multi-line DB errors are dropped")
}

// TestG1_RED_UnclassifiedStderrSurfacedAsUnparsed: an error-looking line that
// matches no pattern must still surface (marked as unparsed) so nothing is
// silently dropped. RED today — ProcessLine drops every unmatched line.
func TestG1_RED_UnclassifiedStderrSurfacedAsUnparsed(t *testing.T) {
	var received []*AlertBatch
	var mu sync.Mutex
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  50 * time.Millisecond,
		DedupeWindow: 60 * time.Second,
		OnAlert: func(b *AlertBatch) {
			mu.Lock()
			received = append(received, b)
			mu.Unlock()
		},
	})
	defer scanner.Stop()

	scanner.ProcessLine(prismaAuthSignal, "api")
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	var total int
	for _, b := range received {
		total += len(b.Matches)
	}
	require.Greater(t, total, 0,
		"RED: an unclassified error-looking stderr line produced no alert — there is no unparsed catch-all, so real errors are silently dropped")
}

// fakeClock is a manually-advanced clock for deterministically exercising
// time-window boundaries (e.g. AlertScanner.dedupeWindow) without depending
// on real elapsed wall time, which is unreliable under host scheduling load.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
