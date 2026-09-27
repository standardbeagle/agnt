package devoidc

import "testing"

func TestRedirectURIMatches(t *testing.T) {
	cases := []struct {
		pattern, uri string
		want         bool
	}{
		{"https://dev.example.com/cb", "https://dev.example.com/cb", true},
		{"https://dev.example.com/cb", "https://dev.example.com/cb/", false},
		{"https://dev.example.com/cb", "https://dev.example.com/cb?x=1", false},
		{"http://localhost:*/cb", "http://localhost:3000/cb", true},
		{"http://localhost:*/cb", "http://localhost:65535/cb", true},
		{"http://localhost:*/cb", "http://localhost/cb", false},
		{"http://localhost:*/cb", "http://localhost:3000/cb/evil", false},
		{"http://localhost:*/cb", "http://localhost:3000.evil.com/cb", false},
		{"http://localhost:*/cb", "http://localhost:3000@evil.com/cb", false},
		{"http://localhost:*/cb", "http://localhost:123456/cb", false},
		{"http://localhost:*/cb", "http://127.0.0.1:3000/cb", false},
	}
	for _, tc := range cases {
		if got := RedirectURIMatches(tc.pattern, tc.uri); got != tc.want {
			t.Errorf("RedirectURIMatches(%q, %q) = %v, want %v", tc.pattern, tc.uri, got, tc.want)
		}
	}
}
