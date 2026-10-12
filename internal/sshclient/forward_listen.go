package sshclient

import (
	"net"
	"os"
	"path/filepath"

	"github.com/standardbeagle/go-cli-server/socket"
)

// listenLocalForward binds the local end of the daemon-socket forward. It is
// the same transport on every platform — the one the daemon itself listens on
// (a Unix-domain socket, AF_UNIX on Windows too) — because everything that
// connects to a forward is a daemon client: `agnt monitor` / `agnt doctor` via
// AGNT_DAEMON_SOCKET, the port-forward manager, the session probes, and
// NotifyFileArrived. A forward on any other transport is one no client can
// dial; native Windows used a named pipe here and so could never be reached.
func listenLocalForward(path string) (net.Listener, func() error, error) {
	// The socket's directory must exist before the bind; on Windows a bind
	// into a missing directory fails as an unrelated-looking "dead network".
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	mgr := socket.NewManager(socket.Config{Path: path, Mode: 0o600, Name: "agnt-ssh-forward", ProcessMatcher: func(pid int) bool { return pid == os.Getpid() }})
	ln, err := mgr.Listen()
	if err != nil {
		return nil, nil, err
	}
	return ln, mgr.Close, nil
}
