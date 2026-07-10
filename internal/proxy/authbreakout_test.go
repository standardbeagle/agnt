package proxy

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthBreakoutMatchesURL(t *testing.T) {
	ab := &AuthBreakout{Patterns: []string{
		"login.microsoftonline.com",
		"figma.com/oauth",
		"*.okta.com/oauth2/*/authorize",
	}}

	// Substring semantics, case-insensitive, scheme-agnostic.
	assert.True(t, ab.MatchesURL("https://login.microsoftonline.com/tenant/oauth2/v2.0/authorize?x=1"))
	assert.True(t, ab.MatchesURL("https://LOGIN.MICROSOFTONLINE.COM/t"))
	assert.True(t, ab.MatchesURL("https://www.figma.com/oauth?client_id=abc"))
	// Wildcard spans arbitrary runs.
	assert.True(t, ab.MatchesURL("https://dev-123.okta.com/oauth2/default/v1/authorize?r=x"))
	// Non-matches.
	assert.False(t, ab.MatchesURL("https://example.com/login"))
	assert.False(t, ab.MatchesURL("http://localhost:3000/oauth/callback"))
	assert.False(t, ab.MatchesURL(""))
	// Regex metachars in patterns are literal: "figma.com" must not match "figmaXcom".
	assert.False(t, ab.MatchesURL("https://figmaxcom/oauth"))

	var nilAB *AuthBreakout
	assert.False(t, nilAB.MatchesURL("https://login.microsoftonline.com/x"))
}

func TestAuthBreakoutClientConfigJS(t *testing.T) {
	var nilAB *AuthBreakout
	assert.Equal(t, "", nilAB.clientConfigJS())

	ab := &AuthBreakout{Mode: "popup", Patterns: []string{`</script><script>alert(1)`}}
	js := ab.clientConfigJS()
	assert.True(t, strings.HasPrefix(js, "window.__devtool_auth_config="))
	assert.Contains(t, js, `"mode":"popup"`)
	// json.Marshal must have neutralised the script break-out.
	assert.NotContains(t, js, "</script>")
}

func TestInterceptAuthRedirect(t *testing.T) {
	newResp := func(status int, location, reqURL string) *http.Response {
		u, err := url.Parse(reqURL)
		require.NoError(t, err)
		resp := &http.Response{
			StatusCode: status,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    &http.Request{URL: u},
		}
		if location != "" {
			resp.Header.Set("Location", location)
		}
		return resp
	}

	ps := &ProxyServer{ID: "test"}
	ps.SetAuthBreakout(&AuthBreakout{Mode: "popup", Patterns: []string{"login.microsoftonline.com"}})

	// Content-frame 302 to a matching IdP → replaced with breakout stub.
	resp := newResp(302, "https://login.microsoftonline.com/t/oauth2/authorize?x=1", "http://localhost:9999/app?__devtool_frame=abc123")
	require.True(t, ps.interceptAuthRedirect(resp))
	assert.Equal(t, 200, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Location"))
	assert.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "__devtool_auth")
	assert.Contains(t, string(body), `login.microsoftonline.com`)

	// Top-level navigation (no frame marker) → not intercepted; the IdP is
	// not framed there and needs no breakout.
	resp = newResp(302, "https://login.microsoftonline.com/t/authorize", "http://localhost:9999/app")
	assert.False(t, ps.interceptAuthRedirect(resp))

	// Content-frame redirect to a non-matching host → untouched.
	resp = newResp(302, "http://localhost:3000/next", "http://localhost:9999/app?__devtool_frame=abc123")
	assert.False(t, ps.interceptAuthRedirect(resp))

	// Non-3xx → untouched.
	resp = newResp(200, "", "http://localhost:9999/app?__devtool_frame=abc123")
	assert.False(t, ps.interceptAuthRedirect(resp))

	// Breakout disabled → untouched even for a matching content-frame redirect.
	ps.SetAuthBreakout(nil)
	resp = newResp(302, "https://login.microsoftonline.com/t/authorize", "http://localhost:9999/app?__devtool_frame=abc123")
	assert.False(t, ps.interceptAuthRedirect(resp))
}

func TestAuthConfigInjectedIntoShellAndContent(t *testing.T) {
	ab := &AuthBreakout{Mode: "top", Patterns: []string{"figma.com/oauth"}}
	js := ab.clientConfigJS()

	shell := string(BuildShellDocument("pid", "chrome-f00d", "/?__devtool_frame=f00d", js))
	assert.Contains(t, shell, `window.__devtool_auth_config={"mode":"top","patterns":["figma.com/oauth"]};`)

	content := string(InjectContentRuntime([]byte("<html><head></head><body></body></html>"), "pid", "f00d", js))
	assert.Contains(t, content, `window.__devtool_auth_config=`)

	// Empty config injects nothing.
	shell = string(BuildShellDocument("pid", "chrome-f00d", "/", ""))
	assert.NotContains(t, shell, "__devtool_auth_config")
}
