//go:build windows

package sshclient

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"
)

func TestWindowsNamedPipePaths(t *testing.T) {
	control, err := ControlSocketPath("User@Example:22")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{control} {
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
	if _, err := listenControlTransport(`C:\tmp\agnt.ctl`); err == nil {
		t.Fatal("filesystem control path must fail loudly")
	}
}

// The forward and the daemon client must speak the same transport: a client
// dialing the forward's path the way it dials any daemon socket must connect.
func TestWindowsForwardIsDialableByDaemonClient(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "fwd.sock")
	ln, cleanup, err := listenLocalForward(path)
	if err != nil {
		t.Fatalf("listenLocalForward: %v", err)
	}
	defer cleanup()
	go func() {
		if conn, err := ln.Accept(); err == nil {
			conn.Close()
		}
	}()

	conn, err := daemonclient.Connect(path)
	if err != nil {
		t.Fatalf("daemon client could not dial the forward: %v", err)
	}
	conn.Close()
}

func TestWindowsDiscoveryFailsLoudly(t *testing.T) {
	if _, err := DiscoverActiveHosts(); err == nil || !strings.Contains(err.Error(), "--host") {
		t.Fatalf("unexpected error: %v", err)
	}
}
