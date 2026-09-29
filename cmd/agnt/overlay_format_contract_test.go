package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// Contract: every OverlayEvent type the proxy package can actually emit
// (internal/proxy/overlay.go, OverlayNotifier.NotifyXxx) must have a branch
// in formatProxyEventText's switch — otherwise it silently falls to
// `default` and never reaches the agent's PTY stdin (only a debug.Error, or
// worse, nothing at all for a deliberately-silent case).
//
// A type formatProxyEventText deliberately treats as agent-silent (e.g.
// responsive_state, which is proxylog-only telemetry) still needs an
// explicit case — see the switch — so this test does not require every
// covered type to produce non-empty text, only that dispatch does not fall
// through to the "unhandled" default branch.
func TestFormatProxyEventText_CoversEveryOverlayNotifierEventType(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	overlayGoPath := filepath.Join(filepath.Dir(thisFile), "..", "..", "internal", "proxy", "overlay.go")
	src, err := os.ReadFile(overlayGoPath)
	if err != nil {
		t.Fatalf("reading %s: %v", overlayGoPath, err)
	}

	// Every NotifyXxx func builds an OverlayEvent{Type: "literal", ...}.
	typeRe := regexp.MustCompile(`Type:\s+"([a-zA-Z0-9_]+)",`)
	matches := typeRe.FindAllStringSubmatch(string(src), -1)
	if len(matches) == 0 {
		t.Fatalf("no OverlayEvent{Type: \"...\"} literals found in %s — regex likely broken", overlayGoPath)
	}

	caseRe := regexp.MustCompile(`(?m)^\tcase "([a-zA-Z0-9_]+)":`)
	caseMatches := caseRe.FindAllStringSubmatch(formatProxyEventTextSource(t), -1)
	handled := map[string]bool{}
	for _, m := range caseMatches {
		handled[m[1]] = true
	}

	var missing []string
	seen := map[string]bool{}
	for _, m := range matches {
		eventType := m[1]
		if seen[eventType] {
			continue
		}
		seen[eventType] = true
		if !handled[eventType] {
			missing = append(missing, eventType)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("overlay.go emits OverlayEvent type(s) with no case in formatProxyEventText's switch: %v", missing)
	}
}

// formatProxyEventTextSource returns the source of this file's own package
// (overlay_format.go) so the switch cases can be matched by the same
// regex-over-source technique as the OverlayEvent literals, rather than
// hand-maintaining a duplicate list that drifts.
func formatProxyEventTextSource(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "overlay_format.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(src)
}
