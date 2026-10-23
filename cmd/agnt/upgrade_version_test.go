package main

import "testing"

func TestParseVersionOutput(t *testing.T) {
	for in, want := range map[string]string{
		"agnt v0.16.0\n":                      "0.16.0",
		"0.16.0\ndaemon: not running\n":       "0.16.0",
		"agnt v0.16.0\r\ndaemon: v0.15.0\r\n": "0.16.0",
		"v0.16.0":                             "0.16.0",
	} {
		if got := parseVersionOutput(in); got != want {
			t.Errorf("parseVersionOutput(%q) = %q, want %q", in, got, want)
		}
	}
}
