package proxy

import (
	"bytes"
	"fmt"
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

// instrumentationInsertOffset returns the byte offset where the instrumentation
// script should be inserted (before </head>, else after <head>/<body>/<html>),
// and whether a structural insertion point was found. When false, callers
// prepend to the body.
func instrumentationInsertOffset(body []byte) (int, bool) {
	if idx := bytes.Index(body, []byte("</head>")); idx != -1 {
		return idx, true
	}
	if idx := bytes.Index(body, []byte("<head>")); idx != -1 {
		return idx + len("<head>"), true
	}
	if idx := bytes.Index(body, []byte("<body")); idx != -1 {
		if endIdx := bytes.Index(body[idx:], []byte(">")); endIdx != -1 {
			return idx + endIdx + 1, true
		}
	}
	if idx := bytes.Index(body, []byte("<html")); idx != -1 {
		if endIdx := bytes.Index(body[idx:], []byte(">")); endIdx != -1 {
			return idx + endIdx + 1, true
		}
	}
	return 0, false
}

// spliceInto returns body with the given fragments inserted at offset at, in a
// single allocation sized for the full result.
func spliceInto(body []byte, at int, fragments ...[]byte) []byte {
	total := len(body)
	for _, f := range fragments {
		total += len(f)
	}
	result := make([]byte, 0, total)
	result = append(result, body[:at]...)
	for _, f := range fragments {
		result = append(result, f...)
	}
	result = append(result, body[at:]...)
	return result
}

// InjectInstrumentation adds monitoring JavaScript to HTML responses.
// The wsPort parameter is deprecated and unused (kept for backward compatibility).
// The script now uses relative URLs via window.location.host.
func InjectInstrumentation(body []byte, wsPort int) []byte {
	script := []byte(instrumentationScript())
	if at, ok := instrumentationInsertOffset(body); ok {
		return spliceInto(body, at, script)
	}
	return spliceInto(body, 0, script) // last resort: prepend
}

// InjectInstrumentationAndMeta injects the instrumentation script and the
// proxy-id meta tag in a SINGLE allocation. This is the hot proxy-response path:
// calling InjectInstrumentation followed by InjectProxyMeta copied the full
// response body twice (costly for large HTML). The meta tag is placed
// immediately after the instrumentation script, so window.__devtool_proxy_id is
// set before any page scripts run.
func InjectInstrumentationAndMeta(body []byte, proxyID string) []byte {
	script := []byte(instrumentationScript())
	meta := []byte(fmt.Sprintf("<script>window.__devtool_proxy_id=%q;</script>", proxyID))
	if at, ok := instrumentationInsertOffset(body); ok {
		return spliceInto(body, at, script, meta)
	}
	return spliceInto(body, 0, script, meta)
}

// InjectProxyMeta inserts a small script tag setting window.__devtool_proxy_id
// after the last </script> in the body. Retained for direct callers/tests; the
// proxy response path uses InjectInstrumentationAndMeta to avoid a second
// full-body copy.
func InjectProxyMeta(body []byte, proxyID string) []byte {
	meta := []byte(fmt.Sprintf("<script>window.__devtool_proxy_id=%q;</script>", proxyID))
	marker := []byte("</script>")
	if idx := bytes.LastIndex(body, marker); idx != -1 {
		return spliceInto(body, idx+len(marker), meta)
	}
	return body
}

// ShouldInject determines if JavaScript should be injected based on content type.
func ShouldInject(contentType string) bool {
	contentType = strings.ToLower(contentType)
	return strings.Contains(contentType, "text/html")
}
