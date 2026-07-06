package scripts

import (
	"strings"
	"testing"
)

func TestVersionInjection(t *testing.T) {
	// Set version like main.go does
	Version = "0.11.2"

	// Build the script fresh (not using GetCombinedScript which caches)
	combined := buildCombinedScript(RoleFull)

	// Check for the version declaration line
	expectedLine := "window.__devtool_version = '" + Version + "';"
	if !strings.Contains(combined, expectedLine) {
		// Find what version line exists
		lines := strings.Split(combined, "\n")
		for i, line := range lines {
			if strings.Contains(line, "window.__devtool_version =") {
				t.Errorf("Expected %q but found line %d: %s", expectedLine, i, line)
				return
			}
		}
		t.Errorf("Version injection line not found")
	}
}
