//go:build !windows

package main

import (
	"strings"
	"testing"
)

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

// TestSSHToolFlagRejected pins the drop decision for the inert --tool flag
// (see .claude/rules/lessons-ssh-transport.md item 3, and the tracking epic
// 01KWMARXTVWKC33EPHZZJ43JT9 where remote tool selection will eventually
// land): agnt ssh must not silently accept-and-ignore --tool. The flag
// definition was removed entirely, so cobra's own flag parser rejects it as
// unknown before RunE (and thus before any SSH dial attempt) ever runs.
//
// Execute must be invoked on rootCmd, not sshCmd directly: cobra's
// ExecuteC redirects any command with a parent to its root and uses the
// ROOT's configured args (see cobra's "run on Root only" behavior), so
// SetArgs on the child alone would be silently ignored.
func TestSSHToolFlagRejected(t *testing.T) {
	rootCmd.SetArgs([]string{"ssh", "myhost", "--tool", "claude"})
	defer rootCmd.SetArgs(nil)

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected an error for unknown --tool flag, got nil")
	}
	if !strings.Contains(err.Error(), "tool") {
		t.Fatalf("expected error to mention the rejected flag %q, got: %v", "--tool", err)
	}
}
