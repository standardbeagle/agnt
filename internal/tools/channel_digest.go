package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// DigestLogFunc delivers an incident digest as an MCP log notification.
// In production it fans out ServerSession.Log; tests capture calls.
type DigestLogFunc func(ctx context.Context, level, message string) error

// incidentDigestChannelType is the `type` meta value of a digest delivered over
// the channel, so an agent can tell it from a raw browser event.
const incidentDigestChannelType = "incident_digest"

// ForwardIncidentDigest delivers one incident_digest stream entry to the agent
// on exactly ONE transport:
//
//   - channel mode on  → a notifications/claude/channel event. Channel mode is
//     the push surface the user chose, and it no longer receives raw error
//     events (see ChannelSink), so this is how a channel-mode agent hears about
//     errors. The event carries the digest line plus the top unread incidents'
//     summaries, so the error text still lands in the conversation.
//   - channel mode off → an MCP log notification, as before.
//
// It never sends both: one error must produce one agent-facing notification.
//
// Every gate has already been applied by the time an entry arrives here. The
// entry exists only because the session's pinger emitted it, i.e. after the
// inbox merge, flow control and coalescing, and after the daemon's sink
// callback honoured forwarding-pause and alerts.push (mcp-notifications). This
// function adds no gate and no dedup of its own — sinks are dumb.
//
// Returns whether a notification was sent.
func ForwardIncidentDigest(ctx context.Context, entry proxy.LogEntry, channelOn bool, logFn DigestLogFunc, notify NotifyFunc) bool {
	if entry.Type != proxy.LogTypeIncidentDigest || entry.Custom == nil {
		return false
	}
	// A digest stamped for another connector (the session's agent is reached by
	// stdin through `agnt run`) is not this process's to deliver. An unstamped
	// one is from an older daemon and keeps the old behaviour.
	if via, _ := entry.Custom.Data[incident.DigestViaKey].(string); via != "" && via != incident.DigestViaMCP {
		return false
	}
	level := entry.Custom.Level
	if level == "" {
		level = "info"
	}

	if !channelOn {
		if logFn == nil {
			return false
		}
		// Best-effort: the incident stays pullable from the inbox either way.
		_ = logFn(ctx, level, entry.Custom.Message)
		return true
	}

	if notify == nil {
		return false
	}
	meta := map[string]string{
		"type":     incidentDigestChannelType,
		"severity": level,
	}
	_ = notify(ctx, ChannelNotificationMethod, channelNotification{
		Content: digestChannelContent(entry.Custom),
		Meta:    meta,
	})
	return true
}

// digestChannelContent renders the digest for the conversation: the one-line
// summary followed by the top unread incidents the daemon attached, one per
// line, so the agent reads what broke without a round trip. When the payload
// is absent (an older daemon) the summary line alone is returned.
func digestChannelContent(c *proxy.CustomLog) string {
	payload, ok := digestPayload(c)
	if !ok || len(payload.Top) == 0 {
		return c.Message
	}
	var b strings.Builder
	b.WriteString(c.Message)
	for _, top := range payload.Top {
		b.WriteString("\n")
		label := string(top.Severity)
		if top.Category != "" {
			label += ":" + top.Category
		}
		fmt.Fprintf(&b, "[%s] ", label)
		if top.Summary != "" {
			// A summary can be a message plus stack; keep the event one line
			// per incident. The full text is one get_incidents call away.
			first, _, _ := strings.Cut(top.Summary, "\n")
			b.WriteString(strings.TrimSpace(first))
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "(%dx, id: %s)", top.Count, top.Fingerprint)
	}
	return b.String()
}

// digestPayload recovers the structured ping the daemon attached to the digest
// entry under Data["payload"]. The entry crossed the daemon socket as JSON, so
// the value is a generic map by now; re-marshal it into the typed payload.
func digestPayload(c *proxy.CustomLog) (incident.PingPayload, bool) {
	raw, ok := c.Data[incident.DigestPayloadKey]
	if !ok {
		return incident.PingPayload{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return incident.PingPayload{}, false
	}
	var p incident.PingPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return incident.PingPayload{}, false
	}
	return p, true
}
