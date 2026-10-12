//go:build !windows

package sshclient

import (
	"fmt"
	"os"
	"path/filepath"
)

func LocalForwardSocketPath(host string) string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "agnt", fmt.Sprintf("ssh-%s.sock", host))
}
