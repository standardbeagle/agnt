package daemon

import (
	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// broadcastIncidentDigest is the one emit point for an agent-directed incident
// ping. It fans the ping out to STREAM-EVENTS subscribers as a synthetic
// incident_digest LogEntry, the cross-process transport for the agent-inbound
// queue; the compact text rides in Custom.Message and level is the severity.
//
// sessionCode scopes the digest to one agent's inbox (streamFilter.session), so
// two agents in one checkout do not see each other's pings; empty leaves it
// project-scoped. The sender never picks a transport: via is the session's
// connector (see connectorFor), and each front end acts only on its own. Empty
// via leaves the digest unrouted (observers only).
func (d *Daemon) broadcastIncidentDigest(level, projectPath, sessionCode, via string, payload incident.PingPayload) {
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
			// The structured ping rides along for consumers that can render
			// more than the one-line text: with channel mode on, the MCP
			// process delivers the digest as a channel event and lists the top
			// incidents from this.
			Data: map[string]interface{}{
				incident.DigestPayloadKey: payload,
				incident.DigestKindKey:    incident.DigestKindAgentMessage,
			},
		},
	}
	if sessionCode != "" {
		entry.Custom.Data[incident.DigestSessionKey] = sessionCode
	}
	if via != "" {
		entry.Custom.Data[incident.DigestViaKey] = via
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
		// Observers (the incidents CLI, a Claude mod's pane) see every digest;
		// the stamp tells the MCP process whether this session's agent is its to
		// notify.
		d.broadcastIncidentDigest(level, s.ProjectPath, sessionCode, string(connectorFor(s)), payload)
		return nil
	}
	ptyInject := func(line string) error {
		s, ok := d.sessionRegistry.Get(sessionCode)
		if !ok || connectorFor(s) != connectorStdin {
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
	// There is no daemon-side claude/channel callback: a channel notification
	// can only be sent by the MCP process that owns the agent's connection. The
	// digest this mcpNotify broadcasts is what that process delivers — as a
	// channel event when channel mode is on, as an MCP log notification
	// otherwise (tools.ForwardIncidentDigest) — so both transports sit behind
	// the same pause / alerts.push gate above, and alerts.push needs no
	// separate channel bit.
	return mcpNotify, nil, ptyInject
}
