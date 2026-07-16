//go:build unix

package daemon

import (
	"syscall"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/platform"
)

// scanOrphans returns orphaned process groups (leader dead, members alive)
// owned by the caller's uid, excluding the daemon's own pgid.
func (d *Daemon) scanOrphans() []platform.OrphanPGID {
	return platform.ScanOrphanedPGIDs(syscall.Getuid(), orphanScanExcludes())
}

// reapOrphans kills every orphaned process group, returning the reaped pgids
// and any failures. Used by PORTS CLEAN-ORPHANS.
func (d *Daemon) reapOrphans(projectPath string) ([]int, []map[string]interface{}) {
	orphans := platform.ScanOrphanedPGIDs(syscall.Getuid(), orphanScanExcludes())
	selfPID := syscall.Getpid()
	return reapOrphanCandidates(orphans, d.knownProjectPaths(projectPath), resolvedDaemonBinary(), platform.WalkParents,
		func(pgid int) error {
			return platform.KillSessionPGID(pgid, selfPID, startupOrphanPGIDGrace, false)
		})
}

// reapOrphanCandidates applies the startup recovery ownership gate before
// signaling a same-uid orphan group. A shared uid is not ownership evidence.
func reapOrphanCandidates(orphans []platform.OrphanPGID, knownProjects []string, daemonBinary string,
	walkFn func(int) []platform.AncestorInfo, killFn func(int) error,
) ([]int, []map[string]interface{}) {
	reaped := make([]int, 0, len(orphans))
	failed := make([]map[string]interface{}, 0)
	for _, orph := range orphans {
		owned, reason := pgidOwnershipCheck(orph.Members, knownProjects, daemonBinary, walkFn)
		if !owned {
			debug.Info("daemon", "PORTS CLEAN-ORPHANS: skip pgid %d (unowned): %s", orph.PGID, reason)
			continue
		}
		if killFn == nil {
			failed = append(failed, map[string]interface{}{"pgid": orph.PGID, "error": "orphan kill function unavailable"})
			continue
		}
		if err := killFn(orph.PGID); err != nil {
			failed = append(failed, map[string]interface{}{"pgid": orph.PGID, "error": err.Error()})
			continue
		}
		reaped = append(reaped, orph.PGID)
	}
	return reaped, failed
}

// orphanScanExcludes returns the pgid set the orphan scan must never reap:
// the daemon's own process group (defensive).
func orphanScanExcludes() map[int]bool {
	excludes := make(map[int]bool)
	if pgid, err := syscall.Getpgid(syscall.Getpid()); err == nil && pgid > 1 {
		excludes[pgid] = true
	}
	return excludes
}
