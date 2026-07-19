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

// scanOrphanedPGIDsFn, walkParentsFn, and killSessionPGIDFn indirect the real
// /proc scan, ancestor-chain walk, and pgid signal through package vars —
// mirroring the portProbe seam in port_preflight.go — so PORTS CLEAN-ORPHANS'
// ownership-gate decision can be regression tested end to end through the
// real command handler without a genuine dead-leader process tree or a real
// kill(2). Production always uses the platform.* implementations; only tests
// override these.
var (
	scanOrphanedPGIDsFn = platform.ScanOrphanedPGIDs
	walkParentsFn       = platform.WalkParents
	killSessionPGIDFn   = platform.KillSessionPGID
)

// reapOrphans kills every orphaned process group that passes the ownership
// gate for projectPath (see pgidOwnershipCheck), returning the reaped pgids
// and any failures. Used by PORTS CLEAN-ORPHANS. A shared uid is not
// ownership evidence on its own — this mirrors the same gate the startup
// orphan-pgid scan applies (daemon_orphan_pgid.go), so a second daemon's (or
// an unrelated same-uid process's) dead-leader pgid is never reaped just
// because it happens to share this daemon's uid.
func (d *Daemon) reapOrphans(projectPath string) ([]int, []map[string]interface{}) {
	orphans := scanOrphanedPGIDsFn(syscall.Getuid(), orphanScanExcludes())
	selfPID := syscall.Getpid()
	return reapOrphanCandidates(orphans, d.knownProjectPaths(projectPath), resolvedDaemonBinary(), walkParentsFn,
		func(pgid int) error {
			return killSessionPGIDFn(pgid, selfPID, startupOrphanPGIDGrace, false)
		})
}

// reapOrphanCandidates applies the startup-recovery ownership gate
// (pgidOwnershipCheck) to each candidate pgid before signaling it. Extracted
// from reapOrphans so the gating decision can be tested without invoking a
// real kill syscall (killFn is injected).
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
