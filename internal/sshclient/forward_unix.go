//go:build !windows

package sshclient

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func LocalForwardSocketPath(host string) string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "agnt", fmt.Sprintf("ssh-%s.sock", host))
}

// checkForwardDir refuses a forward-socket directory another user could reach.
// Without XDG_RUNTIME_DIR the directory is <TMPDIR>/agnt, usually /tmp/agnt,
// which any local user can create first: MkdirAll then leaves their directory
// in place, and its owner can replace or remove the socket that daemon clients
// (monitor, doctor, port forwards) dial. The directory must be a real
// directory (not a symlink), owned by this uid, with no group or other bits.
func checkForwardDir(dir string) error {
	return checkPrivateDir(dir, os.Getuid())
}

func checkPrivateDir(dir string, uid int) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("forward socket directory %s is not a directory (%s); remove it and retry", dir, fi.Mode().Type())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("forward socket directory %s: cannot read its owner", dir)
	}
	if int(st.Uid) != uid {
		return fmt.Errorf("forward socket directory %s is owned by uid %d, not this user (uid %d); another user may have created it — remove it or set XDG_RUNTIME_DIR", dir, st.Uid, uid)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("forward socket directory %s has mode %#o, want 0700; other users can reach the sockets in it — run chmod 700 %s", dir, perm, dir)
	}
	return nil
}
