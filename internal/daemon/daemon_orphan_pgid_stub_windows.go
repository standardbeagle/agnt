//go:build windows

package daemon

// startupOrphanPGIDScan is a no-op on Windows. POSIX process groups do not
// exist on Windows; orphaned process cleanup is handled via Job Objects in
// the platform-specific process manager path, not by scanning /proc for
// dead pgid leaders.
//
// The signature mirrors the unix implementation so cross-platform callers
// (e.g. Daemon.Start) can invoke it unconditionally.
func (d *Daemon) startupOrphanPGIDScan(projectPath string) int {
	return 0
}
