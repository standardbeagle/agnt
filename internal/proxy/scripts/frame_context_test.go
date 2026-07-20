package scripts

import (
	"strings"
	"testing"
)

func TestFrameContextAdapterContract(t *testing.T) {
	for _, symbol := range []string{
		"window.__devtool_context = context",
		"isChrome: function()",
		"isContent: function()",
		"shell: function()",
		"shellExport: function(name)",
		"contentFrame: function()",
		"activeContent: function()",
		"cleanURL: function(raw)",
		"contentURL: function(raw, id)",
		"syncURL: function(raw)",
	} {
		if !strings.Contains(framesJS, symbol) {
			t.Errorf("frame context adapter missing %q", symbol)
		}
	}
}

func TestFirstPartyModulesDoNotReinterpretFrameTopology(t *testing.T) {
	for name, script := range moduleScript {
		if name == "frames" || name == "axe-core" || name == "html2canvas-pro" {
			continue
		}
		for _, forbidden := range []string{
			"window.top", "window.parent", "window.self", "window.frameElement",
			"window.__devtool_frame_role", "window.__devtool_frame_id",
			"window.__devtool_frames", "__devtool_content_frame",
		} {
			if strings.Contains(script, forbidden) {
				t.Errorf("module %q bypasses frame-context adapter with %q", name, forbidden)
			}
		}
	}
}

func TestFrameContextAdapterLoadsBeforeConsumers(t *testing.T) {
	for _, role := range []Role{RoleFull, RoleChrome, RoleContent} {
		bundle := GetCombinedScriptForRole(role)
		adapter := strings.Index(bundle, "window.__devtool_context = context")
		core := strings.Index(bundle, "// core module")
		if adapter < 0 || core < 0 || adapter > core {
			t.Errorf("role %q must load frame adapter before core", role)
		}
	}
}
