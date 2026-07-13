//go:build windows

package sshclient

import (
	"strings"
	"testing"
)

// Kept dependency-narrow so Linux CI can cross-compile this Windows test file
// independently of unrelated Unix-only SSH harness tests in this package.
func TestWindowsForwardPipeContract(t *testing.T) {
	a := LocalForwardSocketPath("a:b")
	b := LocalForwardSocketPath("a@b")
	if a == b {
		t.Fatal("sanitized host identities collided")
	}
	if !strings.HasPrefix(strings.ToLower(a), `\\.\pipe\agnt-ssh-forward-`) {
		t.Fatalf("unexpected pipe path %q", a)
	}
	if _, _, err := listenLocalForward(`C:\tmp\agnt.sock`); err == nil {
		t.Fatal("filesystem path did not fail loudly")
	}
}
