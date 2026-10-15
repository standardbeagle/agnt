package daemon

import (
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/scope"
)

// rebindProxyOverlays points the session's project proxies at its overlay.
// Called when a session registers or reconnects, so proxies created before the
// session get wired up. A proxy bound to another live session's overlay keeps
// that binding; one bound to a socket no registered session owns any more (a
// reconnect rebuilds its overlay on a new socket) is stale and is replaced.
// Scoping to the session's project is what keeps one session from re-pointing
// another project's proxies at its own overlay.
func (d *Daemon) rebindProxyOverlays(session *Session) {
	if session.OverlayPath == "" {
		return
	}

	for _, p := range d.proxym.ListScoped(scope.Project(session.ProjectPath)) {
		current := p.OverlayEndpoint()
		if current == session.OverlayPath || (current != "" && d.overlayEndpointOwned(current)) {
			continue
		}
		if p.CompareAndSwapOverlayEndpoint(current, session.OverlayPath) {
			debug.Log("daemon", "Bound overlay endpoint for proxy %s from session %s: %s (was %q)",
				p.ID, session.Code, session.OverlayPath, current)
		}
	}
}

// releaseProxyOverlays moves the proxies bound to a departed session's overlay
// to the project's remaining session, or unbinds them when none remains. Runs
// after the session left the registry. The swap is guarded by the departed
// endpoint, so a binding another session installed meanwhile is left alone.
func (d *Daemon) releaseProxyOverlays(departed *Session) {
	if departed == nil || departed.OverlayPath == "" || d.proxym == nil {
		return
	}
	for _, p := range d.proxym.ListScoped(scope.Project(departed.ProjectPath)) {
		if p.OverlayEndpoint() != departed.OverlayPath {
			continue
		}
		next := d.overlayEndpointForProject(p.Path)
		if p.CompareAndSwapOverlayEndpoint(departed.OverlayPath, next) {
			debug.Log("daemon", "Released overlay endpoint of departed session %s for proxy %s: now %q",
				departed.Code, p.ID, next)
		}
	}
}

// overlayEndpointOwned reports whether a registered session still owns the
// overlay endpoint. A disconnected session counts: a heartbeat revives it.
func (d *Daemon) overlayEndpointOwned(endpoint string) bool {
	if d.sessionRegistry == nil {
		return false
	}
	for _, s := range d.sessionRegistry.List("", true) {
		if s.OverlayPath == endpoint {
			return true
		}
	}
	return false
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
