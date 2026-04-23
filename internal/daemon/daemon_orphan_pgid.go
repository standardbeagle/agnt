//go:build unix

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	// Test-safety fence: DaemonConfig.OrphanScanEnabled defaults to false
	// (zero value), so any test that constructs a daemon with a literal
	// DaemonConfig{} skips the scan automatically. Production opts in by
	// setting OrphanScanEnabled=true in cmd/agnt/daemon.go. Tests that
	// specifically exercise the scan (procisolation build tag) flip
	// d.config.OrphanScanEnabled = true before calling Start.
	//
	// This field is internal — it must not be documented as a user-facing
	// config knob. See iter 15 (task 6btkGG5QUGTL) for the migration from
	// the legacy AGNT_DISABLE_ORPHAN_SCAN env var fence.
	if !d.config.OrphanScanEnabled {
		log.Add(&StartupLogEntry{
			Level:     "info",
			EventType: "startup_orphan_pgid_skipped_by_config",
			Message:   "orphan-pgid scan skipped: DaemonConfig.OrphanScanEnabled=false",
			Timestamp: time.Now(),
		})
		debug.Log("daemon", "startup orphan-pgid scan: disabled by DaemonConfig (zero-value default)")
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

	// Ownership gate: before reaping any candidate pgid, verify it
	// plausibly belongs to THIS daemon by walking each live member's
	// parent chain and matching cmdline+cwd evidence against the
	// daemon's known project set and binary path. See pgidOwnershipCheck
	// docstring for the full predicate.
	//
	// This fence is the fix for task wJimXk0hzYAC: without it, a second
	// daemon on the same host (including test daemons spawned by
	// `go test ./internal/daemon/...`) can SIGKILL the live session
	// pgids of an unrelated, healthy daemon owned by the same uid —
	// uid alone is not sufficient isolation between multiple daemons.
	knownProjects := d.knownProjectPaths(projectPath)
	daemonBinary := resolvedDaemonBinary()

	killed := 0
	for _, o := range orphans {
		owned, reason := pgidOwnershipCheck(
			o.Members,
			knownProjects,
			daemonBinary,
			platform.WalkParents,
		)
		if !owned {
			debug.Info("daemon", "startup orphan-pgid scan: skip pgid %d (unowned): %s", o.PGID, reason)
			log.Add(&StartupLogEntry{
				Level:     "info",
				EventType: "startup_orphan_pgid_skipped_unowned",
				Message: fmt.Sprintf(
					"skipped orphan pgid %d (%d member(s)): %s",
					o.PGID, len(o.Members), reason,
				),
				Timestamp: time.Now(),
			})
			continue
		}
		debug.Info("daemon", "startup orphan-pgid scan: killing pgid %d (members=%v) owned: %s",
			o.PGID, o.Members, reason)
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
			Message: fmt.Sprintf(
				"reaped orphan pgid %d (%d member(s)): %s",
				o.PGID, len(o.Members), reason,
			),
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

// knownProjectPaths assembles the set of project directories this daemon
// considers its own. The union of:
//
//  1. The projectPath argument, if non-empty.
//  2. Every ProjectPath recorded on a currently-registered session.
//
// Used by the orphan-pgid ownership gate to decide whether a candidate
// pgid's ancestor cwd identifies it as ours. Returns a deduped slice with
// empty entries dropped. Order is not stable — callers must not rely on it.
//
// This is the scanning daemon's own view of "what projects do I manage".
// It is emphatically NOT a global view of every agnt project on the host —
// a second daemon has its own knownProjectPaths set and its own ancestor
// chains to match against. The asymmetry is the point: it lets each daemon
// reap only its own crash leftovers and leave other daemons' live sessions
// alone.
func (d *Daemon) knownProjectPaths(projectPath string) []string {
	seen := make(map[string]bool, 4)
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		clean := filepath.Clean(p)
		if clean == "" || clean == "." {
			return
		}
		if seen[clean] {
			return
		}
		seen[clean] = true
		out = append(out, clean)
	}

	add(projectPath)

	if d.sessionRegistry != nil {
		for _, s := range d.sessionRegistry.List("", true) {
			s.mu.RLock()
			p := s.ProjectPath
			s.mu.RUnlock()
			add(p)
		}
	}
	return out
}

// cmdlineLooksLikeAgnt returns true if a /proc/<pid>/cmdline string
// identifies the process as either an agnt run invocation or a daemon
// binary. Matching is intentionally permissive:
//
//   - "agnt run" substring — covers `agnt run claude`, `/usr/local/bin/agnt run ...`,
//     and renamed copies like `agnt-daemon run` (the binary-copy workaround).
//   - daemon binary path prefix — covers any invocation of `/path/to/agnt daemon start ...`
//     or bare `/path/to/agnt` as pid 0 of a daemon subprocess.
//
// The daemonBinary argument is whatever this daemon's own os.Executable()
// returned (resolved via filepath.EvalSymlinks when possible). Empty
// daemonBinary disables the binary-path branch — only substring matching
// applies. Empty cmdline always returns false.
func cmdlineLooksLikeAgnt(cmdline, daemonBinary string) bool {
	if cmdline == "" {
		return false
	}
	if strings.Contains(cmdline, "agnt run") {
		return true
	}
	if daemonBinary != "" {
		// Match as prefix OR as the first space-separated token
		// (handles cmdlines like "/opt/agnt/bin/agnt daemon start").
		if strings.HasPrefix(cmdline, daemonBinary+" ") || cmdline == daemonBinary {
			return true
		}
		// Also match just the basename at the start, for cases where
		// the cmdline path differs from os.Executable() (e.g. agnt copy
		// at agnt-daemon, symlinks). Conservative: require an exact
		// basename match as the first token.
		base := filepath.Base(daemonBinary)
		if base != "" && (strings.HasPrefix(cmdline, base+" ") || cmdline == base) {
			return true
		}
	}
	return false
}

// cwdIsInsideKnownProject returns true if cwd is equal to or a
// descendant of any entry in knownProjects. Path comparison uses
// filepath.Rel to avoid sibling-prefix collisions (e.g. /proj/foo must
// NOT match /proj/foobar). Empty cwd or empty known set returns false.
func cwdIsInsideKnownProject(cwd string, knownProjects []string) bool {
	if cwd == "" || len(knownProjects) == 0 {
		return false
	}
	cleanCwd := filepath.Clean(cwd)
	for _, proj := range knownProjects {
		if proj == "" {
			continue
		}
		cleanProj := filepath.Clean(proj)
		if cleanCwd == cleanProj {
			return true
		}
		rel, err := filepath.Rel(cleanProj, cleanCwd)
		if err != nil {
			continue
		}
		// rel must not start with ".." and must not be "."; "." is
		// already handled above. A rel like "packages/frontend" means
		// cwd is inside proj. A rel like "../foobar" means they are
		// siblings (or unrelated).
		if rel == "." || rel == "" {
			return true
		}
		if !strings.HasPrefix(rel, "..") && !strings.HasPrefix(rel, string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// pgidOwnershipCheck is the ownership gate for daemon-startup orphan-pgid
// reaping. It answers: "does this candidate pgid plausibly belong to the
// scanning daemon, such that reaping it is safe?"
//
// The gate is a conjunction of two evidence categories, collected across
// the parent chains of ALL live members:
//
//  1. cmdline evidence: at least one ancestor has cmdline matching
//     cmdlineLooksLikeAgnt (agnt run or daemon binary).
//  2. cwd evidence: at least one ancestor has cwd inside a directory in
//     knownProjects (exact match or descendant via filepath.Rel).
//
// Both must be true SOMEWHERE in the walked chains — they need not come
// from the same ancestor. If either evidence is missing the pgid is
// classified as unowned and the caller must log it as
// startup_orphan_pgid_skipped_unowned rather than reaping.
//
// walkFn is injected for testability. Production passes
// platform.WalkParents; tests pass hand-rolled chains. walkFn is called
// once per live member. A nil walkFn or empty members slice always
// returns (false, reason).
//
// The returned reason string is diagnostic-only — it is included in the
// structured log entry so operators can see WHY a pgid was or was not
// reaped. Never shown to end users as an error.
func pgidOwnershipCheck(
	members []int,
	knownProjects []string,
	daemonBinary string,
	walkFn func(int) []platform.AncestorInfo,
) (bool, string) {
	if len(members) == 0 {
		return false, "no live members to walk"
	}
	if walkFn == nil {
		return false, "no parent-chain walker"
	}
	if len(knownProjects) == 0 {
		return false, "no known project paths configured for this daemon"
	}

	var (
		sawCmdlineMatch bool
		sawCwdMatch     bool
		matchingCmdline string
		matchingCwd     string
	)

	for _, m := range members {
		if m <= 1 {
			continue
		}
		chain := walkFn(m)
		for _, anc := range chain {
			if !sawCmdlineMatch && cmdlineLooksLikeAgnt(anc.Cmdline, daemonBinary) {
				sawCmdlineMatch = true
				matchingCmdline = anc.Cmdline
			}
			if !sawCwdMatch && cwdIsInsideKnownProject(anc.Cwd, knownProjects) {
				sawCwdMatch = true
				matchingCwd = anc.Cwd
			}
			if sawCmdlineMatch && sawCwdMatch {
				return true, fmt.Sprintf(
					"ancestor cmdline=%q cwd=%q matches this daemon",
					truncateForLog(matchingCmdline, 200),
					matchingCwd,
				)
			}
		}
	}

	switch {
	case !sawCmdlineMatch && !sawCwdMatch:
		return false, "no ancestor cmdline or cwd matched this daemon"
	case !sawCmdlineMatch:
		return false, fmt.Sprintf("cwd %q in known project but no agnt cmdline ancestor found", matchingCwd)
	default:
		return false, fmt.Sprintf("cmdline %q looks like agnt but no ancestor cwd in known projects", truncateForLog(matchingCmdline, 200))
	}
}

// truncateForLog limits a diagnostic string to n runes, appending "…" if
// clipped. Prevents unbounded log lines for pathological cmdlines.
func truncateForLog(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// resolvedDaemonBinary returns the path of the running daemon binary,
// resolved through symlinks where possible. Used as one of the cmdline
// match targets in pgidOwnershipCheck. Returns empty string on any error
// so the gate falls back to substring-only matching on "agnt run".
func resolvedDaemonBinary() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}
