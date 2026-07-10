//go:build !windows

package main

import "testing"

func TestParseHostPath(t *testing.T) {
	cases := []struct {
		arg      string
		wantHost string
		wantPath string
	}{
		{"myhost", "myhost", ""},
		{"myhost:/remote/path", "myhost", "/remote/path"},
		{"user@myhost:relative/dir", "user@myhost", "relative/dir"},
		{"myhost:", "myhost", ""},
		// Documented rule: split on the FIRST colon, so a second colon
		// (unusual, but shows the rule is unambiguous) stays in the path.
		{"myhost:/a:b", "myhost", "/a:b"},
	}
	for _, c := range cases {
		host, path := parseHostPath(c.arg)
		if host != c.wantHost || path != c.wantPath {
			t.Errorf("parseHostPath(%q) = (%q, %q), want (%q, %q)", c.arg, host, path, c.wantHost, c.wantPath)
		}
	}
}
