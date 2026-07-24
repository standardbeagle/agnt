//go:build windows

package daemon

// migrateLegacySocket is a no-op on Windows.
//
// The legacy migration (see socket_migrate.go) retires a daemon left on the
// pre-0.13.32 socket path /tmp/<name>-<uid>.sock. That path and the
// hardened-bind per-uid-subdirectory move that superseded it are Unix /tmp +
// os.Getuid() constructs; Windows resolves its socket via
// socket.DefaultSocketPath (a different scheme entirely) and never had that
// legacy layout, so there is nothing to migrate. daemon.go calls this on
// every platform, hence the Windows stub.
func (d *Daemon) migrateLegacySocket() {}
