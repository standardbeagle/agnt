package project

import "strings"

// SignalHints predicts which `proc wait` signals are most useful for a script,
// plus a default timeout (in milliseconds) appropriate for the script category.
//
// Heuristics (matched in priority order against script name + command):
//
//	dev/start/serve/watch         → ["url","ready","port"], 30s
//	build/compile/tsc/cargo/go bld → ["error","warning"],   60s
//	test/jest/vitest/pytest/go t  → ["error","ready"],      120s
//	lint/check/fmt/format         → ["error","warning"],    30s
//
// Anything that doesn't match returns ["error","ready"] with 60s — a safe
// generic default that surfaces failures and completion without committing
// to dev-server semantics.
type SignalHints struct {
	Signals   []string
	TimeoutMs int
}

// PredictSignals returns the most likely useful `proc wait` signals for a
// script, derived from its name and command string. Match is case-insensitive.
func PredictSignals(name, command string) SignalHints {
	hay := strings.ToLower(name + " " + command)

	// Dev / serve / watch — long-running servers, want url+ready+port.
	if containsAnyWord(hay, "dev", "start", "serve", "watch") {
		return SignalHints{
			Signals:   []string{"url", "ready", "port"},
			TimeoutMs: 30000,
		}
	}

	// Build / compile — one-shot, want error+warning surfaced.
	if containsAnyWord(hay, "build", "compile", "tsc") {
		return SignalHints{
			Signals:   []string{"error", "warning"},
			TimeoutMs: 60000,
		}
	}

	// Test — long one-shot, want error first then completion ready signal.
	if containsAnyWord(hay, "test", "jest", "vitest", "pytest", "mocha") {
		return SignalHints{
			Signals:   []string{"error", "ready"},
			TimeoutMs: 120000,
		}
	}

	// Lint / fmt / check — one-shot, errors and warnings.
	if containsAnyWord(hay, "lint", "check", "fmt", "format") {
		return SignalHints{
			Signals:   []string{"error", "warning"},
			TimeoutMs: 30000,
		}
	}

	// Generic fallback.
	return SignalHints{
		Signals:   []string{"error", "ready"},
		TimeoutMs: 60000,
	}
}

// containsAnyWord reports whether haystack contains any of the given tokens
// as a whitespace-bounded word. Avoids false-positives like "untestable"
// matching "test" or "discovery" matching "serve".
func containsAnyWord(haystack string, words ...string) bool {
	for _, w := range words {
		if hasWord(haystack, w) {
			return true
		}
	}
	return false
}

// hasWord reports whether word appears as a token in haystack, where tokens
// are separated by whitespace or any of: '-', '_', ':', '/', '.'.
// Both inputs are assumed lowercase.
func hasWord(haystack, word string) bool {
	if word == "" {
		return false
	}
	for {
		i := strings.Index(haystack, word)
		if i < 0 {
			return false
		}
		// Boundary check: char before must be a separator or string start.
		startOK := i == 0 || isWordSep(haystack[i-1])
		end := i + len(word)
		endOK := end == len(haystack) || isWordSep(haystack[end])
		if startOK && endOK {
			return true
		}
		haystack = haystack[i+1:]
	}
}

func isWordSep(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '-', '_', ':', '/', '.':
		return true
	}
	return false
}
