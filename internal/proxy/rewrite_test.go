package proxy

import (
	"net/http"
	"net/url"
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
