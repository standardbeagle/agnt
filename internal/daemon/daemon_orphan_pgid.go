//go:build unix

package daemon

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/platform"
)

// startupOrphanPGIDGrace is how long each orphaned pgid gets to respond
// to SIGTERM before startupOrphanPGIDScan escalates to SIGKILL. Matches
// the short grace used by killSessionPGID on session cleanup: processes
// reached at this point are abandoned background jobs, not anything
// interactive, so a long graceful shutdown is wasted time.
const startupOrphanPGIDGrace = 2 * time.Second

// startupOrphanPGIDScan walks /proc on daemon startup and kills every
// POSIX process group whose session leader is dead but whose members are
// still alive and owned by the daemon's uid.
//
// This is the crash-recovery complement to Slice A. Slice A tracks each
// session's pgid in memory and reaps it on explicit session cleanup.
// If the daemon dies (crash, OOM, kill -9) before cleanup fires, that
// in-memory state is lost and the old PTY-child session pgid leaks --
// its members reparent to init but keep the original pgid. On the next
// startup THIS function catches them.
//
// Returns the number of orphan pgids successfully drained. Caller should
// NOT treat a zero return as failure: most startups will find nothing.
//
// Gated by `session.orphan-pgid-scan` in .agnt.kdl. When disabled, the
// function logs its skip decision through startupErrorStore (not just
// debug.Log) so the decision is visible to the AI agent, honoring the
// "no silent failures" rule from daemon-architecture.md.
func (d *Daemon) startupOrphanPGIDScan(projectPath string) int {
	log := d.startupErrorStore

	// Test safety escape hatch. The daemon test package's TestMain sets
	// AGNT_DISABLE_ORPHAN_SCAN=1 before running the suite so that the dozens
	// of integration tests that call daemon.Start() do not issue real kill(2)
	// syscalls against unrelated host pgids. Tests that specifically exercise
	// the scan unset this env var via t.Setenv and are build-tagged
	// `procisolation` so they only run inside a PID namespace via
	// `make test-isolated`. In production the env var is never set.
	//
	// This env var is NOT a config option and must not be documented as one
	// for end users — it is a test-only fence.
	if os.Getenv("AGNT_DISABLE_ORPHAN_SCAN") != "" {
		return 0
	}

	// Load config for gate evaluation. Failure to load is non-fatal --
	// we fall back to the default (scan enabled) and continue.
	enabled := true
	if projectPath != "" {
		cfg, err := config.LoadAgntConfig(projectPath)
		if err != nil {
			debug.Log("daemon", "startup orphan-pgid scan: config load failed (%v); using default (enabled)", err)
		} else {
			enabled = cfg.Session.OrphanPGIDScanEnabled()
		}
	}

	if !enabled {
		log.Add(&StartupLogEntry{
			Level:     "info",
			EventType: "startup_orphan_pgid_skipped",
			Message:   "orphan-pgid scan disabled by session.orphan-pgid-scan",
			Timestamp: time.Now(),
		})
		debug.Log("daemon", "startup orphan-pgid scan: disabled by config")
		return 0
	}

	selfPID := os.Getpid()
	selfPGID, err := syscall.Getpgid(selfPID)
	if err != nil {
		// On the vanishingly rare case Getpgid fails, fall back to 0 and
		// proceed -- the scan will still honor the selfPID exclusion in
		// KillSessionPGID, and pgid 0 is filtered out by ScanOrphanedPGIDs.
		selfPGID = 0
		debug.Warn("daemon", "startup orphan-pgid scan: Getpgid(self) failed: %v", err)
	}

	// Exclude the daemon's own pgid and any pgid we know belongs to a
	// live agnt session. The live-session guard is defense in depth:
	// Slice A already prevents overlap because live sessions have
	// isProcessAlive(pgid) == true, but belt-and-braces protects against
	// races where the leader momentarily disappears from /proc.
	exclude := make(map[int]bool)
	if selfPGID > 1 {
		exclude[selfPGID] = true
	}
	for _, sess := range d.liveSessionPGIDs() {
		if sess > 1 {
			exclude[sess] = true
		}
	}

	uid := syscall.Getuid()
	orphans := platform.ScanOrphanedPGIDs(uid, exclude)

	if len(orphans) == 0 {
		log.Add(&StartupLogEntry{
			Level:     "info",
			EventType: "startup_orphan_pgid_scan",
			Message:   "orphan-pgid scan: no leaked session groups found",
			Timestamp: time.Now(),
		})
		return 0
	}

	log.Add(&StartupLogEntry{
		Level:     "info",
		EventType: "startup_orphan_pgid_scan",
		Message:   fmt.Sprintf("orphan-pgid scan: found %d leaked session group(s)", len(orphans)),
		Timestamp: time.Now(),
	})
	debug.Info("daemon", "startup orphan-pgid scan: found %d orphan(s)", len(orphans))

	killed := 0
	for _, o := range orphans {
		debug.Info("daemon", "startup orphan-pgid scan: killing pgid %d (members=%v)", o.PGID, o.Members)
		if err := platform.KillSessionPGID(o.PGID, selfPID, startupOrphanPGIDGrace, false); err != nil {
			debug.Warn("daemon", "startup orphan-pgid scan: killpg(%d) failed: %v", o.PGID, err)
			log.Add(&StartupLogEntry{
				Level:     "warning",
				EventType: "startup_orphan_pgid_kill_failed",
				Message:   fmt.Sprintf("failed to reap orphan pgid %d: %v", o.PGID, err),
				Timestamp: time.Now(),
			})
			continue
		}
		killed++
		log.Add(&StartupLogEntry{
			Level:     "info",
			EventType: "startup_orphan_pgid_killed",
			Message:   fmt.Sprintf("reaped orphan pgid %d (%d member(s))", o.PGID, len(o.Members)),
			Timestamp: time.Now(),
		})
	}

	if killed > 0 {
		debug.Info("daemon", "startup orphan-pgid scan: reaped %d/%d orphan group(s)", killed, len(orphans))
	}
	return killed
}

// liveSessionPGIDs returns the pgids of every currently-registered session
// that reported a pgid. Used by startupOrphanPGIDScan to avoid touching
// sessions that are live at scan time. Normally empty at startup (no
// sessions yet), but defensive for unit tests and hot-restart paths that
// may pre-populate the registry.
func (d *Daemon) liveSessionPGIDs() []int {
	if d.sessionRegistry == nil {
		return nil
	}
	var out []int
	for _, s := range d.sessionRegistry.List("", true) {
		s.mu.RLock()
		pgid := s.SessionPGID
		s.mu.RUnlock()
		if pgid > 1 {
			out = append(out, pgid)
		}
	}
	return out
}
