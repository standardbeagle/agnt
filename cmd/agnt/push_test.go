package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/sshclient"
)

func TestSplitPushArgs_SingleFileNeverTreatedAsDest(t *testing.T) {
	files, dest := splitPushArgs([]string{"logo.png"})
	if len(files) != 1 || files[0] != "logo.png" || dest != "" {
		t.Errorf("splitPushArgs single arg = %v, %q; want [logo.png], \"\"", files, dest)
	}
}

func TestSplitPushArgs_TrailingNonExistentArgIsDest(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}

	files, dest := splitPushArgs([]string{existing, "assets/img"})
	if len(files) != 1 || files[0] != existing {
		t.Errorf("files = %v, want [%s]", files, existing)
	}
	if dest != "assets/img" {
		t.Errorf("dest = %q, want assets/img", dest)
	}
}

func TestSplitPushArgs_TrailingExistingFileIsNotDest(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.png")
	b := filepath.Join(dir, "b.png")
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("writing fixture file: %v", err)
		}
	}

	files, dest := splitPushArgs([]string{a, b})
	if len(files) != 2 || files[0] != a || files[1] != b {
		t.Errorf("files = %v, want [%s %s]", files, a, b)
	}
	if dest != "" {
		t.Errorf("dest = %q, want empty (both args are existing files)", dest)
	}
}

func TestSplitPushArgs_TrailingDirectoryIsDest(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.png")
	if err := os.WriteFile(a, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	files, dest := splitPushArgs([]string{a, subdir})
	if len(files) != 1 || files[0] != a {
		t.Errorf("files = %v, want [%s]", files, a)
	}
	if dest != subdir {
		t.Errorf("dest = %q, want %q", dest, subdir)
	}
}

func TestResolveActiveHost_NoSessionsIsLoud(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	_, err := resolveActiveHost()
	if err == nil {
		t.Fatal("expected error when no 'agnt ssh' session is active")
	}
	if !strings.Contains(err.Error(), "agnt ssh") {
		t.Errorf("error %q does not mention how to start a session", err.Error())
	}
}

// fakePingListener runs a minimal control-socket-shaped listener that only
// answers "ping" requests OK, standing in for a live 'agnt ssh' process so
// resolveActiveHost's disambiguation path can be exercised without a real
// SSH/SFTP fixture.
func fakePingListener(t *testing.T, host string) {
	t.Helper()
	path, err := sshclient.ControlSocketPath(host)
	if err != nil {
		t.Fatalf("ControlSocketPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 256)
				conn.Read(buf)
				conn.Write([]byte(`{"ok":true}` + "\n"))
			}()
		}
	}()
}

func TestResolveActiveHost_MultipleSessionsRequiresHostFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakePingListener(t, "host-a")
	fakePingListener(t, "host-b")

	_, err := resolveActiveHost()
	if err == nil {
		t.Fatal("expected error when multiple 'agnt ssh' sessions are active")
	}
	if !strings.Contains(err.Error(), "--host") {
		t.Errorf("error %q does not mention --host disambiguation", err.Error())
	}
	if !strings.Contains(err.Error(), "host-a") || !strings.Contains(err.Error(), "host-b") {
		t.Errorf("error %q does not list both candidate hosts", err.Error())
	}
}

func TestResolveActiveHost_SingleSessionResolves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakePingListener(t, "only-host")

	host, err := resolveActiveHost()
	if err != nil {
		t.Fatalf("resolveActiveHost: %v", err)
	}
	if host != "only-host" {
		t.Errorf("host = %q, want only-host", host)
	}
}
