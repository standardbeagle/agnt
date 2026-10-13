//go:build windows

package main

import (
	"fmt"
	"os"
)

// secureOverlayDir creates dir if needed. Windows has no mode bits to set:
// the directory lives under the user's own %TEMP%, whose ACL already limits
// it to that user, and it inherits that ACL. It still must be a real
// directory, not something planted at the path.
func secureOverlayDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("exists and is not a directory")
	}
	return nil
}

// restrictOverlaySocket is a no-op on Windows: AF_UNIX socket files take
// their access control from the containing directory's ACL (see
// secureOverlayDir), and os.Chmod only toggles the read-only attribute.
func restrictOverlaySocket(string) error { return nil }
