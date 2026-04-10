package scripts

import (
	"strings"
	"testing"
)

func TestResponsiveScriptEmbedded(t *testing.T) {
	// Build the script fresh
	combined := buildCombinedScript()

	// Check for responsive module presence
	if !strings.Contains(combined, "__devtool_responsive") {
		t.Error("Responsive module not found in combined script")
	}

	// Check for responsiveAudit API function
	if !strings.Contains(combined, "responsiveAudit") {
		t.Error("responsiveAudit API function not found in combined script")
	}

	// Check for audit function
	if !strings.Contains(combined, "function audit(options)") {
		t.Error("audit function not found in combined script")
	}
}

func TestResponsiveInScriptNames(t *testing.T) {
	names := GetScriptNames()
	found := false
	for _, name := range names {
		if name == "responsive.js" {
			found = true
			break
		}
	}
	if !found {
		t.Error("responsive.js not found in GetScriptNames()")
	}
}

func TestResponsiveModuleOrder(t *testing.T) {
	combined := buildCombinedScript()

	// Responsive module should come after wireframe and before api
	responsiveIdx := strings.Index(combined, "// responsive module")
	if responsiveIdx == -1 {
		t.Error("Responsive module comment not found")
		return
	}

	wireframeIdx := strings.Index(combined, "// wireframe module")
	if wireframeIdx == -1 {
		t.Error("Wireframe module comment not found")
		return
	}

	apiIdx := strings.Index(combined, "// api module")
	if apiIdx == -1 {
		t.Error("API module comment not found")
		return
	}

	// Verify order: wireframe < responsive < api
	if wireframeIdx > responsiveIdx {
		t.Error("Wireframe module should come before responsive module")
	}
	if responsiveIdx > apiIdx {
		t.Error("Responsive module should come before API module")
	}
}
