//go:build windows

package sshclient

import (
	"os"
	"path/filepath"
	"strings"
)

// LocalForwardSocketPath is an AF_UNIX socket file under the user's temp
// directory. The name carries only the host's hash: Windows filenames cannot
// hold the ':' and '@' of a host identity, and AF_UNIX paths are capped near
// 108 bytes, which a profile-rooted temp directory already eats into.
func LocalForwardSocketPath(host string) string {
	pipe, err := windowsPipeName("forward", host)
	if err != nil {
		return ""
	}
	hash := pipe[strings.LastIndexByte(pipe, '-')+1:]
	return filepath.Join(os.TempDir(), "agnt", "ssh-"+hash+".sock")
}

// checkForwardDir is a no-op on Windows: the forward directory lives under the
// per-user temp directory, which other users cannot reach.
func checkForwardDir(string) error { return nil }
