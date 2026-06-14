package daemon

import (
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/scope"
)

// rebindProxyOverlays updates overlay endpoints for all proxies matching the
// session's project path. Called when a session registers or reconnects,
// ensuring proxies created before the session get wired up. Scoping to the
// session's project is what keeps one session from re-pointing another
// project's proxies at its own overlay.
func (d *Daemon) rebindProxyOverlays(session *Session) {
	if session.OverlayPath == "" {
		return
	}

	for _, p := range d.proxym.ListScoped(scope.Project(session.ProjectPath)) {
		if !p.HasOverlayEndpoint() {
			p.SetOverlayEndpoint(session.OverlayPath)
			debug.Log("daemon", "Late-bound overlay endpoint for proxy %s from session %s: %s",
				p.ID, session.Code, session.OverlayPath)
		}
	}
}

// overlayEndpointForProject returns the overlay socket of the session that owns
// projectPath, or "" if no session is registered for it yet. Returning "" fails
// closed on purpose: a proxy created before its session connects is left
// unbound and wired up later by rebindProxyOverlays, rather than falling back
// to some other project's overlay (the old global-fallback leak).
func (d *Daemon) overlayEndpointForProject(projectPath string) string {
	if session, ok := d.sessionRegistry.FindByDirectory(projectPath); ok {
		return session.OverlayPath
	}
	return ""
}
