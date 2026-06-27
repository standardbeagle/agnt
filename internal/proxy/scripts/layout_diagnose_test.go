package scripts

import (
	"strings"
	"testing"
)

// TestLayoutDiagnoseEmbedded guards that the cause→symptom layout diagnostic and
// its four checks ship in the combined bundle and are reachable on the public
// __devtool API. Behavior is verified by the real-browser e2e in internal/proxy;
// this is the fast presence/wiring check.
func TestLayoutDiagnoseEmbedded(t *testing.T) {
	combined := buildCombinedScript()

	for _, needle := range []string{
		"function diagnose(",
		"diagnose: diagnose",                    // exported on __devtool_layout
		"diagnoseLayoutIssues: layout.diagnose", // wired onto the public API
		"containing-block-trap",
		"ineffective-zindex",
		"click-interception",
		"clipped-descendant",
	} {
		if !strings.Contains(combined, needle) {
			t.Errorf("combined bundle missing %q", needle)
		}
	}
}
