//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// secureOverlayDir makes dir a private (0700) directory owned by the current
// user, creating it if needed. It refuses a path it cannot vouch for: a
// symlink, a non-directory, or a directory owned by someone else — in a
// world-writable /tmp any of those can be planted ahead of us, and sockets
// created inside would be reachable (or redirected) by whoever planted it.
func secureOverlayDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	// Lstat, not Stat: a symlink must be seen as a symlink.
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("exists and is not a directory")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine owner")
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("owned by uid %d, not the current user (uid %d)", st.Uid, os.Getuid())
	}
	if info.Mode().Perm() != 0o700 {
		// Ours but too open (created under an older layout or a loose umask).
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// restrictOverlaySocket makes the socket connectable by its owner only.
// net.Listen creates it under the process umask, which can leave it open to
// the group or everyone.
func restrictOverlaySocket(path string) error {
	return os.Chmod(path, 0o600)
}
