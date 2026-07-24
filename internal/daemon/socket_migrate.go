//go:build !windows

package daemon

import (
	"errors"
	"fmt"
	"os"

	"github.com/standardbeagle/go-cli-server/socket"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
)

// migrateLegacySocket retires a daemon left listening on the pre-0.13.32 socket
// path (/tmp/<name>-<uid>.sock, before the hardened bind forced a per-uid
// subdirectory).
//
// After an in-place upgrade the old daemon keeps running, unreachable: the new
// client looks only at the new path, autostarts a second daemon, and the old one
// lingers holding its managed processes and their ports. Nothing ever reaps it,
// because nothing can still find it.
//
// A live legacy daemon is asked to shut down gracefully, which stops the
// processes it owns rather than orphaning them. A stale socket file is removed.
// Only runs when this daemon owns the default path — an explicit AGNT_SOCKET or
// --socket means the caller is managing socket layout themselves.
func (d *Daemon) migrateLegacySocket() {
	if d.config.SocketPath != protocol.DefaultSocketPath() {
		return
	}
	d.migrateLegacySocketFrom(protocol.LegacySocketPath())
}

// migrateLegacySocketFrom is migrateLegacySocket against an explicit path, so a
// test can exercise it without a daemon on the machine's real default socket.
func (d *Daemon) migrateLegacySocketFrom(legacy string) {
	if _, err := os.Stat(legacy); err != nil {
		return // no legacy socket, nothing to migrate
	}

	if socket.IsRunning(legacy) {
		debug.Log("daemon", "legacy daemon found at %s, requesting shutdown", legacy)
		if err := stopDaemonAt(legacy); err != nil {
			// Loud, not silent: a surviving legacy daemon holds ports this one
			// is about to fight over, and the user needs to know which to kill.
			d.daemonStartupLog("warning", "legacy_socket_migration",
				fmt.Sprintf("a daemon from an older agnt is still running at %s and could not be stopped (%v); "+
					"stop it manually or its processes will keep holding their ports", legacy, err))
			return
		}
		d.daemonStartupLog("info", "legacy_socket_migration",
			fmt.Sprintf("stopped the daemon left at the pre-0.13.32 socket path %s", legacy))
	}

	if err := os.Remove(legacy); err != nil && !os.IsNotExist(err) {
		debug.Warn("daemon", "could not remove legacy socket %s: %v", legacy, err)
	}
}

// stopDaemonAt asks the daemon at socketPath to shut down via the protocol,
// using the daemon's own Conn wrapper rather than the client package (the
// server must not depend on its own client). A missing daemon is success.
func stopDaemonAt(socketPath string) error {
	conn := NewConn(socketPath)
	if err := conn.EnsureConnected(); err != nil {
		if errors.Is(err, socket.ErrSocketNotFound) {
			return nil // Daemon not running, nothing to stop
		}
		return err
	}
	defer conn.Close()
	return conn.Request("SHUTDOWN").OK()
}
