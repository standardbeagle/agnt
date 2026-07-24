package pathutil

// NormalizeTrailingSlash normalizes a path for comparison by stripping
// trailing slashes (keeping a root "/"). Shared by the proxy, tunnel,
// chromedp, and browser managers, which previously each carried a verbatim
// copy.
func NormalizeTrailingSlash(p string) string {
	if p == "" {
		return ""
	}
	for len(p) > 1 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	return p
}
