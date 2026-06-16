package daemon

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/standardbeagle/agnt/internal/scope"
)

// reportPortKills surfaces, on both sides, what an auto-kill did to a dev
// server whose port was reclaimed. Without this the failure is invisible: the
// killer project silently starts and — if another project was proxying the
// reclaimed port — that other project's proxy keeps forwarding to a backend
// that is now down or, worse, to whichever dev server next grabs the port. The
// developer sees "two projects serving the same site" with no explanation.
//
// Two notices, both project-scoped and agent-visible via get_errors:
//   - killer side: which process it killed and on what port.
//   - victim side: every OTHER project whose proxy targets that port learns its
//     dev server was killed out from under it and must be restarted.
func (d *Daemon) reportPortKills(killerProject string, killResults []KillResult) {
	killerName := filepath.Base(killerProject)

	for _, kr := range killResults {
		if !kr.Killed {
			continue
		}

		// Killer side — warning (not info) so it stands out: reclaiming a port
		// from an unmanaged holder is a destructive side effect, not routine.
		d.startupLog(killerProject).WarnPort(kr.ScriptName, "port_conflict_killed",
			fmt.Sprintf("reclaimed port %d from %s (PIDs %v) to start %q; any other project proxying :%d now points at a dead or reused backend",
				kr.Port, kr.ProcessName, kr.PIDs, kr.ScriptName, kr.Port),
			kr.Port)

		// Victim side — notify each other project whose proxy targets this
		// port that its dev server is gone.
		for _, victim := range d.projectsProxyingPort(kr.Port, killerProject) {
			d.startupLog(victim).WarnPort("", "dev_server_killed",
				fmt.Sprintf("dev server on port %d was killed by project %q claiming the port; restart it — your proxy is now serving another project's backend",
					kr.Port, killerName),
				kr.Port)
		}
	}
}

// projectsProxyingPort returns the distinct project paths (excluding
// excludeProject) of running proxies whose target URL points at the given
// localhost port. Used to attribute a reclaimed port to the projects whose
// proxies just lost their backend.
//
// The enumeration is deliberately cross-project (Unscoped): a port conflict is
// inherently a collision between different projects, so scoping to one project
// would defeat the attribution.
func (d *Daemon) projectsProxyingPort(port int, excludeProject string) []string {
	want := strconv.Itoa(port)
	seen := make(map[string]bool)
	var victims []string

	for _, p := range d.proxym.ListScoped(scope.Unscoped("port-kill attribution: find proxies whose backend was just reclaimed")) {
		if p.Path == "" || p.Path == excludeProject || p.TargetURL == nil {
			continue
		}
		if p.TargetURL.Port() != want {
			continue
		}
		if seen[p.Path] {
			continue
		}
		seen[p.Path] = true
		victims = append(victims, p.Path)
	}
	return victims
}
