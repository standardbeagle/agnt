//go:build windows

package daemon

import "github.com/standardbeagle/agnt/internal/platform"

// scanOrphans is a no-op on Windows. Orphaned POSIX process groups have no
// Windows analogue — Job Objects provide cascade-kill for the PTY child tree,
// so there is nothing for the ports panel to surface here.
func (d *Daemon) scanOrphans() []platform.OrphanPGID {
	return nil
}

// reapOrphans is a no-op on Windows (see scanOrphans).
func (d *Daemon) reapOrphans(string) ([]int, []map[string]interface{}) {
	return nil, nil
}
