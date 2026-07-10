package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy/scripts"
)

// A caller/config-supplied proxy id carrying HTML that would break out of the
// inline <script> must be neutralised, not echoed verbatim. proxyID reaches the
// same %q-style interpolation as frameID but cannot be alphabet-constrained, so
// it is JS/HTML-encoded via jsStringLiteral.
func TestInjectProxyID_ScriptBreakoutNeutralised(t *testing.T) {
	const evil = `</script><script>alert(1)</script>`
	// Include an existing <script> so InjectProxyMeta (which splices after the
	// last </script>) actually injects rather than returning the body unchanged.
	body := []byte("<html><head><script>var a=1;</script></head><body></body></html>")

	cases := map[string][]byte{
		"InjectInstrumentationAndMeta": InjectInstrumentationAndMeta(body, evil),
		"InjectProxyMeta":              InjectProxyMeta(body, evil),
		"InjectContentRuntime":         InjectContentRuntime(body, evil, "f00d", ""),
		"BuildShellDocument":           BuildShellDocument(evil, "chrome-f00d", "/", ""),
	}
	for name, out := range cases {
		s := string(out)
		// The evil payload's own <script> tags must never survive un-encoded
		// (that is the break-out). Legit injected tags produce real </script>,
		// so we look for the attacker's opening sequence specifically.
		if strings.Contains(s, "<script>alert(1)") {
			t.Errorf("%s: proxy id broke out of the inline script: %q", name, s)
		}
		// json.Marshal encodes "<" as the six bytes <, so the payload's
		// tags survive only in inert escaped form (match the unambiguous
		// "u003cscript" tail to sidestep backslash-escaping confusion).
		if !strings.Contains(s, "u003cscript") {
			t.Errorf("%s: expected escaped proxy id, got: %q", name, s)
		}
	}
}

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
	if !bytes.Contains(result, []byte("<script")) {
		t.Error("Script not injected")
	}

	// Script should appear before </head>
	scriptIdx := bytes.Index(result, []byte("<script"))
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
	if !bytes.Contains(result, []byte("<script")) {
		t.Error("Script not injected")
	}

	// Script should appear after <head>
	headOpenIdx := bytes.Index(result, []byte("<head>"))
	scriptIdx := bytes.Index(result, []byte("<script"))

	if headOpenIdx == -1 {
		t.Error("<head> tag not found")
	}
	if scriptIdx == -1 {
		t.Error("Script tag not found")
	}
	if scriptIdx < headOpenIdx+6 {
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
	if !bytes.Contains(result, []byte("<script")) {
		t.Error("Script not injected")
	}

	// Script should appear after <body>
	bodyOpenIdx := bytes.Index(result, []byte("<body>"))
	scriptIdx := bytes.Index(result, []byte("<script"))

	if bodyOpenIdx == -1 {
		t.Error("<body> tag not found")
	}
	if scriptIdx == -1 {
		t.Error("Script tag not found")
	}
	if scriptIdx < bodyOpenIdx+6 {
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
	if !bytes.Contains(result, []byte("<script")) {
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
	if !bytes.Contains(result, []byte("<script")) {
		t.Error("Script not injected")
	}
}

func TestInjectInstrumentation_NoHTML(t *testing.T) {
	html := []byte(`Hello World`)

	result := InjectInstrumentation(html, 8080)

	// Should prepend script as last resort
	if !bytes.Contains(result, []byte("<script")) {
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

func TestInjectInstrumentation_EmitsExternalAssetTag(t *testing.T) {
	html := []byte(`<!DOCTYPE html>
<html>
<head></head>
<body></body>
</html>`)

	result := InjectInstrumentation(html, 8080)
	resultStr := string(result)

	// The bundle is no longer inlined; the injected body carries a small
	// external <script src> tag pointing at the content-addressed asset path.
	if !strings.Contains(resultStr, `<script src="`+instrumentationAssetPath()+`">`) {
		t.Errorf("expected external instrumentation asset tag, got: %s", resultStr)
	}
	// The ~1.3MB bundle must NOT be inlined into the response body.
	if len(result) > len(html)+512 {
		t.Errorf("injected body unexpectedly large (%d bytes) — bundle should be external, not inlined", len(result))
	}
}

func TestInstrumentationBundle_ContainsFeatures(t *testing.T) {
	// The instrumentation features now live in the externally-served bundle.
	bundle := string(instrumentationScriptBytes())
	expectedFeatures := []string{
		"WebSocket",
		"error",
		"performance",
		"window.addEventListener",
		"unhandledrejection",
		"window.location.host", // relative URL with current host (port-independent)
		"__devtool_metrics",
	}
	for _, feature := range expectedFeatures {
		if !strings.Contains(bundle, feature) {
			t.Errorf("instrumentation bundle missing feature: %s", feature)
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

func TestInjectInstrumentation_PortIndependent(t *testing.T) {
	html := []byte(`<!DOCTYPE html><html><head></head></html>`)

	// The injected tag references a same-origin relative asset path, so it is
	// identical regardless of the (now-obsolete) port argument; the bundle
	// itself uses window.location.host for the metrics WebSocket.
	want := InjectInstrumentation(html, 8080)
	for _, port := range []int{3000, 9000} {
		if got := InjectInstrumentation(html, port); !bytes.Equal(got, want) {
			t.Errorf("injection should be port-independent; port %d differed", port)
		}
	}
	if !strings.Contains(string(want), `<script src="`+instrumentationAssetPath()+`">`) {
		t.Error("expected external instrumentation asset tag")
	}
}

func TestInjectProxyMeta(t *testing.T) {
	// Simulate body after InjectInstrumentation (contains a </script> tag)
	body := []byte(`<html><head><script>window.__devtool_version='1.0';</script></head><body></body></html>`)
	result := InjectProxyMeta(body, "my-app")
	resultStr := string(result)

	if !strings.Contains(resultStr, `window.__devtool_proxy_id="my-app"`) {
		t.Errorf("Expected proxy ID script, got: %s", resultStr)
	}

	// Verify it's inserted after the last </script>
	idx := strings.LastIndex(resultStr, `window.__devtool_proxy_id`)
	scriptEnd := strings.LastIndex(resultStr[:idx], "</script>")
	if scriptEnd == -1 {
		t.Error("Proxy meta should appear after a </script> tag")
	}
}

func TestInjectInstrumentationAndMeta(t *testing.T) {
	html := []byte(`<html><head><title>t</title></head><body><p>hi</p></body></html>`)
	result := InjectInstrumentationAndMeta(html, "my-app")
	s := string(result)

	// proxy-id is set inline (varies per proxy, cannot be a shared asset).
	if !strings.Contains(s, `window.__devtool_proxy_id="my-app"`) {
		t.Errorf("missing proxy id: %s", s)
	}
	// bundle is loaded via the external content-addressed asset tag.
	assetTag := `<script src="` + instrumentationAssetPath() + `">`
	if !strings.Contains(s, assetTag) {
		t.Errorf("missing external instrumentation asset tag: %s", s)
	}
	// proxy-id must be set BEFORE the bundle loads (document order).
	if idIdx, tagIdx := strings.Index(s, "window.__devtool_proxy_id"), strings.Index(s, assetTag); idIdx == -1 || idIdx > tagIdx {
		t.Error("proxy id must be set before the instrumentation bundle tag")
	}
	// body must not be bloated by an inlined bundle.
	if len(result) > len(html)+512 {
		t.Errorf("injected body too large (%d) — bundle should be external", len(result))
	}
	// original content preserved
	if !strings.Contains(s, "<title>t</title>") || !strings.Contains(s, "<p>hi</p>") {
		t.Error("original content not preserved")
	}
}

func TestInjectProxyMeta_SpecialChars(t *testing.T) {
	body := []byte(`<head><script>x=1;</script></head>`)
	result := InjectProxyMeta(body, `foo"bar`)
	resultStr := string(result)

	// fmt %q escapes quotes, so the ID should be safely quoted
	if !strings.Contains(resultStr, `window.__devtool_proxy_id="foo\"bar"`) {
		t.Errorf("Expected escaped proxy ID, got: %s", resultStr)
	}
}

// TestInstrumentationAsset_NoHTMLWrapper guards the external-asset refactor:
// the bundle is served via <script src> as application/javascript, so it must
// not carry the legacy inline <script>…</script> HTML wrapper, which is a
// parse error in an external JS file and aborts the whole bundle.
func TestInstrumentationAsset_NoHTMLWrapper(t *testing.T) {
	b := instrumentationScriptBytes()
	if bytes.Contains(b, []byte("<script>")) || bytes.Contains(b, []byte("</script>")) {
		t.Fatal("served instrumentation asset still contains <script> HTML wrapper")
	}
	if len(b) == 0 {
		t.Fatal("instrumentation asset is empty")
	}
}

func TestHandleInstrumentationAsset(t *testing.T) {
	// Serves the bundle immutably for the content-addressed path.
	req := httptest.NewRequest(http.MethodGet, instrumentationAssetPath(), nil)
	rec := httptest.NewRecorder()
	handleInstrumentationAsset(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("content-type = %q, want application/javascript", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("cache-control = %q, want immutable", cc)
	}
	if !bytes.Equal(rec.Body.Bytes(), instrumentationScriptBytes()) {
		t.Error("served body does not match the instrumentation bundle")
	}
	// An explicit Content-Length delimits the response so it is not sent
	// chunked. Chunked framing of this large blocking <head> asset has stalled
	// in some browsers, head-of-line-blocking every request behind it on the
	// same HTTP/1.1 connection.
	wantLen := strconv.Itoa(len(instrumentationScriptBytes()))
	if cl := rec.Header().Get("Content-Length"); cl != wantLen {
		t.Errorf("content-length = %q, want %q", cl, wantLen)
	}

	// HEAD returns the headers (incl. Content-Length) but no body.
	reqHead := httptest.NewRequest(http.MethodHead, instrumentationAssetPath(), nil)
	recHead := httptest.NewRecorder()
	handleInstrumentationAsset(recHead, reqHead)
	if recHead.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", recHead.Code)
	}
	if recHead.Body.Len() != 0 {
		t.Errorf("HEAD body len = %d, want 0", recHead.Body.Len())
	}
	if cl := recHead.Header().Get("Content-Length"); cl != wantLen {
		t.Errorf("HEAD content-length = %q, want %q", cl, wantLen)
	}

	// Unrelated path under the subtree 404s rather than leaking the bundle.
	req2 := httptest.NewRequest(http.MethodGet, "/__devtool/other.txt", nil)
	rec2 := httptest.NewRecorder()
	handleInstrumentationAsset(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("non-asset path status = %d, want 404", rec2.Code)
	}
}

func TestInjectProxyMeta_NoScript(t *testing.T) {
	body := []byte(`<html><body>hello</body></html>`)
	result := InjectProxyMeta(body, "test")
	if string(result) != string(body) {
		t.Error("Should return body unchanged when no </script> marker exists")
	}
}

// The shell document must carry the inline auth-popup relay synchronously in
// its first <head> script — before the async instrumentation bundle tag — so
// an OAuth popup returning from the IdP relays its callback to the opener at
// parse time instead of waiting on the whole bundle to fetch and execute
// (the load-sensitive latency behind the popup round-trip e2e flake). The
// relay must be present regardless of auth config: the popup return check
// keys off window.opener/window.name/sessionStorage, not the config.
func TestBuildShellDocument_InlineAuthPopupRelay(t *testing.T) {
	for name, authCfgJS := range map[string]string{"noConfig": "", "withConfig": "window.__devtool_auth_config={};"} {
		t.Run(name, func(t *testing.T) {
			s := string(BuildShellDocument("pid", "chrome-f00d", "/", authCfgJS))

			for _, marker := range []string{
				"__devtool_auth_relayed",                 // double-relay guard flag shared with authbreakout.js
				"__devtool_auth_popup",                   // sessionStorage marker key (mirror of POPUP_KEY)
				"window.opener.__devtool_auth.complete(", // the relay call itself
				"window.name!=='__devtool_auth'",         // window.name half of the marker (mirror of POPUP_NAME)
			} {
				if !strings.Contains(s, marker) {
					t.Errorf("shell document missing inline relay marker %q", marker)
				}
			}

			// Parse-time ordering: the inline relay must precede the async bundle tag.
			relayAt := strings.Index(s, "__devtool_auth_relayed")
			bundleAt := strings.Index(s, "<script src=")
			if bundleAt == -1 {
				t.Fatal("shell document missing bundle script tag")
			}
			if relayAt == -1 || relayAt > bundleAt {
				t.Errorf("inline relay (at %d) must precede the bundle tag (at %d)", relayAt, bundleAt)
			}

			// The bundle's copy must key off the same guard flag so it never relays twice.
			if !strings.Contains(scripts.GetCombinedScriptForRole(scripts.RoleChrome), "__devtool_auth_relayed") {
				t.Error("authbreakout.js missing __devtool_auth_relayed double-relay guard")
			}
		})
	}
}
