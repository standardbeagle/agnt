package tools

import (
	"strings"
	"testing"
)

// TestBuildScreenshotExecCode pins the contract with core.js: ProxyExec source
// is a Promise expression, not top-level await. core.js owns Promise settling.
func TestBuildScreenshotExecCode(t *testing.T) {
	code := buildScreenshotExecCode(`home "wide"`, true, `.card[data-kind="hero"]`)

	if strings.HasPrefix(strings.TrimSpace(code), "await ") {
		t.Fatalf("classic-script exec source contains invalid top-level await: %s", code)
	}
	for _, want := range []string{
		`__devtool.screenshot(`,
		`"fullPage":true`,
		`"name":"home \"wide\""`,
		`"selector":".card[data-kind=\"hero\"]"`,
	} {
		if !strings.Contains(code, want) {
			t.Errorf("screenshot exec source missing %q: %s", want, code)
		}
	}
}
