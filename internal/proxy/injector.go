package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/proxy/scripts"
)

// instrumentationAssetPrefix is the reserved URL subtree the proxy serves the
// instrumentation bundle from. The full path carries a content hash so it can
// be cached immutably and busts automatically when the bundle changes (e.g. a
// binary upgrade ships new instrumentation JS).
const instrumentationAssetPrefix = "/__devtool/inject."

// instrumentationAsset is one cached, content-addressed bundle flavour.
// bytes is read-only (the asset handler and spliceInto only read it), so
// sharing it across requests is safe.
type instrumentationAsset struct {
	bytes []byte
	path  string // "/__devtool/inject.<hash>.js"
}

var (
	// Cache the instrumentation bundles since they never change for the life
	// of the process. Historically the ~1.3MB full bundle was inlined into
	// every HTML response, which (a) copied it per request and (b) sent it
	// over the wire on every page load. Bundles are now served as external,
	// content-addressed, immutably-cacheable assets and only a tiny
	// <script src> tag is injected. There is one asset per frame role
	// (full/passive, chrome shell, content frame); the content hash in each
	// path keys the browser cache per role for free.
	cachedAssets     map[scripts.Role]*instrumentationAsset
	cachedScriptOnce sync.Once
)

func loadCachedScript() {
	cachedScriptOnce.Do(func() {
		assets := make(map[scripts.Role]*instrumentationAsset, 4)
		for _, role := range []scripts.Role{scripts.RoleFull, scripts.RoleChrome, scripts.RoleContent, scripts.RolePublic} {
			// GetCombinedScriptForRole wraps the bundle in <script>…</script>
			// for the legacy inline-injection form. The bundle is now served
			// as an external <script src> asset, where those HTML tags are
			// invalid JavaScript and abort parsing of the whole bundle. Strip
			// the wrapper for the served asset bytes.
			body := []byte(stripScriptWrapper(scripts.GetCombinedScriptForRole(role)))
			sum := sha256.Sum256(body)
			assets[role] = &instrumentationAsset{
				bytes: body,
				path:  instrumentationAssetPrefix + hex.EncodeToString(sum[:8]) + ".js",
			}
		}
		cachedAssets = assets
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

// instrumentationAssetPath returns the content-addressed URL path the full
// bundle is served from, e.g. "/__devtool/inject.<hash>.js".
func instrumentationAssetPath() string {
	return instrumentationAssetPathForRole(scripts.RoleFull)
}

// instrumentationAssetPathForRole returns the content-addressed URL path of
// the bundle flavour for the given frame role. Distinct flavours hash to
// distinct paths, so browser caching is per-role automatically.
func instrumentationAssetPathForRole(role scripts.Role) string {
	loadCachedScript()
	return cachedAssets[role].path
}

// PublicInstrumentationAssetPath returns the content-addressed URL path of the
// HARD-ALLOWLISTED public bundle (scripts.RolePublic) — the only bundle the
// walkthrough-publish plane may inject into a public page. It is served by the
// same handleInstrumentationAsset handler under its own hash, so the public
// page never references the dev (full/chrome/content) inject paths and never
// loads the dev-control surface.
func PublicInstrumentationAssetPath() string {
	return instrumentationAssetPathForRole(scripts.RolePublic)
}

// publicInstrumentationAssetBytes returns the cached RolePublic bundle bytes.
// Read-only — callers MUST NOT mutate the returned slice (it is shared).
func publicInstrumentationAssetBytes() []byte {
	loadCachedScript()
	return cachedAssets[scripts.RolePublic].bytes
}

// PublicInstrumentationAssetCSPHash returns the base64 sha256 of the RolePublic
// bundle bytes, formatted as a CSP source token ("sha256-<b64>"). The public
// plane pins its script-src to this hash ALONE (no 'self'), and tags the
// external <script src> with a matching SRI integrity attribute, so the served
// CSP names the exact bundle content, not just its origin (spec §4).
func PublicInstrumentationAssetCSPHash() string {
	sum := sha256.Sum256(publicInstrumentationAssetBytes())
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

// handlePublicInstrumentationAsset serves the RolePublic bundle for the public
// walkthrough-publish plane — and ONLY that bundle. It is a DISTINCT handler
// from handleInstrumentationAsset precisely to close the dev-bundle fallback:
// handleInstrumentationAsset resolves an unknown content-hash to the FULL DEV
// bundle (its documented "superset" fallback), which would leak the entire
// __devtool control surface (proxy exec, WS, audits) onto a public page. This
// handler instead EXACT-MATCHES the RolePublic content-addressed path and 404s
// anything else. There is no code path here that can ever emit the dev bundle,
// so an unknown/forged hash on the public plane yields 404, never dev JS
// (spec §9 / INV-2; P4 advisory closure).
//
// The asset carries no secret (it is content-addressed public JS), so it is not
// token-gated: Referrer-Policy: no-referrer strips the Referer even for
// same-origin subresources, making referer-binding both impossible and
// unnecessary for a non-secret bundle.
func handlePublicInstrumentationAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	loadCachedScript()
	public := cachedAssets[scripts.RolePublic]
	if r.URL.Path != public.path {
		// Unknown or forged hash on the public plane: 404. NEVER the dev bundle.
		http.NotFound(w, r)
		return
	}
	body := public.bytes
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(body); err != nil {
		debug.Log("proxy", "failed to write public instrumentation asset: %v", err)
	}
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
	loadCachedScript()
	// Resolve which bundle flavour this content-addressed path names. An
	// unknown hash (e.g. a page cached across a binary upgrade requesting the
	// old path) falls back to the full bundle — a superset of every flavour —
	// rather than 404ing the page's whole instrumentation.
	body := cachedAssets[scripts.RoleFull].bytes
	for _, a := range cachedAssets {
		if r.URL.Path == a.path {
			body = a.bytes
			break
		}
	}
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
	// The frontend needs this instrumentation bundle to drive every __devtool
	// debug feature; a write failure means the browser dropped the connection
	// mid-transfer, so surface it to the debug log.
	if _, err := w.Write(body); err != nil {
		debug.Log("proxy", "failed to write instrumentation asset: %v", err)
	}
}

// instrumentationScriptBytes returns the cached full-bundle instrumentation
// script as a read-only []byte. Callers MUST NOT mutate the returned slice —
// it is shared across all requests to avoid re-copying the ~1.3MB bundle per
// response.
func instrumentationScriptBytes() []byte {
	loadCachedScript()
	return cachedAssets[scripts.RoleFull].bytes
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
	tag := []byte(fmt.Sprintf(`<script>window.__devtool_proxy_id=%s;</script><script src=%q></script>`,
		jsStringLiteral(proxyID), instrumentationAssetPath()))
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
	meta := []byte(fmt.Sprintf("<script>window.__devtool_proxy_id=%s;</script>", jsStringLiteral(proxyID)))
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
	// Only a STRUCTURALLY-positioned marker means "full document". The old
	// bytes.Contains matched "<html"/"<head"/"<!doctype html" ANYWHERE in the
	// first 1KB, so an htmx/turbo fragment that merely mentions one of those
	// tokens in text or an attribute (e.g. a code sample, `<p>use &lt;html&gt;
	// …</p>`, or a raw `<div data-tpl="<html>">`) was wrongly wrapped in the
	// chrome shell — breaking partial-render swaps. A real document leads with
	// the doctype or the <html>/<head> element, so anchor the check to the
	// leading position (after optional BOM/whitespace and one leading comment).
	head := bytes.TrimLeft(body, " \t\r\n\f\v\ufeff")
	// Tolerate a single leading HTML comment ("<!-- saved from … -->").
	if bytes.HasPrefix(head, []byte("<!--")) {
		if end := bytes.Index(head, []byte("-->")); end != -1 {
			head = bytes.TrimLeft(head[end+len("-->"):], " \t\r\n\f\v\ufeff")
		}
	}
	if len(head) > 1024 {
		head = head[:1024]
	}
	lower := bytes.ToLower(head)
	return bytes.HasPrefix(lower, []byte("<!doctype html")) ||
		bytes.HasPrefix(lower, []byte("<html")) ||
		bytes.HasPrefix(lower, []byte("<head"))
}

// neutralizeScriptClose defuses the only sequence that can terminate an inline
// <script> element early — a case-insensitive "</script" — by rewriting it to
// "<\/script". Inside valid JavaScript that sequence can only appear within a
// string or regex literal, where "\/" is identical to "/", so the rewrite is
// behaviour-preserving while making an HTML-parser break-out impossible.
//
// Defense in depth: the sole current producer of authCfgJS
// (AuthBreakout.clientConfigJS) already json.Marshal-escapes <, >, & to \uXXXX,
// so a config value containing "</script>" never reaches the injectors
// unescaped today. This guards a FUTURE caller that hands the injectors a raw
// JS string, so a break-out cannot silently regress.
func neutralizeScriptClose(s string) string {
	const tok = "</script"
	lower := strings.ToLower(s)
	if !strings.Contains(lower, tok) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); {
		if strings.HasPrefix(lower[i:], tok) {
			b.WriteString("<\\/script")
			i += len(tok)
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// frameIDPattern matches a server-minted frame id: a lowercase-hex path hash
// (a content frame) optionally carrying the "chrome-" shell prefix. Frame ids
// are echoed verbatim into inline <script> via %q, which is Go-string escaping,
// NOT HTML/JS escaping — an attacker-supplied marker such as
// `</script><script>…` would break out of the script element. Constraining the
// value to this alphabet makes it inert in an HTML/JS context regardless of the
// surrounding quoting.
var frameIDPattern = regexp.MustCompile(`^(chrome-)?[0-9a-f]+$`)

// jsStringLiteral encodes s as a script-safe JS string literal. Unlike %q
// (Go-string escaping, which leaves `<` and `/` intact and so permits a
// `</script>` break-out), json.Marshal escapes `<`, `>`, and `&` to \uXXXX by
// default, making the result inert inside an inline <script> element and its
// string context. Used for values that legitimately vary — proxy ids are
// caller/config-supplied (`makeProcessID` → `basename-hash:name`, or a raw MCP
// `proxy start` id) and so cannot be alphabet-constrained the way frame ids are.
func jsStringLiteral(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// sanitizeFrameID returns id unchanged when it is a well-formed server-minted
// frame id, or "" when it is malformed or attacker-supplied. Callers treat ""
// as "no marker" and fall back to a role-less injection.
func sanitizeFrameID(id string) string {
	if frameIDPattern.MatchString(id) {
		return id
	}
	return ""
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

// authPopupRelayJS is a minimal inline copy of the auth-popup return relay in
// scripts/authbreakout.js (isAuthPopupReturn + complete + close). It runs
// synchronously at shell-document parse time, BEFORE the async instrumentation
// bundle is fetched/parsed/executed. That ordering is the point: the OAuth
// popup's only job after the IdP redirects back is to hand the callback URL to
// its opener and close, and making that wait on the whole bundle (tens of
// modules) turns CPU/network pressure into seconds of extra popup latency —
// the load-sensitive tail that flaked TestE2E_AuthBreakout_PopupRoundTrip
// under full-suite parallelism. The constants (popup name, sessionStorage key)
// and the opener checks mirror authbreakout.js exactly; the
// __devtool_auth_relayed flag tells the bundle's copy the relay already fired
// so it does not run twice when window.close() is delayed or blocked.
const authPopupRelayJS = `(function(){try{` +
	`if(window.top!==window.self||!window.opener||window.opener===window)return;` +
	`if(!window.opener.__devtool_auth)return;` +
	`var K='__devtool_auth_popup',m=false;` +
	`try{m=sessionStorage.getItem(K)==='1';}catch(e){}` +
	`if(!m&&window.name!=='__devtool_auth')return;` +
	`window.__devtool_auth_relayed=true;` +
	`try{sessionStorage.removeItem(K);}catch(e){}` +
	`window.opener.__devtool_auth.complete(window.location.href);` +
	`document.title='Authentication complete';window.close();` +
	`}catch(e){}})();`

// BuildShellDocument returns the outer chrome-shell HTML for a top-level
// navigation. The shell carries the proxy-id, the chrome role marker, and the
// instrumentation bundle, and embeds a single full-viewport content <iframe>
// whose src is the originally-requested resource with the frame marker appended
// — so the proxy serves the real page there, injected in content role. The
// proxy UI runtime (indicator/panels) therefore lives in the shell, isolated
// from page content. Role gating of the runtime lands in Slice 3; this slice
// only establishes the wrap + the browser-side role/registry primitive.
// authCfgJS is the (possibly empty) inline JS publishing the auth-breakout
// config (AuthBreakout.clientConfigJS); it precedes the bundle so
// scripts/authbreakout.js reads it at eval time.
func BuildShellDocument(proxyID, frameID, contentSrc, authCfgJS string) []byte {
	// frameID is server-minted (shellFrameID of a path hash); validate defensively
	// so a malformed value can never break out of the inline <script> below.
	frameID = sanitizeFrameID(frameID)
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\">")
	b.WriteString(fmt.Sprintf("<script>window.__devtool_proxy_id=%s;window.__devtool_role=\"chrome\";window.__devtool_frame_id=%q;%s%s</script>", jsStringLiteral(proxyID), frameID, neutralizeScriptClose(authCfgJS), authPopupRelayJS))
	b.WriteString(fmt.Sprintf("<script src=%q></script>", instrumentationAssetPathForRole(scripts.RoleChrome)))
	b.WriteString("<style>html,body{margin:0;padding:0;height:100%;width:100%;overflow:hidden}#" + contentFrameID + "{position:fixed;inset:0;border:0;width:100%;height:100%;display:block}</style>")
	b.WriteString("</head><body>")
	// contentSrc derives from the requested URI (resp.Request.URL.RequestURI()).
	// It is attacker-influenced, so HTML-escape it into the attribute value: %q
	// is Go-string quoting (it leaves a literal double-quote as \" which an HTML
	// parser reads as a closing quote, permitting `"><script>` break-out).
	b.WriteString("<iframe id=\"" + contentFrameID + "\" src=\"" + html.EscapeString(contentSrc) + "\"></iframe>")
	b.WriteString("</body></html>")
	return []byte(b.String())
}

// InjectContentRuntime injects the proxy-id, the content role marker, and the
// bundle tag into a content-frame (or unwrapped-fallback / fragment) HTML
// response. Mirrors InjectInstrumentationAndMeta but additionally declares
// window.__devtool_role="content" + the frame id so scripts/frames.js resolves
// the frame's role without re-deriving it from the URL.
// authCfgJS mirrors BuildShellDocument: the content frame needs the config
// too, for client-side navigation interception (Navigation API / anchors).
func InjectContentRuntime(body []byte, proxyID, frameID, authCfgJS string) []byte {
	// Defence in depth: frameID originates from an attacker-controllable query
	// marker; only a well-formed server-minted id is echoed into the inline
	// <script>. A malformed one collapses to "" (no forced frame id).
	frameID = sanitizeFrameID(frameID)
	tag := []byte(fmt.Sprintf(`<script>window.__devtool_proxy_id=%s;window.__devtool_role="content";window.__devtool_frame_id=%q;%s</script><script src=%q></script>`,
		jsStringLiteral(proxyID), frameID, neutralizeScriptClose(authCfgJS), instrumentationAssetPathForRole(scripts.RoleContent)))
	if at, ok := instrumentationInsertOffset(body); ok {
		return spliceInto(body, at, tag)
	}
	return spliceInto(body, 0, tag)
}
