package classify

import "testing"

// A successful HMR update of a file whose path contains "error"/"fail" is not
// an HMR failure. `hmr.*(fail|error)` matched it and raised a build failure.
func TestLineRules_ViteHMRUpdateIsNotAFailure(t *testing.T) {
	rules := DefaultLineRules()
	firstMatch := func(line string) string {
		for _, r := range rules {
			if r.Pattern.MatchString(line) {
				return r.ID
			}
		}
		return ""
	}
	updates := []string{
		"10:32:15 AM [vite] hmr update /src/components/ErrorBoundary.tsx",
		"[vite] hmr update /src/app/error.tsx",
		"[vite] (client) hmr update /src/FailureBanner.tsx, /src/errors/index.ts",
		`[vite] hmr update C:\src\error.tsx`,
	}
	for _, line := range updates {
		if got := firstMatch(line); got != "rebuild-vite" {
			t.Errorf("%q matched %q, want rebuild-vite (a successful update)", line, got)
		}
	}

	failures := []string{
		"HMR update failed",
		"[vite] hmr error: failed to reload /src/App.tsx",
		"[hmr] Failed to reload /src/App.tsx. This could be due to syntax errors",
		"[vite] HMR failure",
	}
	for _, line := range failures {
		if got := firstMatch(line); got != "vite-hmr-fail" {
			t.Errorf("%q matched %q, want vite-hmr-fail", line, got)
		}
	}
}
