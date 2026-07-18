package classify

import "testing"

// TestBuildSuccessPattern_FirstMatchDiscrimination pins the retention
// contract: success lines must resolve to rebuild-build-success (which
// triggers error retirement) and start-of-build lines must NOT — the scanner
// is first-match-wins, so ordering in the rule bank is load-bearing.
func TestBuildSuccessPattern_FirstMatchDiscrimination(t *testing.T) {
	rules := DefaultLineRules()
	firstMatch := func(line string) string {
		for _, r := range rules {
			if r.Pattern.MatchString(line) {
				return r.ID
			}
		}
		return ""
	}

	successLines := []string{
		"Build succeeded.",
		"build completed in 3.2s",
		"Rebuild finished",
		"webpack compiled successfully in 1204 ms",
		"✓ built in 342ms",
		"running... ok",
		"Build OK",
	}
	for _, line := range successLines {
		if got := firstMatch(line); got != "rebuild-build-success" {
			t.Errorf("%q matched %q, want rebuild-build-success", line, got)
		}
	}

	startLines := []string{
		"Rebuilding...",
		"recompiling module graph",
		"compiling...",
		"watch: restarting due to file change",
	}
	for _, line := range startLines {
		if got := firstMatch(line); got == "rebuild-build-success" {
			t.Errorf("start-of-build line %q must not classify as build success", line)
		}
	}

	// "Build FAILED." must stay an error, never a rebuild signal.
	if got := firstMatch("Build FAILED."); got != "dotnet-build-error" {
		t.Errorf("Build FAILED. matched %q, want dotnet-build-error", got)
	}
}
