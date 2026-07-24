package incident

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/selflog"
)

const (
	defaultMaxTopFingerprints = 5
	sessionPingFP             = "__session_ping__" // synthetic fp for session-level coalescer slot
	maxSummaryInPing          = 100                // chars
)

// PingConfig controls which channels emit pings and their content density.
type PingConfig struct {
	MCPNotifications bool
	ChannelEnabled   bool
	PTYInjection     bool
	MaxTopFPs        int        // 0 → defaultMaxTopFingerprints
	IncludeSummary   bool       // include short summary text per top entry
	Delays           PingDelays // coalesce backoff; zero → DefaultPingDelays
}

// PingStats is the compact summary inside PingPayload.
type PingStats struct {
	Critical int `json:"critical"`
	Error    int `json:"error"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
	New      int `json:"new"` // unread entries at time of ping
}

// TopEntry is one fingerprint row in PingPayload.Top.
type TopEntry struct {
	Fingerprint string   `json:"fp"`
	Category    string   `json:"category,omitempty"`
	Count       int      `json:"count"`
	Severity    Severity `json:"severity"`
	Summary     string   `json:"summary,omitempty"`
}

// PingPayload is the stable schema for all channels. Target size < 2KB.
type PingPayload struct {
	Type    string     `json:"type"`    // "agnt.incident_ping"
	Version int        `json:"version"` // 1
	Session string     `json:"session"`
	Summary PingStats  `json:"summary"`
	Top     []TopEntry `json:"top,omitempty"`
}

// MCPNotifyFn sends an MCP Log notification. level is "error", "warning", or "info".
type MCPNotifyFn func(level string, payload PingPayload) error

// ChannelNotifyFn sends a claude/channel notification. content is compact text.
type ChannelNotifyFn func(content string, payload PingPayload) error

// PTYInjectFn injects a single-line status string into PTY stdin.
type PTYInjectFn func(line string) error

// PingEmitter subscribes to an Inbox, applies flow gating, and fans pings out to
// up to 3 channels (MCP log, claude/channel, PTY stdin).
//
// Critical events bypass the coalesce timer and emit immediately.
// Non-critical events are debounced via a session-level Coalescer slot.
type PingEmitter struct {
	inbox    *Inbox
	config   PingConfig
	flow     *FlowController
	coalesce *Coalescer

	mcpNotify     MCPNotifyFn
	channelNotify ChannelNotifyFn
	ptyInject     PTYInjectFn

	cancel func() // unsubscribe from Inbox
}

// NewPingEmitter creates and starts a PingEmitter. Stop() must be called to
// release the subscription goroutine. Any of mcpNotify, channelNotify, or
// ptyInject may be nil (that channel is skipped).
func NewPingEmitter(
	inbox *Inbox,
	config PingConfig,
	flow *FlowController,
	mcpNotify MCPNotifyFn,
	channelNotify ChannelNotifyFn,
	ptyInject PTYInjectFn,
) *PingEmitter {
	if config.MaxTopFPs <= 0 {
		config.MaxTopFPs = defaultMaxTopFingerprints
	}
	if config.Delays == (PingDelays{}) {
		config.Delays = DefaultPingDelays
	}

	pe := &PingEmitter{
		inbox:         inbox,
		config:        config,
		flow:          flow,
		mcpNotify:     mcpNotify,
		channelNotify: channelNotify,
		ptyInject:     ptyInject,
	}
	pe.coalesce = NewCoalescer(config.Delays, func(_ IncidentEvent) {
		pe.emit()
	})

	ch, cancel := inbox.Subscribe()
	pe.cancel = cancel
	go pe.run(ch)
	return pe
}

// ForceFlushCoalesce immediately emits any pending coalesced ping.
// Intended for activity-idle callbacks wired by L8.
func (pe *PingEmitter) ForceFlushCoalesce() {
	pe.coalesce.ForceFlush(sessionPingFP)
}

// Stop unsubscribes from the inbox and cancels all pending pings.
func (pe *PingEmitter) Stop() {
	pe.cancel()
	pe.coalesce.Stop()
}

func (pe *PingEmitter) run(ch <-chan InboxDelta) {
	for delta := range ch {
		pe.handleDelta(delta)
	}
}

func (pe *PingEmitter) handleDelta(delta InboxDelta) {
	sev := delta.Entry.Severity

	if !pe.flow.TryPing(sev) {
		return // over rate limit; event is still inboxed
	}

	if sev == SeverityCritical {
		// Cancel any pending coalesced ping and emit now. Stop() clears the
		// slot map but leaves the Coalescer reusable, so we do NOT reassign
		// pe.coalesce — reassigning it raced Stop()/ForceFlushCoalesce on the
		// teardown and activity-idle goroutines.
		pe.coalesce.Stop()
		pe.emit()
		return
	}

	if delta.Escalated {
		// Severity escalation must emit a ping promptly. ForceFlush alone only
		// fires when a coalesced ping is already pending; with an empty
		// coalescer it no-ops, the flow token is already consumed, and the
		// escalation is silently dropped. Schedule the escalated event first so
		// a session-ping slot always exists, then force-flush it — guaranteeing
		// exactly one prompt ping whether or not one was already pending.
		ev := IncidentEvent{Fingerprint: sessionPingFP, Severity: sev}
		pe.coalesce.Schedule(ev)
		pe.coalesce.ForceFlush(sessionPingFP)
		return
	}

	// Non-critical: schedule via coalescer. If agent is active, the ping waits;
	// the caller (L8) wires ActivityDetector.onIdle → ForceFlushCoalesce().
	ev := IncidentEvent{Fingerprint: sessionPingFP, Severity: sev}
	pe.coalesce.Schedule(ev)
}

func (pe *PingEmitter) emit() {
	// Top must mirror Stats' unread-only semantics: re-surfacing entries the
	// agent already marked read makes every ping "shout" about acknowledged
	// incidents until they age out of the inbox.
	entries, stats := pe.inbox.Query(QueryFilter{UnreadOnly: true})
	payload := pe.buildPayload(stats, entries)
	pe.fanOut(payload)
}

func (pe *PingEmitter) buildPayload(stats Stats, entries []InboxEntry) PingPayload {
	p := PingPayload{
		Type:    "agnt.incident_ping",
		Version: 1,
		Session: pe.inbox.SessionID,
		Summary: PingStats{
			Critical: stats.Critical,
			Error:    stats.Error,
			Warning:  stats.Warning,
			Info:     stats.Info,
			New:      stats.New, // total unread; band counts are already unread-only
		},
	}

	// Top N fingerprints sorted by severity then count.
	top := make([]InboxEntry, len(entries))
	copy(top, entries)
	sort.Slice(top, func(i, j int) bool {
		ri, rj := sevRank(top[i].Severity), sevRank(top[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return top[i].Count > top[j].Count
	})
	if len(top) > pe.config.MaxTopFPs {
		top = top[:pe.config.MaxTopFPs]
	}

	for _, e := range top {
		te := TopEntry{
			Fingerprint: e.Fingerprint,
			Count:       e.Count,
			Severity:    e.Severity,
		}
		if e.Sample != nil {
			te.Category = e.Sample.Category
			if pe.config.IncludeSummary {
				te.Summary = truncateToBytes(e.Sample.Summary, maxSummaryInPing)
			}
		}
		p.Top = append(p.Top, te)
	}
	return p
}

func (pe *PingEmitter) fanOut(payload PingPayload) {
	level := highestLevel(payload.Summary)

	// Pinger contract #7: delivery never blocks and one slow/broken sink must
	// not delay the others. Sink errors are best-effort by design — logged for
	// diagnosis, never propagated or retried here.
	if pe.config.MCPNotifications && pe.mcpNotify != nil {
		if err := pe.mcpNotify(level, payload); err != nil {
			debug.Log("incident-pinger", "mcp ping failed: %v", err)
			selflog.Record("pinger", "mcp ping delivery failed: %v", err)
		}
	}
	if pe.config.ChannelEnabled && pe.channelNotify != nil {
		if err := pe.channelNotify(compactText(payload), payload); err != nil {
			debug.Log("incident-pinger", "channel ping failed: %v", err)
			selflog.Record("pinger", "channel ping delivery failed: %v", err)
		}
	}
	if pe.config.PTYInjection && pe.ptyInject != nil {
		if err := pe.ptyInject(ptyLine(payload)); err != nil {
			debug.Log("incident-pinger", "pty ping failed: %v", err)
			selflog.Record("pinger", "pty ping delivery failed: %v", err)
		}
	}
}

func highestLevel(s PingStats) string {
	switch {
	case s.Critical > 0:
		return "error" // MCP log level
	case s.Error > 0:
		return "error"
	case s.Warning > 0:
		return "warning"
	default:
		return "info"
	}
}

// CompactDigestText renders the one-line digest summary for cross-process
// transport (daemon → STREAM-EVENTS → consumer). Exported wrapper over the
// internal compact formatter.
func CompactDigestText(p PingPayload) string {
	return compactText(p)
}

func compactText(p PingPayload) string {
	return fmt.Sprintf("[agnt] incidents: crit=%d err=%d warn=%d info=%d (new=%d). pull: get_incidents",
		p.Summary.Critical, p.Summary.Error, p.Summary.Warning, p.Summary.Info, p.Summary.New)
}

func ptyLine(p PingPayload) string {
	return fmt.Sprintf("[agnt] incidents: crit=%d err=%d warn=%d (new=%d). pull: get_incidents severity=critical,error since=cursor\n",
		p.Summary.Critical, p.Summary.Error, p.Summary.Warning, p.Summary.New)
}

// PingPayloadSize returns the JSON-marshalled byte count of a payload for
// size assertions in tests.
func PingPayloadSize(p PingPayload) int {
	b, _ := json.Marshal(p)
	return len(b)
}
