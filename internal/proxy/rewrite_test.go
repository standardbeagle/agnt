package proxy

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func newTestProxyServer(targetURL string, listenAddr string) *ProxyServer {
	parsed, _ := url.Parse(targetURL)
	return &ProxyServer{
		TargetURL:  parsed,
		ListenAddr: listenAddr,
	}
}

func TestRewriteURL(t *testing.T) {
	ps := newTestProxyServer("http://localhost:3000", ":8080")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "absolute URL matching target",
			input:    "http://localhost:3000/wp-admin/",
			expected: "http://localhost:8080/wp-admin/",
		},
		{
			name:     "absolute URL with query string",
			input:    "http://localhost:3000/page?foo=bar",
			expected: "http://localhost:8080/page?foo=bar",
		},
		{
			name:     "https URL matching target (converted to http)",
			input:    "https://localhost:3000/secure",
			expected: "http://localhost:8080/secure",
		},
		{
			name:     "relative URL unchanged",
			input:    "/wp-admin/",
			expected: "/wp-admin/",
		},
		{
			name:     "different host unchanged",
			input:    "http://example.com/page",
			expected: "http://example.com/page",
		},
		{
			name:     "empty string unchanged",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ps.rewriteURL(tt.input)
			if result != tt.expected {
				t.Errorf("rewriteURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestRewriteURL_DifferentPorts(t *testing.T) {
	ps := newTestProxyServer("http://wordpress.local:8888", ":9090")

	input := "http://wordpress.local:8888/wp-login.php"
	expected := "http://localhost:9090/wp-login.php"

	result := ps.rewriteURL(input)
	if result != expected {
		t.Errorf("rewriteURL(%q) = %q, want %q", input, result, expected)
	}
}

func TestRewriteLocationHeader(t *testing.T) {
	ps := newTestProxyServer("http://localhost:3000", ":8080")

	tests := []struct {
		name            string
		location        string
		expectedRewrite string
	}{
		{
			name:            "redirect to target is rewritten",
			location:        "http://localhost:3000/wp-admin/",
			expectedRewrite: "http://localhost:8080/wp-admin/",
		},
		{
			name:            "relative redirect unchanged",
			location:        "/dashboard",
			expectedRewrite: "/dashboard",
		},
		{
			name:            "external redirect unchanged",
			location:        "https://google.com/",
			expectedRewrite: "https://google.com/",
		},
		{
			name:            "no location header",
			location:        "",
			expectedRewrite: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				Header: make(http.Header),
			}
			if tt.location != "" {
				resp.Header.Set("Location", tt.location)
			}

			ps.rewriteLocationHeader(resp)

			result := resp.Header.Get("Location")
			if result != tt.expectedRewrite {
				t.Errorf("Location header = %q, want %q", result, tt.expectedRewrite)
			}
		})
	}
}

func TestRewriteCookieDomain(t *testing.T) {
	ps := newTestProxyServer("http://wordpress.local:3000", ":8080")

	tests := []struct {
		name     string
		cookie   string
		expected string
	}{
		{
			name:     "domain matching target is removed",
			cookie:   "session=abc123; Domain=wordpress.local; Path=/",
			expected: "session=abc123; Path=/",
		},
		{
			name:     "domain with leading dot is removed",
			cookie:   "session=abc123; Domain=.wordpress.local; Path=/; HttpOnly",
			expected: "session=abc123; Path=/; HttpOnly",
		},
		{
			name:     "no domain attribute unchanged",
			cookie:   "session=abc123; Path=/; HttpOnly",
			expected: "session=abc123; Path=/; HttpOnly",
		},
		{
			name:     "external domain unchanged",
			cookie:   "tracking=xyz; Domain=analytics.com; Path=/",
			expected: "tracking=xyz; Domain=analytics.com; Path=/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ps.rewriteCookieDomain(tt.cookie, "wordpress.local")
			if result != tt.expected {
				t.Errorf("rewriteCookieDomain(%q) = %q, want %q", tt.cookie, result, tt.expected)
			}
		})
	}
}

