package config

import (
	"net/url"
	"testing"
)

func TestLocalHTTPURL(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"", 3000, "http://localhost:3000"},
		{"127.0.0.1", 8080, "http://127.0.0.1:8080"},
		{"dev.internal", 80, "http://dev.internal:80"},
		{"::1", 3000, "http://[::1]:3000"},
		{"[::1]", 3000, "http://[::1]:3000"},
		{"fd7a:115c:a1e0::1", 5173, "http://[fd7a:115c:a1e0::1]:5173"},
	}
	for _, tc := range cases {
		got := LocalHTTPURL(tc.host, tc.port)
		if got != tc.want {
			t.Errorf("LocalHTTPURL(%q, %d) = %q, want %q", tc.host, tc.port, got, tc.want)
		}
		// net/url rejects an unbracketed IPv6 host, so every result must parse.
		if _, err := url.Parse(got); err != nil {
			t.Errorf("LocalHTTPURL(%q, %d) = %q does not parse: %v", tc.host, tc.port, got, err)
		}
	}
}

func TestProxyConfigExplicitTargetURL(t *testing.T) {
	cases := []struct {
		name string
		cfg  ProxyConfig
		want string
	}{
		{"url wins", ProxyConfig{URL: "https://a.test", Port: 3000, Target: "http://b.test"}, "https://a.test"},
		{"port over legacy target", ProxyConfig{Port: 3000, Target: "http://b.test"}, "http://localhost:3000"},
		{"host and port", ProxyConfig{Host: "::1", Port: 3000}, "http://[::1]:3000"},
		{"legacy target", ProxyConfig{Target: "http://b.test"}, "http://b.test"},
		{"script-linked", ProxyConfig{Script: "dev"}, ""},
	}
	for _, tc := range cases {
		if got := tc.cfg.ExplicitTargetURL(); got != tc.want {
			t.Errorf("%s: ExplicitTargetURL() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
