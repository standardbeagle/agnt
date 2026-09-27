package devoidc

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateRedirectPattern accepts an absolute http(s) URL with no fragment.
// The only wildcard is a whole-port `*` on a loopback host, because the
// proxy's port is not something the file can know in advance.
func ValidateRedirectPattern(raw string) error {
	if strings.Contains(raw, "#") {
		return fmt.Errorf("redirect-uri %q must not contain a fragment", raw)
	}
	probe := strings.Replace(raw, ":*/", ":1/", 1)
	if strings.HasSuffix(raw, ":*") {
		probe = strings.TrimSuffix(raw, "*") + "1"
	}
	if strings.Contains(probe, "*") {
		return fmt.Errorf("redirect-uri %q: the only wildcard allowed is a whole port (localhost:*)", raw)
	}
	u, err := url.Parse(probe)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("redirect-uri %q must be an absolute http(s) URL", raw)
	}
	if probe != raw && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("redirect-uri %q: a port wildcard is only allowed on localhost, 127.0.0.1 or [::1]", raw)
	}
	return nil
}

// RedirectURIMatches reports whether uri is allowed by pattern, honouring a
// whole-port `*` on a loopback host. Everything else is an exact match.
func RedirectURIMatches(pattern, uri string) bool {
	if pattern == uri {
		return true
	}
	star := strings.Index(pattern, ":*")
	if star < 0 {
		return false
	}
	prefix, suffix := pattern[:star+1], pattern[star+2:]
	if !strings.HasPrefix(uri, prefix) || !strings.HasSuffix(uri, suffix) || len(uri) < len(prefix)+len(suffix)+1 {
		return false
	}
	port := uri[len(prefix) : len(uri)-len(suffix)]
	for _, r := range port {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(port) <= 5
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
