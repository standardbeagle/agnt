//go:build windows

package sshclient

import (
	"path/filepath"
	"strings"
	"testing"
)

// Kept dependency-narrow so Linux CI can cross-compile this Windows test file
// independently of unrelated Unix-only SSH harness tests in this package.
func TestWindowsForwardSocketContract(t *testing.T) {
	a := LocalForwardSocketPath("a:b")
	b := LocalForwardSocketPath("a@b")
	if a == b {
		t.Fatal("sanitized host identities collided")
	}
	// The forward must be a socket file the daemon client can dial, not a
	// named pipe: every consumer of a forward is a daemon client.
	if strings.HasPrefix(a, `\\.\pipe\`) {
		t.Fatalf("forward endpoint is a named pipe, which no daemon client can dial: %q", a)
	}
	if !filepath.IsAbs(a) || filepath.Ext(a) != ".sock" {
		t.Fatalf("unexpected forward socket path %q", a)
	}
	if strings.ContainsAny(filepath.Base(a), `@:`) {
		t.Fatalf("forward socket name holds characters Windows filenames reject: %q", a)
	}
	if LocalForwardSocketPath("  ") != "" {
		t.Fatal("empty host must not yield a forward path")
	}
}
