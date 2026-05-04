package project

import (
	"reflect"
	"testing"
)

func TestPredictSignals(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		command string
		want    SignalHints
	}{
		// Dev / serve family
		{"npm dev", "dev", "npm run dev", SignalHints{Signals: []string{"url", "ready", "port"}, TimeoutMs: 30000}},
		{"next dev", "dev", "next dev", SignalHints{Signals: []string{"url", "ready", "port"}, TimeoutMs: 30000}},
		{"start", "start", "npm start", SignalHints{Signals: []string{"url", "ready", "port"}, TimeoutMs: 30000}},
		{"serve", "serve", "npm run serve", SignalHints{Signals: []string{"url", "ready", "port"}, TimeoutMs: 30000}},
		{"watch", "watch", "tsc --watch", SignalHints{Signals: []string{"url", "ready", "port"}, TimeoutMs: 30000}},

		// Build / compile family — must take precedence over generic words.
		{"npm build", "build", "npm run build", SignalHints{Signals: []string{"error", "warning"}, TimeoutMs: 60000}},
		{"next build", "build", "next build", SignalHints{Signals: []string{"error", "warning"}, TimeoutMs: 60000}},
		{"go build", "build", "go build ./...", SignalHints{Signals: []string{"error", "warning"}, TimeoutMs: 60000}},
		{"cargo build", "build", "cargo build", SignalHints{Signals: []string{"error", "warning"}, TimeoutMs: 60000}},
		{"tsc", "typecheck", "tsc --noEmit", SignalHints{Signals: []string{"error", "warning"}, TimeoutMs: 60000}},

		// Test family
		{"npm test", "test", "npm test", SignalHints{Signals: []string{"error", "ready"}, TimeoutMs: 120000}},
		{"jest", "test", "jest --ci", SignalHints{Signals: []string{"error", "ready"}, TimeoutMs: 120000}},
		{"vitest", "test", "vitest run", SignalHints{Signals: []string{"error", "ready"}, TimeoutMs: 120000}},
		{"pytest", "test", "pytest -v", SignalHints{Signals: []string{"error", "ready"}, TimeoutMs: 120000}},
		{"go test", "test", "go test ./...", SignalHints{Signals: []string{"error", "ready"}, TimeoutMs: 120000}},

		// Lint / fmt / check family
		{"lint", "lint", "eslint .", SignalHints{Signals: []string{"error", "warning"}, TimeoutMs: 30000}},
		{"fmt-check", "fmt-check", "gofmt -l .", SignalHints{Signals: []string{"error", "warning"}, TimeoutMs: 30000}},
		{"format", "format", "prettier --check .", SignalHints{Signals: []string{"error", "warning"}, TimeoutMs: 30000}},
		{"check", "check", "cargo check", SignalHints{Signals: []string{"error", "warning"}, TimeoutMs: 30000}},

		// Fallback for unrecognized commands.
		{"install", "install", "npm install", SignalHints{Signals: []string{"error", "ready"}, TimeoutMs: 60000}},
		{"unknown", "frobnicate", "do-the-thing", SignalHints{Signals: []string{"error", "ready"}, TimeoutMs: 60000}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PredictSignals(tc.script, tc.command)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("PredictSignals(%q, %q)\n  got:  %+v\n  want: %+v", tc.script, tc.command, got, tc.want)
			}
		})
	}
}

func TestPredictSignals_PriorityOrder(t *testing.T) {
	// "build" must beat the generic fallback even when the script is
	// arbitrary like "production".
	got := PredictSignals("production", "next build")
	want := SignalHints{Signals: []string{"error", "warning"}, TimeoutMs: 60000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PredictSignals: got %+v, want %+v", got, want)
	}

	// "watch" inside "tsc --watch" should hit dev/serve, not build/tsc,
	// because watchers are long-running servers.
	got = PredictSignals("typecheck", "tsc --watch")
	want = SignalHints{Signals: []string{"url", "ready", "port"}, TimeoutMs: 30000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PredictSignals(watch precedence): got %+v, want %+v", got, want)
	}
}

func TestHasWord_Boundaries(t *testing.T) {
	// Token must be whitespace/punctuation-bounded, not a substring.
	cases := []struct {
		hay   string
		word  string
		want  bool
		label string
	}{
		{"npm test", "test", true, "exact"},
		{"untestable", "test", false, "leading-letter"},
		{"tester", "test", false, "trailing-letter"},
		{"go-test", "test", true, "dash"},
		{"npm/test", "test", true, "slash"},
		{"npm.test", "test", true, "dot"},
		{"test", "test", true, "alone"},
		{"", "test", false, "empty hay"},
		{"test", "", false, "empty word"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			if got := hasWord(c.hay, c.word); got != c.want {
				t.Errorf("hasWord(%q, %q) = %v, want %v", c.hay, c.word, got, c.want)
			}
		})
	}
}
