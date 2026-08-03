package daemon

import (
	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// broadcastIncidentDigest fans an incident ping out to all STREAM-EVENTS
// subscribers as a synthetic incident_digest LogEntry. This is the cross-process
// transport for the unified agent-inbound queue: consumer processes (agnt mcp,
// agnt run) that subscribe to the daemon event stream render the digest to the
// agent. The compact digest text rides in Custom.Message; level is the severity.
func (d *Daemon) broadcastIncidentDigest(level, projectPath string, payload incident.PingPayload) {
	if d.eventHub == nil {
		return
	}
	// Never broadcast a digest unscoped: an empty project path disables the
	// project filter in BroadcastLogEntryForProject and would leak this
	// session's pings to every other project. Callers must resolve the owning
	// project first; drop defensively if they didn't.
	if projectPath == "" {
		return
	}
	entry := proxy.LogEntry{
		Type: proxy.LogTypeIncidentDigest,
		Custom: &proxy.CustomLog{
			Level:   level,
			Message: incident.CompactDigestText(payload),
		},
	}
	// Stamp the digest with the owning session's project so project-scoped
	// STREAM-EVENTS subscribers in other projects don't receive this session's
	// pings. projectPath is guaranteed non-empty by the guard above.
	d.eventHub.BroadcastLogEntryForProject(entry, projectPath)
}

// addIncidentSession registers a session pipeline on the incident bus, wired so
// the pinger broadcasts digests over the STREAM-EVENTS transport. Replaces the
// prior nil-sink (pull-only) registration. Safe no-op when the bus is absent.
func (d *Daemon) addIncidentSession(sessionCode string) {
	if d.incidentBus == nil {
		return
	}
	// Idempotent: a pipeline already exists for a re-register or a second
	// connection attaching to the same session. AddSession tears the old
	// pipeline down (wiping unread incidents), so skip it — the mcpNotify
	// closure is session-independent, nothing to refresh.
	if d.incidentBus.HasSession(sessionCode) {
		return
	}
	mcpNotify, channelNotify, ptyInject := d.incidentSinkCallbacks(sessionCode)
	d.incidentBus.AddSession(sessionCode, mcpNotify, channelNotify, ptyInject)
}

// incidentSinkCallbacks wires the effective alerts.push policy to the incident
// pinger. Callbacks remain registered and gate at delivery time, so applying a
// project config after session registration takes effect without replacing the
// pipeline (which would discard unread incidents).
func (d *Daemon) incidentSinkCallbacks(sessionCode string) (incident.MCPNotifyFn, incident.ChannelNotifyFn, incident.PTYInjectFn) {
	mcpNotify := func(level string, payload incident.PingPayload) error {
		// Pause = drop the push, not the record. The incident already landed in
		// this session's inbox upstream of the pinger, so get_incidents
		// stay pullable; we only suppress the interrupt into the agent.
		if d.IsForwardingPaused(sessionCode) {
			return nil
		}
		// Resolve the project fresh each ping — a session's project path is
		// stable for its lifetime, but looking it up here avoids capturing a
		// stale value if the session re-registers.
		//
		// Fail closed on a registry miss: during the teardown window the
		// session is unregistered before the incident bus removes it, so a
		// ping can fire with the session already gone. Broadcasting with an
		// empty project path would degrade to an UNSCOPED broadcast, leaking
		// this session's digest into every other project's STREAM-EVENTS
		// stream. Drop the push instead — the incident already landed in the
		// inbox upstream, so it stays pullable via get_incidents.
		s, ok := d.sessionRegistry.Get(sessionCode)
		if !ok || s.ProjectPath == "" {
			return nil
		}
		if !d.alertPushConfigForProject(s.ProjectPath).MCPNotificationsEnabled() {
			return nil
		}
		d.broadcastIncidentDigest(level, s.ProjectPath, payload)
		return nil
	}
	ptyInject := func(line string) error {
		s, ok := d.sessionRegistry.Get(sessionCode)
		if !ok || s.OverlayPath == "" {
			return nil
		}
		if !d.alertPushConfigForProject(s.ProjectPath).PTYInjectionEnabled() || d.IsForwardingPaused(sessionCode) {
			return nil
		}
		if d.incidentPTYInject != nil {
			return d.incidentPTYInject(s.OverlayPath, line)
		}
		return d.sendMessageToOverlay(s.OverlayPath, line)
	}
	// Top-level `channel` config drives MCP event forwarding, not incident
	// pings. There is no production claude/channel callback in the daemon, and
	// alerts.push deliberately exposes no inert channel bit.
	return mcpNotify, nil, ptyInject
}
