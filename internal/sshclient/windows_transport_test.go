//go:build windows

package sshclient

import (
	"strings"
	"testing"
)

func TestWindowsNamedPipePaths(t *testing.T) {
	forward := LocalForwardSocketPath("User@Example:22")
	control, err := ControlSocketPath("User@Example:22")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{forward, control} {
		if !strings.HasPrefix(strings.ToLower(path), `\\.\pipe\agnt-ssh-`) {
			t.Fatalf("not an agnt named pipe: %q", path)
		}
		if strings.ContainsAny(strings.TrimPrefix(path, `\\.\pipe\`), `@:`) {
			t.Fatalf("unsafe pipe component: %q", path)
		}
	}
}

func TestWindowsControlPathRejectsEmptyHost(t *testing.T) {
	if _, err := ControlSocketPath("  "); err == nil {
		t.Fatal("empty host must fail loudly")
	}
}

func TestWindowsPipeCanonicalizationAvoidsSanitizationCollisions(t *testing.T) {
	if LocalForwardSocketPath("a:b") == LocalForwardSocketPath("a@b") {
		t.Fatal("distinct host identities must not collide after sanitization")
	}
}

func TestWindowsListenersRejectFilesystemPaths(t *testing.T) {
	if _, _, err := listenLocalForward(`C:\tmp\agnt.sock`); err == nil {
		t.Fatal("filesystem forward path must fail loudly")
	}
	if _, err := listenControlTransport(`C:\tmp\agnt.ctl`); err == nil {
		t.Fatal("filesystem control path must fail loudly")
	}
}

func TestWindowsDiscoveryFailsLoudly(t *testing.T) {
	if _, err := DiscoverActiveHosts(); err == nil || !strings.Contains(err.Error(), "--host") {
		t.Fatalf("unexpected error: %v", err)
	}
}
