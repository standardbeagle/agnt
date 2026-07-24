package proxy

import (
	"net/url"
	"testing"
)

// TestBackendProbeAddr pins the health-probe dial address: a target URL
// without an explicit port must probe the scheme-default port instead of
// handing net.Dial a bare hostname ("missing port in address"), which made
// every probe of an https://host target fail and falsely marked healthy
// backends unreachable.
func TestBackendProbeAddr(t *testing.T) {
	cases := []struct {
		target string
		want   string
	}{
		{"https://standardbeagle.com", "standardbeagle.com:443"},
		{"http://standardbeagle.com", "standardbeagle.com:80"},
		{"https://standardbeagle.com:8443", "standardbeagle.com:8443"},
		{"http://localhost:3000", "localhost:3000"},
		{"HTTPS://example.com", "example.com:443"},
	}
	for _, c := range cases {
		u, err := url.Parse(c.target)
		if err != nil {
			t.Fatalf("parse %s: %v", c.target, err)
		}
		if got := backendProbeAddr(u); got != c.want {
			t.Errorf("backendProbeAddr(%s) = %q, want %q", c.target, got, c.want)
		}
	}
}