func TestRewriteSetCookieHeaders(t *testing.T) {
	ps := newTestProxyServer("http://app.example.com:3000", ":8080")

	resp := &http.Response{
		Header: make(http.Header),
	}
	resp.Header.Add("Set-Cookie", "session=abc; Domain=app.example.com; Path=/")
	resp.Header.Add("Set-Cookie", "prefs=dark; Path=/; HttpOnly")

	ps.rewriteSetCookieHeaders(resp)

	cookies := resp.Header["Set-Cookie"]
	if len(cookies) != 2 {
		t.Fatalf("expected 2 cookies, got %d", len(cookies))
	}

	// First cookie should have domain removed
	if cookies[0] != "session=abc; Path=/" {
		t.Errorf("cookie[0] = %q, want %q", cookies[0], "session=abc; Path=/")
	}

	// Second cookie should be unchanged (no domain)
	if cookies[1] != "prefs=dark; Path=/; HttpOnly" {
		t.Errorf("cookie[1] = %q, want %q", cookies[1], "prefs=dark; Path=/; HttpOnly")
	}
}

func TestRewriteURLsInBody(t *testing.T) {
	ps := newTestProxyServer("http://localhost:3000", ":8080")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "http href rewritten",
			input:    `<a href="http://localhost:3000/page">Link</a>`,
			expected: `<a href="http://localhost:8080/page">Link</a>`,
		},
		{
			// Regression (01KXM3MH9C3CXM12W7VEGSD1V4): an https reference must
			// stay https — the proxy must never silently downgrade a target
			// link to plaintext, even though it is locally served over http.
			name:     "https href rewritten without scheme downgrade",
			input:    `<a href="https://localhost:3000/page">Link</a>`,
			expected: `<a href="https://localhost:8080/page">Link</a>`,
		},
		{
			name:     "src attribute rewritten",
			input:    `<script src="http://localhost:3000/bundle.js"></script>`,
			expected: `<script src="http://localhost:8080/bundle.js"></script>`,
		},
		{
			name:     "form action attribute rewritten",
			input:    `<form action="http://localhost:3000/submit"></form>`,
			expected: `<form action="http://localhost:8080/submit"></form>`,
		},
		{
			// Regression: a bare JSON body (no HTML attribute context) that
			// happens to contain the target host as data must pass through
			// unmodified — it is not a navigational URL, it is app data.
			name:     "JSON body containing target host is not corrupted",
			input:    `{"apiUrl":"https://localhost:3000/api","note":"see http:\/\/localhost:3000\/docs"}`,
			expected: `{"apiUrl":"https://localhost:3000/api","note":"see http:\/\/localhost:3000\/docs"}`,
		},
		{
			// Regression: inline <script> content (JS source, not a src
			// attribute) mentioning the target host as a string literal must
			// not be rewritten — it is executable/text content, not a URL
			// attribute.
			name:     "inline script body containing target host is not corrupted",
			input:    `<script>var apiBase = "https://localhost:3000/api"; console.log("http://localhost:3000/x");</script>`,
			expected: `<script>var apiBase = "https://localhost:3000/api"; console.log("http://localhost:3000/x");</script>`,
		},
		{
			// Regression: plain visible text mentioning the target host (no
			// surrounding href/src/action attribute) must not be rewritten.
			name:     "plain text body containing target host is not corrupted",
			input:    `<p>Docs available at http://localhost:3000/a and https://localhost:3000/b</p>`,
			expected: `<p>Docs available at http://localhost:3000/a and https://localhost:3000/b</p>`,
		},
		{
			name:     "external URLs unchanged",
			input:    `<a href="http://example.com/page">External</a>`,
			expected: `<a href="http://example.com/page">External</a>`,
		},
		{
			name:     "relative URLs unchanged",
			input:    `<a href="/page">Relative</a>`,
			expected: `<a href="/page">Relative</a>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ps.rewriteURLsInBody([]byte(tt.input))
			if string(result) != tt.expected {
				t.Errorf("rewriteURLsInBody(%q) = %q, want %q", tt.input, string(result), tt.expected)
			}
		})
	}
}

func TestGetProxyHost(t *testing.T) {
	tests := []struct {
		listenAddr string
		expected   string
	}{
		{":8080", "localhost:8080"},
		{":3000", "localhost:3000"},
		{"[::]:9090", "localhost:9090"},
		{"0.0.0.0:8000", "localhost:8000"},
	}

	for _, tt := range tests {
		t.Run(tt.listenAddr, func(t *testing.T) {
			ps := &ProxyServer{ListenAddr: tt.listenAddr}
			result := ps.getProxyHost()
			if result != tt.expected {
				t.Errorf("getProxyHost() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestInjectPublicBundle covers the body half of the public plane's proxied
// upstream serve: the RolePublic bundle tag is spliced in at the structural
// insertion point, the upstream's own bytes survive byte-for-byte around it, and
// a document with no <head>/<body>/<html> still gets the bundle rather than
// silently going uninstrumented.
func TestInjectPublicBundle(t *testing.T) {
	const assetPath = "/__devtool/inject.abc123.js"
	const integrity = "sha256-Zm9vYmFy"
	wantTag := `<script src="` + assetPath + `" integrity="` + integrity + `" crossorigin="anonymous"></script>`

	cases := []struct {
		name string
		body string
		// wantBefore is the upstream substring the tag must precede, proving the
		// bundle runs ahead of the upstream's own body content.
		wantBefore string
	}{
		{
			name:       "before closing head",
			body:       "<!DOCTYPE html><html><head><title>Up</title></head><body><p>upstream</p><script>x=1</script></body></html>",
			wantBefore: "</head>",
		},
		{
			name:       "after open head when unclosed",
			body:       "<html><head><body><p>upstream</p>",
			wantBefore: "<body>",
		},
		{
			name:       "body only",
			body:       `<body class="x"><p>upstream</p>`,
			wantBefore: "<p>upstream</p>",
		},
		{
			name:       "no structure at all still injects",
			body:       "<p>bare fragment</p>",
			wantBefore: "<p>bare fragment</p>",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := string(injectPublicBundle([]byte(c.body), assetPath, integrity))
			tagAt := strings.Index(got, wantTag)
			if tagAt < 0 {
				t.Fatalf("bundle tag missing from injected document: %s", got)
			}
			if n := strings.Count(got, wantTag); n != 1 {
				t.Fatalf("bundle tag injected %d times, want exactly 1", n)
			}
			if beforeAt := strings.Index(got, c.wantBefore); beforeAt < 0 || tagAt > beforeAt {
				t.Fatalf("tag at %d must precede %q at %d: %s", tagAt, c.wantBefore, beforeAt, got)
			}
			// The upstream document is otherwise passed through untouched: removing
			// the injected tag must reproduce the input exactly.
			if restored := strings.Replace(got, wantTag, "", 1); restored != c.body {
				t.Fatalf("upstream bytes were modified.\n got: %q\nwant: %q", restored, c.body)
			}
		})
	}
}

// TestStripFrameDenyHeadersPreservesUpstreamScriptSrc pins the exact behaviour
// that makes this function FORBIDDEN on the public publish plane (INV-11/INV-12).
// It is a strip-MERGE — it removes only frame-ancestors — so every other upstream
// directive, including script-src 'unsafe-inline', survives. Reusing it for
// public responses instead of the wholesale Del+Set already caused one review
// rewind on this epic.
//
// This test therefore asserts the *unsafe* outcome on purpose: if someone
// "hardens" stripFrameDenyHeaders into a wholesale replace, this fails and they
// are forced to notice that the dev content path depends on the merge, while the
// public plane has its own composer. The companion assertion — that the public
// header policy discards the same upstream CSP — lives in
// TestHostileUpstreamCSPCannotSurviveWidening.
func TestStripFrameDenyHeadersPreservesUpstreamScriptSrc(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("X-Frame-Options", "DENY")
	resp.Header.Set("Content-Security-Policy",
		"frame-ancestors 'none'; script-src 'unsafe-inline' https://evil.example; default-src *")

	stripFrameDenyHeaders(resp)

	if got := resp.Header.Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options not dropped: %q", got)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if strings.Index(csp, "frame-ancestors") >= 0 {
		t.Fatalf("frame-ancestors not stripped: %q", csp)
	}
	// The whole point: these DID survive. That is why the public plane must not
	// route through here.
	for _, survivor := range []string{"script-src 'unsafe-inline'", "https://evil.example", "default-src *"} {
		if strings.Index(csp, survivor) < 0 {
			t.Fatalf("strip-merge semantics changed: %q no longer survives in %q — "+
				"the public plane's wholesale Del+Set is the only INV-12-safe path; "+
				"re-read .claude/rules/publish-security-review-lessons.md before adjusting this test",
				survivor, csp)
		}
	}
}
