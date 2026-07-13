//go:build !windows

package sshclient

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/standardbeagle/go-cli-server/socket"
)

func LocalForwardSocketPath(host string) string {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "agnt", fmt.Sprintf("ssh-%s.sock", host))
}

func listenLocalForward(path string) (net.Listener, func() error, error) {
	mgr := socket.NewManager(socket.Config{Path: path, Mode: 0o600, Name: "agnt-ssh-forward", ProcessMatcher: func(pid int) bool { return pid == os.Getpid() }})
	ln, err := mgr.Listen()
	if err != nil {
		return nil, nil, err
	}
	return ln, mgr.Close, nil
}
