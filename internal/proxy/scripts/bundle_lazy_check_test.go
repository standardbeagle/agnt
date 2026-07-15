package scripts

import (
	"strings"
	"testing"
)

// libSignature is a string that appears ONLY inside the html2canvas-pro library
// source (its copyright banner), never in the loader or any module call site.
const libSignature = "html2canvas-pro 1.5.8"

// TestHtml2CanvasIsLazyNotBundled asserts the split: the always-injected dev
// bundles carry the on-demand loader symbol but NOT the ~211KB html2canvas
// library body, which is now served separately via GetHtml2Canvas /
// /__devtool_html2canvas.
func TestHtml2CanvasIsLazyNotBundled(t *testing.T) {
	lib := GetHtml2Canvas()
	if !strings.Contains(lib, libSignature) {
		t.Fatalf("GetHtml2Canvas() does not contain the library signature %q", libSignature)
	}

	for _, role := range []Role{RoleFull, RoleContent, RoleChrome} {
		b := GetCombinedScriptForRole(role)
		if !strings.Contains(b, "__devtool_ensureHtml2canvas") {
			t.Errorf("role %q bundle is missing the on-demand loader symbol", role)
		}
		if strings.Contains(b, libSignature) {
			t.Errorf("role %q bundle still inlines the html2canvas library body", role)
		}
	}

	// The public bundle ships neither the loader nor the library.
	pub := GetCombinedScriptForRole(RolePublic)
	if strings.Contains(pub, "__devtool_ensureHtml2canvas") {
		t.Error("public bundle must not ship the html2canvas loader")
	}
	if strings.Contains(pub, libSignature) {
		t.Error("public bundle must not ship the html2canvas library")
	}
}
