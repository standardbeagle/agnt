//go:build !windows

package sshclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/testutil/sockdir"
)

// Without XDG_RUNTIME_DIR the forward socket lives in <TMPDIR>/agnt, a name
// any local user can create first. A directory that is not this user's, or
// that others can reach, must stop the bind loudly rather than be used.
func TestListenLocalForward_RefusesDirectoryOthersCanReach(t *testing.T) {
	root := sockdir.Short(t)

	open := filepath.Join(root, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o777); err != nil { // umask-proof
		t.Fatal(err)
	}
	if _, _, err := listenLocalForward(filepath.Join(open, "f.sock")); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("world-writable directory: err = %v, want a refusal naming the required mode", err)
	}
	if _, err := os.Lstat(filepath.Join(open, "f.sock")); !os.IsNotExist(err) {
		t.Fatalf("a socket was bound in the refused directory (lstat err = %v)", err)
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(sockdir.Short(t), link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listenLocalForward(filepath.Join(link, "f.sock")); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("symlinked directory: err = %v, want a refusal", err)
	}

	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateDir(private, os.Getuid()+1); err == nil || !strings.Contains(err.Error(), "owned by uid") {
		t.Fatalf("foreign owner: err = %v, want a refusal naming the owner", err)
	}

	// The normal case still binds, and a fresh directory is created private.
	ln, cleanup, err := listenLocalForward(filepath.Join(root, "fresh", "f.sock"))
	if err != nil {
		t.Fatalf("private directory refused: %v", err)
	}
	ln.Close()
	_ = cleanup()
}
