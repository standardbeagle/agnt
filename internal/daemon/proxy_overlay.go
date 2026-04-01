package daemon

import "github.com/standardbeagle/agnt/internal/debug"

// rebindProxyOverlays updates overlay endpoints for all proxies matching the
// session's project path. Called when a session registers or reconnects,
// ensuring proxies created before the session get wired up.
func (d *Daemon) rebindProxyOverlays(session *Session) {
	if session.OverlayPath == "" {
		return
	}
	normalizedPath := normalizePath(session.ProjectPath)

	for _, p := range d.proxym.List() {
		if normalizePath(p.Path) != normalizedPath {
			continue
		}
		if !p.HasOverlayEndpoint() {
			p.SetOverlayEndpoint(session.OverlayPath)
			debug.Log("daemon", "Late-bound overlay endpoint for proxy %s from session %s: %s",
				p.ID, session.Code, session.OverlayPath)
		}
	}
}
