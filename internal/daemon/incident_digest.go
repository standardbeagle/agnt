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
func (d *Daemon) broadcastIncidentDigest(level string, payload incident.PingPayload) {
	if d.eventHub == nil {
		return
	}
	entry := proxy.LogEntry{
		Type: proxy.LogTypeIncidentDigest,
		Custom: &proxy.CustomLog{
			Level:   level,
			Message: incident.CompactDigestText(payload),
		},
	}
	d.eventHub.BroadcastLogEntry(entry, "")
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
	mcpNotify := func(level string, payload incident.PingPayload) error {
		d.broadcastIncidentDigest(level, payload)
		return nil
	}
	d.incidentBus.AddSession(sessionCode, mcpNotify, nil, nil)
}
