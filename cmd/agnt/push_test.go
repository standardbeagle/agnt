package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/sshclient"
)

func TestSplitPushArgs_SingleFileNeverTreatedAsDest(t *testing.T) {
	files, dest, ambiguous := splitPushArgs([]string{"logo.png"})
	if len(files) != 1 || files[0] != "logo.png" || dest != "" || ambiguous {
		t.Errorf("splitPushArgs single arg = %v, %q, ambiguous=%v; want [logo.png], \"\", false", files, dest, ambiguous)
	}
}

func TestSplitPushArgs_TrailingNonExistentArgIsAmbiguousDest(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}

	files, dest, ambiguous := splitPushArgs([]string{existing, "assets/img"})
	if len(files) != 1 || files[0] != existing {
		t.Errorf("files = %v, want [%s]", files, existing)
	}
	if dest != "assets/img" {
		t.Errorf("dest = %q, want assets/img", dest)
	}
	// A last arg that does not exist on disk must be flagged ambiguous so it
	// is confirmed rather than silently misrouted (a typo'd filename would
	// otherwise become a remote directory).
	if !ambiguous {
		t.Error("non-existent trailing arg must be flagged ambiguous, not silently used as a dest dir")
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

	files, dest, ambiguous := splitPushArgs([]string{a, b})
	if len(files) != 2 || files[0] != a || files[1] != b {
		t.Errorf("files = %v, want [%s %s]", files, a, b)
	}
	if dest != "" || ambiguous {
		t.Errorf("dest = %q, ambiguous=%v; want empty, false (both args are existing files)", dest, ambiguous)
	}
}

func TestSplitPushArgs_TrailingDirectoryIsUnambiguousDest(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.png")
	if err := os.WriteFile(a, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing fixture file: %v", err)
	}
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	files, dest, ambiguous := splitPushArgs([]string{a, subdir})
	if len(files) != 1 || files[0] != a {
		t.Errorf("files = %v, want [%s]", files, a)
	}
	if dest != subdir {
		t.Errorf("dest = %q, want %q", dest, subdir)
	}
	// An existing directory is an unambiguous destination — no confirmation.
	if ambiguous {
		t.Error("existing directory dest must not be flagged ambiguous")
	}
}

func TestConfirmAmbiguousDest_NonInteractiveFailsLoud(t *testing.T) {
	// os.Stdin in the test process is not a terminal, so confirmAmbiguousDest
	// must refuse rather than silently proceed to treat the arg as a dest dir.
	cmd := &cobra.Command{}
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(""))

	err := confirmAmbiguousDest(cmd, "assets/img")
	if err == nil {
		t.Fatal("expected confirmAmbiguousDest to fail loud in a non-interactive session")
	}
	if !strings.Contains(err.Error(), "assets/img") || !strings.Contains(err.Error(), "non-interactive") {
		t.Errorf("error = %v, want it to name the ambiguous arg and the non-interactive refusal", err)
	}
	if !strings.Contains(stderr.String(), "does not name an existing local file") {
		t.Errorf("stderr = %q, want a warning that the arg is being treated as a destination", stderr.String())
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
