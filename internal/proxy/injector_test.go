package proxy

import (
	"bytes"
	"strings"
	"testing"
)

func TestShouldInject(t *testing.T) {
	tests := []struct {
		contentType string
		expected    bool
	}{
		{"text/html", true},
		{"text/html; charset=utf-8", true},
		{"TEXT/HTML", true},
		{"application/json", false},
		{"text/plain", false},
		{"application/javascript", false},
		{"image/png", false},
	}

	for _, tt := range tests {
		t.Run(tt.contentType, func(t *testing.T) {
			result := ShouldInject(tt.contentType)
			if result != tt.expected {
				t.Errorf("ShouldInject(%q) = %v, expected %v", tt.contentType, result, tt.expected)
			}
		})
	}
}

func TestInjectInstrumentation_BeforeHeadClose(t *testing.T) {
	html := []byte(`<!DOCTYPE html>
<html>
<head>
<title>Test</title>
</head>
<body>
<h1>Hello World</h1>
</body>
</html>`)

	result := InjectInstrumentation(html, 8080)

	// Should inject before </head>
	if !bytes.Contains(result, []byte("<script>")) {
		t.Error("Script not injected")
	}

	// Script should appear before </head>
	scriptIdx := bytes.Index(result, []byte("<script>"))
	headCloseIdx := bytes.Index(result, []byte("</head>"))

	if scriptIdx == -1 {
		t.Error("Script tag not found")
	}
	if headCloseIdx == -1 {
		t.Error("</head> tag not found")
	}
	if scriptIdx >= headCloseIdx {
		t.Error("Script should be injected before </head>")
	}
}

func TestInjectInstrumentation_AfterHeadOpen(t *testing.T) {
	html := []byte(`<!DOCTYPE html>
<html>
<head><title>Test</title>
<body>
<h1>Hello World</h1>
</body>
</html>`)

	result := InjectInstrumentation(html, 8080)

	// Should inject after <head>
	if !bytes.Contains(result, []byte("<script>")) {
		t.Error("Script not injected")
	}

	// Script should appear after <head>
	headOpenIdx := bytes.Index(result, []byte("<head>"))
	scriptIdx := bytes.Index(result, []byte("<script>"))

	if headOpenIdx == -1 {
		t.Error("<head> tag not found")
	}
	if scriptIdx == -1 {
		t.Error("Script tag not found")
	}
	if scriptIdx <= headOpenIdx+6 {
		t.Error("Script should be injected after <head>")
	}
}

func TestInjectInstrumentation_AfterBody(t *testing.T) {
	html := []byte(`<!DOCTYPE html>
<html>
<body>
<h1>Hello World</h1>
</body>
</html>`)

	result := InjectInstrumentation(html, 8080)

	// Should inject after <body>
	if !bytes.Contains(result, []byte("<script>")) {
		t.Error("Script not injected")
	}

	// Script should appear after <body>
	bodyOpenIdx := bytes.Index(result, []byte("<body>"))
	scriptIdx := bytes.Index(result, []byte("<script>"))

	if bodyOpenIdx == -1 {
		t.Error("<body> tag not found")
	}
	if scriptIdx == -1 {
		t.Error("Script tag not found")
	}
	if scriptIdx <= bodyOpenIdx+6 {
		t.Error("Script should be injected after <body>")
	}
}

func TestInjectInstrumentation_BodyWithAttributes(t *testing.T) {
	html := []byte(`<!DOCTYPE html>
<html>
<body class="page" id="main">
<h1>Hello World</h1>
</body>
</html>`)

	result := InjectInstrumentation(html, 8080)

	// Should inject after <body ...>
	if !bytes.Contains(result, []byte("<script>")) {
		t.Error("Script not injected")
	}

	// Should still contain original body tag
	if !bytes.Contains(result, []byte(`<body class="page" id="main">`)) {
		t.Error("Original body tag modified")
	}
}

func TestInjectInstrumentation_MinimalHTML(t *testing.T) {
	html := []byte(`<html><body>Hello</body></html>`)

	result := InjectInstrumentation(html, 8080)

	// Should inject somewhere
	if !bytes.Contains(result, []byte("<script>")) {
		t.Error("Script not injected")
	}
}

func TestInjectInstrumentation_NoHTML(t *testing.T) {
	html := []byte(`Hello World`)

	result := InjectInstrumentation(html, 8080)

	// Should prepend script as last resort
	if !bytes.Contains(result, []byte("<script>")) {
		t.Error("Script not injected")
	}

	// Script should be at the beginning (checking for html2canvas script tag)
	scriptIdx := bytes.Index(result, []byte("<script"))
	if scriptIdx == -1 {
		t.Error("Script tag not found")
	}
	if scriptIdx > 10 { // Allow for some whitespace/newlines
		t.Error("Script should be prepended when no HTML tags found")
	}

	// Original content should still be there
	if !bytes.Contains(result, []byte("Hello World")) {
		t.Error("Original content missing")
	}
}

func TestInjectInstrumentation_ScriptContent(t *testing.T) {
	html := []byte(`<!DOCTYPE html>
<html>
<head></head>
<body></body>
</html>`)

	result := InjectInstrumentation(html, 8080)

	resultStr := string(result)

	// Check for key instrumentation features
	expectedFeatures := []string{
		"WebSocket",
		"error",
		"performance",
		"window.addEventListener",
		"unhandledrejection",
		"window.location.host", // Should use relative URL with current host
		"__devtool_metrics",
	}

	for _, feature := range expectedFeatures {
		if !strings.Contains(resultStr, feature) {
			t.Errorf("Injected script missing feature: %s", feature)
		}
	}
}

func TestInjectInstrumentation_PreservesOriginalContent(t *testing.T) {
	html := []byte(`<!DOCTYPE html>
<html>
<head>
<title>Test Page</title>
<meta charset="utf-8">
</head>
<body>
<h1>Hello World</h1>
<p>This is a test.</p>
</body>
</html>`)

	result := InjectInstrumentation(html, 8080)

	// Original content should still be present
	expectedContent := []string{
		"<!DOCTYPE html>",
		"<title>Test Page</title>",
		"<meta charset=\"utf-8\">",
		"<h1>Hello World</h1>",
		"<p>This is a test.</p>",
	}

	resultStr := string(result)
	for _, content := range expectedContent {
		if !strings.Contains(resultStr, content) {
			t.Errorf("Original content missing: %s", content)
		}
	}
}

func TestInjectInstrumentation_DifferentPorts(t *testing.T) {
	html := []byte(`<!DOCTYPE html><html><head></head></html>`)

	ports := []int{8080, 3000, 9000}

	for _, port := range ports {
		result := InjectInstrumentation(html, port)
		resultStr := string(result)

		// With the new relative URL approach, the script should use window.location.host
		// which automatically includes the current port, making the port parameter obsolete
		expectedPattern := "window.location.host + '/__devtool_metrics'"

		if !strings.Contains(resultStr, expectedPattern) {
			t.Errorf("Script should use window.location.host for WebSocket URL (port-independent)")
		}
	}
}
