package publish

import (
	"regexp"
	"strings"
)

// CSS property name form: lowercase ident, optional leading vendor dash.
var cssPropNameRe = regexp.MustCompile(`^-?[a-z][a-z-]*$`)

// forbiddenCSSTokens are the classic CSS-injection / CSS-exfil vectors the spec
// (§5, §5b/§6) forbids anywhere in a style value (case-insensitive substring
// match). image-set / cross-fade are URL-bearing function forms that fetch a URL
// when the renderer assigns el.style[prop]=val, so they must be denied alongside
// url() — a substring scan for url( alone lets them through (spec §5b/§6).
var forbiddenCSSTokens = []string{
	"url(", "expression(", "behavior", "-moz-binding", "@import", "javascript:",
	"image-set(", "-webkit-image-set(", "cross-fade(", "-webkit-cross-fade(",
}

// cssStripRe matches CSS whitespace and /* */ comments. We strip both before the
// forbidden-token scan so that whitespace- or comment-splitting (e.g. `url ( )`,
// `ur/**/l(`) cannot smuggle a forbidden function past the substring check. The
// declarative style values accepted here (§5b/§6) never legitimately need either.
var cssStripRe = regexp.MustCompile(`/\*[^*]*\*+(?:[^/*][^*]*\*+)*/|\s+`)

// allowedCSSExact / allowedCSSPrefix encode the §5b applyStyle property
// allowlist (layout/paint-safe only). Deny-by-default: any property not covered
// is rejected.
var allowedCSSExact = map[string]bool{
	"color": true, "background-color": true, "background": true,
	"line-height": true, "letter-spacing": true, "opacity": true,
	"display": true, "visibility": true, "width": true, "height": true,
	"box-shadow": true, "border-radius": true, "transform": true, "transition": true,
}

// allowedCSSPrefix covers the spec's wildcard families: border*, outline*,
// padding*, margin*, font*, text-*, min-*, max-*.
var allowedCSSPrefix = []string{
	"border", "outline", "padding", "margin", "font", "text-", "min-", "max-",
}

// allowedCSSProperty reports whether prop (already lowercased) is on the §5b
// allowlist.
func allowedCSSProperty(prop string) bool {
	if allowedCSSExact[prop] {
		return true
	}
	for _, p := range allowedCSSPrefix {
		if strings.HasPrefix(prop, p) {
			return true
		}
	}
	return false
}

// hasForbiddenCSSToken reports whether s contains any §5 forbidden token
// (case-insensitive).
func hasForbiddenCSSToken(s string) bool {
	lower := strings.ToLower(cssStripRe.ReplaceAllString(s, ""))
	for _, t := range forbiddenCSSTokens {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

// ValidateStyleProps enforces §5b (property allowlist) + §5 (forbidden tokens,
// size cap) on an applyStyle payload.
func ValidateStyleProps(props map[string]string) error {
	if len(props) == 0 {
		return errf("applyStyle: no properties")
	}
	total := 0
	for prop, val := range props {
		total += len(prop) + len(val)
		name := strings.ToLower(strings.TrimSpace(prop))
		if !cssPropNameRe.MatchString(name) {
			return errf("applyStyle: illegal property name %q", prop)
		}
		if !allowedCSSProperty(name) {
			return errf("applyStyle: property %q not on allowlist", prop)
		}
		// Reject CSS backslash escapes outright (§5b/§6): they have no legitimate
		// use in the declarative style patches this validator accepts, and the
		// CSSOM unescapes them (e.g. `\75` -> `u`) — reconstituting url(...) after
		// a raw-byte substring scan has already passed. Deny by default.
		if strings.ContainsRune(val, '\\') {
			return errf("applyStyle: property %q value has illegal backslash escape", prop)
		}
		if hasForbiddenCSSToken(val) {
			return errf("applyStyle: property %q has forbidden token in value", prop)
		}
		// Structural CSS-breakout characters are rejected: a value is a single
		// declaration value, never a new rule/declaration.
		if strings.ContainsAny(val, ";{}<>") {
			return errf("applyStyle: property %q value has illegal character", prop)
		}
	}
	if total > MaxStylePatchBytes {
		return errf("applyStyle: patch size %d exceeds max %d", total, MaxStylePatchBytes)
	}
	return nil
}
