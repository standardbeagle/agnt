package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
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
		// GetCombinedScript wraps the bundle in <script>…</script> for the
		// legacy inline-injection form. The bundle is now served as an external
		// <script src> asset, where those HTML tags are invalid JavaScript and
		// abort parsing of the whole bundle. Strip the wrapper for the served
		// asset bytes.
		cachedScript = stripScriptWrapper(scripts.GetCombinedScript())
		cachedScriptBytes = []byte(cachedScript)
		sum := sha256.Sum256(cachedScriptBytes)
		cachedScriptPath = instrumentationAssetPrefix + hex.EncodeToString(sum[:8]) + ".js"
	})
}

// stripScriptWrapper removes a single leading <script> and trailing </script>
// (with surrounding whitespace) so the bundle is valid when served as an
// external JavaScript asset.
func stripScriptWrapper(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<script>")
	s = strings.TrimSuffix(strings.TrimSpace(s), "</script>")
	return strings.TrimSpace(s)
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
	body := instrumentationScriptBytes()
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	// Set an explicit Content-Length so the response is delimited rather than
	// chunked. The bundle is a fixed cached slice, so the length is known up
	// front. Chunked framing of this ~1.3MB blocking <head> asset has been seen
	// to stall in some browsers — the connection is held open waiting for a
	// terminator the client never observes, head-of-line-blocking every request
	// pipelined behind it on the same HTTP/1.1 connection (e.g. main.tsx). A
	// fixed Content-Length frees the connection deterministically and prevents a
	// truncated transfer from being cached as complete under the immutable
	// Cache-Control above.
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
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

// frameMarkerParam is the query parameter the proxy stamps on the content-frame
// request URL so a wrapped content frame is distinguishable from a top-level
// navigation. Its presence on a request means "this is the content frame; serve
// the real page (injected in content role), do not re-wrap." The matching
// browser-side constant lives in scripts/frames.js (FRAME_PARAM).
const frameMarkerParam = "__devtool_frame"

// contentFrameID is the DOM id of the single content <iframe> the chrome shell
// embeds. scripts/frames.js + scripts/responsive-mode.js (Slice 7) address it.
const contentFrameID = "__devtool_content_frame"

// isContentFrameRequest reports whether r is a request for the inner content
// frame (carries the frame marker). A nil request, or a request without the
// marker, is treated as a top-level navigation.
func isContentFrameRequest(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	return r.URL.Query().Has(frameMarkerParam)
}

// isTopLevelNavigation reports whether r is a top-level document navigation
// rather than a nested browsing context (an app-embedded iframe/frame/object).
// Only top-level navigations are wrapped in the chrome shell; an app's own
// iframe must render its content, not our shell. Modern browsers send
// Sec-Fetch-Dest; an empty value (legacy browsers / non-browser clients)
// defaults to top-level so the wrap still applies.
func isTopLevelNavigation(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "iframe", "frame", "embed", "object":
		return false
	default:
		return true
	}
}

// isFullHTMLDocument reports whether body looks like a complete HTML document
// rather than an HTML fragment (htmx/turbo partial responses). Only full
// documents are wrapped in the chrome shell; fragments are injected in place so
// partial-render flows keep working.
func isFullHTMLDocument(body []byte) bool {
	head := body
	if len(head) > 1024 {
		head = head[:1024]
	}
	lower := bytes.ToLower(head)
	return bytes.Contains(lower, []byte("<!doctype html")) ||
		bytes.Contains(lower, []byte("<html")) ||
		bytes.Contains(lower, []byte("<head"))
}

// frameIDForPath derives a stable, opaque frame id from the request path. The id
// only needs to be unique among the live content frames of one shell; the path
// is stable for a given page, so a short hash avoids minting random ids
// server-side while staying deterministic for tests.
func frameIDForPath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:6])
}

// shellFrameID derives the chrome shell's own frame id from the content frame id.
// The shell and its content frame must carry DISTINCT ids: they share a WebSocket
// and both run the exec handler, so an identical id would make every
// content-targeted exec also run in the shell (duplicate replies) and would make
// "outer" vs "inner" unaddressable. The "chrome-" prefix keeps the id readable
// and guarantees it never collides with a content frame's path hash.
func shellFrameID(contentFrameID string) string {
	return "chrome-" + contentFrameID
}

// contentFrameSrc appends the frame marker to the requested URI so the inner
// frame's request is recognisable as the content frame on its way back through
// the proxy.
func contentFrameSrc(reqURI, frameID string) string {
	sep := "?"
	if strings.Contains(reqURI, "?") {
		sep = "&"
	}
	return reqURI + sep + frameMarkerParam + "=" + frameID
}

// BuildShellDocument returns the outer chrome-shell HTML for a top-level
// navigation. The shell carries the proxy-id, the chrome role marker, and the
// instrumentation bundle, and embeds a single full-viewport content <iframe>
// whose src is the originally-requested resource with the frame marker appended
// — so the proxy serves the real page there, injected in content role. The
// proxy UI runtime (indicator/panels) therefore lives in the shell, isolated
// from page content. Role gating of the runtime lands in Slice 3; this slice
// only establishes the wrap + the browser-side role/registry primitive.
func BuildShellDocument(proxyID, frameID, contentSrc string) []byte {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\">")
	b.WriteString(fmt.Sprintf("<script>window.__devtool_proxy_id=%q;window.__devtool_role=\"chrome\";window.__devtool_frame_id=%q;</script>", proxyID, frameID))
	b.WriteString(fmt.Sprintf("<script src=%q></script>", instrumentationAssetPath()))
	b.WriteString("<style>html,body{margin:0;padding:0;height:100%;width:100%;overflow:hidden}#" + contentFrameID + "{position:fixed;inset:0;border:0;width:100%;height:100%;display:block}</style>")
	b.WriteString("</head><body>")
	b.WriteString(fmt.Sprintf("<iframe id=%q src=%q></iframe>", contentFrameID, contentSrc))
	b.WriteString("</body></html>")
	return []byte(b.String())
}

// InjectContentRuntime injects the proxy-id, the content role marker, and the
// bundle tag into a content-frame (or unwrapped-fallback / fragment) HTML
// response. Mirrors InjectInstrumentationAndMeta but additionally declares
// window.__devtool_role="content" + the frame id so scripts/frames.js resolves
// the frame's role without re-deriving it from the URL.
func InjectContentRuntime(body []byte, proxyID, frameID string) []byte {
	tag := []byte(fmt.Sprintf(`<script>window.__devtool_proxy_id=%q;window.__devtool_role="content";window.__devtool_frame_id=%q;</script><script src=%q></script>`,
		proxyID, frameID, instrumentationAssetPath()))
	if at, ok := instrumentationInsertOffset(body); ok {
		return spliceInto(body, at, tag)
	}
	return spliceInto(body, 0, tag)
}
