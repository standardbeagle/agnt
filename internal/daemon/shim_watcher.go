package daemon

import (
	"os"
	"os/exec"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/shims"
)

// ensureShimWatcher spawns the detached `agnt shim watch` process if no
// live watcher is recorded in the manifest. The watcher is the SIGKILL
// safety net: it outlives the daemon (own session/group, not tracked by
// the process manager) and removes every registered shim bin dir once the
// daemon has been dead past a grace window that lets auto-restart win.
func (d *Daemon) ensureShimWatcher() {
	if !d.config.ShimWatcherEnabled {
		return
	}
	m := shims.LoadManifest()
	if m.WatcherPID > 0 && m.WatcherPID != os.Getpid() && shimWatcherAlive(m.WatcherPID, m.WatcherBirth) {
		return
	}

	exe, err := os.Executable()
	if err != nil {
		debug.Log("shim-hub", "ensureShimWatcher: resolve executable: %v", err)
		return
	}
	cmd := exec.Command(exe, "shim", "watch", "--socket", d.config.SocketPath)
	detachShimWatcher(cmd)
	if err := cmd.Start(); err != nil {
		debug.Log("shim-hub", "ensureShimWatcher: start: %v", err)
		return
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	birth, _ := platform.ProcessBirthID(pid)
	if err := shims.WithManifest(func(m *shims.Manifest) {
		m.WatcherPID = pid
		m.WatcherBirth = birth
	}); err != nil {
		debug.Log("shim-hub", "ensureShimWatcher: record pid: %v", err)
	}
	debug.Log("shim-hub", "shim watcher spawned (pid %d)", pid)
}

// releaseProjectShims detaches sessionCode from the project's shim
// manifest entry and removes the bin dir once no session — registered in
// the session registry or recorded in the manifest — still depends on it.
//
// This must run for EVERY session end, not just the project's last one:
// detaching is what keeps stale session codes from pinning the manifest
// entry forever. A session that ends while others live and is never
// detached would keep stillUsed=true on every later release, so the bin
// dir would never be removed. Fail-open scripts make removal safe even if
// a shell still has the dir on PATH.
func (d *Daemon) releaseProjectShims(projectPath, sessionCode string) {
	if projectPath == "" {
		return
	}
	if shims.ReleaseSession(projectPath, sessionCode) {
		return
	}
	for _, other := range d.sessionRegistry.List(projectPath, false) {
		if other.Code != sessionCode {
			return
		}
	}
	shims.Remove(projectPath)
	shims.DropProject(projectPath)
}

// cleanupAllShims removes every registered shim bin dir and clears the
// manifest. Called from Daemon.Stop: graceful shutdown takes its wrappers
// down with it. Fail-open shim scripts make this safe even with shells
// still holding .agnt/bin on PATH — lookups just fall through to the real
// binaries.
func (d *Daemon) cleanupAllShims() {
	m := shims.LoadManifest()
	for projectPath := range m.Projects {
		shims.Remove(projectPath)
	}
	if err := shims.SaveManifest(&shims.Manifest{Projects: map[string]*shims.ManifestEntry{}}); err != nil {
		debug.Log("shim-hub", "cleanupAllShims: save manifest: %v", err)
	}
}

// sweepShimManifest reconciles the manifest at daemon start: entries whose
// project or bin dir vanished are dropped, session lists are reset (no
// session survives a daemon restart), and the watcher is (re)spawned when
// projects remain.
func (d *Daemon) sweepShimManifest() {
	m := shims.LoadManifest()
	if len(m.Projects) == 0 {
		return
	}
	changed := false
	for projectPath, e := range m.Projects {
		if _, err := os.Stat(e.BinDir); err != nil {
			delete(m.Projects, projectPath)
			changed = true
			continue
		}
		if len(e.Sessions) > 0 {
			e.Sessions = nil
			changed = true
		}
	}
	if changed {
		if err := shims.SaveManifest(m); err != nil {
			debug.Log("shim-hub", "sweepShimManifest: save: %v", err)
		}
	}
	if len(m.Projects) > 0 {
		d.ensureShimWatcher()
	}
}
