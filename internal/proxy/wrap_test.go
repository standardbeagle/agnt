package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy/scripts"
)

// htmlResp builds a text/html response whose request is req (may be nil for a
// direct-call unit path).
func htmlResp(body string, req *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

const sampleDoc = "<html><head><title>T</title></head><body>Hello World</body></html>"

// TestModifyResponse_TopLevelWrapsInShell: a genuine top-level navigation is
// replaced by a chrome shell containing exactly one content iframe; the page
// content is NOT inlined (it loads inside the frame).
func TestModifyResponse_TopLevelWrapsInShell(t *testing.T) {
	ps := &ProxyServer{ListenAddr: ":8080", ID: "px1"}
	req := httptest.NewRequest(http.MethodGet, "/dashboard?q=1", nil)
	resp := htmlResp(sampleDoc, req)

	if err := ps.modifyResponse(resp); err != nil {
		t.Fatalf("modifyResponse: %v", err)
	}
	body := readBody(t, resp)

	if c := strings.Count(body, "<iframe"); c != 1 {
		t.Fatalf("shell must contain exactly one content iframe, got %d\n%s", c, body)
	}
	if !strings.Contains(body, `id="`+contentFrameID+`"`) {
		t.Errorf("content iframe id %q missing", contentFrameID)
	}
	if !strings.Contains(body, `window.__devtool_role="chrome"`) {
		t.Errorf("shell must declare chrome role")
	}
	if !strings.Contains(body, `window.__devtool_proxy_id="px1"`) {
		t.Errorf("shell must carry proxy id")
	}
	if !strings.Contains(body, instrumentationAssetPrefix) {
		t.Errorf("shell must load instrumentation bundle")
	}
	// The content frame src carries the marker so its request is recognisable.
	if !strings.Contains(body, frameMarkerParam+"=") {
		t.Errorf("content iframe src must carry the frame marker")
	}
	// Page content lives in the frame, not inlined in the shell.
	if strings.Contains(body, "Hello World") {
		t.Errorf("top-level shell must not inline page content")
	}
	// Content-Length is resynced to the shell body.
	if got := resp.Header.Get("Content-Length"); got == "" {
		t.Errorf("Content-Length must be set on the wrapped response")
	}
}

// TestModifyResponse_ContentFrameInjectsInPlace: a request carrying the frame
// marker is served unwrapped — page content preserved + content runtime injected.
func TestModifyResponse_ContentFrameInjectsInPlace(t *testing.T) {
	ps := &ProxyServer{ListenAddr: ":8080", ID: "px1"}
	req := httptest.NewRequest(http.MethodGet, "/dashboard?q=1&"+frameMarkerParam+"=abc123", nil)
	resp := htmlResp(sampleDoc, req)

	if err := ps.modifyResponse(resp); err != nil {
		t.Fatalf("modifyResponse: %v", err)
	}
	body := readBody(t, resp)

	if !strings.Contains(body, "Hello World") {
		t.Errorf("content frame must preserve page content")
	}
	if strings.Contains(body, "<iframe") {
		t.Errorf("content frame must not be re-wrapped")
	}
	if !strings.Contains(body, `window.__devtool_role="content"`) {
		t.Errorf("content frame must declare content role")
	}
	if !strings.Contains(body, `window.__devtool_frame_id="abc123"`) {
		t.Errorf("content frame must echo the marker frame id, got:\n%s", body)
	}
	if !strings.Contains(body, instrumentationAssetPrefix) {
		t.Errorf("content frame must load instrumentation bundle")
	}
}

// TestModifyResponse_FragmentNotWrapped: an HTML fragment (no <html>/<head>) is
// injected in place, never wrapped — partial-render flows keep working.
func TestModifyResponse_FragmentNotWrapped(t *testing.T) {
	ps := &ProxyServer{ListenAddr: ":8080", ID: "px1"}
	req := httptest.NewRequest(http.MethodGet, "/partial", nil)
	resp := htmlResp(`<div class="row">fragment</div>`, req)

	if err := ps.modifyResponse(resp); err != nil {
		t.Fatalf("modifyResponse: %v", err)
	}
	body := readBody(t, resp)

	if strings.Contains(body, "<iframe") {
		t.Errorf("fragment must not be wrapped in a shell")
	}
	if !strings.Contains(body, "fragment") {
		t.Errorf("fragment content must be preserved")
	}
	// Markerless response: the bundle is injected but no role is forced — the
	// browser (frames.js) resolves the role.
	if !strings.Contains(body, instrumentationAssetPrefix) {
		t.Errorf("fragment must still load the instrumentation bundle")
	}
	if strings.Contains(body, `window.__devtool_role="content"`) {
		t.Errorf("markerless fragment must not force the content role")
	}
}

// TestModifyResponse_NestedIframeNotWrapped: a foreign app-embedded iframe
// (Sec-Fetch-Dest: iframe) is NOT wrapped — the app owns it. It gets a
// markerless inject so frames.js resolves it to the passive role in-browser.
func TestModifyResponse_NestedIframeNotWrapped(t *testing.T) {
	ps := &ProxyServer{ListenAddr: ":8080", ID: "px1"}
	req := httptest.NewRequest(http.MethodGet, "/widget", nil)
	req.Header.Set("Sec-Fetch-Dest", "iframe")
	resp := htmlResp(sampleDoc, req)

	if err := ps.modifyResponse(resp); err != nil {
		t.Fatalf("modifyResponse: %v", err)
	}
	body := readBody(t, resp)

	if strings.Contains(body, "<iframe") {
		t.Errorf("foreign app iframe must not be wrapped in a shell")
	}
	if !strings.Contains(body, "Hello World") {
		t.Errorf("foreign iframe content must be preserved")
	}
	if strings.Contains(body, `window.__devtool_role="content"`) {
		t.Errorf("foreign nested frame must not be forced to content role")
	}
}

// TestModifyResponse_DocumentDestWraps: an explicit Sec-Fetch-Dest: document is
// a top-level navigation and is wrapped.
func TestModifyResponse_DocumentDestWraps(t *testing.T) {
	ps := &ProxyServer{ListenAddr: ":8080", ID: "px1"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Sec-Fetch-Dest", "document")
	resp := htmlResp(sampleDoc, req)

	if err := ps.modifyResponse(resp); err != nil {
		t.Fatalf("modifyResponse: %v", err)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "<iframe") || !strings.Contains(body, `window.__devtool_role="chrome"`) {
		t.Errorf("Sec-Fetch-Dest=document must wrap in a chrome shell")
	}
}

func TestIsTopLevelNavigation(t *testing.T) {
	mk := func(dest string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if dest != "" {
			r.Header.Set("Sec-Fetch-Dest", dest)
		}
		return r
	}
	if isTopLevelNavigation(nil) {
		t.Errorf("nil request is not a navigation")
	}
	for _, d := range []string{"iframe", "frame", "embed", "object"} {
		if isTopLevelNavigation(mk(d)) {
			t.Errorf("Sec-Fetch-Dest=%q must not be top-level", d)
		}
	}
	for _, d := range []string{"", "document"} {
		if !isTopLevelNavigation(mk(d)) {
			t.Errorf("Sec-Fetch-Dest=%q must be top-level", d)
		}
	}
}

// TestBundleRoleGating: the bundle gates the telemetry runtime (core WS +
// interaction listeners) on the resolved frame role, and frames.js publishes
// the stable role global.
func TestBundleRoleGating(t *testing.T) {
	bundle := scripts.GetCombinedScript()
	for _, want := range []string{
		"window.__devtool_frame_role", // stable role global (frames.js)
		"telemetry_suppressed",        // core gates WS/error tracking
	} {
		if !strings.Contains(bundle, want) {
			t.Errorf("bundle missing role-gating marker %q", want)
		}
	}
	// frames.js must appear before core.js in the bundle so the role global is
	// set before core's init reads it.
	if i, j := strings.Index(bundle, "window.__devtool_frame_role ="), strings.Index(bundle, "telemetry_suppressed"); i == -1 || j == -1 || i > j {
		t.Errorf("frames.js must set the role global before core reads it (frames=%d core=%d)", i, j)
	}
}

// TestModifyResponse_NilRequestInjectsInPlace: a direct call with no request
// (unit path) injects in place rather than wrapping — production always has a
// request, so wrapping is gated on one.
func TestModifyResponse_NilRequestInjectsInPlace(t *testing.T) {
	ps := &ProxyServer{ListenAddr: ":8080", ID: "px1"}
	resp := htmlResp(sampleDoc, nil)

	if err := ps.modifyResponse(resp); err != nil {
		t.Fatalf("modifyResponse: %v", err)
	}
	body := readBody(t, resp)
	if strings.Contains(body, "<iframe") {
		t.Errorf("nil-request call must not wrap")
	}
	if !strings.Contains(body, "Hello World") {
		t.Errorf("nil-request call must preserve content")
	}
}

// TestStripFrameDenyHeaders: content responses get X-Frame-Options dropped and
// the frame-ancestors CSP directive removed while other directives survive.
func TestStripFrameDenyHeaders(t *testing.T) {
	ps := &ProxyServer{ListenAddr: ":8080", ID: "px1"}
	req := httptest.NewRequest(http.MethodGet, "/x?"+frameMarkerParam+"=z", nil)
	resp := htmlResp(sampleDoc, req)
	resp.Header.Set("X-Frame-Options", "DENY")
	resp.Header.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; img-src *")

	if err := ps.modifyResponse(resp); err != nil {
		t.Fatalf("modifyResponse: %v", err)
	}
	if resp.Header.Get("X-Frame-Options") != "" {
		t.Errorf("X-Frame-Options must be stripped")
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if strings.Contains(csp, "frame-ancestors") {
		t.Errorf("frame-ancestors must be stripped, got %q", csp)
	}
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "img-src *") {
		t.Errorf("other CSP directives must survive, got %q", csp)
	}
}

func TestStripFrameAncestors(t *testing.T) {
	cases := []struct{ in, want string }{
		{"frame-ancestors 'none'", ""},
		{"default-src 'self'; frame-ancestors 'none'", "default-src 'self'"},
		{"frame-ancestors 'none'; img-src *", "img-src *"},
		{"default-src 'self'", "default-src 'self'"},
	}
	for _, c := range cases {
		if got := stripFrameAncestors(c.in); got != c.want {
			t.Errorf("stripFrameAncestors(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFrameIDForPathDeterministic(t *testing.T) {
	a := frameIDForPath("/dashboard")
	b := frameIDForPath("/dashboard")
	c := frameIDForPath("/other")
	if a != b {
		t.Errorf("frameIDForPath must be deterministic: %q != %q", a, b)
	}
	if a == c {
		t.Errorf("distinct paths must produce distinct ids")
	}
	if a == "" {
		t.Errorf("frame id must be non-empty")
	}
}

func TestContentFrameSrc(t *testing.T) {
	if got := contentFrameSrc("/a", "id"); got != "/a?"+frameMarkerParam+"=id" {
		t.Errorf("no-query src wrong: %q", got)
	}
	if got := contentFrameSrc("/a?x=1", "id"); got != "/a?x=1&"+frameMarkerParam+"=id" {
		t.Errorf("query src wrong: %q", got)
	}
}

func TestIsContentFrameRequest(t *testing.T) {
	if isContentFrameRequest(nil) {
		t.Errorf("nil request is not a content frame")
	}
	if isContentFrameRequest(httptest.NewRequest(http.MethodGet, "/a", nil)) {
		t.Errorf("unmarked request is not a content frame")
	}
	if !isContentFrameRequest(httptest.NewRequest(http.MethodGet, "/a?"+frameMarkerParam+"=1", nil)) {
		t.Errorf("marked request is a content frame")
	}
}

// TestBundleIncludesFramesPrimitive: the served bundle carries the frames module
// + the role/registry primitive so later slices can gate on it.
func TestBundleIncludesFramesPrimitive(t *testing.T) {
	bundle := scripts.GetCombinedScript()
	for _, want := range []string{
		"window.__devtool.role",
		"__devtool_frames",
		"window.__devtool_sync_url",
		"resolveRole",
	} {
		if !strings.Contains(bundle, want) {
			t.Errorf("bundle missing frames primitive marker %q", want)
		}
	}
}
