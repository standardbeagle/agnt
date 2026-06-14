package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/standardbeagle/agnt/internal/proxy/scripts"
)

// instrumentationAssetPrefix is the reserved URL subtree the proxy serves the
// instrumentation bundle from. The full path carries a content hash so it can
// be cached immutably and busts automatically when the bundle changes (e.g. a
// binary upgrade ships new instrumentation JS).
const instrumentationAssetPrefix = "/__devtool/inject."

var (
	// Cache the instrumentation script since it never changes for the life of
	// the process. Historically the ~1.3MB bundle was inlined into every HTML
	// response, which (a) copied it per request and (b) sent it over the wire
	// on every page load. It is now served as an external, content-addressed,
	// immutably-cacheable asset and only a tiny <script src> tag is injected.
	// cachedScriptBytes is read-only (the asset handler and spliceInto only
	// read it), so sharing it is safe.
	cachedScript      string
	cachedScriptBytes []byte
	cachedScriptPath  string // "/__devtool/inject.<hash>.js"
	cachedScriptOnce  sync.Once
)

func loadCachedScript() {
	cachedScriptOnce.Do(func() {
		cachedScript = scripts.GetCombinedScript()
		cachedScriptBytes = []byte(cachedScript)
		sum := sha256.Sum256(cachedScriptBytes)
		cachedScriptPath = instrumentationAssetPrefix + hex.EncodeToString(sum[:8]) + ".js"
	})
}

// instrumentationAssetPath returns the content-addressed URL path the bundle is
// served from, e.g. "/__devtool/inject.<hash>.js".
func instrumentationAssetPath() string {
	loadCachedScript()
	return cachedScriptPath
}

// handleInstrumentationAsset serves the instrumentation bundle for the reserved
// "/__devtool/inject.<hash>.js" path. Because the path is content-addressed the
// response is cached immutably: a different bundle ships under a different hash,
// so the browser fetches it once per version and reuses it across page loads.
func handleInstrumentationAsset(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, instrumentationAssetPrefix) || !strings.HasSuffix(r.URL.Path, ".js") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(instrumentationScriptBytes())
}

// instrumentationScript returns JavaScript code for error and performance monitoring.
// The script is cached after first call for performance.
func instrumentationScript() string {
	loadCachedScript()
	return cachedScript
}

// instrumentationScriptBytes returns the cached instrumentation script as a
// read-only []byte. Callers MUST NOT mutate the returned slice — it is shared
// across all requests to avoid re-copying the ~1.3MB bundle per response.
func instrumentationScriptBytes() []byte {
	loadCachedScript()
	return cachedScriptBytes
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

// InjectInstrumentation adds monitoring JavaScript to HTML responses by
// inserting a small external <script src> tag that loads the content-addressed
// instrumentation bundle (served by the proxy at instrumentationAssetPath).
// The wsPort parameter is deprecated and unused (kept for backward compatibility).
// The tag is a blocking, same-origin script so it executes, in order, before
// the page's own scripts run.
func InjectInstrumentation(body []byte, wsPort int) []byte {
	tag := []byte(`<script src="` + instrumentationAssetPath() + `"></script>`)
	if at, ok := instrumentationInsertOffset(body); ok {
		return spliceInto(body, at, tag)
	}
	return spliceInto(body, 0, tag) // last resort: prepend
}

// InjectInstrumentationAndMeta injects, in a single body copy, the proxy-id
// inline (it varies per proxy, so it cannot be a shared external asset)
// followed by the external instrumentation bundle tag. Both are blocking
// same-origin scripts placed in document order, so window.__devtool_proxy_id is
// set, then the bundle loads, before any page script runs.
//
// The bundle (~1.3MB) was previously inlined into every HTML response — copied
// per request and re-sent on every page load. Serving it as an external,
// immutably-cached asset shrinks the injected payload from ~1.3MB to ~150 bytes
// and lets the browser cache the bundle across page loads.
func InjectInstrumentationAndMeta(body []byte, proxyID string) []byte {
	tag := []byte(fmt.Sprintf(`<script>window.__devtool_proxy_id=%q;</script><script src=%q></script>`,
		proxyID, instrumentationAssetPath()))
	if at, ok := instrumentationInsertOffset(body); ok {
		return spliceInto(body, at, tag)
	}
	return spliceInto(body, 0, tag)
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
