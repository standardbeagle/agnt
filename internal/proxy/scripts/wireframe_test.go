package scripts

import (
	"strings"
	"testing"
)

// TestWireframeScriptEmbedded verifies the wireframe.js is properly embedded
func TestWireframeScriptEmbedded(t *testing.T) {
	// Verify the embedded script is not empty
	if wireframeJS == "" {
		t.Fatal("wireframeJS is empty - file not embedded")
	}

	// Check for key module components
	expectedPatterns := []string{
		"__devtool_wireframe",
		"generateWireframe",
		"generateMinimalWireframe",
		"generateSemanticWireframe",
		"SEMANTIC_COLORS",
		"DEFAULT_CONFIG",
	}

	for _, pattern := range expectedPatterns {
		if !strings.Contains(wireframeJS, pattern) {
			t.Errorf("wireframe.js missing expected pattern: %s", pattern)
		}
	}
}

// TestWireframeScriptInCombined verifies wireframe is included in combined script
func TestWireframeScriptInCombined(t *testing.T) {
	combined := GetCombinedScript()

	// Verify the combined script includes wireframe module
	if !strings.Contains(combined, "__devtool_wireframe") {
		t.Error("Combined script missing __devtool_wireframe")
	}

	// Verify wireframe functions are accessible from the API
	expectedAPIMethods := []string{
		"generateWireframe",
		"generateMinimalWireframe",
		"generateSemanticWireframe",
	}

	for _, method := range expectedAPIMethods {
		if !strings.Contains(combined, method) {
			t.Errorf("Combined script missing API method: %s", method)
		}
	}
}

// TestWireframeScriptOrder verifies wireframe loads before API
func TestWireframeScriptOrder(t *testing.T) {
	combined := GetCombinedScript()

	wireframeIdx := strings.Index(combined, "// Wireframe generation module")
	apiIdx := strings.Index(combined, "// API assembly module")

	if wireframeIdx == -1 {
		t.Error("Wireframe module comment not found in combined script")
	}
	if apiIdx == -1 {
		t.Error("API module comment not found in combined script")
	}

	if wireframeIdx >= apiIdx {
		t.Error("Wireframe module should load before API module")
	}
}

// TestWireframeInScriptNames verifies wireframe is listed in script names
func TestWireframeInScriptNames(t *testing.T) {
	names := GetScriptNames()

	found := false
	for _, name := range names {
		if name == "wireframe.js" {
			found = true
			break
		}
	}

	if !found {
		t.Error("wireframe.js not found in GetScriptNames()")
	}
}

// TestWireframeSVGGeneration verifies the script contains valid SVG generation logic
func TestWireframeSVGGeneration(t *testing.T) {
	// Check for SVG-specific patterns
	svgPatterns := []string{
		`<svg`,
		`xmlns="http://www.w3.org/2000/svg"`,
		`viewBox`,
		`<rect`,
		`<text`,
		`</svg>`,
	}

	for _, pattern := range svgPatterns {
		if !strings.Contains(wireframeJS, pattern) {
			t.Errorf("wireframe.js missing SVG pattern: %s", pattern)
		}
	}
}

// TestWireframeConfigDefaults verifies default configuration is present
func TestWireframeConfigDefaults(t *testing.T) {
	configPatterns := []string{
		"maxDepth",
		"minWidth",
		"minHeight",
		"includeText",
		"viewportOnly",
		"colorScheme",
		"maxElements",
	}

	for _, pattern := range configPatterns {
		if !strings.Contains(wireframeJS, pattern) {
			t.Errorf("wireframe.js missing config option: %s", pattern)
		}
	}
}

// TestWireframeSemanticColors verifies semantic color definitions
func TestWireframeSemanticColors(t *testing.T) {
	colorTypes := []string{
		"header",
		"nav",
		"main",
		"footer",
		"button",
		"form",
		"link",
		"image",
		"heading",
	}

	for _, colorType := range colorTypes {
		if !strings.Contains(wireframeJS, colorType) {
			t.Errorf("wireframe.js missing semantic color type: %s", colorType)
		}
	}
}

// TestWireframeXMLEscaping verifies XML escaping function exists
func TestWireframeXMLEscaping(t *testing.T) {
	// Check for XML escaping patterns
	escapePatterns := []string{
		"escapeXml",
		"&amp;",
		"&lt;",
		"&gt;",
	}

	for _, pattern := range escapePatterns {
		if !strings.Contains(wireframeJS, pattern) {
			t.Errorf("wireframe.js missing XML escape pattern: %s", pattern)
		}
	}
}
