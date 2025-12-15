package proxy

import (
	"bytes"
	"strings"
	"sync"

	"github.com/standardbeagle/agnt/internal/proxy/scripts"
)

var (
	// Cache the instrumentation script since it never changes
	cachedScript     string
	cachedScriptOnce sync.Once
)

// instrumentationScript returns JavaScript code for error and performance monitoring.
// The script is cached after first call for performance.
func instrumentationScript() string {
	cachedScriptOnce.Do(func() {
		cachedScript = scripts.GetCombinedScript()
	})
	return cachedScript
}

// InjectInstrumentation adds monitoring JavaScript to HTML responses.
// The wsPort parameter is deprecated and unused (kept for backward compatibility).
// The script now uses relative URLs via window.location.host.
func InjectInstrumentation(body []byte, wsPort int) []byte {
	script := instrumentationScript()

	// Try to inject before </head>
	if idx := bytes.Index(body, []byte("</head>")); idx != -1 {
		result := make([]byte, 0, len(body)+len(script))
		result = append(result, body[:idx]...)
		result = append(result, []byte(script)...)
		result = append(result, body[idx:]...)
		return result
	}

	// Try to inject after <head>
	if idx := bytes.Index(body, []byte("<head>")); idx != -1 {
		insertAt := idx + 6
		result := make([]byte, 0, len(body)+len(script))
		result = append(result, body[:insertAt]...)
		result = append(result, []byte(script)...)
		result = append(result, body[insertAt:]...)
		return result
	}

	// Try to inject after <body>
	if idx := bytes.Index(body, []byte("<body")); idx != -1 {
		// Find the end of the body tag
		endIdx := bytes.Index(body[idx:], []byte(">"))
		if endIdx != -1 {
			insertAt := idx + endIdx + 1
			result := make([]byte, 0, len(body)+len(script))
			result = append(result, body[:insertAt]...)
			result = append(result, []byte(script)...)
			result = append(result, body[insertAt:]...)
			return result
		}
	}

	// Try to inject after <html>
	if idx := bytes.Index(body, []byte("<html")); idx != -1 {
		endIdx := bytes.Index(body[idx:], []byte(">"))
		if endIdx != -1 {
			insertAt := idx + endIdx + 1
			result := make([]byte, 0, len(body)+len(script))
			result = append(result, body[:insertAt]...)
			result = append(result, []byte(script)...)
			result = append(result, body[insertAt:]...)
			return result
		}
	}

	// Last resort: prepend to body
	result := make([]byte, 0, len(body)+len(script))
	result = append(result, []byte(script)...)
	result = append(result, body...)
	return result
}

// ShouldInject determines if JavaScript should be injected based on content type.
func ShouldInject(contentType string) bool {
	contentType = strings.ToLower(contentType)
	return strings.Contains(contentType, "text/html")
}
