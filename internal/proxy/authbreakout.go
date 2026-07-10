package proxy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// AuthBreakout is the runtime form of the .agnt.kdl auth-breakout block
// (config.AuthBreakoutConfig). When set on a proxy, content-frame
// navigations to matching URLs are carried out in a top-level window instead
// of the content iframe, and the OAuth callback is replayed into the iframe.
// See docs/configuration.md § Auth Breakout.
type AuthBreakout struct {
	// Mode is "popup" or "top" (validated at config parse time).
	Mode string `json:"mode"`
	// Patterns are case-insensitive wildcard fragments matched against the
	// full navigation URL: `*` matches any run of characters, plain text
	// matches as a substring.
	Patterns []string `json:"patterns"`

	compileOnce sync.Once
	compiled    []*regexp.Regexp
}

// compile builds the pattern regexps once. A malformed pattern (cannot
// happen for QuoteMeta output, defensive only) is skipped.
func (ab *AuthBreakout) compile() {
	ab.compileOnce.Do(func() {
		ab.compiled = make([]*regexp.Regexp, 0, len(ab.Patterns))
		for _, p := range ab.Patterns {
			// Escape everything, then turn the escaped wildcard back into a
			// non-greedy any-run. Unanchored: substring semantics.
			quoted := strings.ReplaceAll(regexp.QuoteMeta(strings.ToLower(p)), `\*`, `.*`)
			re, err := regexp.Compile(quoted)
			if err != nil {
				continue
			}
			ab.compiled = append(ab.compiled, re)
		}
	})
}

// MatchesURL reports whether rawURL matches any breakout pattern.
// Case-insensitive; patterns use substring-with-`*`-wildcard semantics.
func (ab *AuthBreakout) MatchesURL(rawURL string) bool {
	if ab == nil || rawURL == "" {
		return false
	}
	ab.compile()
	lower := strings.ToLower(rawURL)
	for _, re := range ab.compiled {
		if re.MatchString(lower) {
			return true
		}
	}
	return false
}

// clientConfigJS returns the inline <script> body publishing the breakout
// config to the browser runtime (scripts/authbreakout.js reads it). Empty
// string when ab is nil so callers can splice it unconditionally.
func (ab *AuthBreakout) clientConfigJS() string {
	if ab == nil {
		return ""
	}
	b, err := json.Marshal(struct {
		Mode     string   `json:"mode"`
		Patterns []string `json:"patterns"`
	}{ab.Mode, ab.Patterns})
	if err != nil {
		return ""
	}
	// json.Marshal escapes <, >, & to \uXXXX, so the literal is inert
	// inside an inline <script> element.
	return "window.__devtool_auth_config=" + string(b) + ";"
}

// buildAuthBreakoutStub returns the HTML served in place of a content-frame
// 3xx redirect whose Location matches a breakout pattern. Navigating the
// iframe to the IdP would be refused (X-Frame-Options on a foreign origin
// the proxy never sees), so instead this stub asks the chrome shell to run
// the flow in a top-level window. Fallback chain when the shell surface is
// missing: navigate the top window, then the frame itself (old behavior).
func buildAuthBreakoutStub(targetURL string) []byte {
	u := jsStringLiteral(targetURL)
	return []byte(fmt.Sprintf(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>Authentication</title></head><body>`+
		`<p style="font:14px system-ui,sans-serif;color:#555;padding:2em">Sign-in continues in a separate window. This page resumes automatically after authentication.</p>`+
		`<script>(function(){var u=%s;var ok=false;`+
		`try{if(window.parent&&window.parent!==window&&window.parent.__devtool_auth){window.parent.__devtool_auth.breakout(u);ok=true;}}catch(e){}`+
		`if(!ok){try{window.top.location.href=u;}catch(e2){window.location.href=u;}}`+
		`})();</script></body></html>`, u))
}
